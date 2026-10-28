package host

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A hook's Client and Session go out as headers on every call; unset labels send no header.
func TestCallSendsClientAndSessionLabels(t *testing.T) {
	var got []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldURL, oldFile := BaseURL, TokenFile
	defer func() { BaseURL, TokenFile, Client, Session = oldURL, oldFile, "", "" }()
	BaseURL, TokenFile = srv.URL, file
	Client, Session = "codex", "s-1"
	if _, err := Post("/v1/search", map[string]any{}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	Client, Session = "", ""
	if _, err := Post("/v1/search", map[string]any{}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Get("X-Arkism-Client") != "codex" || got[0].Get("X-Arkism-Session") != "s-1" {
		t.Fatal("labels not sent", got)
	}
	if _, has := got[1]["X-Arkism-Client"]; has {
		t.Fatal("empty label sent")
	}
}
