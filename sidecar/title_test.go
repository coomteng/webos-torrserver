package torrremote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeWikidata serves the recorded responses in testdata/ (沙丘 -> dune, 流浪地球 -> wandering).
func fakeWikidata(t *testing.T, calls *int32, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		q := r.URL.Query()
		name := ""
		switch {
		case q.Get("action") == "wbsearchentities" && q.Get("search") == "沙丘":
			name = "wd_search_dune.json"
		case q.Get("action") == "wbsearchentities" && q.Get("search") == "流浪地球":
			name = "wd_search_wandering.json"
		case q.Get("action") == "wbsearchentities":
			w.Write([]byte(`{"search":[]}`))
			return
		case q.Get("action") == "wbgetentities" && strings.Contains(q.Get("ids"), "Q60834962"):
			name = "wd_entities_dune.json"
		case q.Get("action") == "wbgetentities":
			name = "wd_entities_wandering.json"
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("no User-Agent sent to wikidata")
		}
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatalf("fixture %q: %v", name, err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testTitles(base string) *Titles {
	tt := newTitles()
	tt.Base = base
	tt.Spacing = 0
	return tt
}

func TestTitleLookupFilmsOnlyNewestFirst(t *testing.T) {
	var calls int32
	tt := testTitles(fakeWikidata(t, &calls, 0).URL)
	c, err := tt.Lookup(context.Background(), "流浪地球")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, x := range c {
		got = append(got, x.En+"/"+strconv.Itoa(x.Year))
	}
	// novella (2001), wikiproject item and the essay are dropped; films newest first
	want := "The Wandering Earth 3/2027 The Wandering Earth 2/2023 The Wandering Earth/2019"
	if strings.Join(got, " ") != want {
		t.Fatalf("candidates:\n got %v\nwant %s", got, want)
	}
	if c[2].Kind != "film" || c[2].ID != "Q57966215" || c[2].Label != "流浪地球" {
		t.Fatalf("fields: %+v", c[2])
	}
}

func TestTitleLookupUsesMulLabelAndSkipsNonFilms(t *testing.T) {
	var calls int32
	tt := testTitles(fakeWikidata(t, &calls, 0).URL)
	c, err := tt.Lookup(context.Background(), "沙丘")
	if err != nil {
		t.Fatal(err)
	}
	// the landform, board game, novel, grassland and crane are not films; the 2021 film has only a "mul" label
	if len(c) != 2 || c[0].En != "Dune" || c[0].Year != 2021 || c[1].En != "Dune" || c[1].Year != 1984 {
		t.Fatalf("candidates: %+v", c)
	}
}

func TestTitleLookupCachesAndEmpty(t *testing.T) {
	var calls int32
	tt := testTitles(fakeWikidata(t, &calls, 0).URL)
	tt.Lookup(context.Background(), "沙丘")
	n := atomic.LoadInt32(&calls)
	tt.Lookup(context.Background(), "沙丘")
	if atomic.LoadInt32(&calls) != n {
		t.Fatalf("second lookup hit wikidata (%d -> %d calls)", n, calls)
	}
	c, err := tt.Lookup(context.Background(), "小气鬼")
	if err != nil || len(c) != 0 {
		t.Fatalf("unknown title: %v %v", c, err)
	}
}

func TestTitleLookupBacksOffOn429(t *testing.T) {
	var calls int32
	tt := testTitles(fakeWikidata(t, &calls, 429).URL)
	if _, err := tt.Lookup(context.Background(), "沙丘"); err != errTitleBusy {
		t.Fatalf("429: err=%v", err)
	}
	if _, err := tt.Lookup(context.Background(), "流浪地球"); err != errTitleBusy || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("during back-off: err=%v calls=%d (want no new request)", err, calls)
	}
}

func TestTitleEndpoint(t *testing.T) {
	var calls int32
	wd := fakeWikidata(t, &calls, 0)
	app, srv := newTestApp(t)
	app.titles = testTitles(wd.URL)
	_, m := getJSON(t, srv.URL+"/title?q="+"%E6%B2%99%E4%B8%98")
	c, _ := m["candidates"].([]interface{})
	if len(c) != 2 || c[0].(map[string]interface{})["en"] != "Dune" {
		t.Fatalf("/title 沙丘: %v", m)
	}
	n := atomic.LoadInt32(&calls)
	_, m = getJSON(t, srv.URL+"/title?q=Dune")
	if c, _ := m["candidates"].([]interface{}); len(c) != 0 || atomic.LoadInt32(&calls) != n {
		t.Fatalf("latin query should not be translated: %v", m)
	}
	r, _ := http.Get(srv.URL + "/title")
	if r.StatusCode != 400 {
		t.Fatalf("missing q: %d", r.StatusCode)
	}
	app.titles = testTitles(fakeWikidata(t, &calls, 429).URL)
	r, _ = http.Get(srv.URL + "/title?q=%E6%B5%81%E6%B5%AA%E5%9C%B0%E7%90%83")
	if r.StatusCode != 429 {
		t.Fatalf("busy: %d", r.StatusCode)
	}
}
