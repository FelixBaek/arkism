package memory

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func webUIFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	base := t.TempDir()
	dir := filepath.Join(base, "ui")
	files := map[string]string{
		"index.html":           "<!doctype html><title>Arkism</title>",
		"assets/entry.js":      "export {};",
		"app.css":              "body{}",
		"fonts/a.woff2":        "w",
		".arkism-frontend-out": "marker",
		"notes.txt":            "private",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "secret.js"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "secret.js"), filepath.Join(dir, "link.js")); err != nil {
		t.Fatal(err)
	}
	f.svc.WebUIDir = dir
	return f, dir
}

func webUIGet(h http.Handler, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestWebUIDisabledByDefault(t *testing.T) {
	h := Handler(newFixture(t).svc)
	if w := webUIGet(h, "/"); w.Code != 200 || !strings.Contains(w.Body.String(), "Arkism API") {
		t.Fatalf("root changed while disabled: %d %q", w.Code, w.Body.String())
	}
	if w := webUIGet(h, "/app/"); w.Code != http.StatusNotFound {
		t.Fatalf("disabled /app/ = %d", w.Code)
	}
}

func TestWebUIRedirectAndAssets(t *testing.T) {
	f, _ := webUIFixture(t)
	h := Handler(f.svc)
	if w := webUIGet(h, "/"); w.Code != http.StatusFound || w.Header().Get("Location") != "/app/" {
		t.Fatalf("root redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
	cases := map[string]string{
		"/app/":                "text/html; charset=utf-8",
		"/app/index.html":      "text/html; charset=utf-8",
		"/app/assets/entry.js": "text/javascript; charset=utf-8",
		"/app/app.css":         "text/css; charset=utf-8",
		"/app/fonts/a.woff2":   "font/woff2",
	}
	for target, want := range cases {
		w := webUIGet(h, target)
		if w.Code != 200 || w.Header().Get("Content-Type") != want {
			t.Errorf("%s: %d %q", target, w.Code, w.Header().Get("Content-Type"))
		}
		hd := w.Header()
		csp := hd.Get("Content-Security-Policy")
		for _, part := range []string{"default-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, part) {
				t.Errorf("%s: CSP %q lacks %s", target, csp, part)
			}
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers %v", target, hd)
		}
	}
}

func TestWebUINeverLeavesItsDirectory(t *testing.T) {
	f, _ := webUIFixture(t)
	h := Handler(f.svc)
	for _, target := range []string{
		"/app/../secret.js", "/app/%2e%2e/secret.js", "/app/assets/../../secret.js", "/app/..%2fsecret.js",
		"/app/link.js",              // symlink out of the directory
		"/app/.arkism-frontend-out", // hidden file
		"/app/notes.txt",            // not a product asset type
		"/app/assets/",              // no directory listings
		"/app/assets", "/app/missing.js", "/app/a%5c..%5csecret.js",
	} {
		w := webUIGet(h, target)
		if w.Code == 200 || strings.Contains(w.Body.String(), "outside") || strings.Contains(w.Body.String(), "private") {
			t.Errorf("%s: %d %q", target, w.Code, w.Body.String())
		}
	}
}

func TestWebUILeavesAPIAuthorizationUnchanged(t *testing.T) {
	f, _ := webUIFixture(t)
	if w := webUIGet(Handler(f.svc), "/v1/capabilities?workspace_id=ws-a&project_id=proj-a"); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /v1 with web UI on: %d", w.Code)
	}
}
