package main

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// --------------------------------------------------------------------------- static (app + simulator)

// staticFile returns (bytes, content-type, true) for rel inside root, or false if missing /
// outside root. Mirrors static_file(): realpath both sides so symlinks and ".." cannot escape.
func staticFile(root, rel string) ([]byte, string, bool) {
	if root == "" {
		return nil, "", false
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, "", false
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, "", false
	}
	full := filepath.Join(base, filepath.FromSlash(strings.TrimLeft(rel, "/")))
	full = realpath(full)
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return nil, "", false
	}
	if st, err := os.Stat(full); err == nil && st.IsDir() {
		full = filepath.Join(full, "index.html")
	}
	st, err := os.Stat(full)
	if err != nil || !st.Mode().IsRegular() {
		return nil, "", false
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, "", false
	}
	return data, contentTypeFor(full), true
}

// realpath resolves symlinks for as much of the path as exists (like os.path.realpath, which
// does not require the leaf to exist).
func realpath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if abs, err := filepath.Abs(r); err == nil {
			return abs
		}
		return r
	}
	dir, leaf := filepath.Split(p)
	dir = filepath.Clean(dir)
	if dir == p { // root
		return p
	}
	return filepath.Join(realpath(dir), leaf)
}

// contentTypeFor mirrors mimetypes.guess_type + the Python charset rule:
// text/*, application/javascript and application/json get "; charset=utf-8".
func contentTypeFor(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	ctype := staticTypes[ext]
	if ctype == "" {
		if t := mime.TypeByExtension(ext); t != "" {
			if mt, _, err := mime.ParseMediaType(t); err == nil {
				ctype = mt
			} else {
				ctype = t
			}
		}
	}
	if ctype == "" {
		return "application/octet-stream"
	}
	if strings.HasPrefix(ctype, "text/") || ctype == "application/javascript" || ctype == "application/json" {
		ctype += "; charset=utf-8"
	}
	return ctype
}

// staticTypes pins the types Python's mimetypes returns for the files the TV app ships, so the
// output does not depend on the host's /etc/mime.types (Go would otherwise say text/javascript).
var staticTypes = map[string]string{
	".html":  "text/html",
	".htm":   "text/html",
	".css":   "text/css",
	".js":    "text/javascript", // what Python's mimetypes returns on the reference deployment
	".mjs":   "text/javascript",
	".json":  "application/json",
	".txt":   "text/plain",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".ico":   "image/vnd.microsoft.icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".xml":   "application/xml",
	".map":   "application/json",
}

// serveApp handles GET/HEAD /tv and /tv/...: the TV app directory with no-cache headers.
func (a *App) serveApp(w http.ResponseWriter, r *http.Request, rel string) {
	if rel == "" {
		rel = "/"
	}
	data, ctype, ok := staticFile(a.cfg.AppDir, rel)
	if !ok {
		sendError(w, r, 404, "not found")
		return
	}
	cors(w)
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(200)
	if r.Method != http.MethodHead {
		w.Write(data)
	}
}

// serveSim handles GET /sim: the browser simulator HTML file.
func (a *App) serveSim(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(a.cfg.SimFile)
	if err != nil {
		sendError(w, r, 404, "not found")
		return
	}
	send(w, r, 200, data, "text/html; charset=utf-8", 0)
}
