package crewrun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

var hooksMu sync.Mutex

// HookToken returns the webhook secret of a workflow from the hooks file
// (~/.config/arkism/crew-hooks.json, 0600: {"hooks": {"WORKFLOW_ID": "TOKEN"}}).
// With create, a missing secret is generated and saved.
func HookToken(path, workflowID string, create bool) (string, error) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	var f struct {
		Hooks map[string]string `json:"hooks"`
	}
	raw, err := readPrivateFile(path)
	switch {
	case err == nil:
		if json.Unmarshal(raw, &f) != nil {
			return "", errors.New("crew-hooks.json unreadable")
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return "", err
	}
	if tok := f.Hooks[workflowID]; tok != "" || !create {
		return tok, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	if f.Hooks == nil {
		f.Hooks = map[string]string{}
	}
	tok := hex.EncodeToString(b)
	f.Hooks[workflowID] = tok
	out, _ := json.MarshalIndent(f, "", " ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return "", err
	}
	return tok, os.Rename(tmp, path)
}

// sameSecret compares in constant time over fixed-length digests.
func sameSecret(got, want string) bool {
	if want == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(w http.ResponseWriter, r *http.Request) (any, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, 413, map[string]string{"code": "payload_too_large"})
		return nil, false
	}
	if len(b) == 0 {
		return map[string]any{}, true
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		writeJSON(w, 400, map[string]string{"code": "invalid_json"})
		return nil, false
	}
	return v, true
}

// Handler serves webhooks. Runs started here continue on
// ctx (the daemon's lifetime), not the request's.
//
//	POST /hooks/{workflow_id}          header X-Arkism-Hook-Token; body JSON is the run input → 202 {run_id}
func (e *Engine) Handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		it, ok := e.item(id)
		if !ok || it.Crew.Type != "workflow" || !it.Crew.Enabled || it.Crew.Trigger == nil || it.Crew.Trigger.Kind != "webhook" {
			writeJSON(w, 404, map[string]string{"code": "not_found"})
			return
		}
		want, err := HookToken(e.HooksFile, id, false)
		if err != nil || !sameSecret(r.Header.Get("X-Arkism-Hook-Token"), want) {
			writeJSON(w, 403, map[string]string{"code": "access_denied"})
			return
		}
		input, ok := readBody(w, r)
		if !ok {
			return
		}
		run, err := e.StartRun(r.Context(), id, "webhook", input)
		if err != nil {
			e.log().Warn("crew webhook", "workflow_id", id, "error", err.Error())
			writeJSON(w, 503, map[string]string{"code": "dependency_unavailable"})
			return
		}
		e.Go(ctx, run.ID)
		writeJSON(w, 202, map[string]string{"run_id": run.ID})
	})
	return mux
}
