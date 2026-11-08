package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTS mimics the parts of TorrServer the precache code talks to.
// GET /stream?...&preload&stat blocks until finish(hash) (-> stat 3) or
// {"action":"preload_stop"} (-> stat 3 as well: that is what the fork answers
// once Preload returns after StopPreload). A hash listed in "already" answers
// stat 2 at once (TorrServer was preloading it before we asked) and "get"
// reports 2 until finish.
type fakeTS struct {
	srv *httptest.Server
	mu  sync.Mutex

	preloads []string              // "hash/index" of every /stream preload request
	stops    []string              // hashes of preload_stop requests
	waiters  map[string][]chan int // hash -> blocked /stream requests
	already  map[string]bool
	down     bool
	partial  map[string]bool // "get" reports the file as incomplete
	removed  map[string]bool // torrent removed: /stream and "get" answer 404
}

func newFakeTS(t *testing.T) *fakeTS {
	f := &fakeTS{waiters: map[string][]chan int{}, already: map[string]bool{}, partial: map[string]bool{}, removed: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.down {
			f.mu.Unlock()
			hj, _ := w.(http.Hijacker)
			if hj != nil {
				c, _, _ := hj.Hijack()
				c.Close()
			}
			return
		}
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/stream":
			q := r.URL.Query()
			if _, ok := q["preload"]; !ok {
				http.Error(w, "no preload flag", 400)
				return
			}
			hash := q.Get("link")
			f.mu.Lock()
			if f.removed[hash] {
				f.mu.Unlock()
				http.Error(w, `{"error":"torrent was removed"}`, 404)
				return
			}
			f.preloads = append(f.preloads, hash+"/"+q.Get("index"))
			if f.already[hash] {
				f.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]int{"stat": 2})
				return
			}
			ch := make(chan int, 1)
			f.waiters[hash] = append(f.waiters[hash], ch)
			f.mu.Unlock()
			select {
			case st := <-ch:
				json.NewEncoder(w).Encode(map[string]int{"stat": st})
			case <-r.Context().Done():
			}
		case r.URL.Path == "/torrents":
			var b map[string]interface{}
			json.NewDecoder(r.Body).Decode(&b)
			hash, _ := b["hash"].(string)
			switch b["action"] {
			case "preload_stop":
				f.mu.Lock()
				f.stops = append(f.stops, hash)
				ws := f.waiters[hash]
				delete(f.waiters, hash)
				f.mu.Unlock()
				for _, ch := range ws {
					ch <- 3
				}
				json.NewEncoder(w).Encode(map[string]bool{"stopped": len(ws) > 0})
			case "get":
				f.mu.Lock()
				if f.removed[hash] {
					f.mu.Unlock()
					w.WriteHeader(404)
					return
				}
				st := 3
				if f.already[hash] {
					st = 2
				}
				bc := int64(1000)
				if f.partial[hash] {
					bc = 400
				}
				f.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]interface{}{"stat": st, "file_stats": []map[string]interface{}{
					{"id": 1, "length": 1000, "bytes_completed": bc}, {"id": 2, "length": 1000, "bytes_completed": bc}, {"id": 3, "length": 1000, "bytes_completed": bc}}})
			default:
				http.Error(w, "unknown action", 400)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	// Close waits for in-flight handlers: let blocked /stream requests go first.
	t.Cleanup(func() {
		f.mu.Lock()
		ws := f.waiters
		f.waiters = map[string][]chan int{}
		f.mu.Unlock()
		for _, l := range ws {
			for _, ch := range l {
				ch <- 3
			}
		}
	})
	return f
}

// remove is the user removing the torrent on TorrServer while it preloads: the
// blocked /stream request returns (TorrServer: "Error preload: torrent closed",
// stat 3) and from then on every request for the hash answers 404.
func (f *fakeTS) remove(hash string) {
	f.mu.Lock()
	f.removed[hash] = true
	ws := f.waiters[hash]
	delete(f.waiters, hash)
	f.mu.Unlock()
	for _, ch := range ws {
		ch <- 3
	}
}

// finish ends the preload of hash the normal way (download complete).
func (f *fakeTS) finish(hash string) {
	f.mu.Lock()
	ws := f.waiters[hash]
	delete(f.waiters, hash)
	delete(f.already, hash)
	f.mu.Unlock()
	for _, ch := range ws {
		ch <- 3
	}
}

func (f *fakeTS) count(list *[]string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(*list)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newPrecacheTest(t *testing.T, dir string) (*fakeTS, *Precache) {
	ts := newFakeTS(t)
	p := newPrecache(Config{TorrServer: ts.srv.URL, DataDir: dir})
	p.retryDelay = 50 * time.Millisecond
	p.pollDelay = 20 * time.Millisecond
	t.Cleanup(p.close) // runs before the fake's waiter release and TempDir removal
	p.resume()
	return ts, p
}

const hA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const hB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func item(p *Precache, hash string) *precacheItem {
	for _, it := range p.snapshot().Items {
		if it.Hash == hash {
			return it
		}
	}
	return nil
}

func TestPrecacheAddRunsPreloadUntilDone(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.add(hA, 1, "Movie A")
	waitFor(t, "preload request", func() bool { return ts.count(&ts.preloads) == 1 })
	if it := item(p, hA); it == nil || it.Done {
		t.Fatalf("item after add: %+v", it)
	}
	ts.finish(hA)
	waitFor(t, "done", func() bool { it := item(p, hA); return it != nil && it.Done })
	if ts.count(&ts.stops) != 0 {
		t.Fatal("a normally finished preload must not be stopped")
	}
	// the file reflects it
	b, _ := os.ReadFile(filepath.Join(p.path))
	if !strings.Contains(string(b), `"done": true`) {
		t.Fatalf("precache.json does not record done: %s", b)
	}
}

func TestPrecacheRemoveStopsAndIsNotDone(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.add(hA, 2, "")
	waitFor(t, "preload request", func() bool { return ts.count(&ts.preloads) == 1 })
	if !p.remove(hA) {
		t.Fatal("remove returned false")
	}
	waitFor(t, "preload_stop", func() bool { return ts.count(&ts.stops) == 1 })
	time.Sleep(150 * time.Millisecond) // past retryDelay: a stopped item must not be retried
	if item(p, hA) != nil {
		t.Fatal("removed item still listed")
	}
	if n := ts.count(&ts.preloads); n != 1 {
		t.Fatalf("preload requested %d times after remove, want 1", n)
	}
}

func TestPrecacheSwitchOffStopsAllAndOnResumes(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.add(hA, 1, "")
	p.add(hB, 1, "")
	waitFor(t, "two preloads", func() bool { return ts.count(&ts.preloads) == 2 })

	p.setEnabled(false)
	waitFor(t, "two stops", func() bool { return ts.count(&ts.stops) == 2 })
	snap := p.snapshot()
	if snap.Enabled || len(snap.Items) != 2 {
		t.Fatalf("after off: %+v", snap)
	}
	for _, it := range snap.Items {
		if it.Done {
			t.Fatalf("stopped item marked done: %+v", it)
		}
	}
	time.Sleep(150 * time.Millisecond)
	if n := ts.count(&ts.preloads); n != 2 {
		t.Fatalf("preloads restarted while switched off: %d", n)
	}
	// adding while off only registers
	p.add("cccccccccccccccccccccccccccccccccccccccc", 1, "")
	time.Sleep(50 * time.Millisecond)
	if n := ts.count(&ts.preloads); n != 2 {
		t.Fatalf("add while off started a preload: %d", n)
	}

	p.setEnabled(true)
	waitFor(t, "three preloads after on", func() bool { return ts.count(&ts.preloads) == 5 })
}

func TestPrecacheResumeAfterRestartAndWatchRunningPreload(t *testing.T) {
	dir := t.TempDir()
	ts, p := newPrecacheTest(t, dir)
	p.add(hA, 1, "A")
	p.add(hB, 3, "B")
	waitFor(t, "two preloads", func() bool { return ts.count(&ts.preloads) == 2 })
	ts.finish(hB)
	waitFor(t, "B done", func() bool { it := item(p, hB); return it != nil && it.Done })

	// "sidecar restart": a new Precache over the same file and the same TorrServer,
	// which is still preloading A (answers stat 2 right away).
	ts.mu.Lock()
	ts.already[hA] = true
	ts.mu.Unlock()
	p2 := newPrecache(Config{TorrServer: ts.srv.URL, DataDir: dir})
	p2.retryDelay = 50 * time.Millisecond
	p2.pollDelay = 20 * time.Millisecond
	t.Cleanup(p2.close)
	p2.resume()
	waitFor(t, "A re-requested", func() bool { return ts.count(&ts.preloads) == 3 })
	time.Sleep(100 * time.Millisecond)
	if n := ts.count(&ts.preloads); n != 3 {
		t.Fatalf("done item B was re-requested or A requested twice: %d", n)
	}
	if it := item(p2, hA); it == nil || it.Done {
		t.Fatalf("A should be watched, not done: %+v", it)
	}
	ts.finish(hA) // "get" now answers 3
	waitFor(t, "A done via watch", func() bool { it := item(p2, hA); return it != nil && it.Done })
}

// SetSettings ("drop all torrents") or a closed torrent make Preload return
// with stat 3 before the file is complete; the sidecar must not call that done.
func TestPrecacheIncompleteFileIsNotDoneAndRetries(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	ts.mu.Lock()
	ts.partial[hA] = true
	ts.mu.Unlock()
	p.add(hA, 1, "")
	waitFor(t, "preload", func() bool { return ts.count(&ts.preloads) == 1 })
	ts.finish(hA) // preload "returns" although the file is 400/1000
	waitFor(t, "incomplete recorded", func() bool {
		it := item(p, hA)
		return it != nil && !it.Done && strings.Contains(it.Error, "incomplete")
	})
	waitFor(t, "preload retried", func() bool { return ts.count(&ts.preloads) >= 2 })
	ts.mu.Lock()
	ts.partial[hA] = false
	ts.mu.Unlock()
	ts.finish(hA)
	waitFor(t, "done once complete", func() bool { it := item(p, hA); return it != nil && it.Done && it.Error == "" })
}

// The user removes a torrent while it is being pre-cached: the item must vanish
// from the list and the preload must NOT be retried (the retry would re-add the
// torrent on TorrServer, which is how a removed torrent kept coming back).
func TestPrecacheRemovedTorrentIsForgottenNotRetried(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.add(hA, 1, "")
	p.add(hB, 1, "")
	waitFor(t, "two preloads", func() bool { return ts.count(&ts.preloads) == 2 })

	ts.remove(hA)
	waitFor(t, "A forgotten", func() bool { return item(p, hA) == nil })
	time.Sleep(3 * p.retryDelay)
	if n := ts.count(&ts.preloads); n != 2 {
		t.Fatalf("removed torrent was re-requested: %d preloads", n)
	}
	if n := ts.count(&ts.stops); n != 0 {
		t.Fatalf("preload_stop sent for a removed torrent: %d", n)
	}
	// B is untouched
	if it := item(p, hB); it == nil || it.Done {
		t.Fatalf("B: %+v", it)
	}
	ts.finish(hB)
	waitFor(t, "B done", func() bool { it := item(p, hB); return it != nil && it.Done })
}

func TestPrecacheRetriesWhileTorrServerDown(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	ts.mu.Lock()
	ts.down = true
	ts.mu.Unlock()
	p.add(hA, 1, "")
	waitFor(t, "error recorded", func() bool { it := item(p, hA); return it != nil && it.Error != "" })
	ts.mu.Lock()
	ts.down = false
	ts.mu.Unlock()
	waitFor(t, "preload after recovery", func() bool { return ts.count(&ts.preloads) >= 1 })
	ts.finish(hA)
	waitFor(t, "done", func() bool { it := item(p, hA); return it != nil && it.Done && it.Error == "" })
}

func TestPrecacheHTTP(t *testing.T) {
	ts := newFakeTS(t)
	app := newApp(Config{TorrServer: ts.srv.URL, Port: "8791", AppDir: t.TempDir(), DataDir: t.TempDir()})
	t.Cleanup(app.precache.close)
	srv := httptest.NewServer(app.routes())
	t.Cleanup(srv.Close)

	resp, m := postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": strings.ToUpper(hA), "index": 2, "title": "X"})
	if resp.StatusCode != 200 || m["ok"] != true {
		t.Fatalf("POST /precache: %d %v", resp.StatusCode, m)
	}
	resp, _ = postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": "nope", "index": 1})
	if resp.StatusCode != 400 {
		t.Fatalf("bad hash accepted: %d", resp.StatusCode)
	}
	resp, _ = postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hA})
	if resp.StatusCode != 400 {
		t.Fatalf("missing index accepted: %d", resp.StatusCode)
	}
	waitFor(t, "preload", func() bool { return ts.count(&ts.preloads) == 1 })

	r, err := http.Get(srv.URL + "/precache")
	if err != nil {
		t.Fatal(err)
	}
	var f precacheFile
	json.NewDecoder(r.Body).Decode(&f)
	r.Body.Close()
	if !f.Enabled || len(f.Items) != 1 || f.Items[0].Hash != hA || f.Items[0].Index != 2 || f.Items[0].Title != "X" {
		t.Fatalf("GET /precache: %+v", f)
	}

	resp, m = postJSON(t, srv.URL+"/precache/enabled", map[string]interface{}{"enabled": false})
	if resp.StatusCode != 200 || m["enabled"] != false {
		t.Fatalf("enabled=false: %d %v", resp.StatusCode, m)
	}
	waitFor(t, "stop", func() bool { return ts.count(&ts.stops) == 1 })
	resp, _ = postJSON(t, srv.URL+"/precache/enabled", map[string]interface{}{"enabled": "yes"})
	if resp.StatusCode != 400 {
		t.Fatalf("non-bool enabled accepted: %d", resp.StatusCode)
	}

	resp, m = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hA})
	if resp.StatusCode != 200 || m["removed"] != true {
		t.Fatalf("stop: %d %v", resp.StatusCode, m)
	}
	resp, m = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hA})
	if resp.StatusCode != 200 || m["removed"] != false {
		t.Fatalf("second stop: %d %v", resp.StatusCode, m)
	}
}
