package memory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Run the real stdio adapter, HTTP handler and PostgreSQL transaction. Runtime
// profiles share this adapter; this does not certify an installed desktop app.
func TestPostgresSessionArchiveUsageMCPProfiles(t *testing.T) {
	p := isolatedPostgres(t)
	f, now := archiveSearchFixture(t, p)
	f.auth.actors[f.agent] = actor("usage-agent", "agent", true, "read", "use")
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	f.svc.SessionArchiveNow = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	id := ContentHash("mcp-usage-original")
	header := archiveSearchUpload(t, f, *now, id, jsonLine(map[string]string{"role": "user", "content": "명시 사용 근거"}), "jsonl")
	source := f.source()
	m := Record{Sources: refs(source), ID: ID(), Scope: scopeA, Status: "active", Kind: "decision", Revision: 1, Text: "사용할 기억", Tags: []string{"session:search-session"}, CreatedAt: *now, UpdatedAt: *now}
	if err := p.Insert(testCtx, colMemories, m); err != nil {
		t.Fatal(err)
	}
	var failResponse atomic.Bool
	handler := Handler(f.svc)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/session-archives/used" && failResponse.Swap(false) {
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, r)
			if result.Code >= 300 {
				t.Errorf("use did not commit before injected failure: %d", result.Code)
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "agent.token")
	if err := os.WriteFile(tokenFile, []byte(f.agent), 0600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	call := func(runtime, name string, args map[string]any) (map[string]json.RawMessage, error) {
		t.Helper()
		message, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		cmd := exec.Command(arkismBinary(t), "skill-router")
		cmd.Env = arkismEnv(home, server.URL, tokenFile, "ARKISM_RUNTIME="+runtime, "ARKISM_CLIENT=")
		cmd.Stdin = bytes.NewReader(append(message, '\n'))
		output, e := cmd.Output()
		if e != nil {
			return nil, e
		}
		var response map[string]json.RawMessage
		e = json.Unmarshal(output, &response)
		return response, e
	}
	currentExpiry := *header.ExpiresAt
	for _, runtime := range []string{"codex", "claude_code", "zcode", "gemini", "grok"} {
		t.Run(runtime, func(t *testing.T) {
			clock.Add(int64(time.Hour))
			response, err := call(runtime, "search_session_archives", map[string]any{"archive_id": id, "query": "근거"})
			if err != nil || response["error"] != nil {
				t.Fatal("search failed", err, string(response["error"]))
			}
			before, err := p.Get(testCtx, colSessionArchives, scopeA, id)
			if err != nil || !before.ExpiresAt.Equal(currentExpiry) {
				t.Fatal("search renewed", err)
			}
			args := map[string]any{"memory_id": m.ID, "event_id": "actual-" + runtime}
			failResponse.Store(true)
			response, err = call(runtime, "record_memory_usage", args)
			if err != nil || response["error"] == nil {
				t.Fatal("lost response not surfaced", err)
			}
			stored, err := p.Get(testCtx, colSessionArchives, scopeA, id)
			if err != nil || !stored.ExpiresAt.Equal(f.svc.sessionArchiveNow().Add(SessionArchiveTTL)) {
				t.Fatal("use not recorded", err)
			}
			currentExpiry = *stored.ExpiresAt
			clock.Add(int64(time.Hour))
			response, err = call(runtime, "record_memory_usage", args)
			if err != nil || response["error"] != nil {
				t.Fatal("retry failed", err, string(response["error"]))
			}
			var result struct {
				Structured struct {
					Receipts []SessionArchiveUsedOutput `json:"receipts"`
				} `json:"structuredContent"`
			}
			if err = json.Unmarshal(response["result"], &result); err != nil || len(result.Structured.Receipts) != 1 || result.Structured.Receipts[0].Extended {
				t.Fatal("retry receipt changed", err)
			}
			got, err := p.Get(testCtx, colSessionArchives, scopeA, id)
			if err != nil || got.Revision != stored.Revision || !got.ExpiresAt.Equal(currentExpiry) {
				t.Fatal("retry extended retention", err)
			}
		})
	}
	// A reconnect through another runtime must replay the same shared receipt.
	response, err := call("claude_code", "record_memory_usage", map[string]any{"memory_id": m.ID, "event_id": "actual-codex"})
	if err != nil || response["error"] != nil {
		t.Fatal("cross-runtime replay failed", err)
	}
	var result struct {
		Structured struct {
			Receipts []SessionArchiveUsedOutput `json:"receipts"`
		} `json:"structuredContent"`
	}
	if err = json.Unmarshal(response["result"], &result); err != nil || len(result.Structured.Receipts) != 1 || result.Structured.Receipts[0].Extended {
		t.Fatal("cross-runtime replay renewed", err)
	}
	got, err := p.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || !got.ExpiresAt.Equal(currentExpiry) {
		t.Fatal("cross-runtime retention changed", err)
	}

}
