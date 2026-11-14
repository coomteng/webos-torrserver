package torrremote

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/coomteng/webos-torrserver/sidecar/subs"
)

func newTestApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>tv</html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.js"), []byte("var a=1;"), 0o644)
	app := newApp(Config{TorrServer: "http://ts.example:8090", Port: "8791", AppDir: dir})
	srv := httptest.NewServer(app.Handler())
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
	if r.StatusCode != 200 || !strings.Contains(s, `var ID="qrtest", BASE="", TS="http://ts.example:8090"||location.origin`) {
		t.Fatalf("phone page: %d %q", r.StatusCode, s[:200])
	}
	if !strings.Contains(s, `id="ts-q"`) || !strings.Contains(s, "/torznab/search/?query=") {
		t.Fatal("phone page lacks the torrent search block")
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

// ---------------------------------------------------------------- subtitles

func newSubsTestApp(t *testing.T) (*App, *httptest.Server, *httptest.Server) {
	t.Helper()
	search, _ := os.ReadFile("subs/testdata/assrt_search.json")
	detail, _ := os.ReadFile("subs/testdata/assrt_detail.json")
	const srt = "1\n00:00:01,000 --> 00:00:02,000\n你好\n\n2\n00:00:03,000 --> 00:00:04,000\nworld\n"
	var fake *httptest.Server
	fake = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sub/search":
			w.Write(search)
		case "/v1/sub/detail":
			// rewrite the CDN urls in the recorded detail to point at this fake
			w.Write(bytes.ReplaceAll(detail, []byte("http://file1.assrt.net"), []byte(fake.URL)))
		default:
			if strings.HasSuffix(r.URL.Path, ".srt") {
				w.Write([]byte(srt))
				return
			}
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	dir := t.TempDir()
	app := newApp(Config{TorrServer: "http://ts.example:8090", AppDir: dir, SubsDir: filepath.Join(dir, "subs"), AssrtToken: "tok"})
	a := app.subs.sources["assrt"].(*subs.ASSRT)
	a.Base = fake.URL + "/v1"
	a.Spacing = time.Millisecond
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return app, srv, fake
}

func getJSON(t *testing.T, url string) (*http.Response, map[string]interface{}) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	return resp, m
}

func TestSubsSearchFetchListVTT(t *testing.T) {
	_, srv, _ := newSubsTestApp(t)
	hash := "0123456789abcdef0123456789abcdef01234567"

	_, cfg := getJSON(t, srv.URL+"/config")
	if sc, _ := cfg["subs"].(map[string]interface{}); sc == nil || sc["enabled"] != true {
		t.Fatalf("/config subs: %v", cfg["subs"])
	}

	resp, m := getJSON(t, srv.URL+"/subs/search?file=Sintel.2010.1080p.x264-GRP.mkv")
	if resp.StatusCode != 200 || m["query"] != "Sintel 2010" {
		t.Fatalf("search: %d %v", resp.StatusCode, m)
	}
	results := m["results"].([]interface{})
	if len(results) != 4 {
		t.Fatalf("results %d", len(results))
	}
	first := results[0].(map[string]interface{})

	resp, m = postJSON(t, srv.URL+"/subs/fetch", map[string]interface{}{"hash": hash, "index": 1, "src": "assrt", "id": first["id"], "video": "Sintel.mkv"})
	if resp.StatusCode != 200 {
		t.Fatalf("fetch: %d %v", resp.StatusCode, m)
	}
	sub := m["sub"].(map[string]interface{})
	if sub["url"] != "/subs/"+hash+"/1/1.vtt" || sub["cues"].(float64) != 2 || sub["src"] != "assrt" {
		t.Fatalf("fetched sub: %v", sub)
	}

	_, m = getJSON(t, srv.URL+"/subs/list?hash="+hash+"&index=1")
	if l := m["subs"].([]interface{}); len(l) != 1 {
		t.Fatalf("list: %v", m)
	}
	vresp, err := http.Get(srv.URL + sub["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(vresp.Body)
	if vresp.StatusCode != 200 || !strings.HasPrefix(vresp.Header.Get("Content-Type"), "text/vtt") || !strings.Contains(string(body), "00:00:01.000 --> 00:00:02.000\n你好") {
		t.Fatalf("vtt: %d %s %q", vresp.StatusCode, vresp.Header.Get("Content-Type"), body)
	}

	// upload a GBK srt as raw body
	gbk, _, _ := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte("1\n00:00:05,000 --> 00:00:06,000\n上传的字幕\n"))
	up, err := http.Post(srv.URL+"/subs/upload?hash="+hash+"&index=1&name=my.chs.srt", "application/octet-stream", bytes.NewReader(gbk))
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(up.Body).Decode(&m)
	up.Body.Close()
	sub2 := m["sub"].(map[string]interface{})
	if up.StatusCode != 200 || sub2["n"].(float64) != 2 || sub2["label"] != "简中" || sub2["src"] != "upload" {
		t.Fatalf("upload: %d %v", up.StatusCode, m)
	}
	v2, _ := http.Get(srv.URL + sub2["url"].(string))
	b2, _ := io.ReadAll(v2.Body)
	if !strings.Contains(string(b2), "上传的字幕") {
		t.Fatalf("GBK upload not decoded: %q", b2)
	}

	// delete #1, list has only #2
	resp, _ = postJSON(t, srv.URL+"/subs/delete", map[string]interface{}{"hash": hash, "index": 1, "n": 1})
	_, m = getJSON(t, srv.URL+"/subs/list?hash="+hash+"&index=1")
	if l := m["subs"].([]interface{}); resp.StatusCode != 200 || len(l) != 1 || l[0].(map[string]interface{})["n"].(float64) != 2 {
		t.Fatalf("after delete: %v", m)
	}
	if r, _ := http.Get(srv.URL + "/subs/" + hash + "/1/1.vtt"); r.StatusCode != 404 {
		t.Fatalf("deleted vtt still served: %d", r.StatusCode)
	}

	// bad inputs
	if r, _ := http.Get(srv.URL + "/subs/list?hash=zz&index=1"); r.StatusCode != 400 {
		t.Fatalf("bad hash accepted: %d", r.StatusCode)
	}
	if r, _ := http.Post(srv.URL+"/subs/upload?hash="+hash+"&index=1&name=x.srt", "text/plain", strings.NewReader("<html>")); r.StatusCode != 400 {
		t.Fatalf("garbage upload accepted: %d", r.StatusCode)
	}
}

func TestRemoteSubAction(t *testing.T) {
	_, srv := newTestApp(t)
	_, m := postJSON(t, srv.URL+"/remote/new", map[string]string{})
	id := m["id"].(string)
	hash := "0123456789abcdef0123456789abcdef01234567"
	resp, _ := postJSON(t, srv.URL+"/remote/"+id+"/send", map[string]interface{}{"action": "sub", "hash": hash, "index": 1, "url": "http://evil/x.vtt"})
	if resp.StatusCode != 400 {
		t.Fatalf("foreign url accepted: %d", resp.StatusCode)
	}
	resp, _ = postJSON(t, srv.URL+"/remote/"+id+"/send", map[string]interface{}{"action": "sub", "hash": hash, "index": 1, "url": "/subs/" + hash + "/1/1.vtt", "title": "简中"})
	if resp.StatusCode != 200 {
		t.Fatalf("sub send: %d", resp.StatusCode)
	}
	_, p := getJSON(t, srv.URL+"/remote/"+id+"/poll?since=0")
	msgs := p["messages"].([]interface{})
	if len(msgs) != 1 || msgs[0].(map[string]interface{})["action"] != "sub" || msgs[0].(map[string]interface{})["url"] != "/subs/"+hash+"/1/1.vtt" {
		t.Fatalf("poll: %v", p)
	}
}

func TestPhonePageCarriesSubsConfig(t *testing.T) {
	_, srv, _ := newSubsTestApp(t)
	_, m := postJSON(t, srv.URL+"/remote/new", map[string]string{})
	resp, err := http.Get(srv.URL + "/r/" + m["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `var SUBS={"enabled":true,"sources":["assrt"]}`) {
		t.Fatalf("phone page SUBS missing: %s", regexpFind(string(body), "var SUBS=[^;]*"))
	}
	if !strings.Contains(string(body), "上传字幕文件") || !strings.Contains(string(body), "/subs/search?q=") {
		t.Fatal("phone page lacks subtitle UI")
	}
}

func regexpFind(s, pat string) string {
	m := regexp.MustCompile(pat).FindString(s)
	return m
}

// lockedBuffer collects log output written by handler goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A failed download reaches the TV as a 502 and is logged with source, id, file and reason, but
// never with the signed query of the link.
func TestSubsFetchDownloadFailureIsLogged(t *testing.T) {
	app, srv, _ := newSubsTestApp(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sub/detail":
			w.Write([]byte(`{"status":0,"sub":{"subs":[{"id":42,"filelist":[{"f":"x.chs.srt","url":"http://` + r.Host + `/onthefly/x.srt?_=1&-=SECRETSIG"}]}]}}`))
		case "/onthefly/x.srt":
			http.Redirect(w, r, "/download/error/abc?sig=SECRETSIG", http.StatusFound)
		default:
			w.WriteHeader(493)
			w.Write([]byte("<html><body><h1>Too many requests</h1></body></html>"))
		}
	}))
	t.Cleanup(cdn.Close)
	app.subs.sources["assrt"].(*subs.ASSRT).Base = cdn.URL + "/v1"
	var logged lockedBuffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	resp, m := postJSON(t, srv.URL+"/subs/fetch", map[string]interface{}{"hash": "0123456789abcdef0123456789abcdef01234567", "index": 1, "src": "assrt", "id": "42"})
	want := "assrt download: HTTP 493 at " + strings.TrimPrefix(cdn.URL, "http://") + "/download/error/abc: Too many requests"
	if resp.StatusCode != 502 || m["error"] != want {
		t.Fatalf("fetch: %d %v, want 502 %q", resp.StatusCode, m, want)
	}
	out := logged.String()
	if !strings.Contains(out, `subs: fetch assrt#42 "x.chs.srt": `+want) {
		t.Fatalf("download failure not logged: %q", out)
	}
	if strings.Contains(out, "SECRETSIG") || strings.Contains(out, "?") {
		t.Fatalf("log leaks the query: %q", out)
	}
}

// ASSRT's per-file extraction answers 493 for every file; the fetch must still succeed by taking
// the file out of the package's archive, whose link comes in the same detail response.
func TestSubsFetchFallsBackToArchive(t *testing.T) {
	app, srv, _ := newSubsTestApp(t)
	hash := "0123456789abcdef0123456789abcdef01234567"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{
		{"pack/x.chs.srt", "1\n00:00:01,000 --> 00:00:02,000\n来自压缩包\n\n2\n00:00:03,000 --> 00:00:04,000\nsecond\n\n3\n00:00:05,000 --> 00:00:06,000\nthird\n"},
		{"pack/x.eng.srt", "1\n00:00:01,000 --> 00:00:02,000\nenglish only\n"},
	} {
		w, _ := zw.Create(f[0])
		w.Write([]byte(f[1]))
	}
	zw.Close()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sub/detail":
			w.Write([]byte(`{"status":0,"sub":{"subs":[{"id":42,"filename":"pack.zip","url":"http://` + r.Host + `/download/42/pack.zip?_=1&-=SECRETSIG",` +
				`"filelist":[{"f":"x.chs.srt","url":"http://` + r.Host + `/onthefly/42/-/1/x.chs.srt?_=1&-=SECRETSIG"},{"f":"x.eng.srt","url":"http://` + r.Host + `/onthefly/42/-/2/x.eng.srt?_=1&-=SECRETSIG"}]}]}}`))
		case "/download/42/pack.zip":
			w.Write(buf.Bytes())
		default:
			w.WriteHeader(493)
			w.Write([]byte("<html><body><h1>啊呀！服务器抽风啦！请稍后重试~</h1></body></html>"))
		}
	}))
	t.Cleanup(cdn.Close)
	app.subs.sources["assrt"].(*subs.ASSRT).Base = cdn.URL + "/v1"

	resp, m := postJSON(t, srv.URL+"/subs/fetch", map[string]interface{}{"hash": hash, "index": 1, "src": "assrt", "id": "42", "video": "x.mkv"})
	if resp.StatusCode != 200 {
		t.Fatalf("fetch: %d %v", resp.StatusCode, m)
	}
	sub := m["sub"].(map[string]interface{})
	if sub["name"] != "x.chs.srt" || sub["cues"].(float64) != 3 || sub["src"] != "assrt" {
		t.Fatalf("fetched sub: %v", sub)
	}
	_, m = getJSON(t, srv.URL+"/subs/list?hash="+hash+"&index=1")
	if l := m["subs"].([]interface{}); len(l) != 1 {
		t.Fatalf("list: %v", m)
	}
	vresp, err := http.Get(srv.URL + sub["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(vresp.Body)
	if vresp.StatusCode != 200 || !strings.Contains(string(body), "来自压缩包") {
		t.Fatalf("vtt: %d %q", vresp.StatusCode, body)
	}
}

// The same with a rar package whose members are not named like the file list: the whole archive
// comes back from the download and the member for the video's episode and language is stored.
// The same archive uploaded from the phone is expanded too.
func TestSubsFetchFallsBackToRar(t *testing.T) {
	app, srv, _ := newSubsTestApp(t)
	hash := "0123456789abcdef0123456789abcdef01234567"
	pack, err := os.ReadFile("subs/testdata/pack.rar")
	if err != nil {
		t.Fatal(err)
	}
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sub/detail":
			w.Write([]byte(`{"status":0,"sub":{"subs":[{"id":42,"filename":"pack.rar","url":"http://` + r.Host + `/download/42/pack.rar?_=1&-=SECRETSIG",` +
				`"filelist":[{"f":"第1集.简体.srt","url":"http://` + r.Host + `/onthefly/42/-/1/a.srt?_=1&-=SECRETSIG"},{"f":"第2集.简体.srt","url":"http://` + r.Host + `/onthefly/42/-/2/b.srt?_=1&-=SECRETSIG"}]}]}}`))
		case "/download/42/pack.rar":
			w.Write(pack)
		default:
			w.WriteHeader(493)
		}
	}))
	t.Cleanup(cdn.Close)
	app.subs.sources["assrt"].(*subs.ASSRT).Base = cdn.URL + "/v1"

	resp, m := postJSON(t, srv.URL+"/subs/fetch", map[string]interface{}{"hash": hash, "index": 1, "src": "assrt", "id": "42", "video": "Show.S01E02.1080p.WEB-DL.mkv"})
	if resp.StatusCode != 200 {
		t.Fatalf("fetch: %d %v", resp.StatusCode, m)
	}
	sub := m["sub"].(map[string]interface{})
	if sub["name"] != "Show.S01E02.chs.srt" || sub["cues"].(float64) != 1 || sub["src"] != "assrt" {
		t.Fatalf("fetched sub: %v", sub)
	}
	vresp, err := http.Get(srv.URL + sub["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(vresp.Body)
	if vresp.StatusCode != 200 || !strings.Contains(string(body), "第二集") {
		t.Fatalf("vtt: %d %q", vresp.StatusCode, body)
	}

	up, err := http.Post(srv.URL+"/subs/upload?hash="+hash+"&index=2&name=pack.rar&video=Show.S01E01.mkv", "application/octet-stream", bytes.NewReader(pack))
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(up.Body).Decode(&m)
	up.Body.Close()
	if sub := m["sub"].(map[string]interface{}); up.StatusCode != 200 || sub["name"] != "Show.S01E01.chs.srt" || sub["src"] != "upload" {
		t.Fatalf("upload: %d %v", up.StatusCode, m)
	}
}

func TestSubsDisabled(t *testing.T) {
	_, srv := newTestApp(t)
	if r, _ := http.Get(srv.URL + "/subs/search?q=x"); r.StatusCode != 404 {
		t.Fatalf("expected 404 when disabled, got %d", r.StatusCode)
	}
}

// A TV parked in the player reports state once and then only long-polls. The phone page judges
// "TV online" by state.t, so polling must keep it fresh (it showed 可能已关闭 during playback).
func TestStateFreshnessFollowsPolling(t *testing.T) {
	app, srv := newTestApp(t)
	postJSON(t, srv.URL+"/remote/new", map[string]string{"id": "live23"})
	postJSON(t, srv.URL+"/remote/live23/state", map[string]interface{}{"view": "player", "title": "Dune"})
	ch := app.remote.channel("live23", false)
	ch.mu.Lock()
	ch.state.T -= 600 // reported 10 minutes ago
	ch.mu.Unlock()
	_, m := getJSON(t, srv.URL+"/remote/live23/state")
	if old := int64(m["t"].(float64)); time.Now().Unix()-old < 590 {
		t.Fatalf("precondition: state should look 10 min old, t=%d", old)
	}
	// a poll that returns immediately (a message is pending)
	postJSON(t, srv.URL+"/remote/live23/send", map[string]interface{}{"action": "added", "hash": strings.Repeat("a", 40)})
	http.Get(srv.URL + "/remote/live23/poll?since=0")
	_, m = getJSON(t, srv.URL+"/remote/live23/state")
	if age := time.Now().Unix() - int64(m["t"].(float64)); age > 2 {
		t.Fatalf("state.t not refreshed by polling: %d s old", age)
	}
	if m["view"] != "player" || m["title"] != "Dune" {
		t.Fatalf("state content changed: %v", m)
	}
}
