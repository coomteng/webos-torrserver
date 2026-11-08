package torrremote

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// Config configures one App. Every field is optional except TorrServer.
type Config struct {
	// TorrServer is the URL the TV app and the phone page use to reach TorrServer,
	// e.g. http://192.168.0.9:8090. Empty means "same origin as this page", which is
	// right when the App is mounted inside TorrServer itself.
	TorrServer string
	// InternalTorrServer is what the sidecar's own goroutines (precache) call.
	// Defaults to TorrServer; when embedded it is TorrServer's loopback listener.
	InternalTorrServer string
	// BasePath is the URL prefix the App is mounted under ("" standalone, "/tr"
	// inside TorrServer). It is stripped before routing and prepended to every
	// URL the App hands out (phone page link, subtitle .vtt URLs).
	BasePath string

	Listen     string // standalone only
	Port       string // standalone only; also the fallback for Host-less requests
	AppDir     string // TV app files served under /tv/; empty disables
	SimFile    string // browser simulator html served at /sim; empty disables
	Verbose    bool
	SubsDir    string // where converted subtitles live; empty disables /subs/*
	AssrtToken string // assrt.net API token (free account); empty disables search
	Wikidata   string // Wikidata API for Chinese -> English titles (tests point it at a fake)
	DataDir    string // sidecar state (precache.json); empty = memory only
}

// App is the torr-remote service: phone -> TV channel, phone page, TV app hosting,
// title lookup, external subtitles and background pre-caching. Use New, mount
// Handler, Close on shutdown.
type App struct {
	cfg      Config
	remote   *Remote
	subs     *Subs
	titles   *Titles
	precache *Precache
}

// New builds the App and resumes unfinished pre-cache items.
func New(cfg Config) *App {
	cfg.TorrServer = strings.TrimRight(cfg.TorrServer, "/")
	cfg.InternalTorrServer = strings.TrimRight(cfg.InternalTorrServer, "/")
	if cfg.InternalTorrServer == "" {
		cfg.InternalTorrServer = cfg.TorrServer
	}
	cfg.BasePath = strings.TrimRight(cfg.BasePath, "/")
	if cfg.BasePath != "" && !strings.HasPrefix(cfg.BasePath, "/") {
		cfg.BasePath = "/" + cfg.BasePath
	}
	t := newTitles()
	if cfg.Wikidata != "" {
		t.Base = cfg.Wikidata
	}
	a := &App{cfg: cfg, remote: newRemote(), subs: newSubs(cfg), titles: t, precache: newPrecache(cfg)}
	a.precache.resume()
	return a
}

// newApp is the test-side shorthand.
func newApp(cfg Config) *App { return New(cfg) }

// Close stops the background goroutines (pre-cache runs) and waits for them.
func (a *App) Close() { a.precache.close() }

// SubsSources lists the enabled subtitle sources (for the startup log).
func (a *App) SubsSources() []string { return a.subs.sourceNames() }

// ---------------------------------------------------------------- response helpers

func cors(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
}

func send(w http.ResponseWriter, r *http.Request, status int, data []byte, ctype string, cacheSec int) {
	if ctype == "" {
		ctype = "application/json; charset=utf-8"
	}
	cors(w)
	w.Header().Set("Content-Type", ctype)
	if cacheSec > 0 {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(cacheSec))
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(data)
	}
}

func marshal(v interface{}) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func sendJSON(w http.ResponseWriter, r *http.Request, status int, v interface{}) {
	send(w, r, status, marshal(v), "", 0)
}

func sendError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	sendJSON(w, r, status, map[string]string{"error": msg})
}

func readJSONBody(r *http.Request) (map[string]interface{}, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]interface{}{}, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func atoi(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return n
}

func str(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---------------------------------------------------------------- routing

// Handler serves the whole App. Requests outside BasePath get 404.
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.Verbose {
			log.Printf("%s %s", r.Method, r.URL.RequestURI())
		}
		p := r.URL.Path
		if a.cfg.BasePath != "" {
			if p != a.cfg.BasePath && !strings.HasPrefix(p, a.cfg.BasePath+"/") {
				sendError(w, r, 404, "not found")
				return
			}
			p = strings.TrimPrefix(p, a.cfg.BasePath)
		}
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
		switch r.Method {
		case http.MethodOptions:
			cors(w)
			w.WriteHeader(204)
		case http.MethodGet, http.MethodHead:
			a.get(w, r, p)
		case http.MethodPost:
			a.post(w, r, p)
		default:
			sendError(w, r, 405, "method not allowed")
		}
	})
}

func (a *App) get(w http.ResponseWriter, r *http.Request, p string) {
	switch {
	case p == "/" && a.cfg.SimFile != "":
		w.Header().Set("Location", "/sim")
		w.WriteHeader(302)
	case p == "/sim" && a.cfg.SimFile != "":
		a.serveSim(w, r)
	case p == "/tv" || strings.HasPrefix(p, "/tv/"):
		a.serveApp(w, r, strings.TrimPrefix(p, "/tv"))
	case p == "/config":
		sendJSON(w, r, 200, map[string]interface{}{"torrserver": a.cfg.TorrServer, "subs": a.subs.configInfo()})
	case p == "/health":
		sendJSON(w, r, 200, map[string]interface{}{"ok": true, "channels": a.remote.count()})
	case strings.HasPrefix(p, "/r/"):
		a.servePhonePage(w, r, strings.TrimPrefix(p, "/r/"))
	case strings.HasPrefix(p, "/remote/"):
		a.remoteGet(w, r, p)
	case strings.HasPrefix(p, "/subs/"):
		a.subsGet(w, r, p)
	case p == "/title":
		a.titleGet(w, r)
	case p == "/precache":
		a.precacheGet(w, r)
	default:
		sendError(w, r, 404, "not found")
	}
}

func (a *App) post(w http.ResponseWriter, r *http.Request, p string) {
	if strings.HasPrefix(p, "/subs/") {
		a.subsPost(w, r, p) // reads its own body (upload is raw bytes)
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		sendError(w, r, 400, "bad JSON body")
		return
	}
	switch {
	case p == "/remote/new":
		a.remoteNew(w, r, body)
	case strings.HasPrefix(p, "/remote/"):
		a.remotePost(w, r, p, body)
	case p == "/precache" || strings.HasPrefix(p, "/precache/"):
		a.precachePost(w, r, p, body)
	default:
		sendError(w, r, 404, "not found")
	}
}
