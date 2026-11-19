package subs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPkgCacheRoundTrip(t *testing.T) {
	c := NewPkgCache(t.TempDir())
	if _, ok := c.Archive("42"); ok {
		t.Fatal("empty cache has an archive")
	}
	c.PutArchive("42", []byte("ARCHIVE"))
	c.PutFile("42", "a/b 中文.srt", []byte("FILE"))
	c.PutList("42", PkgList{Archive: "p.rar", Files: []string{"x.srt", "y.srt"}})
	if b, ok := c.Archive("42"); !ok || string(b) != "ARCHIVE" {
		t.Fatalf("archive: %q %v", b, ok)
	}
	if b, ok := c.File("42", "a/b 中文.srt"); !ok || string(b) != "FILE" {
		t.Fatalf("file: %q %v", b, ok)
	}
	if _, ok := c.File("42", "other.srt"); ok {
		t.Fatal("a file that was never stored")
	}
	if l, ok := c.List("42"); !ok || l.Archive != "p.rar" || len(l.Files) != 2 {
		t.Fatalf("list: %+v %v", l, ok)
	}
	// a second cache on the same directory (a restart) sees the same
	if b, ok := NewPkgCache(c.Dir).Archive("42"); !ok || string(b) != "ARCHIVE" {
		t.Fatalf("after restart: %q %v", b, ok)
	}
}

func TestPkgCacheRejectsBadID(t *testing.T) {
	dir := t.TempDir()
	c := NewPkgCache(filepath.Join(dir, "cache"))
	for _, id := range []string{"", "..", "../x", "a/b", "a.b", strings.Repeat("1", 33)} {
		c.PutArchive(id, []byte("X"))
		if _, ok := c.Archive(id); ok {
			t.Errorf("id %q accepted", id)
		}
	}
	var names []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	if len(names) != 0 {
		t.Fatalf("files written: %v", names)
	}
	var none *PkgCache // no cache configured
	none.PutArchive("42", []byte("X"))
	if _, ok := none.Archive("42"); ok {
		t.Fatal("nil cache returned data")
	}
}

func TestPkgCacheRemovesLeastRecentlyUsed(t *testing.T) {
	c := NewPkgCache(t.TempDir())
	c.Max = 250
	blob := bytes.Repeat([]byte("x"), 100)
	old := time.Now().Add(-time.Hour)
	c.PutArchive("1", blob)
	os.Chtimes(filepath.Join(c.Dir, "1"), old, old)
	c.PutArchive("2", blob)
	os.Chtimes(filepath.Join(c.Dir, "2"), old.Add(time.Minute), old.Add(time.Minute))
	if _, ok := c.Archive("1"); !ok { // used now: 2 is the older one
		t.Fatal("1 missing before the limit")
	}
	c.PutArchive("3", blob)
	_, ok1 := c.Archive("1")
	_, ok2 := c.Archive("2")
	_, ok3 := c.Archive("3")
	if !ok1 || ok2 || !ok3 {
		t.Fatalf("kept 1=%v 2=%v 3=%v, want 2 removed", ok1, ok2, ok3)
	}
	// one package larger than the limit is still kept: it was just asked for
	c.PutArchive("4", bytes.Repeat([]byte("x"), 400))
	if _, ok := c.Archive("4"); !ok {
		t.Fatal("the package just stored was removed")
	}
}

// seasonCDN is an ASSRT whose package 42 holds two episodes in two languages. perFile is the
// status of the per-file links (the subtitle itself when 200). It counts the requests.
type seasonCDN struct {
	srv                      *httptest.Server
	detail, perFile, archive int32
}

const (
	srtE1Chs = "1\n00:00:01,000 --> 00:00:02,000\n第一集\n"
	srtE1Eng = "1\n00:00:01,000 --> 00:00:02,000\nepisode one\n"
	srtE2Chs = "1\n00:00:01,000 --> 00:00:02,000\n第二集\n"
	srtE2Eng = "1\n00:00:01,000 --> 00:00:02,000\nepisode two\n"
)

var seasonFiles = map[string]string{
	"Show.S01E01.chs.srt": srtE1Chs, "Show.S01E01.eng.srt": srtE1Eng,
	"Show.S01E02.chs.srt": srtE2Chs, "Show.S01E02.eng.srt": srtE2Eng,
}

func newSeasonCDN(t *testing.T, perFileStatus int) *seasonCDN {
	t.Helper()
	pack := makeZip(t, seasonFiles)
	c := &seasonCDN{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sub/detail":
			atomic.AddInt32(&c.detail, 1)
			var list []string
			for _, n := range []string{"Show.S01E01.chs.srt", "Show.S01E01.eng.srt", "Show.S01E02.chs.srt", "Show.S01E02.eng.srt"} {
				list = append(list, `{"f":"`+n+`","url":"http://`+r.Host+`/onthefly/42/-/1/`+n+`?_=1&-=SECRETSIG"}`)
			}
			w.Write([]byte(`{"status":0,"sub":{"subs":[{"id":42,"filename":"pack.zip","url":"http://` + r.Host +
				`/download/42/pack.zip?_=1&-=SECRETSIG","filelist":[` + strings.Join(list, ",") + `]}]}}`))
		case strings.HasPrefix(r.URL.Path, "/onthefly/"):
			atomic.AddInt32(&c.perFile, 1)
			if perFileStatus != 200 {
				w.WriteHeader(perFileStatus)
				return
			}
			w.Write([]byte(seasonFiles[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]))
		case strings.HasPrefix(r.URL.Path, "/download/"):
			atomic.AddInt32(&c.archive, 1)
			w.Write(pack)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *seasonCDN) counts() [3]int32 {
	return [3]int32{atomic.LoadInt32(&c.detail), atomic.LoadInt32(&c.perFile), atomic.LoadInt32(&c.archive)}
}

// get does what the /subs/fetch handler does: list the package's files, download the named one.
func (c *seasonCDN) get(t *testing.T, a *ASSRT, name string) string {
	t.Helper()
	files, err := a.Files(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == name {
			data, err := a.Download(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("%s not in %v", name, files)
	return ""
}

func cachedASSRT(srv *httptest.Server, dir string) *ASSRT {
	a := newTestASSRT(srv, "tok")
	a.Spacing = time.Millisecond
	a.Cache = NewPkgCache(dir)
	return a
}

// The per-file links fail (as they do on the real site), so the first episode downloads the
// whole package. Every other episode and language must come from the disk: no API call, no
// download. That holds across a restart.
func TestASSRTPackageIsDownloadedOnce(t *testing.T) {
	cdn := newSeasonCDN(t, 493)
	dir := t.TempDir()
	a := cachedASSRT(cdn.srv, dir)

	if got := cdn.get(t, a, "Show.S01E01.chs.srt"); got != srtE1Chs {
		t.Fatalf("E01 chs: %q", got)
	}
	if n := cdn.counts(); n != [3]int32{1, 1, 1} {
		t.Fatalf("first fetch: detail/per-file/archive = %v, want 1/1/1", n)
	}
	for name, want := range seasonFiles {
		if got := cdn.get(t, a, name); got != want {
			t.Fatalf("%s: %q", name, got)
		}
	}
	if got := cdn.get(t, cachedASSRT(cdn.srv, dir), "Show.S01E02.eng.srt"); got != srtE2Eng {
		t.Fatalf("after restart: %q", got)
	}
	if n := cdn.counts(); n != [3]int32{1, 1, 1} {
		t.Fatalf("later fetches went to the network: detail/per-file/archive = %v, want 1/1/1", n)
	}
}

// When the per-file link works, that one file is kept; another file of the package needs fresh
// links (they are signed for a short time), so one API call and one download more.
func TestASSRTSingleFileIsDownloadedOnce(t *testing.T) {
	cdn := newSeasonCDN(t, 200)
	a := cachedASSRT(cdn.srv, t.TempDir())
	for i := 0; i < 3; i++ {
		if got := cdn.get(t, a, "Show.S01E01.chs.srt"); got != srtE1Chs {
			t.Fatalf("E01 chs: %q", got)
		}
	}
	if n := cdn.counts(); n != [3]int32{1, 1, 0} {
		t.Fatalf("same file three times: detail/per-file/archive = %v, want 1/1/0", n)
	}
	if got := cdn.get(t, a, "Show.S01E02.eng.srt"); got != srtE2Eng {
		t.Fatalf("E02 eng: %q", got)
	}
	if n := cdn.counts(); n != [3]int32{2, 2, 0} {
		t.Fatalf("another file: detail/per-file/archive = %v, want 2/2/0", n)
	}
}

// A stored package that has gone bad on disk is not an error: it is downloaded again and replaced.
func TestASSRTDamagedCacheIsRefetched(t *testing.T) {
	cdn := newSeasonCDN(t, 493)
	a := cachedASSRT(cdn.srv, t.TempDir())
	cdn.get(t, a, "Show.S01E01.chs.srt")
	if err := os.WriteFile(filepath.Join(a.Cache.Dir, "42", "archive"), []byte("PK\x03\x04 garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := cdn.get(t, a, "Show.S01E02.chs.srt"); got != srtE2Chs {
		t.Fatalf("E02 chs: %q", got)
	}
	if n := cdn.counts(); n != [3]int32{2, 2, 2} {
		t.Fatalf("refetch: detail/per-file/archive = %v, want 2/2/2", n)
	}
	if got := cdn.get(t, a, "Show.S01E02.eng.srt"); got != srtE2Eng {
		t.Fatalf("E02 eng: %q", got)
	}
	if n := cdn.counts(); n != [3]int32{2, 2, 2} {
		t.Fatalf("the replaced package was not used: %v", n)
	}
}

// What is not a subtitle is not kept: a per-file link that answers 200 with an error page must
// not make that page the answer for good.
func TestASSRTDoesNotKeepGarbage(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sub/detail":
			w.Write([]byte(`{"status":0,"sub":{"subs":[{"id":42,"filename":"pack.zip","url":"http://` + r.Host +
				`/download/42/pack.zip","filelist":[{"f":"x.chs.srt","url":"http://` + r.Host + `/onthefly/x.chs.srt"}]}]}}`))
		default:
			if atomic.AddInt32(&hits, 1) == 1 {
				w.Write([]byte("<html>busy, try later</html>"))
				return
			}
			w.Write([]byte(srtE1Chs))
		}
	}))
	t.Cleanup(srv.Close)
	a := cachedASSRT(srv, t.TempDir())
	c := &seasonCDN{}
	if got := c.get(t, a, "x.chs.srt"); !strings.Contains(got, "busy") {
		t.Fatalf("first answer: %q", got)
	}
	if got := c.get(t, a, "x.chs.srt"); got != srtE1Chs {
		t.Fatalf("second answer: %q (the error page was kept)", got)
	}
}

func TestStoreAddSameSubtitleTwiceKeepsOne(t *testing.T) {
	s := NewStore(t.TempDir())
	hash := "0123456789abcdef0123456789abcdef01234567"
	a, err := s.Add(hash, 1, "x.chs.srt", "assrt", []byte(srtE1Chs))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Add(hash, 1, "x.chs.srt", "assrt", []byte(srtE1Chs))
	if err != nil || b.N != a.N {
		t.Fatalf("second add: #%d, %v; want #%d again", b.N, err, a.N)
	}
	// same name, other text (a corrected upload) is a new one; so is another source
	c, _ := s.Add(hash, 1, "x.chs.srt", "assrt", []byte(srtE2Chs))
	d, _ := s.Add(hash, 1, "x.chs.srt", "upload", []byte(srtE1Chs))
	if list, _ := s.List(hash, 1); len(list) != 3 || c.N != 2 || d.N != 3 {
		t.Fatalf("list %+v", list)
	}
}
