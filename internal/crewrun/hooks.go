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
	"time"
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

// Handler serves webhooks and n8n callbacks. Runs started here continue on
// ctx (the daemon's lifetime), not the request's.
//
//	POST /hooks/{workflow_id}          header X-Arkism-Hook-Token; body JSON is the run input → 202 {run_id}
//	POST /callbacks/{run_id}/{node_id} header X-Arkism-Callback-Token (one-time); body JSON is the node output
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
	mux.HandleFunc("POST /callbacks/{run}/{node}", func(w http.ResponseWriter, r *http.Request) {
		out, ok := readBody(w, r)
		if !ok {
			return
		}
		status, code := e.Callback(r.Context(), r.PathValue("run"), r.PathValue("node"), r.Header.Get("X-Arkism-Callback-Token"), out)
		if status == 200 {
			e.Go(ctx, r.PathValue("run"))
		}
		writeJSON(w, status, map[string]string{"code": code})
	})
	return mux
}

// Callback completes an async n8n node once: the token must match the hash
// the run holds, and the hash is cleared with the result.
func (e *Engine) Callback(ctx context.Context, runID, nodeID, token string, out any) (int, string) {
	// A fast n8n may answer before the engine has saved the waiting state;
	// wait up to 10 s for the drive that posted the request to let go.
	for i := 0; !e.claim(runID); i++ {
		if i == 50 || ctx.Err() != nil {
			return 409, "busy"
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer e.release(runID)
	run, err := e.Store.GetRun(ctx, runID)
	if err != nil {
		return 404, "not_found"
	}
	w := run.State.Waiting
	if run.State.Status != "waiting" || w == nil || w.Kind != "callback" || w.NodeID != nodeID {
		return 404, "not_waiting"
	}
	sum := sha256.Sum256([]byte(token))
	want, err := hex.DecodeString(w.CallbackHash)
	if err != nil || subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return 403, "access_denied"
	}
	if w.ResumeAt != nil && e.now().After(*w.ResumeAt) {
		return 410, "expired"
	}
	x := &runExec{e: e, run: run}
	st := run.State.Nodes[nodeID]
	if st == nil {
		return 404, "not_found"
	}
	t := e.now().UTC().Truncate(time.Millisecond)
	st.Status, st.Output, st.FinishedAt = "done", encodeStored(out), &t
	x.run.State.Status, x.run.State.Waiting = "running", nil
	x.mu.Lock()
	err = x.saveLocked(ctx, nodeID)
	x.mu.Unlock()
	if err != nil {
		return 503, "dependency_unavailable"
	}
	return 200, "ok"
}
