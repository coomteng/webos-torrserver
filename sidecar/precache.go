package torrremote

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

// precacheItem is one torrent with its queue of files. TorrServer preloads one
// file per torrent at a time, so the files run in order, one after the other.
// Index/Done/Error mirror the queue for clients that know one file per torrent
// (see sync): Index is the file being processed (the last one when all are done),
// Done means every file is done, Error belongs to the file being processed.
type precacheItem struct {
	Hash  string `json:"hash"`
	Index int    `json:"index"`
	Title string `json:"title,omitempty"`
	Added int64  `json:"added"`
	Done  bool   `json:"done"`
	// Error is the last start failure, for the phone page; cleared on success.
	Error string          `json:"error,omitempty"`
	Files []precacheEntry `json:"files,omitempty"` // execution order
}

type precacheEntry struct {
	Index int    `json:"index"`
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

// current is the file to process: the first one that is not done, or nil.
func (it *precacheItem) current() *precacheEntry {
	for i := range it.Files {
		if !it.Files[i].Done {
			return &it.Files[i]
		}
	}
	return nil
}

// find returns the position of index in the queue, or -1.
func (it *precacheItem) find(index int) int {
	for i := range it.Files {
		if it.Files[i].Index == index {
			return i
		}
	}
	return -1
}

// sync recomputes the mirror fields; call it after every change to Files.
func (it *precacheItem) sync() {
	if cur := it.current(); cur != nil {
		it.Index, it.Done, it.Error = cur.Index, false, cur.Error
	} else if n := len(it.Files); n > 0 {
		it.Index, it.Done, it.Error = it.Files[n-1].Index, true, ""
	}
}

// clone copies the item including Files (copies are encoded outside the lock).
func (it *precacheItem) clone() *precacheItem {
	c := *it
	c.Files = append([]precacheEntry(nil), it.Files...)
	return &c
}

type precacheFile struct {
	Enabled bool            `json:"enabled"`
	Items   []*precacheItem `json:"items"`
}

// precacheRun is the goroutine working on one torrent. The pointer is its
// identity: a run only unregisters itself while p.running[hash] is still it.
type precacheRun struct {
	cancel context.CancelFunc
	skip   chan struct{} // cap 1: the file being processed left the front of the queue
	index  int           // file being processed; guarded by p.mu
}

type Precache struct {
	ts     string        // TorrServer base URL
	tsFunc func() string // overrides ts when set (embedded: loopback port known late)
	path   string        // JSON file; "" = memory only

	mu      sync.Mutex
	wg      sync.WaitGroup
	enabled bool
	items   map[string]*precacheItem // by hash
	running map[string]*precacheRun  // hash -> its goroutine

	// test hooks
	retryDelay time.Duration // wait after a failed /stream request
	pollDelay  time.Duration // interval while TorrServer already preloads (stat 2) and we only watch
	stopGrace  time.Duration // wait for the /stream request to return after preload_stop
	client     *http.Client
	// exitHook, when set, is called by a run that found no file left, right
	// before it unregisters (p.mu held). nil outside tests.
	exitHook func(hash string)
}

func newPrecache(cfg Config) *Precache {
	ts := cfg.InternalTorrServer
	if ts == "" {
		ts = cfg.TorrServer
	}
	p := &Precache{
		ts:         ts,
		tsFunc:     cfg.InternalTorrServerFunc,
		enabled:    true,
		items:      map[string]*precacheItem{},
		running:    map[string]*precacheRun{},
		retryDelay: 30 * time.Second,
		pollDelay:  10 * time.Second,
		stopGrace:  10 * time.Second,
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
		if it == nil || it.Hash == "" {
			continue
		}
		// a file written before the queue existed names one file per item
		files := it.Files
		if len(files) == 0 {
			files = []precacheEntry{{Index: it.Index, Done: it.Done, Error: it.Error}}
		}
		it.Files = nil
		for _, e := range files {
			if e.Index >= 1 && it.find(e.Index) < 0 {
				it.Files = append(it.Files, e)
			}
		}
		if len(it.Files) == 0 {
			continue
		}
		it.sync()
		p.items[it.Hash] = it
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
		out.Items = append(out.Items, it.clone())
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

// add makes index the only file of hash (the one-file-per-torrent call) and
// starts it when the switch is on. Re-adding a finished item restarts it
// (pieces may have been evicted since).
func (p *Precache) add(hash string, index int, title string) *precacheItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.items[hash]
	if it == nil {
		it = &precacheItem{Hash: hash, Added: time.Now().Unix()}
		p.items[hash] = it
	}
	if title != "" {
		it.Title = title
	}
	it.Files = []precacheEntry{{Index: index}}
	it.sync()
	p.kickLocked(it) // a run on another file moves over; one on this file goes on
	if p.enabled {
		p.startLocked(it)
	}
	p.saveLocked()
	return it.clone()
}

// addFiles appends indexes to the queue of hash (files already waiting keep
// their place, finished ones are queued again at the end) and starts the
// torrent when the switch is on. The file being processed is not disturbed.
// title only names an item that has no title yet.
func (p *Precache) addFiles(hash string, indexes []int, title string) *precacheItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.items[hash]
	if it == nil {
		it = &precacheItem{Hash: hash, Added: time.Now().Unix()}
	}
	if title != "" && it.Title == "" {
		it.Title = title // appending keeps the title the item has
	}
	for _, index := range indexes {
		if index < 1 {
			continue
		}
		if i := it.find(index); i >= 0 {
			if !it.Files[i].Done {
				continue
			}
			it.Files = append(it.Files[:i], it.Files[i+1:]...)
		}
		it.Files = append(it.Files, precacheEntry{Index: index})
	}
	if len(it.Files) == 0 {
		return nil
	}
	p.items[hash] = it
	it.sync()
	if p.enabled && !it.Done {
		p.startLocked(it)
	}
	p.saveLocked()
	return it.clone()
}

// removeFiles drops indexes from the queue of hash. A waiting file just goes;
// dropping the file being processed stops its preload and moves on to the next
// one; an item left without files is removed like remove does. With pending
// only files that are not done are dropped (a caller working from an older
// snapshot must not erase the mark of a file that finished meanwhile), and nil
// indexes then mean every such file of hash. It returns whether anything was
// dropped and what is left of the item (nil = nothing).
func (p *Precache) removeFiles(hash string, indexes []int, pending bool) (bool, *precacheItem) {
	p.mu.Lock()
	it := p.items[hash]
	if it == nil {
		p.mu.Unlock()
		return false, nil
	}
	if pending && indexes == nil {
		for _, e := range it.Files {
			if !e.Done {
				indexes = append(indexes, e.Index)
			}
		}
	}
	wasDone, removed := it.Done, false
	for _, index := range indexes {
		if i := it.find(index); i >= 0 && !(pending && it.Files[i].Done) {
			it.Files = append(it.Files[:i], it.Files[i+1:]...)
			removed = true
		}
	}
	if !removed {
		c := it.clone()
		p.mu.Unlock()
		return false, c
	}
	if len(it.Files) == 0 {
		delete(p.items, hash)
		p.stopLocked(hash)
		p.saveLocked()
		p.mu.Unlock()
		if !wasDone {
			p.preloadStop(hash)
		}
		return true, nil
	}
	it.sync()
	p.kickLocked(it)
	p.saveLocked()
	c := it.clone()
	p.mu.Unlock()
	return true, c
}

// kickLocked tells the run of it, if any, that the file it is processing is no
// longer the one to process; the run sends preload_stop and picks again.
func (p *Precache) kickLocked(it *precacheItem) {
	r := p.running[it.Hash]
	if r == nil {
		return
	}
	if cur := it.current(); cur != nil && cur.Index == r.index {
		return
	}
	select {
	case r.skip <- struct{}{}:
	default:
	}
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
	r := &precacheRun{cancel: cancel, skip: make(chan struct{}, 1)}
	p.running[it.Hash] = r
	p.wg.Add(1)
	go p.run(ctx, it.Hash, r)
}

// close cancels every running goroutine and waits for them to exit (tests;
// in production the process just ends).
func (p *Precache) close() {
	p.mu.Lock()
	for h, r := range p.running {
		r.cancel()
		delete(p.running, h)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// stopLocked cancels the goroutine (it does not talk to TorrServer).
func (p *Precache) stopLocked(hash string) {
	if r, ok := p.running[hash]; ok {
		r.cancel()
		delete(p.running, hash)
	}
}

// run drives one item, file after file in queue order, until every file is
// done or it is cancelled or removed. The /stream request only returns when
// TorrServer's preload ends: normally (stat 3 -> done), by preload_stop (we
// cancelled: ctx done, not done; or the file left the queue: next file), or
// because TorrServer went away (retry).
func (p *Precache) run(ctx context.Context, hash string, r *precacheRun) {
	defer p.wg.Done()
	defer func() {
		p.mu.Lock()
		if p.running[hash] == r {
			delete(p.running, hash)
		}
		p.mu.Unlock()
		r.cancel()
	}()
	for {
		// Pick the file and, when there is none, unregister in the same critical
		// section: whoever appends a file either finds no run and starts one, or
		// finds this one before it picks.
		p.mu.Lock()
		if p.running[hash] != r {
			p.mu.Unlock()
			return
		}
		var cur *precacheEntry
		if it := p.items[hash]; it != nil {
			cur = it.current()
		}
		if cur == nil {
			if p.exitHook != nil {
				p.exitHook(hash)
			}
			delete(p.running, hash)
			r.cancel()
			p.mu.Unlock()
			return
		}
		index := cur.Index
		r.index = index
		select {
		case <-r.skip: // meant for the previous file
		default:
		}
		p.mu.Unlock()

		st, err, skipped := p.preload(ctx, hash, index, r)
		if ctx.Err() != nil {
			return
		}
		if skipped {
			continue
		}
		switch {
		case err == nil && st == 2:
			// TorrServer is busy preloading a file of this torrent and did not take
			// our request: usually the previous file of the queue still unwinding,
			// or a preload from before we restarted. Watch until that ends. Its end
			// says nothing about our file, so ask for our own file then.
			_, err, skipped = p.watch(ctx, hash, r)
			if ctx.Err() != nil {
				return
			}
			if skipped {
				p.preloadStop(hash)
				p.waitIdle(ctx, hash)
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if err == nil {
				continue
			}
			st = 0
		}
		if err == nil && st == 3 {
			// Preload also returns early when the torrent is dropped under it
			// (SetSettings drops all torrents, "torrent closed"), so trust the
			// file's own byte count when TorrServer reports it (fork >= 2026-11-08).
			done, why, absent := p.fileComplete(ctx, hash, index)
			if ctx.Err() != nil {
				return
			}
			if done {
				p.finish(hash, index, "", r)
				continue
			}
			if absent {
				// can never complete: do not hold up the files behind it
				p.dropFile(hash, index, r)
				continue
			}
			st, err = 0, errors.New(why)
		}
		// The preload also ends when the user removes the torrent. Retrying the
		// /stream request then would silently re-add it (TorrServer adds any hash
		// it does not know), which is exactly how a removed torrent kept coming
		// back: forget the item instead.
		if p.torrentGone(ctx, hash) {
			p.forget(hash, r)
			return
		}
		if ctx.Err() != nil {
			return
		}
		msg := "TorrServer stat " + strconv.Itoa(st)
		if err != nil {
			msg = err.Error()
		}
		p.finish(hash, index, msg, r) // keeps Done=false, records the error
		select {
		case <-ctx.Done():
			return
		case <-r.skip:
		case <-time.After(p.retryDelay):
		}
	}
}

// preload issues the /stream request for index and waits for it. When the file
// leaves the front of the queue meanwhile (r.skip) it sends preload_stop and
// waits for the request to return before reporting skipped: TorrServer answers
// preload_stop at once but only answers /stream after its Preload has unwound,
// and a preload of the same torrent asked for before that finds it still busy.
func (p *Precache) preload(ctx context.Context, hash string, index int, r *precacheRun) (st int, err error, skipped bool) {
	type result struct {
		st  int
		err error
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan result, 1)
	go func() {
		st, err := p.preloadRequest(cctx, hash, index)
		ch <- result{st, err}
	}()
	select {
	case res := <-ch:
		return res.st, res.err, false
	case <-r.skip:
	}
	p.preloadStop(hash)
	grace := time.NewTimer(p.stopGrace)
	defer grace.Stop()
	select {
	case <-ch:
		return 0, nil, true
	case <-grace.C:
		log.Printf("precache: %s file %d: preload still running %s after preload_stop, dropping the request", hash, index, p.stopGrace)
	}
	cancel()
	<-ch
	return 0, nil, true
}

// base is the TorrServer URL for internal calls.
func (p *Precache) base() string {
	if p.tsFunc != nil {
		if u := strings.TrimRight(p.tsFunc(), "/"); u != "" {
			return u
		}
	}
	return p.ts
}

// forget drops an item whose torrent no longer exists on TorrServer, unless
// run r was replaced meanwhile (the item was removed, or removed and added again).
func (p *Precache) forget(hash string, r *precacheRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running[hash] != r {
		return
	}
	delete(p.running, hash)
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.base()+"/torrents", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNotFound
}

// dropFile takes file index, which the torrent does not have, out of the queue
// (the item goes with its last file), unless run r was replaced meanwhile.
func (p *Precache) dropFile(hash string, index int, r *precacheRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.items[hash]
	if p.running[hash] != r || it == nil {
		return
	}
	i := it.find(index)
	if i < 0 {
		return
	}
	it.Files = append(it.Files[:i], it.Files[i+1:]...)
	if len(it.Files) == 0 {
		delete(p.items, hash) // the run finds no item and exits
	} else {
		it.sync()
	}
	p.saveLocked()
	log.Printf("precache: %s file %d is not in the torrent, dropped from the queue", hash, index)
}

// finish records the outcome of run r's attempt on file index. Empty msg =
// done. It does nothing when the file left the queue meanwhile or when r is no
// longer the run of hash (the item was removed and added again under it).
func (p *Precache) finish(hash string, index int, msg string, r *precacheRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running[hash] != r {
		return
	}
	it := p.items[hash]
	if it == nil {
		return
	}
	i := it.find(index)
	if i < 0 {
		return
	}
	it.Files[i].Done = msg == ""
	it.Files[i].Error = msg
	it.sync()
	p.saveLocked()
	if msg == "" {
		log.Printf("precache: done %s file %d", hash, index)
	} else {
		log.Printf("precache: %s file %d: %s (retry in %s)", hash, index, msg, p.retryDelay)
	}
}

// preloadRequest issues GET /stream?...&preload&stat and returns the torrent's
// stat from the JSON answer (3 = working, i.e. the preload ended).
func (p *Precache) preloadRequest(ctx context.Context, hash string, index int) (int, error) {
	q := url.Values{"link": {hash}, "index": {strconv.Itoa(index)}}
	u := p.base() + "/stream?" + q.Encode() + "&preload&stat"
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

// watch polls {"action":"get"} until the torrent leaves state 2 or the watched
// file leaves the front of the queue (skipped).
func (p *Precache) watch(ctx context.Context, hash string, r *precacheRun) (st int, err error, skipped bool) {
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err(), false
		case <-r.skip:
			return 0, nil, true
		case <-time.After(p.pollDelay):
		}
		st, err := p.getStat(ctx, hash)
		if err != nil {
			return 0, err, false
		}
		if st != 2 {
			return st, nil, false
		}
	}
}

// waitIdle waits until TorrServer no longer reports hash as preloading (stat 2),
// for at most stopGrace: preload_stop answers before the preload has unwound,
// and there is no /stream request of ours whose return would tell.
func (p *Precache) waitIdle(ctx context.Context, hash string) {
	step := p.pollDelay
	if step > time.Second {
		step = time.Second
	}
	grace := time.NewTimer(p.stopGrace)
	defer grace.Stop()
	for {
		if st, err := p.getStat(ctx, hash); err != nil || st != 2 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-grace.C:
			log.Printf("precache: %s: still preloading %s after preload_stop", hash, p.stopGrace)
			return
		case <-time.After(step):
		}
	}
}

// fileComplete reports whether file index of hash is fully on disk according to
// file_stats[].bytes_completed. Without that field (older TorrServer) it
// answers true so the stat-3 return keeps its old meaning. absent means the
// torrent lists its files and index is not one of them.
func (p *Precache) fileComplete(ctx context.Context, hash string, index int) (done bool, why string, absent bool) {
	body := strings.NewReader(`{"action":"get","hash":"` + hash + `"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.base()+"/torrents", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return false, err.Error(), false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, "TorrServer " + resp.Status, false
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
		return false, err.Error(), false
	}
	for _, f := range st.Files {
		if f.Id != index {
			continue
		}
		if f.BytesCompleted == nil {
			return true, "", false
		}
		if *f.BytesCompleted >= f.Length {
			return true, "", false
		}
		return false, "incomplete " + strconv.FormatInt(*f.BytesCompleted>>20, 10) + "/" + strconv.FormatInt(f.Length>>20, 10) + " MB", false
	}
	if len(st.Files) == 0 {
		// torrent already unloaded (stat 5): no file info, cannot verify
		return false, "torrent unloaded before the file was verified", false
	}
	return false, "file index " + strconv.Itoa(index) + " not in torrent", true
}

func (p *Precache) getStat(ctx context.Context, hash string) (int, error) {
	body := strings.NewReader(`{"action":"get","hash":"` + hash + `"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.base()+"/torrents", body)
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.base()+"/torrents", body)
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

// precacheIndexes reads body["indexes"]: a non-empty list of at most 5000 file
// ids (integers >= 1).
func precacheIndexes(v interface{}) ([]int, bool) {
	l, ok := v.([]interface{})
	if !ok || len(l) == 0 || len(l) > 5000 {
		return nil, false
	}
	out := make([]int, 0, len(l))
	for _, e := range l {
		n, ok := e.(float64)
		if !ok || n < 1 || n > 1<<31-1 || n != float64(int(n)) {
			return nil, false
		}
		out = append(out, int(n))
	}
	return out, true
}

func (a *App) precachePost(w http.ResponseWriter, r *http.Request, p string, body map[string]interface{}) {
	switch p {
	case "/precache":
		hash := strings.ToLower(str(body["hash"]))
		if !hashRe.MatchString(hash) {
			sendError(w, r, 400, "hash must be a 40-hex infohash")
			return
		}
		title := truncRunes(str(body["title"]), 200)
		var it *precacheItem
		if v, ok := body["indexes"]; ok && v != nil { // null = absent
			// queue: append to the files of this torrent
			indexes, ok := precacheIndexes(v)
			if !ok {
				sendError(w, r, 400, "indexes must be a list of 1..5000 file ids (>= 1)")
				return
			}
			it = a.precache.addFiles(hash, indexes, title)
		} else {
			index := atoi(str(body["index"]), 0)
			if index <= 0 {
				sendError(w, r, 400, "index must be a file id (>= 1)")
				return
			}
			it = a.precache.add(hash, index, title)
		}
		sendJSON(w, r, 200, map[string]interface{}{"ok": true, "enabled": a.precache.isEnabled(), "item": it})
	case "/precache/stop":
		hash := strings.ToLower(str(body["hash"]))
		if !hashRe.MatchString(hash) {
			sendError(w, r, 400, "hash must be a 40-hex infohash")
			return
		}
		// without index/indexes the whole torrent goes, else only those files;
		// pending restricts either to the files that are not done
		pending, ok := body["pending"].(bool)
		if _, has := body["pending"]; has && !ok {
			sendError(w, r, 400, "pending must be a boolean")
			return
		}
		var indexes []int
		if v, ok := body["indexes"]; ok {
			if indexes, ok = precacheIndexes(v); !ok {
				sendError(w, r, 400, "indexes must be a list of 1..5000 file ids (>= 1)")
				return
			}
		} else if v, ok := body["index"]; ok {
			index := atoi(str(v), 0)
			if index <= 0 {
				sendError(w, r, 400, "index must be a file id (>= 1)")
				return
			}
			indexes = []int{index}
		}
		if indexes == nil && !pending {
			sendJSON(w, r, 200, map[string]interface{}{"ok": true, "removed": a.precache.remove(hash)})
			return
		}
		removed, it := a.precache.removeFiles(hash, indexes, pending)
		out := map[string]interface{}{"ok": true, "removed": removed}
		if it != nil {
			out["item"] = it
		}
		sendJSON(w, r, 200, out)
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
