package memory

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

const webUIPrefix = "/app/"

// The UI calls only this origin with a Bearer token from sessionStorage; no cookies, so no CSRF surface.
const webUICSP = "default-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

// Only product asset types are served; anything else in the directory stays private.
var webUITypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
}

// registerWebuiCoreHTTP serves the built desktop UI (ARKISM_WEB_UI_DIR) to plain browsers.
// Public on purpose: static files carry no data; every /v1 call still needs its Bearer token.
func registerWebuiCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET "+webUIPrefix, s.serveWebUI)
}

func (s *Service) serveWebUI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, webUIPrefix)
	if name == "" {
		name = "index.html"
	}
	contentType, ok := webUITypes[path.Ext(name)]
	if s.WebUIDir == "" || !ok || !webUIName(name) {
		http.NotFound(w, r)
		return
	}
	// os.Root refuses every escape, including ".." and symlinks that point outside the directory.
	root, err := os.OpenRoot(s.WebUIDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set(headerContentType, contentType)
	h.Set("Content-Security-Policy", webUICSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// webUIName accepts a clean relative path with no hidden segment (no "..", no dotfiles, no backslashes).
func webUIName(name string) bool {
	if !fs.ValidPath(name) || strings.Contains(name, `\`) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
