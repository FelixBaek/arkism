package memory

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const webUIPrefix = "/app/"

// webUIBuildMarker is written by desktop/frontend/build.cjs; the source folder (with its tests and fixtures) has none.
const webUIBuildMarker = ".arkism-frontend-output"

// The UI calls only this origin with a Bearer token from sessionStorage; no cookies, so no CSRF surface.
// Image and font hosts match the desktop shell's CSP (desktop/src-tauri/tauri.conf.json): team cover photos and the web font.
const webUICSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https://images.unsplash.com https://avatars.githubusercontent.com; font-src 'self' https://cdn.jsdelivr.net; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

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

// SetWebUIDir turns the web screens on ("" keeps them off). It refuses a folder that is not a UI build,
// so pointing it at desktop/ui by mistake never serves test files or test data.
func (s *Service) SetWebUIDir(dir string) error {
	if dir != "" {
		if info, err := os.Stat(filepath.Join(dir, webUIBuildMarker)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("ARKISM_WEB_UI_DIR %q is not a built UI (no %s); build it with node desktop/frontend/build.cjs and point at its output", dir, webUIBuildMarker)
		}
	}
	s.WebUIDir = dir
	return nil
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
