package torrremote

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mounted inside TorrServer under /tr: every route lives below the prefix, the
// bare paths are 404, and every URL the service hands out (phone page link,
// subtitle .vtt, same-origin fetches in the phone page) carries the prefix.
func TestBasePathMount(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>tv</html>"), 0o644)
	app := newApp(Config{BasePath: "/tr", AppDir: dir, SubsDir: filepath.Join(dir, "subs")})
	t.Cleanup(app.Close)
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)

	get := func(p string) (int, string) {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	if st, _ := get("/tr/health"); st != 200 {
		t.Fatalf("/tr/health: %d", st)
	}
	if st, _ := get("/health"); st != 404 {
		t.Fatalf("/health outside the prefix: %d, want 404", st)
	}
	if st, _ := get("/trx/health"); st != 404 {
		t.Fatalf("/trx/health must not match /tr: %d", st)
	}
	if st, body := get("/tr/tv/"); st != 200 || body != "<html>tv</html>" {
		t.Fatalf("/tr/tv/: %d %q", st, body)
	}
	// empty TorrServer = same origin: /config says so with ""
	if _, body := get("/tr/config"); !strings.Contains(body, `"torrserver":""`) {
		t.Fatalf("/tr/config: %s", body)
	}

	resp, m := postJSON(t, srv.URL+"/tr/remote/new", map[string]string{"id": "abc234"})
	if resp.StatusCode != 200 {
		t.Fatalf("remote/new: %d", resp.StatusCode)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if m["url"] != "http://"+host+"/tr/r/abc234" {
		t.Fatalf("phone url %v", m["url"])
	}
	st, page := get("/tr/r/abc234")
	if st != 200 || !strings.Contains(page, `BASE="/tr"`) || !strings.Contains(page, `TS=""||location.origin`) {
		t.Fatalf("phone page: %d, BASE/TS not injected", st)
	}
	if st, _ := get("/tr/remote/abc234/qr.svg"); st != 200 {
		t.Fatalf("qr: %d", st)
	}

	// subtitle URLs carry the prefix
	rec, err := app.subs.add("0123456789abcdef0123456789abcdef01234567", 1, "x.srt", "upload",
		[]byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	saved := app.withURL(rec, "0123456789abcdef0123456789abcdef01234567", 1)
	if saved.URL != "/tr/subs/0123456789abcdef0123456789abcdef01234567/1/1.vtt" {
		t.Fatalf("vtt url %q", saved.URL)
	}
	if st, body := get(saved.URL); st != 200 || !strings.HasPrefix(body, "WEBVTT") {
		t.Fatalf("vtt fetch: %d %q", st, body)
	}
}
