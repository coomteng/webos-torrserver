package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- Chinese title -> English title
//
// The torrent indexers behind TorrServer's Torznab search (TPB, YTS, LimeTorrents, TorrentGalaxy) only
// have English release names, and TPB answers an unmatched query with its top-100 list. So the phone
// page asks GET /title?q=<中文片名> first: Wikidata entity search in Chinese -> English label / enwiki
// title of the film / TV entities, newest first. No key needed; results are cached and calls spaced,
// because Wikidata answers bursts with 429.

type TitleCandidate struct {
	ID    string `json:"id"`              // Q-id
	En    string `json:"en"`              // English title to search with
	Year  int    `json:"year,omitempty"`  // first publication year
	Kind  string `json:"kind"`            // "film" | "series"
	Label string `json:"label,omitempty"` // the Chinese label Wikidata matched
}

type Titles struct {
	Base    string // https://www.wikidata.org/w/api.php
	Client  *http.Client
	UA      string
	Spacing time.Duration

	mu    sync.Mutex
	last  time.Time
	until time.Time // back off after a 429 until this time
	cache map[string]titleEntry
}

type titleEntry struct {
	at  time.Time
	res []TitleCandidate
}

const titleCacheTTL = 24 * time.Hour

var errTitleBusy = errors.New("片名查询太频繁，稍后再试")

func newTitles() *Titles {
	return &Titles{Base: "https://www.wikidata.org/w/api.php", Client: &http.Client{Timeout: 15 * time.Second},
		UA: "torr-remote/0.4 (+https://github.com/coomteng/webos-torrserver)", Spacing: time.Second,
		cache: map[string]titleEntry{}}
}

var hanRe = regexp.MustCompile(`\p{Han}`)

// HasHan reports whether s contains CJK ideographs (only those queries need translating).
func HasHan(s string) bool { return hanRe.MatchString(s) }

// P31 (instance of) values treated as watchable. Wikidata has many film/series subclasses; these cover
// what the entity search returns for the usual titles.
var filmKinds = map[string]bool{"Q11424": true, "Q24869": true, "Q202866": true, "Q506240": true, "Q229390": true, "Q1259759": true, "Q24862": true, "Q20667187": true, "Q17517379": true}
var seriesKinds = map[string]bool{"Q5398426": true, "Q21191270": true, "Q1366112": true, "Q63952888": true, "Q117467246": true, "Q581714": true, "Q526877": true, "Q3464665": true}

func (t *Titles) gate(ctx context.Context) error {
	t.mu.Lock()
	if time.Now().Before(t.until) {
		t.mu.Unlock()
		return errTitleBusy
	}
	wait := time.Until(t.last.Add(t.Spacing))
	t.last = time.Now()
	if wait > 0 {
		t.last = t.last.Add(wait)
	}
	t.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Titles) getJSON(ctx context.Context, params url.Values, out interface{}) error {
	if err := t.gate(ctx); err != nil {
		return err
	}
	params.Set("format", "json")
	req, _ := http.NewRequestWithContext(ctx, "GET", t.Base+"?"+params.Encode(), nil)
	req.Header.Set("User-Agent", t.UA)
	resp, err := t.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		t.mu.Lock()
		t.until = time.Now().Add(30 * time.Second)
		t.mu.Unlock()
		return errTitleBusy
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("wikidata HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type wdSearch struct {
	Search []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"search"`
}

type wdEntities struct {
	Entities map[string]struct {
		Labels map[string]struct {
			Value string `json:"value"`
		} `json:"labels"`
		Sitelinks map[string]struct {
			Title string `json:"title"`
		} `json:"sitelinks"`
		Claims map[string][]struct {
			Mainsnak struct {
				Datavalue *struct {
					Value json.RawMessage `json:"value"`
				} `json:"datavalue"`
			} `json:"mainsnak"`
		} `json:"claims"`
	} `json:"entities"`
}

var parenRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// Lookup returns film/series candidates for a Chinese title, newest first. Empty = nothing found.
func (t *Titles) Lookup(ctx context.Context, q string) ([]TitleCandidate, error) {
	q = strings.TrimSpace(q)
	key := strings.ToLower(q)
	t.mu.Lock()
	if e, ok := t.cache[key]; ok && time.Since(e.at) < titleCacheTTL {
		t.mu.Unlock()
		return e.res, nil
	}
	t.mu.Unlock()

	var s wdSearch
	if err := t.getJSON(ctx, url.Values{"action": {"wbsearchentities"}, "language": {"zh"}, "uselang": {"zh"},
		"type": {"item"}, "limit": {"10"}, "search": {q}}, &s); err != nil {
		return nil, err
	}
	res := []TitleCandidate{}
	if len(s.Search) > 0 {
		ids := make([]string, 0, len(s.Search))
		labels := map[string]string{}
		for _, h := range s.Search {
			ids = append(ids, h.ID)
			labels[h.ID] = h.Label
		}
		var e wdEntities
		if err := t.getJSON(ctx, url.Values{"action": {"wbgetentities"}, "props": {"labels|claims|sitelinks"},
			"sitefilter": {"enwiki"}, "languages": {"en|mul"}, "ids": {strings.Join(ids, "|")}}, &e); err != nil {
			return nil, err
		}
		for _, id := range ids {
			ent, ok := e.Entities[id]
			if !ok {
				continue
			}
			kind := ""
			for _, c := range ent.Claims["P31"] {
				if c.Mainsnak.Datavalue == nil {
					continue
				}
				var v struct {
					ID string `json:"id"`
				}
				json.Unmarshal(c.Mainsnak.Datavalue.Value, &v)
				if filmKinds[v.ID] {
					kind = "film"
					break
				}
				if seriesKinds[v.ID] {
					kind = "series"
				}
			}
			if kind == "" {
				continue
			}
			en := ent.Labels["en"].Value
			if en == "" {
				en = ent.Labels["mul"].Value
			}
			if en == "" {
				en = parenRe.ReplaceAllString(ent.Sitelinks["enwiki"].Title, "")
			}
			if en == "" || HasHan(en) {
				continue
			}
			year := 0
			for _, c := range ent.Claims["P577"] {
				if c.Mainsnak.Datavalue == nil {
					continue
				}
				var v struct {
					Time string `json:"time"`
				}
				json.Unmarshal(c.Mainsnak.Datavalue.Value, &v)
				var y int
				if _, err := fmt.Sscanf(strings.TrimPrefix(v.Time, "+"), "%4d", &y); err == nil && (year == 0 || y < year) {
					year = y
				}
			}
			res = append(res, TitleCandidate{ID: id, En: en, Year: year, Kind: kind, Label: labels[id]})
		}
		// newest first; unknown years last
		sort.SliceStable(res, func(i, j int) bool {
			if (res[i].Year == 0) != (res[j].Year == 0) {
				return res[j].Year == 0
			}
			return res[i].Year > res[j].Year
		})
	}
	t.mu.Lock()
	t.cache[key] = titleEntry{at: time.Now(), res: res}
	t.mu.Unlock()
	return res, nil
}

// titleGet handles GET /title?q=: {query, candidates:[TitleCandidate]}. Non-Chinese queries come back
// with no candidates (the phone searches them as typed).
func (a *App) titleGet(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		sendError(w, r, 400, "missing q")
		return
	}
	if !HasHan(q) {
		sendJSON(w, r, 200, map[string]interface{}{"query": q, "candidates": []TitleCandidate{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	c, err := a.titles.Lookup(ctx, q)
	if err != nil {
		code := 502
		if err == errTitleBusy {
			code = 429
		}
		sendError(w, r, code, err.Error())
		return
	}
	sendJSON(w, r, 200, map[string]interface{}{"query": q, "candidates": c})
}
