package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>tv</html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.js"), []byte("var a=1;"), 0o644)
	app := newApp(Config{TorrServer: "http://ts.example:8090", Port: "8791", AppDir: dir})
	srv := httptest.NewServer(app.routes())
	t.Cleanup(srv.Close)
	return app, srv
}

func postJSON(t *testing.T, url string, body interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	return resp, m
}

func TestChannelRoundTrip(t *testing.T) {
	_, srv := newTestApp(t)
	// TV registers, reusing a remembered id
	resp, m := postJSON(t, srv.URL+"/remote/new", map[string]string{"id": "abc234"})
	if resp.StatusCode != 200 || m["id"] != "abc234" {
		t.Fatalf("new: %d %v", resp.StatusCode, m)
	}
	if !strings.HasSuffix(m["url"].(string), "/r/abc234") {
		t.Fatalf("url %v", m["url"])
	}
	// invalid remembered id -> fresh one
	_, m2 := postJSON(t, srv.URL+"/remote/new", map[string]string{"id": "../x"})
	if m2["id"] == "../x" || len(m2["id"].(string)) != 6 {
		t.Fatalf("bad id reuse: %v", m2)
	}
	// phone sends play; poll returns it
	hash := strings.Repeat("ab", 20)
	resp, m = postJSON(t, srv.URL+"/remote/abc234/send", map[string]interface{}{"action": "play", "hash": strings.ToUpper(hash), "index": 6, "title": "Sintel"})
	if resp.StatusCode != 200 || m["seq"].(float64) != 1 {
		t.Fatalf("send: %d %v", resp.StatusCode, m)
	}
	r, err := http.Get(srv.URL + "/remote/abc234/poll?since=0")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Seq      int         `json:"seq"`
		Messages []remoteMsg `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&p)
	if p.Seq != 1 || len(p.Messages) != 1 || p.Messages[0].Hash != hash || p.Messages[0].Index != 6 || p.Messages[0].Action != "play" {
		t.Fatalf("poll: %+v", p)
	}
	// bad action / bad hash rejected
	resp, _ = postJSON(t, srv.URL+"/remote/abc234/send", map[string]interface{}{"action": "submit", "text": "x"})
	if resp.StatusCode != 400 {
		t.Fatalf("bad action accepted: %d", resp.StatusCode)
	}
	resp, _ = postJSON(t, srv.URL+"/remote/abc234/send", map[string]interface{}{"action": "play", "hash": "nope", "index": 1})
	if resp.StatusCode != 400 {
		t.Fatalf("bad hash accepted: %d", resp.StatusCode)
	}
	// unknown channel
	resp, _ = postJSON(t, srv.URL+"/remote/zzzzzz/send", map[string]interface{}{"action": "added", "hash": hash})
	if resp.StatusCode != 404 {
		t.Fatalf("unknown channel: %d", resp.StatusCode)
	}
	// state
	postJSON(t, srv.URL+"/remote/abc234/state", map[string]interface{}{"view": "player", "title": "Sintel", "hash": hash, "index": 6})
	r, _ = http.Get(srv.URL + "/remote/abc234/state")
	var st remoteState
	json.NewDecoder(r.Body).Decode(&st)
	if st.View != "player" || st.Hash != hash || st.T == 0 {
		t.Fatalf("state: %+v", st)
	}
}

func TestPollWakesOnSend(t *testing.T) {
	app, _ := newTestApp(t)
	ch := app.remote.channel("", true)
	done := make(chan map[string]interface{})
	go func() { done <- app.remote.poll(ch, 0, 5*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	app.remote.send(ch, remoteMsg{Action: "added", Hash: strings.Repeat("0", 40)})
	select {
	case res := <-done:
		if res["seq"].(int) != 1 {
			t.Fatalf("seq %v", res["seq"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not wake")
	}
	// poll with timeout and nothing new returns empty
	res := app.remote.poll(ch, 1, 30*time.Millisecond)
	if len(res["messages"].([]remoteMsg)) != 0 {
		t.Fatalf("expected no messages: %v", res)
	}
}

func TestQRAndPhonePage(t *testing.T) {
	_, srv := newTestApp(t)
	postJSON(t, srv.URL+"/remote/new", map[string]string{"id": "qrtest"})
	r, _ := http.Get(srv.URL + "/remote/qrtest/qr.svg")
	b := new(bytes.Buffer)
	b.ReadFrom(r.Body)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(b.String(), "<path d=\"M") {
		t.Fatalf("qr: %d %s %s", r.StatusCode, r.Header.Get("Content-Type"), b.String()[:60])
	}
	r, _ = http.Get(srv.URL + "/r/qrtest")
	b.Reset()
	b.ReadFrom(r.Body)
	s := b.String()
	if r.StatusCode != 200 || !strings.Contains(s, `var ID="qrtest", TS="http://ts.example:8090"`) {
		t.Fatalf("phone page: %d %q", r.StatusCode, s[:200])
	}
	r, _ = http.Get(srv.URL + "/r/..%2F..")
	if r.StatusCode != 404 {
		t.Fatalf("bad id: %d", r.StatusCode)
	}
}

func TestStaticAndConfig(t *testing.T) {
	_, srv := newTestApp(t)
	r, _ := http.Get(srv.URL + "/tv/")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("/tv/: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	r, _ = http.Get(srv.URL + "/tv/a.js")
	if r.StatusCode != 200 || r.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("/tv/a.js: %d", r.StatusCode)
	}
	r, _ = http.Get(srv.URL + "/tv/../remote.go")
	if r.StatusCode != 404 {
		t.Fatalf("traversal: %d", r.StatusCode)
	}
	r, _ = http.Get(srv.URL + "/config")
	var c map[string]string
	json.NewDecoder(r.Body).Decode(&c)
	if c["torrserver"] != "http://ts.example:8090" {
		t.Fatalf("config: %v", c)
	}
	r, _ = http.Get(srv.URL + "/health")
	if r.StatusCode != 200 {
		t.Fatalf("health: %d", r.StatusCode)
	}
}
