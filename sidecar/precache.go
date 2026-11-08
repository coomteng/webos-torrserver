package main

// Background pre-caching ("prefetch") of whole files, switchable on and off.
//
// TorrServer only downloads what a reader asks for and drops an idle torrent
// after TorrentDisconnectTimeout, so a torrent that nobody is watching does not
// download at all. GET /stream?link=<hash>&index=<n>&preload&stat makes it read
// the file sequentially (state 2 "Torrent preload", exempt from the idle
// timeout); the request returns when the preload ends. The fork's
// {"action":"preload_stop"} ends it without dropping the torrent (a player
// streaming the same torrent keeps going).
//
// TorrServer forgets a running preload when it restarts, so the list lives
// here (precache.json) and every unfinished item is started again on boot and
// whenever TorrServer comes back. One global switch stops everything (the list
// is kept) and resumes it later from where the pieces on disk end.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type precacheItem struct {
	Hash  string `json:"hash"`
	Index int    `json:"index"`
	Title string `json:"title,omitempty"`
	Added int64  `json:"added"`
	Done  bool   `json:"done"`
	// Error is the last start failure, for the phone page; cleared on success.
	Error string `json:"error,omitempty"`
}

type precacheFile struct {
	Enabled bool            `json:"enabled"`
	Items   []*precacheItem `json:"items"`
}

type Precache struct {
	ts   string // TorrServer base URL
	path string // JSON file; "" = memory only

	mu      sync.Mutex
	wg      sync.WaitGroup
	enabled bool
	items   map[string]*precacheItem      // by hash
	running map[string]context.CancelFunc // hash -> cancels the start goroutine

	// test hooks
	retryDelay time.Duration // wait after a failed /stream request
	pollDelay  time.Duration // interval while TorrServer already preloads (stat 2) and we only watch
	client     *http.Client
}

func newPrecache(cfg Config) *Precache {
	p := &Precache{
		ts:         cfg.TorrServer,
		enabled:    true,
		items:      map[string]*precacheItem{},
		running:    map[string]context.CancelFunc{},
		retryDelay: 30 * time.Second,
		pollDelay:  10 * time.Second,
		client:     &http.Client{}, // no timeout: the preload request lasts until the download ends
	}
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			log.Printf("precache: cannot create %s: %v (list kept in memory only)", cfg.DataDir, err)
		} else {
			p.path = filepath.Join(cfg.DataDir, "precache.json")
		}
	} else {
		log.Printf("precache: TORR_DATA_DIR not set, list kept in memory only (lost on restart)")
	}
	p.load()
	return p
}

func (p *Precache) load() {
	if p.path == "" {
		return
	}
	b, err := os.ReadFile(p.path)
	if err != nil {
		return
	}
	var f precacheFile
	if err := json.Unmarshal(b, &f); err != nil {
		log.Printf("precache: %s unreadable: %v", p.path, err)
		return
	}
	p.enabled = f.Enabled
	for _, it := range f.Items {
		if it != nil && it.Hash != "" {
			p.items[it.Hash] = it
		}
	}
}

// saveLocked writes the file; caller holds p.mu.
func (p *Precache) saveLocked() {
	if p.path == "" {
		return
	}
	b, _ := json.MarshalIndent(p.snapshotLocked(), "", "  ")
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		os.Rename(tmp, p.path)
	}
}

func (p *Precache) snapshotLocked() precacheFile {
	out := precacheFile{Enabled: p.enabled, Items: make([]*precacheItem, 0, len(p.items))}
	for _, it := range p.items {
		c := *it
		out.Items = append(out.Items, &c)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Added > out.Items[j].Added })
	return out
}

func (p *Precache) snapshot() precacheFile {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

// resume starts every unfinished item if the switch is on. Called once at boot.
func (p *Precache) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return
	}
	for _, it := range p.items {
		if !it.Done {
			p.startLocked(it)
		}
	}
}

// add registers hash/index and starts it when the switch is on. Re-adding a
// finished item restarts it (pieces may have been evicted since).
func (p *Precache) add(hash string, index int, title string) *precacheItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.items[hash]
	if it == nil {
		it = &precacheItem{Hash: hash, Added: time.Now().Unix()}
		p.items[hash] = it
	}
	it.Index = index
	if title != "" {
		it.Title = title
	}
	it.Done = false
	it.Error = ""
	if p.enabled {
		p.startLocked(it)
	}
	p.saveLocked()
	c := *it
	return &c
}

// remove forgets the item and tells TorrServer to stop its preload.
func (p *Precache) remove(hash string) bool {
	p.mu.Lock()
	it, ok := p.items[hash]
	if ok {
		delete(p.items, hash)
	}
	p.stopLocked(hash)
	p.saveLocked()
	p.mu.Unlock()
	if ok && !it.Done {
		p.preloadStop(hash)
	}
	return ok
}

// setEnabled flips the global switch: off stops every running preload (items
// are kept), on starts every unfinished item again.
func (p *Precache) setEnabled(on bool) {
	p.mu.Lock()
	p.enabled = on
	var stop []string
	for h, it := range p.items {
		if on {
			if !it.Done {
				p.startLocked(it)
			}
		} else {
			p.stopLocked(h)
			if !it.Done {
				stop = append(stop, h)
			}
		}
	}
	p.saveLocked()
	p.mu.Unlock()
	for _, h := range stop {
		p.preloadStop(h)
	}
}

func (p *Precache) isEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enabled
}

// startLocked launches the goroutine for it unless one is already running.
func (p *Precache) startLocked(it *precacheItem) {
	if _, running := p.running[it.Hash]; running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.running[it.Hash] = cancel
	p.wg.Add(1)
	go p.run(ctx, it.Hash, it.Index)
}

// close cancels every running goroutine and waits for them to exit (tests;
// in production the process just ends).
func (p *Precache) close() {
	p.mu.Lock()
	for h, cancel := range p.running {
		cancel()
		delete(p.running, h)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// stopLocked cancels the goroutine (it does not talk to TorrServer).
func (p *Precache) stopLocked(hash string) {
	if cancel, ok := p.running[hash]; ok {
		cancel()
		delete(p.running, hash)
	}
}

// run drives one item until it is done, cancelled, or removed. The /stream
// request only returns when TorrServer's preload ends: normally (stat 3 ->
// done), by preload_stop (we cancelled: ctx done, not done), or because
// TorrServer went away (retry).
func (p *Precache) run(ctx context.Context, hash string, index int) {
	defer p.wg.Done()
	defer func() {
		p.mu.Lock()
		if cancel, ok := p.running[hash]; ok {
			cancel()
			delete(p.running, hash)
		}
		p.mu.Unlock()
	}()
	for {
		st, err := p.preloadRequest(ctx, hash, index)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err == nil && st == 2:
			// TorrServer was already preloading this one (e.g. we restarted while it
			// kept going): watch it until the state changes.
			st, err = p.watch(ctx, hash)
			if ctx.Err() != nil {
				return
			}
		}
		if err == nil && st == 3 {
			// Preload also returns early when the torrent is dropped under it
			// (SetSettings drops all torrents, "torrent closed"), so trust the
			// file's own byte count when TorrServer reports it (fork >= 2026-11-08).
			done, why := p.fileComplete(ctx, hash, index)
			if ctx.Err() != nil {
				return
			}
			if done {
				p.finish(hash, "")
				return
			}
			st, err = 0, errors.New(why)
		}
		// The preload also ends when the user removes the torrent. Retrying the
		// /stream request then would silently re-add it (TorrServer adds any hash
		// it does not know), which is exactly how a removed torrent kept coming
		// back: forget the item instead.
		if p.torrentGone(ctx, hash) {
			p.forget(hash)
			return
		}
		if ctx.Err() != nil {
			return
		}
		msg := "TorrServer stat " + strconv.Itoa(st)
		if err != nil {
			msg = err.Error()
		}
		p.finish(hash, msg) // keeps Done=false, records the error
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.retryDelay):
		}
	}
}

// forget drops an item whose torrent no longer exists on TorrServer.
func (p *Precache) forget(hash string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.items[hash]; !ok {
		return
	}
	delete(p.items, hash)
	p.saveLocked()
	log.Printf("precache: %s removed on TorrServer, forgotten", hash)
}

// torrentGone reports whether TorrServer answers 404 for hash ({"action":"get"}
// looks in memory and in the DB, so 404 means removed, not merely unloaded).
func (p *Precache) torrentGone(ctx context.Context, hash string) bool {
	body := strings.NewReader(`{"action":"get","hash":"` + hash + `"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.ts+"/torrents", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNotFound
}

// finish records the outcome of a run attempt. Empty msg = done.
func (p *Precache) finish(hash, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.items[hash]
	if it == nil {
		return
	}
	it.Done = msg == ""
	it.Error = msg
	p.saveLocked()
	if msg == "" {
		log.Printf("precache: done %s", hash)
	} else {
		log.Printf("precache: %s: %s (retry in %s)", hash, msg, p.retryDelay)
	}
}

// preloadRequest issues GET /stream?...&preload&stat and returns the torrent's
// stat from the JSON answer (3 = working, i.e. the preload ended).
func (p *Precache) preloadRequest(ctx context.Context, hash string, index int) (int, error) {
	q := url.Values{"link": {hash}, "index": {strconv.Itoa(index)}}
	u := p.ts + "/stream?" + q.Encode() + "&preload&stat"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var st struct {
		Stat int `json:"stat"`
	}
	if resp.StatusCode != 200 {
		return 0, errors.New("TorrServer " + resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, err
	}
	return st.Stat, nil
}

// watch polls {"action":"get"} until the torrent leaves state 2.
func (p *Precache) watch(ctx context.Context, hash string) (int, error) {
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(p.pollDelay):
		}
		st, err := p.getStat(ctx, hash)
		if err != nil {
			return 0, err
		}
		if st != 2 {
			return st, nil
		}
	}
}

// fileComplete reports whether file index of hash is fully on disk according to
// file_stats[].bytes_completed. Without that field (older TorrServer) it
// answers true so the stat-3 return keeps its old meaning.
func (p *Precache) fileComplete(ctx context.Context, hash string, index int) (bool, string) {
	body := strings.NewReader(`{"action":"get","hash":"` + hash + `"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.ts+"/torrents", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, "TorrServer " + resp.Status
	}
	var st struct {
		Stat  int `json:"stat"`
		Files []struct {
			Id             int    `json:"id"`
			Length         int64  `json:"length"`
			BytesCompleted *int64 `json:"bytes_completed"`
		} `json:"file_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return false, err.Error()
	}
	for _, f := range st.Files {
		if f.Id != index {
			continue
		}
		if f.BytesCompleted == nil {
			return true, ""
		}
		if *f.BytesCompleted >= f.Length {
			return true, ""
		}
		return false, "incomplete " + strconv.FormatInt(*f.BytesCompleted>>20, 10) + "/" + strconv.FormatInt(f.Length>>20, 10) + " MB"
	}
	if len(st.Files) == 0 {
		// torrent already unloaded (stat 5): no file info, cannot verify
		return false, "torrent unloaded before the file was verified"
	}
	return false, "file index " + strconv.Itoa(index) + " not in torrent"
}

func (p *Precache) getStat(ctx context.Context, hash string) (int, error) {
	body := strings.NewReader(`{"action":"get","hash":"` + hash + `"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.ts+"/torrents", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, errors.New("TorrServer " + resp.Status)
	}
	var st struct {
		Stat int `json:"stat"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, err
	}
	return st.Stat, nil
}

// preloadStop asks TorrServer to end the preload of hash (fork action).
func (p *Precache) preloadStop(hash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := strings.NewReader(`{"action":"preload_stop","hash":"` + hash + `"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.ts+"/torrents", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("precache: preload_stop %s: %v", hash, err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("precache: preload_stop %s: TorrServer %s (fork without preload_stop?)", hash, resp.Status)
	}
}

// ---------------------------------------------------------------- HTTP

func (a *App) precacheGet(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, r, 200, a.precache.snapshot())
}

func (a *App) precachePost(w http.ResponseWriter, r *http.Request, p string, body map[string]interface{}) {
	switch p {
	case "/precache":
		hash := strings.ToLower(str(body["hash"]))
		if !hashRe.MatchString(hash) {
			sendError(w, r, 400, "hash must be a 40-hex infohash")
			return
		}
		index := atoi(str(body["index"]), 0)
		if index <= 0 {
			sendError(w, r, 400, "index must be a file id (>= 1)")
			return
		}
		it := a.precache.add(hash, index, truncRunes(str(body["title"]), 200))
		sendJSON(w, r, 200, map[string]interface{}{"ok": true, "enabled": a.precache.isEnabled(), "item": it})
	case "/precache/stop":
		hash := strings.ToLower(str(body["hash"]))
		if !hashRe.MatchString(hash) {
			sendError(w, r, 400, "hash must be a 40-hex infohash")
			return
		}
		sendJSON(w, r, 200, map[string]interface{}{"ok": true, "removed": a.precache.remove(hash)})
	case "/precache/enabled":
		on, ok := body["enabled"].(bool)
		if !ok {
			sendError(w, r, 400, "enabled must be a boolean")
			return
		}
		a.precache.setEnabled(on)
		sendJSON(w, r, 200, map[string]interface{}{"ok": true, "enabled": on})
	default:
		sendError(w, r, 404, "not found")
	}
}
