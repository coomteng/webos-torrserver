package subs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
