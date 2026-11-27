package memory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCaptureIdentityHTTP(t *testing.T) {
	f := newFixture(t)
	id := strings.Repeat("a", 64)
	f.svc.CaptureRuntimeID = id
	f.auth.actors[f.work] = actor("pc-capture", "service", true, allVerbs...)
	h := Handler(f.svc)
	for _, tc := range []struct {
		method, path, header string
		status               int
	}{{"GET", "/readyz", "", 200}, {"GET", "/readyz", id, 200}, {"GET", "/readyz", "bad", 409}, {"POST", "/v1/sources", "", 409}} {
		body, err := json.Marshal(map[string]any{"scope": scopeA})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.work)
		if tc.header != "" {
			req.Header.Set(captureRuntimeHeader, tc.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status || (w.Code == http.StatusOK && w.Header().Get(captureRuntimeHeader) != id) {
			t.Fatalf("%s %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{{"GET", "/v1/capture", false}, {"POST", "/v1/capture", true}, {"POST", "/v1/memories", false}} {
		if captureRuntimeWrite(httptest.NewRequest(tc.method, tc.path, nil)) != tc.want {
			t.Fatal(tc)
		}
	}
	t.Setenv("MEMORY_FILE_KEEP_ORIGINALS", "request")
	v, err := FileSettingsFromEnv()
	if err != nil || v.KeepOriginals || !v.RequestOriginals {
		t.Fatal("request-scoped retention")
	}
}
