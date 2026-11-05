package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type App struct {
	cfg    Config
	remote *Remote
}

func newApp(cfg Config) *App { return &App{cfg: cfg, remote: newRemote()} }

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

func (a *App) routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.Verbose {
			log.Printf("%s %s", r.Method, r.URL.RequestURI())
		}
		p := strings.TrimRight(r.URL.Path, "/")
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
		sendJSON(w, r, 200, map[string]string{"torrserver": a.cfg.TorrServer})
	case p == "/health":
		sendJSON(w, r, 200, map[string]interface{}{"ok": true, "channels": a.remote.count()})
	case strings.HasPrefix(p, "/r/"):
		a.servePhonePage(w, r, strings.TrimPrefix(p, "/r/"))
	case strings.HasPrefix(p, "/remote/"):
		a.remoteGet(w, r, p)
	default:
		sendError(w, r, 404, "not found")
	}
}

func (a *App) post(w http.ResponseWriter, r *http.Request, p string) {
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
	default:
		sendError(w, r, 404, "not found")
	}
}
