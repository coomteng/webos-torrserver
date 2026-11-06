package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"torr-remote/subs"
)

// Subtitle endpoints. The TV never touches subtitle sites: the sidecar searches, downloads,
// converts everything to UTF-8 WebVTT and serves it; the TV fetches the .vtt and draws it itself.
//
//   GET  /subs/search?q=<text>|file=<release name>      -> {query, results:[subs.Result]}
//   GET  /subs/list?hash=&index=                         -> {subs:[subs.Saved]}
//   POST /subs/fetch {hash,index,src,id,file?,video?}    -> {sub: subs.Saved}  (download + convert + store)
//   POST /subs/upload?hash=&index=&name=   (raw body)    -> {sub: subs.Saved}  (.srt/.ass/.vtt/.zip ≤ 5 MB)
//   POST /subs/delete {hash,index,n}                     -> {ok}
//   GET  /subs/<hash>/<index>/<n>.vtt                    -> text/vtt

type Subs struct {
	store   *subs.Store
	sources map[string]subs.Source
}

func newSubs(cfg Config) *Subs {
	s := &Subs{sources: map[string]subs.Source{}}
	if cfg.SubsDir != "" {
		s.store = subs.NewStore(cfg.SubsDir)
	}
	if cfg.AssrtToken != "" {
		a := subs.NewASSRT(cfg.AssrtToken)
		if b := os.Getenv("ASSRT_BASE"); b != "" { // tests point this at a fake
			a.Base = strings.TrimRight(b, "/")
		}
		if ms, err := strconv.Atoi(os.Getenv("ASSRT_SPACING_MS")); err == nil && ms > 0 {
			a.Spacing = time.Duration(ms) * time.Millisecond
		}
		s.sources["assrt"] = a
	}
	return s
}

func (s *Subs) enabled() bool { return s != nil && s.store != nil }

func (s *Subs) sourceNames() []string {
	var n []string
	for k := range s.sources {
		n = append(n, k)
	}
	return n
}

func (s *Subs) configInfo() map[string]interface{} {
	names := s.sourceNames()
	if names == nil {
		names = []string{}
	}
	return map[string]interface{}{"enabled": s.enabled(), "sources": names}
}

func withURL(rec subs.Saved, hash string, index int) subs.Saved {
	rec.URL = fmt.Sprintf("/subs/%s/%d/%d.vtt", hash, index, rec.N)
	return rec
}

func (a *App) subsGet(w http.ResponseWriter, r *http.Request, p string) {
	if !a.subs.enabled() {
		sendError(w, r, 404, "subtitles disabled (TORR_SUBS_DIR not set)")
		return
	}
	q := r.URL.Query()
	switch {
	case p == "/subs/search":
		text := strings.TrimSpace(q.Get("q"))
		if text == "" {
			text = subs.SearchQuery(q.Get("file"))
		}
		if text == "" {
			sendError(w, r, 400, "q or file required")
			return
		}
		if len(a.subs.sources) == 0 {
			sendError(w, r, 503, "no subtitle source configured (set ASSRT_TOKEN)")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		var all []subs.Result
		var errs []string
		for _, src := range a.subs.sources {
			res, err := src.Search(ctx, text)
			if err != nil {
				errs = append(errs, src.Name()+": "+err.Error())
				continue
			}
			all = append(all, res...)
		}
		if len(all) == 0 && len(errs) > 0 {
			sendError(w, r, 502, strings.Join(errs, "; "))
			return
		}
		sendJSON(w, r, 200, map[string]interface{}{"query": text, "results": all, "errors": errs})
	case p == "/subs/list":
		hash, index := strings.ToLower(q.Get("hash")), atoi(q.Get("index"), -1)
		list, err := a.subs.store.List(hash, index)
		if err != nil {
			sendError(w, r, 400, err.Error())
			return
		}
		out := []subs.Saved{}
		for _, rec := range list {
			out = append(out, withURL(rec, hash, index))
		}
		sendJSON(w, r, 200, map[string]interface{}{"subs": out})
	case strings.HasSuffix(p, ".vtt"):
		parts := strings.Split(strings.TrimPrefix(p, "/subs/"), "/")
		if len(parts) != 3 {
			sendError(w, r, 404, "not found")
			return
		}
		n, _ := strconv.Atoi(strings.TrimSuffix(parts[2], ".vtt"))
		data, err := a.subs.store.VTT(strings.ToLower(parts[0]), atoi(parts[1], -1), n)
		if err != nil {
			sendError(w, r, 404, "not found")
			return
		}
		send(w, r, 200, data, "text/vtt; charset=utf-8", 3600)
	default:
		sendError(w, r, 404, "not found")
	}
}

func (a *App) subsPost(w http.ResponseWriter, r *http.Request, p string) {
	if !a.subs.enabled() {
		sendError(w, r, 404, "subtitles disabled (TORR_SUBS_DIR not set)")
		return
	}
	switch p {
	case "/subs/upload":
		q := r.URL.Query()
		hash, index := strings.ToLower(q.Get("hash")), atoi(q.Get("index"), -1)
		name := path.Base(strings.TrimSpace(q.Get("name")))
		data, err := io.ReadAll(io.LimitReader(r.Body, 5<<20+1))
		if err != nil || len(data) == 0 || len(data) > 5<<20 {
			sendError(w, r, 400, "empty body or larger than 5 MB")
			return
		}
		rec, err := a.subs.add(hash, index, name, "upload", data, q.Get("video"))
		if err != nil {
			sendError(w, r, 400, err.Error())
			return
		}
		log.Printf("subs: uploaded %q for %s/%d -> #%d (%d cues)", name, hash, index, rec.N, rec.Cues)
		sendJSON(w, r, 200, map[string]interface{}{"sub": withURL(rec, hash, index)})
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		sendError(w, r, 400, "bad JSON body")
		return
	}
	hash, index := strings.ToLower(str(body["hash"])), atoi(str(body["index"]), -1)
	switch p {
	case "/subs/fetch":
		src, ok := a.subs.sources[str(body["src"])]
		if !ok {
			sendError(w, r, 400, "unknown source")
			return
		}
		id := str(body["id"])
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		files, err := src.Files(ctx, id)
		if err != nil {
			sendError(w, r, 502, err.Error())
			return
		}
		// one package may hold several files (season pack / languages): pick by name unless told
		want := str(body["file"])
		var chosen *subs.File
		for i := range files {
			if files[i].Name == want {
				chosen = &files[i]
			}
		}
		if chosen == nil {
			entries := make([]subs.Entry, len(files))
			for i, f := range files {
				entries[i] = subs.Entry{Name: f.Name}
			}
			if best := subs.Pick(entries, str(body["video"])); best != nil {
				for i := range files {
					if files[i].Name == best.Name {
						chosen = &files[i]
					}
				}
			}
		}
		if chosen == nil {
			chosen = &files[0]
		}
		data, err := src.Download(ctx, *chosen)
		if err != nil {
			sendError(w, r, 502, err.Error())
			return
		}
		rec, err := a.subs.add(hash, index, chosen.Name, src.Name(), data, str(body["video"]))
		if err != nil {
			sendError(w, r, 400, err.Error())
			return
		}
		log.Printf("subs: fetched %s#%s %q for %s/%d -> #%d (%d cues)", src.Name(), id, chosen.Name, hash, index, rec.N, rec.Cues)
		sendJSON(w, r, 200, map[string]interface{}{"sub": withURL(rec, hash, index), "files": len(files)})
	case "/subs/delete":
		if err := a.subs.store.Delete(hash, index, atoi(str(body["n"]), 0)); err != nil {
			sendError(w, r, 404, "not found")
			return
		}
		sendJSON(w, r, 200, map[string]bool{"ok": true})
	default:
		sendError(w, r, 404, "not found")
	}
}

// add expands archives, picks the best member for the video and stores it converted.
func (s *Subs) add(hash string, index int, name, src string, data []byte, video string) (subs.Saved, error) {
	entries := subs.Expand(name, data)
	best := subs.Pick(entries, video)
	if best == nil {
		return subs.Saved{}, errors.New("no subtitle file inside")
	}
	if best.Name == "" {
		best.Name = "subtitle"
	}
	return s.store.Add(hash, index, best.Name, src, best.Data)
}
