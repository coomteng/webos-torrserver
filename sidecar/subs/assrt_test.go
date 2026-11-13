package subs

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeASSRT serves the recorded real responses in testdata/ and counts API hits.
func fakeASSRT(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	search, _ := os.ReadFile("testdata/assrt_search.json")
	detail, _ := os.ReadFile("testdata/assrt_detail.json")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(400)
			w.Write([]byte(`{"errmsg":"invalid token","status":20001}`))
			return
		}
		switch r.URL.Path {
		case "/v1/sub/search":
			atomic.AddInt32(&hits, 1)
			if r.URL.Query().Get("q") == "nothing" {
				w.Write([]byte(`{"status":0,"sub":{"subs":[]}}`))
				return
			}
			w.Write(search)
		case "/v1/sub/detail":
			atomic.AddInt32(&hits, 1)
			w.Write(detail)
		case "/dl/2010.Sintel.UTF-8.tw.srt":
			w.Write([]byte(srtUTF8))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newTestASSRT(srv *httptest.Server, token string) *ASSRT {
	a := NewASSRT(token)
	a.Base = srv.URL + "/v1"
	a.Spacing = 50 * time.Millisecond
	return a
}

func TestASSRTSearchParsesRealSample(t *testing.T) {
	srv, hits := fakeASSRT(t)
	a := newTestASSRT(srv, "tok")
	res, err := a.Search(context.Background(), "Sintel 2010")
	if err != nil {
		t.Fatal(err)
	}
	// the sample has 5 entries, one of them a "muxer" row with id null that must be skipped
	if len(res) != 4 {
		t.Fatalf("got %d results: %+v", len(res), res)
	}
	r := res[0]
	if r.Src != "assrt" || r.ID != "173817" || r.Format != "srt" || r.Title != "2010.Sintel.UTF-8.tw" {
		t.Fatalf("first result: %+v", r)
	}
	if r.Label != "繁中" { // from the ".tw" in the name, lang was null in the API
		t.Fatalf("label %q lang %v", r.Label, r.Lang)
	}
	// cached: second search must not hit the API
	a.Search(context.Background(), "Sintel 2010")
	if *hits != 1 {
		t.Fatalf("search not cached: %d hits", *hits)
	}
	if res, _ := a.Search(context.Background(), "nothing"); len(res) != 0 {
		t.Fatalf("empty search returned %d", len(res))
	}
}

func TestASSRTFilesAndDownload(t *testing.T) {
	srv, _ := fakeASSRT(t)
	a := newTestASSRT(srv, "tok")
	files, err := a.Files(context.Background(), "173817")
	if err != nil || len(files) != 1 || files[0].Name != "2010.Sintel.UTF-8.tw.srt" || !strings.HasPrefix(files[0].URL, "http://file1.assrt.net/onthefly/") {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	data, err := a.Download(context.Background(), File{URL: srv.URL + "/dl/2010.Sintel.UTF-8.tw.srt"})
	if err != nil || string(data) != srtUTF8 {
		t.Fatalf("download err=%v len=%d", err, len(data))
	}
}

// A failed download is answered by a redirect to an error page; the message must say where it
// ended up and what the page said, without the signed query of either URL.
func TestASSRTDownloadErrorShowsWhereAndWhy(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/onthefly/x.srt":
			http.Redirect(w, r, "/download/error/abc?sig=SECRETSIG", http.StatusFound)
		case "/download/error/abc":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(493)
			w.Write([]byte(body.Load().(string)))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	a := newTestASSRT(srv, "tok")
	where := strings.TrimPrefix(srv.URL, "http://") + "/download/error/abc"
	download := func(t *testing.T, page string) string {
		t.Helper()
		body.Store(page)
		data, err := a.Download(context.Background(), File{URL: srv.URL + "/onthefly/x.srt?_=1&-=SECRETSIG"})
		if err == nil {
			t.Fatalf("want error, got %d bytes", len(data))
		}
		msg := err.Error()
		if !strings.Contains(msg, "493") || !strings.Contains(msg, where) {
			t.Fatalf("message lacks status or final host+path %q: %q", where, msg)
		}
		if strings.Contains(msg, "SECRETSIG") || strings.ContainsAny(msg, "?<") {
			t.Fatalf("message leaks query or markup: %q", msg)
		}
		if !utf8.ValidString(msg) {
			t.Fatalf("message is not valid UTF-8: %q", msg)
		}
		return msg
	}

	t.Run("reason", func(t *testing.T) {
		msg := download(t, "<html><body><h1>Too  many</h1>\n<p>requests from your IP</p></body></html>")
		if !strings.Contains(msg, "Too many requests from your IP") {
			t.Fatalf("message lacks the page text: %q", msg)
		}
	})
	t.Run("long body", func(t *testing.T) {
		msg := download(t, "<p>"+strings.Repeat("blocked ", 1280)+"</p>") // ~10 KB
		if len(msg) >= 400 {
			t.Fatalf("message is %d bytes: %q", len(msg), msg)
		}
		if !strings.Contains(msg, "blocked blocked") {
			t.Fatalf("message lacks the page text: %q", msg)
		}
		if _, why, ok := strings.Cut(msg, where+": "); !ok || len(why) > 200 {
			t.Fatalf("page text is %d bytes: %q", len(why), msg)
		}
	})
	t.Run("cjk", func(t *testing.T) {
		// 3-byte runes at every offset mod 3, so a byte cut would split one
		for _, pad := range []string{"", "a", "ab"} {
			msg := download(t, "<p>"+pad+strings.Repeat("啊呀 下载链接好像有点问题？请返回重试", 40)+"</p>")
			if !strings.Contains(msg, "下载链接好像有点问题？请返回重试") {
				t.Fatalf("message lacks the page text: %q", msg)
			}
			if len(msg) >= 400 {
				t.Fatalf("message is %d bytes: %q", len(msg), msg)
			}
		}
		// the 4 KB read limit lands inside a rune
		download(t, strings.Repeat(" ", 4095)+"啊呀")
	})
	t.Run("empty body", func(t *testing.T) {
		for _, page := range []string{"", "<html><body> \n </body></html>"} {
			if msg := download(t, page); !strings.HasSuffix(msg, where) {
				t.Fatalf("message should end with %q: %q", where, msg)
			}
		}
	})
}

// The final URL is whatever the redirect said: its path must not be able to break the one-line
// message or blow it up, and the real "link expired" page (492, XML) must come through readable.
func TestASSRTDownloadErrorPages(t *testing.T) {
	hostile := "/download/error/%3Cb%3E%0A%3F" + strings.Repeat("a", 400)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/onthefly/hostile.srt":
			http.Redirect(w, r, hostile+"?sig=SECRETSIG", http.StatusFound)
		case r.URL.Path == "/onthefly/expired.srt":
			http.Redirect(w, r, "/download/failed/aHR0cDovL2Fzc3J0Lm5ldC8=?sig=SECRETSIG", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/download/error/"):
			w.WriteHeader(493)
		case strings.HasPrefix(r.URL.Path, "/download/failed/"):
			w.Header().Set("Content-Type", "text/xml")
			w.WriteHeader(492)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>` + "\n" + `<html><head><title>出错了</title></head><body><h1>啊呀 下载链接好像有点问题？请返回重试</h1></body></html>`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	a := newTestASSRT(srv, "tok")
	host := strings.TrimPrefix(srv.URL, "http://")
	download := func(t *testing.T, name string) string {
		t.Helper()
		data, err := a.Download(context.Background(), File{URL: srv.URL + "/onthefly/" + name + "?_=1&-=SECRETSIG"})
		if err == nil {
			t.Fatalf("want error, got %d bytes", len(data))
		}
		if msg := err.Error(); strings.Contains(msg, "SECRETSIG") {
			t.Fatalf("message leaks the query: %q", msg)
		}
		return err.Error()
	}

	t.Run("hostile path", func(t *testing.T) {
		msg := download(t, "hostile.srt")
		if strings.ContainsAny(msg, "\n<>?") {
			t.Fatalf("message carries decoded path characters: %q", msg)
		}
		_, where, ok := strings.Cut(msg, "HTTP 493 at ")
		if !ok || !strings.HasPrefix(where, host+"/download/error/%3Cb%3E%0A%3Faaa") || !strings.HasSuffix(where, "…") {
			t.Fatalf("want status and the escaped, cut path: %q", msg)
		}
		if len(where) > 125 {
			t.Fatalf("where is %d bytes: %q", len(where), msg)
		}
	})
	t.Run("expired link", func(t *testing.T) {
		msg := download(t, "expired.srt")
		if !strings.Contains(msg, "HTTP 492 at "+host+"/download/failed/aHR0cDovL2Fzc3J0Lm5ldC8=") {
			t.Fatalf("message lacks status or final host+path: %q", msg)
		}
		if !strings.Contains(msg, "啊呀 下载链接好像有点问题？请返回重试") || strings.ContainsAny(msg, "<>?") {
			t.Fatalf("message lacks the page text or carries markup: %q", msg)
		}
	})
}

// Go's transport errors quote the whole request URL, signature included; only the cause may pass.
func TestASSRTDownloadErrorHidesURL(t *testing.T) {
	check := func(t *testing.T, err error, causes ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("want error")
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "assrt download: ") || strings.Contains(msg, "SECRETSIG") || strings.Contains(msg, "?") {
			t.Fatalf("message leaks the URL or lacks the prefix: %q", msg)
		}
		for _, c := range causes {
			if strings.Contains(msg, c) {
				return
			}
		}
		t.Fatalf("message lacks the cause %q: %q", causes, msg)
	}

	t.Run("refused", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		_, err = NewASSRT("tok").Download(context.Background(), File{URL: "http://" + addr + "/onthefly/x.srt?_=1&-=SECRETSIG"})
		check(t, err, "refused")
	})
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select { // never answers
			case <-release:
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		a := newTestASSRT(srv, "tok")
		a.Client = &http.Client{Timeout: 100 * time.Millisecond}
		_, err := a.Download(context.Background(), File{URL: srv.URL + "/onthefly/x.srt?_=1&-=SECRETSIG"})
		check(t, err, "deadline", "timeout", "Timeout")
	})
	t.Run("bad url", func(t *testing.T) {
		_, err := NewASSRT("tok").Download(context.Background(), File{URL: "http://127.0.0.1/x\x7f.srt?_=1&-=SECRETSIG"})
		check(t, err, "invalid")
	})
}

func TestPageText(t *testing.T) {
	for _, c := range []struct {
		name, page  string
		want        string // exact, unless has/hasNot are set
		has, hasNot []string
	}{
		{name: "script and style",
			page: "<head><title>T</title><style>p{color:red}</style><script>var a = 1 < 2;</script></head><body><p>Real reason</p></body>",
			want: "T Real reason"},
		{name: "upper case and attributes",
			page: "<P>one</P><SCRIPT type=\"text/javascript\">alert(1)</Script><STYLE media=all>b{}</STYLE >two", want: "one two"},
		{name: "unclosed script", page: "seen<script>var a = 1;", want: "seen"},
		{name: "comment", page: "before<!-- hidden <b>note</b> -->after<!---->.", want: "before after ."},
		{name: "unclosed comment", page: "seen<!-- hidden", want: "seen"},
		{name: "entities", page: "<p>a &amp; b&nbsp;&nbsp;c &lt;3 &#x554a;</p>", want: "a & b c <3 啊"},
		{name: "control and format runes", page: "a\x1b[31mb\x00c\u202ed\r\ne\u200bf&#27;g", hasNot: []string{"\x1b", "\x00", "\u202e", "\r", "\n", "\u200b"}, has: []string{"a [31mb c d e f g"}},
		{name: "only tags", page: "<html><body><br/><script>x()</script><!-- c --> \n </body></html>", want: ""},
		{name: "empty", page: "", want: ""},
		{name: "invalid utf-8", page: "ok\xff\xfe \xe5\x95", want: "ok"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := pageText([]byte(c.page), 200)
			if len(c.has)+len(c.hasNot) == 0 && got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			for _, s := range c.has {
				if !strings.Contains(got, s) {
					t.Fatalf("%q lacks %q", got, s)
				}
			}
			for _, s := range c.hasNot {
				if strings.Contains(got, s) {
					t.Fatalf("%q contains %q", got, s)
				}
			}
		})
	}
	t.Run("truncation", func(t *testing.T) {
		for _, pad := range []string{"", "a", "ab"} {
			got := pageText([]byte("<p>"+pad+strings.Repeat("啊呀 &amp; 下载链接", 60)+"</p>"), 200)
			if len(got) > 200 || len(got) < 190 || !utf8.ValidString(got) {
				t.Fatalf("got %d bytes, valid=%v: %q", len(got), utf8.ValidString(got), got)
			}
		}
	})
}

func TestASSRTErrors(t *testing.T) {
	srv, _ := fakeASSRT(t)
	if _, err := newTestASSRT(srv, "bad").Search(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("want invalid token error, got %v", err)
	}
	if _, err := newTestASSRT(srv, "").Search(context.Background(), "x"); err != ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestASSRTGateSpacesCalls(t *testing.T) {
	srv, hits := fakeASSRT(t)
	a := newTestASSRT(srv, "tok")
	a.Spacing = 120 * time.Millisecond
	start := time.Now()
	a.Search(context.Background(), "a 1")
	a.Search(context.Background(), "a 2")
	a.Search(context.Background(), "a 3")
	if d := time.Since(start); d < 230*time.Millisecond {
		t.Fatalf("three calls took only %v; gate not spacing", d)
	}
	if *hits != 3 {
		t.Fatalf("hits %d", *hits)
	}
	// a cancelled context gives up waiting instead of hanging
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := a.Search(ctx, "a 4"); err == nil {
		t.Fatal("expected ctx error while gated")
	}
}
