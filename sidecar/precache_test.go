package torrremote

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
// reports 2 until finish or until preload_stop has unwound.
//
// Like TorrServer it preloads one file per torrent: a /stream request that
// arrives while the hash still has a blocked one counts as an overlap and
// answers stat 2. finishFile(hash,index) completes one file; a hash listed in
// "strict" reports bytes_completed=length only for completed files (and a
// preload of a completed file returns at once). preload_stop answers before
// the stopped /stream request returns, and a client that drops its /stream
// request does not end the preload.
type fakeTS struct {
	srv *httptest.Server
	mu  sync.Mutex

	preloads []string                // "hash/index" of every /stream preload request
	stops    []string                // hashes of preload_stop requests
	waiters  map[string][]fakeWaiter // hash -> blocked /stream requests
	overlaps int                     // /stream requests while the hash already had one blocked
	busy     int                     // /stream requests answered stat 2 because the hash is in "already"
	strict   map[string]bool
	complete map[string]bool // "hash/index" finished by finishFile
	noStop   bool            // preload_stop answers 400 (TorrServer without the fork action)
	already  map[string]bool
	down     bool
	partial  map[string]bool // "get" reports the file as incomplete
	noBytes  map[string]bool // "get" omits bytes_completed (TorrServer before the fork reported it)
	removed  map[string]bool // torrent removed: /stream and "get" answer 404
}

type fakeWaiter struct {
	index string
	ch    chan int
}

// take removes and returns the waiters of hash (index "" = all); caller holds f.mu.
func (f *fakeTS) take(hash, index string) []fakeWaiter {
	var out, keep []fakeWaiter
	for _, w := range f.waiters[hash] {
		if index == "" || w.index == index {
			out = append(out, w)
		} else {
			keep = append(keep, w)
		}
	}
	if len(keep) == 0 {
		delete(f.waiters, hash)
	} else {
		f.waiters[hash] = keep
	}
	return out
}

func newFakeTS(t *testing.T) *fakeTS {
	f := &fakeTS{waiters: map[string][]fakeWaiter{}, already: map[string]bool{}, partial: map[string]bool{}, removed: map[string]bool{},
		strict: map[string]bool{}, complete: map[string]bool{}, noBytes: map[string]bool{}}
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
				f.busy++
				f.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]int{"stat": 2})
				return
			}
			if f.strict[hash] && f.complete[hash+"/"+q.Get("index")] {
				f.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]int{"stat": 3})
				return
			}
			if len(f.waiters[hash]) > 0 {
				f.overlaps++
				f.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]int{"stat": 2})
				return
			}
			ch := make(chan int, 1)
			f.waiters[hash] = append(f.waiters[hash], fakeWaiter{q.Get("index"), ch})
			f.mu.Unlock()
			select {
			case st := <-ch:
				json.NewEncoder(w).Encode(map[string]int{"stat": st})
			case <-r.Context().Done():
				// the client gave up; like TorrServer the preload goes on and keeps
				// the torrent busy until it finishes or is stopped
			}
		case r.URL.Path == "/torrents":
			var b map[string]interface{}
			json.NewDecoder(r.Body).Decode(&b)
			hash, _ := b["hash"].(string)
			switch b["action"] {
			case "preload_stop":
				f.mu.Lock()
				f.stops = append(f.stops, hash)
				if f.noStop {
					f.mu.Unlock()
					http.Error(w, "unknown action", 400)
					return
				}
				n := len(f.waiters[hash])
				f.mu.Unlock()
				// Like TorrServer: the answer comes at once, the preload (and with it
				// the blocked /stream request) only ends a little later.
				go func() {
					time.Sleep(30 * time.Millisecond)
					f.mu.Lock()
					ws := f.take(hash, "")
					delete(f.already, hash)
					f.mu.Unlock()
					for _, w := range ws {
						w.ch <- 3
					}
				}()
				json.NewEncoder(w).Encode(map[string]bool{"stopped": n > 0})
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
				files := []map[string]interface{}{}
				for id := 1; id <= 3; id++ {
					n := bc
					if f.strict[hash] && !f.complete[hash+"/"+strconv.Itoa(id)] {
						n = 400
					}
					file := map[string]interface{}{"id": id, "length": 1000}
					if !f.noBytes[hash] {
						file["bytes_completed"] = n
					}
					files = append(files, file)
				}
				f.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]interface{}{"stat": st, "file_stats": files})
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
		f.waiters = map[string][]fakeWaiter{}
		f.mu.Unlock()
		for _, l := range ws {
			for _, w := range l {
				w.ch <- 3
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
	ws := f.take(hash, "")
	f.mu.Unlock()
	for _, w := range ws {
		w.ch <- 3
	}
}

// finish ends the preload of hash the normal way (download complete).
func (f *fakeTS) finish(hash string) {
	f.mu.Lock()
	ws := f.take(hash, "")
	delete(f.already, hash)
	f.mu.Unlock()
	for _, w := range ws {
		w.ch <- 3
	}
}

// finishFile completes one file: its blocked /stream request returns and
// "get" reports it fully downloaded from now on.
func (f *fakeTS) finishFile(hash string, index int) {
	f.mu.Lock()
	f.complete[hash+"/"+strconv.Itoa(index)] = true
	ws := f.take(hash, strconv.Itoa(index))
	f.mu.Unlock()
	for _, w := range ws {
		w.ch <- 3
	}
}

// restart is TorrServer restarting: every running preload is forgotten.
func (f *fakeTS) restart() {
	f.mu.Lock()
	ws := f.waiters
	f.waiters = map[string][]fakeWaiter{}
	f.mu.Unlock()
	for _, l := range ws {
		for _, w := range l {
			w.ch <- 3
		}
	}
}

// requested returns the "hash/index" preload requests so far, in order.
func (f *fakeTS) requested() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.preloads, " ")
}

func (f *fakeTS) numOverlaps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overlaps
}

func (f *fakeTS) numBusy() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy
}

func (f *fakeTS) setAlready(hash string) {
	f.mu.Lock()
	f.already[hash] = true
	f.mu.Unlock()
}

func (f *fakeTS) setStrict(hash string) {
	f.mu.Lock()
	f.strict[hash] = true
	f.mu.Unlock()
}

func (f *fakeTS) count(list *[]string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(*list)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
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
	p.stopGrace = 2 * time.Second
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
	ts.finish(hA) // "get" now answers 3: TorrServer is free again
	// the end of a preload we only watched is not the end of ours: A is asked for again
	waitFor(t, "A requested after the watch", func() bool { return ts.count(&ts.preloads) == 4 })
	if it := item(p2, hA); it == nil || it.Done {
		t.Fatalf("A done before its own preload returned: %+v", it)
	}
	ts.finish(hA)
	waitFor(t, "A done", func() bool { it := item(p2, hA); return it != nil && it.Done })
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
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)

	resp, m := postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": strings.ToUpper(hA), "index": 2, "title": "X"})
	if resp.StatusCode != 200 || m["ok"] != true {
		t.Fatalf("POST /precache: %d %v", resp.StatusCode, m)
	}
	// what a client that knows one file per torrent reads
	if it, _ := m["item"].(map[string]interface{}); it["index"] != float64(2) || it["done"] != false || it["hash"] != hA {
		t.Fatalf("POST /precache item: %v", m["item"])
	}
	// index as a string, and indexes:null (a client serialising an absent list) next to it
	resp, m = postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hA, "index": "2"})
	if it, _ := m["item"].(map[string]interface{}); resp.StatusCode != 200 || it["index"] != float64(2) {
		t.Fatalf("POST /precache index \"2\": %d %v", resp.StatusCode, m)
	}
	resp, m = postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hA, "index": 2, "indexes": nil})
	if it, _ := m["item"].(map[string]interface{}); resp.StatusCode != 200 || it["index"] != float64(2) {
		t.Fatalf("POST /precache index 2, indexes null: %d %v", resp.StatusCode, m)
	}
	resp, _ = postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hA, "indexes": nil})
	if resp.StatusCode != 400 {
		t.Fatalf("indexes null without index accepted: %d", resp.StatusCode)
	}
	resp, m = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hB, "index": 1})
	if _, hasItem := m["item"]; resp.StatusCode != 200 || m["removed"] != false || hasItem {
		t.Fatalf("stop index on an unknown hash: %d %v", resp.StatusCode, m)
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

	// queue API (the switch is off: nothing starts)
	resp, m = postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hA, "indexes": []int{2, 3, 2, 3, 3}, "index": 7})
	it, _ := m["item"].(map[string]interface{})
	fl, _ := it["files"].([]interface{})
	if resp.StatusCode != 200 || m["ok"] != true || m["enabled"] != false || len(fl) != 2 || it["index"] != float64(2) {
		t.Fatalf("POST /precache indexes: %d %v", resp.StatusCode, m)
	}
	big := make([]int, 5001)
	for i := range big {
		big[i] = i + 1
	}
	for name, bad := range map[string]interface{}{"zero": []int{0}, "string": "x", "empty": []int{}, "fraction": []float64{1.5},
		"string element": []string{"1"}, "too many": big} {
		resp, _ = postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hA, "indexes": bad, "index": 1})
		if resp.StatusCode != 400 {
			t.Fatalf("indexes %s accepted: %d", name, resp.StatusCode)
		}
		resp, _ = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hA, "indexes": bad})
		if resp.StatusCode != 400 {
			t.Fatalf("stop indexes %s accepted: %d", name, resp.StatusCode)
		}
	}
	resp, _ = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hA, "index": 0})
	if resp.StatusCode != 400 {
		t.Fatalf("stop index 0 accepted: %d", resp.StatusCode)
	}
	resp, m = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hA, "index": 3})
	it, _ = m["item"].(map[string]interface{})
	fl, _ = it["files"].([]interface{})
	if resp.StatusCode != 200 || m["removed"] != true || len(fl) != 1 {
		t.Fatalf("stop index 3: %d %v", resp.StatusCode, m)
	}
	resp, m = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hA, "indexes": []int{9}})
	if resp.StatusCode != 200 || m["removed"] != false {
		t.Fatalf("stop unknown file: %d %v", resp.StatusCode, m)
	}
	r, err = http.Get(srv.URL + "/precache")
	if err != nil {
		t.Fatal(err)
	}
	f = precacheFile{}
	json.NewDecoder(r.Body).Decode(&f)
	r.Body.Close()
	if len(f.Items) != 1 || len(f.Items[0].Files) != 1 || f.Items[0].Files[0].Index != 2 || f.Items[0].Files[0].Done || f.Items[0].Index != 2 {
		t.Fatalf("GET /precache after dropping a file: %+v", f.Items)
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

// A run that was stopped must not unregister (or cancel) the run started right
// after it for the same hash.
func TestPrecacheCleanupKeepsNewRun(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	ts.setStrict(hA)
	p.add(hA, 1, "")
	waitFor(t, "preload", func() bool { return ts.count(&ts.preloads) == 1 })
	p.mu.Lock()
	p.stopLocked(hA)
	p.startLocked(p.items[hA])
	p.mu.Unlock()
	time.Sleep(100 * time.Millisecond) // the old run's cleanup happens meanwhile
	p.mu.Lock()
	_, registered := p.running[hA]
	p.mu.Unlock()
	if !registered {
		t.Fatal("the old run's cleanup unregistered the new run")
	}
	ts.finishFile(hA, 1)
	waitFor(t, "done", func() bool { it := item(p, hA); return it != nil && it.Done })
}

// files renders the queue of hash in order: "1+ 2 3!" = file 1 done, 2 waiting,
// 3 with an error; "-" when the item does not exist.
func files(p *Precache, hash string) string {
	it := item(p, hash)
	if it == nil {
		return "-"
	}
	var out []string
	for _, f := range it.Files {
		s := strconv.Itoa(f.Index)
		if f.Done {
			s += "+"
		}
		if f.Error != "" {
			s += "!"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// A precache.json written before the queue existed has one file per item.
func TestPrecacheLoadUpgradesLegacyFile(t *testing.T) {
	dir := t.TempDir()
	old := `{"enabled": false, "items": [
		{"hash": "` + hA + `", "index": 2, "title": "A", "added": 5, "done": true},
		{"hash": "` + hB + `", "index": 3, "added": 6, "done": false, "error": "boom"},
		{"hash": "cccccccccccccccccccccccccccccccccccccccc", "index": 9, "added": 7, "done": false,
		 "files": [{"index": 1, "done": true}, {"index": 2, "done": false}, {"index": 1, "done": false}, {"index": 0, "done": false}, {"index": 4, "done": false}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "precache.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	_, p := newPrecacheTest(t, dir)
	if got := files(p, hA); got != "2+" {
		t.Fatalf("A files = %q, want 2+", got)
	}
	if it := item(p, hA); it.Index != 2 || !it.Done || it.Title != "A" || it.Added != 5 {
		t.Fatalf("A: %+v", it)
	}
	if got := files(p, hB); got != "3!" {
		t.Fatalf("B files = %q, want 3!", got)
	}
	if it := item(p, hB); it.Index != 3 || it.Done || it.Error != "boom" {
		t.Fatalf("B: %+v", it)
	}
	// duplicates and indexes < 1 are dropped; the mirrors follow the queue
	c := "cccccccccccccccccccccccccccccccccccccccc"
	if got := files(p, c); got != "1+ 2 4" {
		t.Fatalf("C files = %q, want 1+ 2 4", got)
	}
	if it := item(p, c); it.Index != 2 || it.Done {
		t.Fatalf("C: %+v", it)
	}
}

// Copies handed out are encoded to JSON outside the lock: they must not share
// the Files array with the live item.
func TestPrecacheSnapshotIsDeepCopy(t *testing.T) {
	_, p := newPrecacheTest(t, t.TempDir())
	p.setEnabled(false)
	c := p.add(hA, 1, "")
	if len(c.Files) != 1 {
		t.Fatalf("add returned %+v", c)
	}
	c.Files[0].Done = true
	s := item(p, hA)
	if s.Files[0].Done {
		t.Fatal("add's copy shares Files with the live item")
	}
	s.Files[0].Done = true
	s.Files[0].Index = 7
	if got := files(p, hA); got != "1" {
		t.Fatalf("snapshot shares Files with the live item: %q", got)
	}
}

// THE regression: switching to another file of the same torrent while the first
// one preloads must stop the first, preload the second, and only call the item
// done when the second file is.
func TestPrecacheLegacyAddSwitchesFile(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	ts.setStrict(hA)
	p.add(hA, 1, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	p.add(hA, 2, "")
	waitFor(t, "preload of file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })
	if n := ts.count(&ts.stops); n != 1 {
		t.Fatalf("preload_stop sent %d times, want 1", n)
	}
	time.Sleep(3 * p.retryDelay)
	if got := ts.requested(); got != hA+"/1 "+hA+"/2" {
		t.Fatalf("preloads = %q", got)
	}
	if n := ts.numOverlaps(); n != 0 {
		t.Fatalf("file 2 requested while file 1 was still preloading (%d overlaps)", n)
	}
	if it := item(p, hA); it == nil || it.Index != 2 || it.Done || it.Error != "" || files(p, hA) != "2" {
		t.Fatalf("after switch: %+v", it)
	}
	ts.finishFile(hA, 2)
	waitFor(t, "done", func() bool { it := item(p, hA); return it != nil && it.Done && it.Index == 2 })
	if n := ts.count(&ts.stops); n != 1 {
		t.Fatalf("preload_stop sent %d times, want 1", n)
	}
}

func TestPrecacheQueueRunsOneAtATime(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	ts.setStrict(hA)
	if it := p.addFiles(hA, []int{1, 2, 3, 2}, "Show"); it == nil || len(it.Files) != 3 || it.Index != 1 || it.Title != "Show" {
		t.Fatalf("addFiles returned %+v", it)
	}
	want := hA + "/1"
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == want })
	time.Sleep(3 * p.retryDelay)
	if got := ts.requested(); got != want {
		t.Fatalf("more than the first file requested: %q", got)
	}
	if got := files(p, hA); got != "1 2 3" {
		t.Fatalf("files = %q", got)
	}
	for i, state := range []string{"1+ 2 3", "1+ 2+ 3", "1+ 2+ 3+"} {
		ts.finishFile(hA, i+1)
		waitFor(t, "files "+state, func() bool { return files(p, hA) == state })
		it := item(p, hA)
		if i < 2 {
			want += " " + hA + "/" + strconv.Itoa(i+2)
			waitFor(t, "next preload", func() bool { return ts.requested() == want })
			if it.Done || it.Index != i+2 {
				t.Fatalf("after file %d: %+v", i+1, it)
			}
		} else if !it.Done || it.Index != 3 || it.Error != "" {
			t.Fatalf("at the end: %+v", it)
		}
	}
	time.Sleep(3 * p.retryDelay)
	if got := ts.requested(); got != want {
		t.Fatalf("preloads = %q, want %q", got, want)
	}
	if ts.count(&ts.stops) != 0 || ts.numOverlaps() != 0 {
		t.Fatalf("stops=%d overlaps=%d, want 0/0", ts.count(&ts.stops), ts.numOverlaps())
	}
	b, _ := os.ReadFile(p.path)
	if !strings.Contains(string(b), `"files"`) {
		t.Fatalf("precache.json has no files: %s", b)
	}
}

// Appending a file at the very moment the run finishes its last one must not
// leave the new file without a run.
func TestPrecacheAppendWhileFinishingStarts(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	for i := 0; i < 50; i++ {
		h := strings.Repeat("c", 38) + strconv.Itoa(10+i)
		ts.setStrict(h)
		p.addFiles(h, []int{1}, "")
		waitFor(t, "preload of file 1", func() bool { return strings.Contains(ts.requested(), h+"/1") })
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); ts.finishFile(h, 1) }()
		go func() {
			defer wg.Done()
			// append right when the run marks file 1 done, i.e. while it is leaving
			for it := item(p, h); !it.Done && i%5 != 0; it = item(p, h) {
				runtime.Gosched()
			}
			p.addFiles(h, []int{2}, "")
		}()
		wg.Wait()
		waitFor(t, "preload of the appended file", func() bool { return strings.Contains(ts.requested(), h+"/2") })
		ts.finishFile(h, 2)
		waitFor(t, "all done", func() bool { return files(p, h) == "1+ 2+" })
	}
	if n := ts.numOverlaps(); n != 0 {
		t.Fatalf("%d overlapping preloads", n)
	}
}

// Asking again for a finished file queues it again, behind the waiting ones.
func TestPrecacheRequeueDoneFile(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.addFiles(hA, []int{1, 2}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.finishFile(hA, 1)
	waitFor(t, "preload of file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })
	if it := p.addFiles(hA, []int{1, 2}, ""); it.Index != 2 || it.Done {
		t.Fatalf("after re-adding: %+v", it)
	}
	if got := files(p, hA); got != "2 1" {
		t.Fatalf("files = %q, want 2 1", got)
	}
	ts.finishFile(hA, 2)
	waitFor(t, "file 1 again", func() bool { return ts.requested() == hA+"/1 "+hA+"/2 "+hA+"/1" })
	ts.finishFile(hA, 1)
	waitFor(t, "done", func() bool { return files(p, hA) == "2+ 1+" && item(p, hA).Done })
	// a finished torrent has no run left: adding to it starts one
	p.addFiles(hA, []int{3}, "")
	waitFor(t, "file 3", func() bool { return strings.HasSuffix(ts.requested(), hA+"/3") })
	if it := item(p, hA); it.Done || it.Index != 3 {
		t.Fatalf("after appending to a finished item: %+v", it)
	}
	if ts.count(&ts.stops) != 0 || ts.numOverlaps() != 0 {
		t.Fatalf("stops=%d overlaps=%d, want 0/0", ts.count(&ts.stops), ts.numOverlaps())
	}
}

func TestPrecacheRemoveFile(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.retryDelay = time.Hour // moving on must not depend on the retry timer
	ts.setStrict(hA)
	p.addFiles(hA, []int{1, 2, 3}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })

	// a waiting file just leaves the queue
	removed, it := p.removeFiles(hA, []int{2, 9}, false)
	if !removed || it == nil || files(p, hA) != "1 3" || it.Index != 1 {
		t.Fatalf("drop waiting file: removed=%v %+v", removed, it)
	}
	if removed, it = p.removeFiles(hA, []int{9}, false); removed || it == nil {
		t.Fatalf("drop unknown file: removed=%v %+v", removed, it)
	}
	time.Sleep(100 * time.Millisecond)
	if ts.count(&ts.stops) != 0 || ts.requested() != hA+"/1" {
		t.Fatalf("dropping a waiting file disturbed the preload: stops=%d preloads=%q", ts.count(&ts.stops), ts.requested())
	}

	// the file being preloaded: stop it, go on with the next one
	removed, it = p.removeFiles(hA, []int{1}, false)
	if !removed || it == nil || it.Index != 3 || it.Done {
		t.Fatalf("drop running file: removed=%v %+v", removed, it)
	}
	waitFor(t, "preload of file 3", func() bool { return ts.requested() == hA+"/1 "+hA+"/3" })
	if n := ts.count(&ts.stops); n != 1 {
		t.Fatalf("preload_stop sent %d times, want 1", n)
	}
	if n := ts.numOverlaps(); n != 0 {
		t.Fatalf("file 3 requested while file 1 was still preloading (%d overlaps)", n)
	}
	if it := item(p, hA); it.Error != "" || files(p, hA) != "3" {
		t.Fatalf("after dropping the running file: %+v", it)
	}

	// the last file: the item goes, like remove
	if removed, it = p.removeFiles(hA, []int{3}, false); !removed || it != nil {
		t.Fatalf("drop last file: removed=%v %+v", removed, it)
	}
	waitFor(t, "second preload_stop", func() bool { return ts.count(&ts.stops) == 2 })
	if item(p, hA) != nil {
		t.Fatal("item without files still listed")
	}
	p.mu.Lock()
	_, running := p.running[hA]
	p.mu.Unlock()
	if running {
		t.Fatal("run of a removed item still registered")
	}
	if removed, it = p.removeFiles(hA, []int{3}, false); removed || it != nil {
		t.Fatalf("drop from unknown item: removed=%v %+v", removed, it)
	}
	time.Sleep(100 * time.Millisecond)
	if got := ts.requested(); got != hA+"/1 "+hA+"/3" {
		t.Fatalf("preloads = %q", got)
	}
}

// A TorrServer without preload_stop never ends the old preload: after
// stopGrace the run gives the request up and still moves on (TorrServer is
// then still busy with the old file; the next one is retried like any failure).
func TestPrecacheRemoveFileWithoutPreloadStop(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.retryDelay = time.Hour
	p.stopGrace = 100 * time.Millisecond
	ts.mu.Lock()
	ts.noStop = true
	ts.strict[hA] = true
	ts.mu.Unlock()
	p.addFiles(hA, []int{1, 2}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	start := time.Now()
	p.removeFiles(hA, []int{1}, false)
	waitFor(t, "preload of file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })
	if d := time.Since(start); d < p.stopGrace {
		t.Fatalf("moved on after %s, before stopGrace %s ran out", d, p.stopGrace)
	}
	if n := ts.count(&ts.stops); n != 1 {
		t.Fatalf("preload_stop sent %d times, want 1", n)
	}
	if got := files(p, hA); !strings.HasPrefix(got, "2") || strings.Contains(got, "1") {
		t.Fatalf("files = %q, want only file 2", got)
	}
}

func TestPrecacheQueueSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ts, p := newPrecacheTest(t, dir)
	ts.setStrict(hA)
	p.addFiles(hA, []int{1, 2, 3}, "Show")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.finishFile(hA, 1)
	waitFor(t, "preload of file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })
	p.close() // sidecar and TorrServer stop
	ts.restart()

	p2 := newPrecache(Config{TorrServer: ts.srv.URL, DataDir: dir})
	p2.retryDelay = 50 * time.Millisecond
	p2.pollDelay = 20 * time.Millisecond
	t.Cleanup(p2.close)
	if got := files(p2, hA); got != "1+ 2 3" {
		t.Fatalf("files after restart = %q", got)
	}
	p2.resume()
	want := hA + "/1 " + hA + "/2 " + hA + "/2"
	waitFor(t, "file 2 requested again", func() bool { return ts.requested() == want })
	time.Sleep(3 * p2.retryDelay)
	if got := ts.requested(); got != want {
		t.Fatalf("preloads after restart = %q, want only file 2 again", got)
	}
	if it := item(p2, hA); it.Index != 2 || it.Done || it.Title != "Show" {
		t.Fatalf("after restart: %+v", it)
	}
	ts.finishFile(hA, 2)
	waitFor(t, "file 3", func() bool { return ts.requested() == want+" "+hA+"/3" })
	ts.finishFile(hA, 3)
	waitFor(t, "done", func() bool { return files(p2, hA) == "1+ 2+ 3+" })
}

func TestPrecacheSwitchOffOnKeepsQueue(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	ts.setStrict(hA)
	p.addFiles(hA, []int{1, 2, 3}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.finishFile(hA, 1)
	waitFor(t, "preload of file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })

	p.setEnabled(false)
	waitFor(t, "stop", func() bool { return ts.count(&ts.stops) == 1 })
	p.addFiles(hA, []int{1}, "") // while off: queued only
	time.Sleep(3 * p.retryDelay)
	if got := ts.requested(); got != hA+"/1 "+hA+"/2" {
		t.Fatalf("preloads while off = %q", got)
	}
	if got := files(p, hA); got != "2 3 1" {
		t.Fatalf("files while off = %q, want 2 3 1", got)
	}
	ts.mu.Lock()
	delete(ts.complete, hA+"/1")
	ts.mu.Unlock()

	p.setEnabled(true)
	want := hA + "/1 " + hA + "/2 " + hA + "/2"
	waitFor(t, "file 2 resumed", func() bool { return ts.requested() == want })
	time.Sleep(3 * p.retryDelay)
	if got := ts.requested(); got != want {
		t.Fatalf("preloads after on = %q, want %q", got, want)
	}
	if n := ts.numOverlaps(); n != 0 {
		t.Fatalf("%d overlapping preloads", n)
	}
	if it := item(p, hA); it.Index != 2 || it.Done || it.Error != "" {
		t.Fatalf("after on: %+v", it)
	}
}

// A preload that returns before its file is complete is retried; the queue
// does not move on to the next file meanwhile.
func TestPrecacheIncompleteFileRetriesSameFileNotNext(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	ts.setStrict(hA)
	p.addFiles(hA, []int{1, 2}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.finish(hA) // the preload "returns" although file 1 is 400/1000
	waitFor(t, "incomplete recorded", func() bool {
		it := item(p, hA)
		return it != nil && !it.Done && it.Index == 1 && strings.Contains(it.Error, "incomplete")
	})
	waitFor(t, "file 1 retried", func() bool { return ts.requested() == hA+"/1 "+hA+"/1" })
	if got := files(p, hA); got != "1! 2" {
		t.Fatalf("files = %q, want 1! 2", got)
	}
	ts.finishFile(hA, 1)
	waitFor(t, "file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/1 "+hA+"/2" })
	if it := item(p, hA); it.Index != 2 || it.Error != "" || files(p, hA) != "1+ 2" {
		t.Fatalf("after file 1: %+v", it)
	}
	ts.finishFile(hA, 2)
	waitFor(t, "done", func() bool { it := item(p, hA); return it.Done && it.Error == "" })
}

// A file that completes just as it is dropped or replaced must not mark the
// file now at the front of the queue.
func TestPrecacheFinishIgnoresFileNoLongerQueued(t *testing.T) {
	_, p := newPrecacheTest(t, t.TempDir())
	p.setEnabled(false)
	p.add(hA, 1, "")
	p.add(hA, 2, "")
	p.finish(hA, 1, "", nil) // nil: no run registered (the switch is off)
	p.finish(hA, 1, "boom", nil)
	if it := item(p, hA); it.Done || it.Error != "" || files(p, hA) != "2" {
		t.Fatalf("finish of a dropped file changed the item: %+v", it)
	}
	p.finish(hA, 2, "", nil)
	if it := item(p, hA); !it.Done || it.Index != 2 {
		t.Fatalf("finish of the queued file: %+v", it)
	}
}

// A run that was replaced (item removed and added again while its last request
// was still returning) must not write its outcome onto the new item, even when
// the new item queues the same file.
func TestPrecacheFinishIgnoresStaleRun(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.add(hA, 1, "")
	waitFor(t, "preload", func() bool { return ts.count(&ts.preloads) == 1 })
	p.mu.Lock()
	cur := p.running[hA]
	p.mu.Unlock()
	stale := &precacheRun{}
	p.finish(hA, 1, "", stale)
	p.finish(hA, 1, "boom", stale)
	if it := item(p, hA); it.Done || it.Error != "" {
		t.Fatalf("a stale run changed the item: %+v", it)
	}
	p.finish(hA, 1, "boom", cur)
	if it := item(p, hA); it.Done || it.Error != "boom" {
		t.Fatalf("the registered run could not record its error: %+v", it)
	}
}

// The exit of a run ("no file left" -> unregister) is one critical section. The
// hook runs exactly there and appends a file: were the lock released in between,
// the append would see the run still registered, start nothing, and the file
// would wait forever.
func TestPrecacheAppendInExitWindowStarts(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.retryDelay = time.Hour
	ts.setStrict(hA)
	var once sync.Once
	appended := make(chan struct{})
	p.mu.Lock()
	p.exitHook = func(hash string) {
		once.Do(func() {
			go func() { p.addFiles(hash, []int{2}, ""); close(appended) }()
			// give the append every chance to get in before the run unregisters
			select {
			case <-appended:
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
	p.mu.Unlock()
	p.addFiles(hA, []int{1}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.finishFile(hA, 1)
	waitFor(t, "append", func() bool {
		select {
		case <-appended:
			return true
		default:
			return false
		}
	})
	waitFor(t, "preload of the appended file", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })
	ts.finishFile(hA, 2)
	waitFor(t, "all done", func() bool { return files(p, hA) == "1+ 2+" })
}

// TorrServer answers stat 2 at once while it is still busy with the previous
// file of the torrent. The end of that busy state says nothing about OUR file:
// it must be requested (again) and finish before it counts as done. The fake
// reports no bytes_completed here, so "get" cannot tell whether a file is
// complete (fileComplete answers true): only the request order shows it.
func TestPrecacheBusyWithPreviousFileDoesNotMarkNextDone(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.retryDelay = time.Hour
	ts.mu.Lock()
	ts.noBytes[hA] = true
	ts.mu.Unlock()
	p.addFiles(hA, []int{1, 2}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.setAlready(hA) // the /stream request returns, the server stays busy a while
	ts.finishFile(hA, 1)
	waitFor(t, "file 2 asked for (answered busy)", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })
	time.Sleep(3 * p.pollDelay)
	if got := files(p, hA); got != "1+ 2" {
		t.Fatalf("files while busy = %q, want 1+ 2", got)
	}
	ts.finish(hA) // busy state over: "get" answers 3
	waitFor(t, "file 2 requested itself once the server is free", func() bool { return ts.requested() == hA+"/1 "+hA+"/2 "+hA+"/2" })
	time.Sleep(3 * p.pollDelay)
	if got := files(p, hA); got != "1+ 2" {
		t.Fatalf("files before file 2's own preload ended = %q, want 1+ 2", got)
	}
	ts.finishFile(hA, 2)
	waitFor(t, "done", func() bool { return files(p, hA) == "1+ 2+" })
	if n := ts.count(&ts.stops); n != 0 {
		t.Fatalf("preload_stop sent %d times, want 0", n)
	}
}

// Dropping the file whose (foreign) preload we only watch: preload_stop answers
// before the preload has unwound, so the next file must wait for the busy state
// to end instead of being asked for while TorrServer still answers stat 2.
func TestPrecacheRemoveWatchedFileWaitsForStop(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.retryDelay = time.Hour
	ts.setAlready(hA)
	p.addFiles(hA, []int{1, 2}, "")
	waitFor(t, "preload of file 1 (answered busy)", func() bool { return ts.requested() == hA+"/1" })
	if removed, it := p.removeFiles(hA, []int{1}, false); !removed || it == nil || it.Index != 2 {
		t.Fatalf("drop watched file: removed=%v %+v", removed, it)
	}
	waitFor(t, "preload of file 2", func() bool { return strings.HasSuffix(ts.requested(), hA+"/2") })
	time.Sleep(200 * time.Millisecond)
	if got := ts.requested(); got != hA+"/1 "+hA+"/2" {
		t.Fatalf("preloads = %q, want file 1 then file 2 once", got)
	}
	if n := ts.count(&ts.stops); n != 1 {
		t.Fatalf("preload_stop sent %d times, want 1", n)
	}
	if b, o := ts.numBusy(), ts.numOverlaps(); b != 1 || o != 0 {
		t.Fatalf("file 2 requested while TorrServer was still busy: busy answers=%d (want 1, file 1), overlaps=%d", b, o)
	}
	if got := files(p, hA); got != "2" {
		t.Fatalf("files = %q, want 2 (not done)", got)
	}
	ts.finishFile(hA, 2)
	waitFor(t, "done", func() bool { return files(p, hA) == "2+" })
}

// A file id the torrent does not have can never complete: it leaves the queue
// instead of blocking the files behind it.
func TestPrecacheUnknownFileIndexIsDropped(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.retryDelay = time.Hour // the next file must not wait for a retry
	p.addFiles(hA, []int{99, 2}, "")
	waitFor(t, "preload of file 99", func() bool { return ts.requested() == hA+"/99" })
	ts.finish(hA) // TorrServer preloads some other file for an id it does not have, then returns
	waitFor(t, "file 2 requested after 99", func() bool { return ts.requested() == hA+"/99 "+hA+"/2" })
	if got := files(p, hA); got != "2" {
		t.Fatalf("files = %q, want 2", got)
	}
	ts.finishFile(hA, 2)
	waitFor(t, "done", func() bool { return files(p, hA) == "2+" })
	// the only file of an item: the item goes
	p.addFiles(hB, []int{99}, "")
	waitFor(t, "preload of file 99", func() bool { return strings.HasSuffix(ts.requested(), hB+"/99") })
	ts.finish(hB)
	waitFor(t, "item with only an unknown file removed", func() bool { return item(p, hB) == nil })
	p.mu.Lock()
	_, running := p.running[hB]
	p.mu.Unlock()
	if running {
		t.Fatal("run of the dropped item still registered")
	}
}

// Appending files keeps the title the item has; the one-file call sets it.
func TestPrecacheAppendKeepsTitle(t *testing.T) {
	_, p := newPrecacheTest(t, t.TempDir())
	p.setEnabled(false)
	if it := p.addFiles(hA, []int{1}, "First"); it.Title != "First" {
		t.Fatalf("title of a new item: %q", it.Title)
	}
	if it := p.addFiles(hA, []int{2}, "Second"); it.Title != "First" {
		t.Fatalf("append changed the title to %q", it.Title)
	}
	if it := p.add(hA, 3, "Third"); it.Title != "Third" {
		t.Fatalf("legacy add did not set the title: %q", it.Title)
	}
	p.add(hB, 1, "")
	if it := p.addFiles(hB, []int{2}, "Late"); it.Title != "Late" {
		t.Fatalf("append to an untitled item: %q", it.Title)
	}
}

// removeFiles with pending leaves done files alone: a caller that picked the
// files from an older snapshot must not erase the mark of one that finished since.
func TestPrecacheRemoveFilesPendingKeepsDone(t *testing.T) {
	ts, p := newPrecacheTest(t, t.TempDir())
	p.retryDelay = time.Hour
	ts.setStrict(hA)
	p.addFiles(hA, []int{1, 2, 3}, "")
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.finishFile(hA, 1)
	waitFor(t, "preload of file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })

	// file 1 finished after the caller looked: it stays, and nothing was dropped
	if removed, it := p.removeFiles(hA, []int{1}, true); removed || it == nil || files(p, hA) != "1+ 2 3" {
		t.Fatalf("pending drop of a done file: removed=%v files=%q", removed, files(p, hA))
	}
	// done and running file together: only the running one goes, the next starts
	if removed, it := p.removeFiles(hA, []int{1, 2}, true); !removed || it == nil || it.Index != 3 || files(p, hA) != "1+ 3" {
		t.Fatalf("pending drop of [1,2]: removed=%v files=%q", removed, files(p, hA))
	}
	waitFor(t, "preload of file 3", func() bool { return ts.requested() == hA+"/1 "+hA+"/2 "+hA+"/3" })
	if n := ts.count(&ts.stops); n != 1 {
		t.Fatalf("preload_stop sent %d times, want 1", n)
	}
	// no indexes: every file that is not done; the item stays with its done file
	removed, it := p.removeFiles(hA, nil, true)
	if !removed || it == nil || !it.Done || it.Index != 1 || files(p, hA) != "1+" {
		t.Fatalf("pending drop of all: removed=%v %+v", removed, it)
	}
	waitFor(t, "second preload_stop", func() bool { return ts.count(&ts.stops) == 2 })
	if removed, it = p.removeFiles(hA, nil, true); removed || it == nil || files(p, hA) != "1+" {
		t.Fatalf("pending drop with nothing pending: removed=%v files=%q", removed, files(p, hA))
	}
	// without pending the done file goes as before, and with it the item
	if removed, it = p.removeFiles(hA, []int{1}, false); !removed || it != nil || item(p, hA) != nil {
		t.Fatalf("plain drop of the done file: removed=%v %+v", removed, it)
	}
	if n := ts.count(&ts.stops); n != 2 {
		t.Fatalf("preload_stop sent for a finished item: %d", n)
	}
	// nothing done: the item goes
	p.addFiles(hB, []int{1, 2}, "")
	if removed, it = p.removeFiles(hB, nil, true); !removed || it != nil || item(p, hB) != nil {
		t.Fatalf("pending drop of an item without done files: removed=%v %+v", removed, it)
	}
	if removed, it = p.removeFiles(hB, nil, true); removed || it != nil {
		t.Fatalf("pending drop on an unknown hash: removed=%v %+v", removed, it)
	}
}

// POST /precache/stop with pending:true only drops files that are not done.
func TestPrecacheStopPendingHTTP(t *testing.T) {
	ts := newFakeTS(t)
	app := newApp(Config{TorrServer: ts.srv.URL, Port: "8792", AppDir: t.TempDir(), DataDir: t.TempDir()})
	t.Cleanup(app.precache.close)
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	p := app.precache
	ts.setStrict(hA)
	stop := func(body map[string]interface{}) (int, interface{}, string) {
		t.Helper()
		resp, m := postJSON(t, srv.URL+"/precache/stop", body)
		_, hasItem := m["item"]
		got := files(p, hA)
		if !hasItem && got != "-" || hasItem && got == "-" {
			t.Fatalf("stop %v: item in answer = %v, files = %q", body, hasItem, got)
		}
		return resp.StatusCode, m["removed"], got
	}

	postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hA, "indexes": []int{1, 2, 3}})
	waitFor(t, "preload of file 1", func() bool { return ts.requested() == hA+"/1" })
	ts.finishFile(hA, 1)
	waitFor(t, "preload of file 2", func() bool { return ts.requested() == hA+"/1 "+hA+"/2" })

	// the done file named in the list stays, the waiting one goes
	if code, removed, got := stop(map[string]interface{}{"hash": hA, "indexes": []int{1, 3}, "pending": true}); code != 200 || removed != true || got != "1+ 2" {
		t.Fatalf("stop pending [1,3]: %d removed=%v files=%q, want 200 true 1+ 2", code, removed, got)
	}
	if code, removed, got := stop(map[string]interface{}{"hash": hA, "index": 1, "pending": true}); code != 200 || removed != false || got != "1+ 2" {
		t.Fatalf("stop pending index 1 (done): %d removed=%v files=%q, want 200 false 1+ 2", code, removed, got)
	}
	if resp, _ := postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hA, "pending": "yes"}); resp.StatusCode != 400 {
		t.Fatalf("non-bool pending accepted: %d", resp.StatusCode)
	}
	if ts.count(&ts.stops) != 0 {
		t.Fatal("preload_stop sent although the running file was not dropped")
	}
	// without index/indexes: every file that is not done, the done ones stay
	if code, removed, got := stop(map[string]interface{}{"hash": hA, "pending": true}); code != 200 || removed != true || got != "1+" {
		t.Fatalf("stop pending: %d removed=%v files=%q, want 200 true 1+", code, removed, got)
	}
	waitFor(t, "preload_stop for the running file", func() bool { return ts.count(&ts.stops) == 1 })
	if it := item(p, hA); !it.Done || it.Index != 1 {
		t.Fatalf("item left with its done file: %+v", it)
	}
	if code, removed, got := stop(map[string]interface{}{"hash": hA, "pending": true}); code != 200 || removed != false || got != "1+" {
		t.Fatalf("second stop pending: %d removed=%v files=%q, want 200 false 1+", code, removed, got)
	}
	// pending:false is the old call: done files go too
	if code, removed, got := stop(map[string]interface{}{"hash": hA, "index": 1, "pending": false}); code != 200 || removed != true || got != "-" {
		t.Fatalf("stop index 1 pending=false: %d removed=%v files=%q, want 200 true -", code, removed, got)
	}
	// nothing done: the item goes
	postJSON(t, srv.URL+"/precache", map[string]interface{}{"hash": hB, "indexes": []int{1, 2}})
	resp, m := postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hB, "pending": true})
	if _, hasItem := m["item"]; resp.StatusCode != 200 || m["removed"] != true || hasItem || item(p, hB) != nil {
		t.Fatalf("stop pending on an item without done files: %d %v", resp.StatusCode, m)
	}
	resp, m = postJSON(t, srv.URL+"/precache/stop", map[string]interface{}{"hash": hB, "pending": true})
	if resp.StatusCode != 200 || m["removed"] != false {
		t.Fatalf("stop pending on an unknown hash: %d %v", resp.StatusCode, m)
	}
}
