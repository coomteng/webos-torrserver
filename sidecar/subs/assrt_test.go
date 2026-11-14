package subs

import (
	"archive/zip"
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
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

// The package's whole-archive link comes in the same detail response; Files keeps it next to
// every per-file link so that Download can fall back to it.
func TestASSRTFilesKeepArchive(t *testing.T) {
	srv, _ := fakeASSRT(t)
	files, err := newTestASSRT(srv, "tok").Files(context.Background(), "173817")
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	if !strings.HasPrefix(files[0].Archive, "http://file1.assrt.net/download/173817/") || files[0].ArchiveName != "2010_Sintel_UTF_8_tw.rar" {
		t.Fatalf("archive not kept: %+v", files[0])
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

// fallbackCDN answers /onthefly/… with perFileStatus (the subtitle itself when 200) and hands
// /download/… to archive, counting those hits.
func fallbackCDN(t *testing.T, perFileStatus int, archive http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/onthefly/"):
			if perFileStatus != 200 {
				w.WriteHeader(perFileStatus)
				w.Write([]byte("<html><body><h1>啊呀！服务器抽风啦！请稍后重试~</h1></body></html>"))
				return
			}
			w.Write([]byte(srtUTF8))
		case strings.HasPrefix(r.URL.Path, "/download/"):
			atomic.AddInt32(&hits, 1)
			archive(w, r)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// fallbackFile is a File as Files builds it: a per-file link and the package's archive link,
// both signed.
func fallbackFile(base, name, archiveName string) File {
	return File{Name: name, URL: base + "/onthefly/42/-/1/" + name + "?_=1&-=SECRETSIG",
		Archive: base + "/download/42/" + archiveName + "?_=1&-=SECRETSIG", ArchiveName: archiveName}
}

func serveBytes(b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write(b) }
}

const srtEng = "1\n00:00:01,000 --> 00:00:02,000\nenglish only\n"

// ASSRT's per-file extraction fails server-side (493); the member is taken out of the package's
// archive instead.
func TestASSRTFallbackZip(t *testing.T) {
	z := makeZip(t, map[string]string{"pack/x.chs.srt": srtUTF8, "pack/x.eng.srt": srtEng})
	srv, hits := fallbackCDN(t, 493, serveBytes(z))
	data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.zip"))
	if err != nil || string(data) != srtUTF8 {
		t.Fatalf("download err=%v len=%d", err, len(data))
	}
	if *hits != 1 {
		t.Fatalf("archive hits %d", *hits)
	}
}

func TestASSRTPerFileSuccessSkipsArchive(t *testing.T) {
	srv, hits := fallbackCDN(t, 200, serveBytes(makeZip(t, map[string]string{"x.chs.srt": srtEng})))
	data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.zip"))
	if err != nil || string(data) != srtUTF8 {
		t.Fatalf("download err=%v len=%d", err, len(data))
	}
	if *hits != 0 {
		t.Fatalf("archive requested %d times although the per-file link worked", *hits)
	}
}

// rar is read since rardecode was added (rar_test.go); 7z still is not.
func TestASSRTFallbackUnsupported(t *testing.T) {
	for _, c := range []struct{ kind, magic string }{{"7z", "7z\xbc\xaf\x27\x1c"}} {
		t.Run(c.kind, func(t *testing.T) {
			srv, _ := fallbackCDN(t, 493, serveBytes([]byte(c.magic+strings.Repeat("\x00", 64))))
			_, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack."+c.kind))
			want := "assrt: per-file download failed (HTTP 493) and the archive is " + c.kind + ", which is not supported: pack." + c.kind
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}

func TestASSRTFallbackBothFail(t *testing.T) {
	check := func(t *testing.T, err error, want string) {
		t.Helper()
		if err == nil {
			t.Fatal("want error")
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, want) {
			t.Fatalf("got %q, want prefix %q", msg, want)
		}
		if strings.Contains(msg, "SECRETSIG") || strings.Contains(msg, "?") || len(msg) > 160 || !utf8.ValidString(msg) {
			t.Fatalf("message leaks the URL or is too long (%d bytes): %q", len(msg), msg)
		}
	}
	t.Run("status", func(t *testing.T) {
		srv, hits := fallbackCDN(t, 493, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			w.Write([]byte("<html><body>" + strings.Repeat("not here ", 100) + "</body></html>"))
		})
		_, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.zip"))
		check(t, err, "assrt: per-file download failed (HTTP 493); archive failed too (HTTP 404)")
		if err.Error() != "assrt: per-file download failed (HTTP 493); archive failed too (HTTP 404)" || *hits != 1 {
			t.Fatalf("got %q after %d archive hits", err, *hits)
		}
	})
	t.Run("refused", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		srv, _ := fallbackCDN(t, 493, serveBytes(nil))
		f := fallbackFile(srv.URL, "x.chs.srt", "pack.zip")
		f.Archive = "http://" + addr + "/download/42/pack.zip?_=1&-=SECRETSIG"
		_, err = newTestASSRT(srv, "tok").Download(context.Background(), f)
		check(t, err, "assrt: per-file download failed (HTTP 493); archive failed too (")
		if !strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "assrt download") {
			t.Fatalf("want the short cause: %q", err)
		}
	})
}

func TestASSRTFallbackArchiveTooLarge(t *testing.T) {
	if maxArchiveBytes != 20<<20 {
		t.Fatalf("archive cap is %d bytes, want 20 MB", maxArchiveBytes)
	}
	old := maxArchiveBytes
	maxArchiveBytes = 1 << 20
	t.Cleanup(func() { maxArchiveBytes = old })
	srv, _ := fallbackCDN(t, 493, serveBytes(bytes.Repeat([]byte("PK\x03\x04"), 1<<18+1)))
	data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.zip"))
	if want := "assrt: per-file download failed (HTTP 493) and the archive is larger than 1 MB: pack.zip"; err == nil || err.Error() != want {
		t.Fatalf("got %d bytes, err=%v, want %q", len(data), err, want)
	}
}

// The archive's file name is the remote server's to choose and ends up in the log and on the TV:
// one line, no control or format characters, bounded; a placeholder when nothing is left.
func TestASSRTFallbackArchiveNameSanitised(t *testing.T) {
	old := maxArchiveBytes
	maxArchiveBytes = 1 << 10
	t.Cleanup(func() { maxArchiveBytes = old })
	const pre = "assrt: per-file download failed (HTTP 493) and the archive "
	hostile := "evil\nINJECTED LOG LINE\r\n<x>?sig=SECRET"
	bodies := []struct {
		name          string
		body          []byte
		hostile, none string // message for the hostile name and for no name
	}{
		{"7z", []byte("7z\xbc\xaf\x27\x1c"), pre + "is 7z, which is not supported: evil INJECTED LOG LINE <x>?sig=SECRET", pre + "is 7z, which is not supported: package"},
		{"broken rar", []byte("Rar!\x1a\x07\x00"), pre + "evil INJECTED LOG LINE <x>?sig=SECRET could not be read", pre + "package could not be read"},
		{"rar without subtitle", makeRar5(0, 0, rarPairs("readme.txt", "nope")...), pre + "evil INJECTED LOG LINE <x>?sig=SECRET has no subtitle file", pre + "package has no subtitle file"},
		{"unknown", []byte("\x1f\x8b\x08 something else"), pre + "evil INJECTED LOG LINE <x>?sig=SECRET is in an unknown format", pre + "package is in an unknown format"},
		{"broken zip", []byte("PK\x03\x04 truncated"), pre + "evil INJECTED LOG LINE <x>?sig=SECRET could not be read", pre + "package could not be read"},
		{"no subtitle", makeZip(t, map[string]string{"readme.txt": "nope"}), pre + "evil INJECTED LOG LINE <x>?sig=SECRET has no subtitle file", pre + "package has no subtitle file"},
		{"too large", bytes.Repeat([]byte("PK\x03\x04"), 512), pre + "is larger than 0 MB: evil INJECTED LOG LINE <x>?sig=SECRET", pre + "is larger than 0 MB: package"},
	}
	for _, c := range bodies {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := fallbackCDN(t, 493, serveBytes(c.body))
			download := func(archiveName string) string {
				t.Helper()
				f := fallbackFile(srv.URL, "x.chs.srt", "pack.zip")
				f.ArchiveName = archiveName
				_, err := newTestASSRT(srv, "tok").Download(context.Background(), f)
				if err == nil {
					t.Fatalf("%q: want error", archiveName)
				}
				msg := err.Error()
				for _, r := range msg {
					if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
						t.Fatalf("control or format character %U in %q", r, msg)
					}
				}
				if len(msg) > 200 || !utf8.ValidString(msg) {
					t.Fatalf("message is %d bytes or not UTF-8: %q", len(msg), msg)
				}
				return msg
			}
			if got := download(hostile); got != c.hostile {
				t.Fatalf("hostile name:\n got %q\nwant %q", got, c.hostile)
			}
			for _, empty := range []string{"", " \r\n\t", "\x1b\u202e\u200b"} {
				if got := download(empty); got != c.none {
					t.Fatalf("name %q:\n got %q\nwant %q", empty, got, c.none)
				}
			}
			// escape, bidi override, zero-width and line separators, then cut between runes
			if got := download("a\x1b[31m\u202eb\u2028c\x00" + strings.Repeat("字幕", 40) + ".rar"); !strings.Contains(got, "a [31m b c "+strings.Repeat("字幕", 11)) || strings.Contains(got, ".rar") {
				t.Fatalf("long name: %q", got)
			}
		})
	}
}

// The wanted member is in the zip but cannot be read: say so rather than hand on the whole zip,
// from which another member would be picked.
func TestASSRTFallbackMemberUnreadable(t *testing.T) {
	old := maxZipMember
	maxZipMember = 100
	t.Cleanup(func() { maxZipMember = old })
	const want = "assrt: per-file download failed (HTTP 493) and x.chs.srt in the archive could not be read"
	big := strings.Repeat("y", 300)

	var lying bytes.Buffer // declares 10 bytes, carries 300
	zw := zip.NewWriter(&lying)
	w, _ := zw.CreateRaw(&zip.FileHeader{Name: "x.chs.srt", Method: zip.Store, CompressedSize64: 300, UncompressedSize64: 10})
	w.Write([]byte(big))
	w, _ = zw.Create("x.eng.srt")
	w.Write([]byte(srtEng))
	zw.Close()

	for name, z := range map[string][]byte{
		"oversize":    makeZipOrdered(t, "pack/x.chs.srt", big, "pack/x.eng.srt", srtEng),
		"folded name": makeZipOrdered(t, "pack/X.CHS.SRT", big, "pack/x.eng.srt", srtEng),
		"lying size":  lying.Bytes(),
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := fallbackCDN(t, 493, serveBytes(z))
			data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.zip"))
			w := want
			if name == "folded name" { // the member is named as the archive names it
				w = strings.Replace(want, "x.chs.srt", "X.CHS.SRT", 1)
			}
			if err == nil || err.Error() != w {
				t.Fatalf("got %d bytes, err=%v, want %q", len(data), err, w)
			}
		})
	}
	// another member being too large does not matter
	srv, _ := fallbackCDN(t, 493, serveBytes(makeZipOrdered(t, "x.chs.srt", srtEng, "x.eng.srt", big)))
	if data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.zip")); err != nil || string(data) != srtEng {
		t.Fatalf("readable member: err=%v len=%d", err, len(data))
	}
}

// Chinese archives carry GBK member names without the UTF-8 flag; the file list names are UTF-8.
func TestASSRTFallbackGBKName(t *testing.T) {
	const name = "流浪地球.简体中文.srt"
	gbk, err := simplifiedchinese.GBK.NewEncoder().String("字幕/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: gbk, NonUTF8: true, Method: zip.Deflate})
	w.Write([]byte(srtUTF8))
	w, _ = zw.Create("other.eng.srt")
	w.Write([]byte(srtEng))
	zw.Close()
	srv, _ := fallbackCDN(t, 493, serveBytes(buf.Bytes()))
	data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, name, "pack.zip"))
	if err != nil || string(data) != srtUTF8 {
		t.Fatalf("download err=%v len=%d", err, len(data))
	}

	// archives made on Windows may separate directories with a backslash
	if es := Expand("pack.zip", makeZip(t, map[string]string{`dir\a.chs.srt`: srtUTF8})); len(es) != 1 || es[0].Name != "a.chs.srt" {
		t.Fatalf("backslash name: %+v", names(es))
	}
}

// The archive's members are named differently from the file list: hand the whole zip on, the
// caller expands it and picks by the video's name.
func TestASSRTFallbackNoNameMatch(t *testing.T) {
	z := makeZip(t, map[string]string{"pack/Show.S01E01.chs.srt": srtUTF8, "pack/Show.S01E02.chs.srt": srtEng, "readme.txt": "nope"})
	srv, _ := fallbackCDN(t, 493, serveBytes(z))
	data, err := newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", "pack.zip"))
	if err != nil || !bytes.Equal(data, z) {
		t.Fatalf("download err=%v len=%d, want the %d zip bytes", err, len(data), len(z))
	}
	if es := Expand("x.chs.srt", data); len(es) != 2 {
		t.Fatalf("expanded %v", names(es))
	}
}

// What is not a zip, rar or 7z is the subtitle itself if the package is named like one; a zip
// that cannot be opened is an error, not a subtitle.
func TestASSRTFallbackOtherFormats(t *testing.T) {
	download := func(t *testing.T, body []byte, archiveName string) ([]byte, error) {
		t.Helper()
		srv, _ := fallbackCDN(t, 493, serveBytes(body))
		return newTestASSRT(srv, "tok").Download(context.Background(), fallbackFile(srv.URL, "x.chs.srt", archiveName))
	}
	if data, err := download(t, []byte(srtUTF8), "x.chs.srt"); err != nil || string(data) != srtUTF8 {
		t.Fatalf("plain subtitle: err=%v len=%d", err, len(data))
	}
	long := strings.Repeat("字幕", 40) + ".bin"
	_, err := download(t, []byte("\x1f\x8b\x08 something else"), long)
	if err == nil || !strings.HasPrefix(err.Error(), "assrt: per-file download failed (HTTP 493) and the archive ") || !strings.HasSuffix(err.Error(), " is in an unknown format") {
		t.Fatalf("unknown format: %v", err)
	}
	if !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "the archive "+strings.Repeat("字幕", 13)+" is") {
		t.Fatalf("archive name not cut to 80 bytes between runes: %q", err)
	}
	_, err = download(t, []byte("PK\x03\x04 truncated"), "pack.zip")
	if want := "assrt: per-file download failed (HTTP 493) and the archive pack.zip could not be read"; err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	// a package named like a subtitle is one only up to the per-file size
	body := bytes.Repeat([]byte("x"), 8<<20)
	if data, err := download(t, body, "x.chs.srt"); err != nil || len(data) != len(body) {
		t.Fatalf("subtitle of the per-file size: err=%v len=%d", err, len(data))
	}
	data, err := download(t, append(body, 'x'), "x.chs.srt")
	if want := "assrt: per-file download failed (HTTP 493) and the archive x.chs.srt is in an unknown format"; err == nil || err.Error() != want {
		t.Fatalf("got %d bytes, err=%v, want %q", len(data), err, want)
	}
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
