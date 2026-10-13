package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPostgresWorkRelatedHooks(t *testing.T) {
	p := isolatedPostgres(t)
	f, now := archiveSearchFixture(t, p)
	memory := f.activeMemory("관련 작업의 확인된 결정")
	source, err := p.Get(testCtx, "sources", scopeA, memory.Sources[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
	if _, err := workCheckpointSave(f, f.agent, "related-work", "initial", 0, pkg); err != nil {
		t.Fatal(err)
	}
	archiveID := ContentHash("related-work-no-use")
	header := archiveSearchUpload(t, f, *now, archiveID, jsonLine(map[string]string{"role": "user", "content": "original"}), "jsonl")
	// The handler can refuse search or let another program save during it, as the
	// Python test did by patching the client's post.
	var mode atomic.Value
	mode.Store("")
	handler := Handler(f.svc)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/search" {
			switch mode.Load() {
			case "denied":
				http.Error(w, "do not expose this detail", http.StatusForbidden)
				return
			case "changing":
				handler.ServeHTTP(w, r)
				changed := pkg
				changed.Goal = "다른 프로그램에서 갱신한 목표"
				if _, err := workCheckpointSave(f, f.agent, "related-work", "changed", 1, changed); err != nil {
					t.Error(err)
				}
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	home := t.TempDir()
	blockSpawnedDrain(t, home)
	token := filepath.Join(home, "token")
	if err := os.WriteFile(token, []byte(f.agent), 0600); err != nil {
		t.Fatal(err)
	}
	read := func() WorkCheckpointView {
		t.Helper()
		view, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: "related-work"})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	before := read()
	type related struct {
		Package         json.RawMessage `json:"package"`
		RelatedMemories struct {
			State     string                `json:"state"`
			Items     []struct{ ID string } `json:"items"`
			Authority string                `json:"authority"`
		} `json:"related_memories"`
	}
	invoke := func(origin, event string) (related, string) {
		t.Helper()
		env := arkismEnv(home, server.URL, token, "ARKISM_ORIGIN="+origin, "ARKISM_CLIENT=server")
		text := arkismHook(t, env, map[string]any{"hook_event_name": event, "session_id": "s", "cwd": home, "arkism_work_id": "related-work"})
		if len(text) > 9500 {
			t.Fatal("hook context over budget", len(text))
		}
		var value related
		hookWork(t, text, &value)
		return value, text
	}
	for _, origin := range []string{"claude_code", "codex", "zcode"} {
		for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
			value, _ := invoke(origin, event)
			evidence := value.RelatedMemories
			if !sameJSON(value.Package, before.Package) || evidence.State != "ready" || len(evidence.Items) != 1 || evidence.Items[0].ID != memory.ID || evidence.Authority != "data_only_untrusted" {
				t.Fatalf("%s %s related work: %+v", origin, event, value)
			}
		}
	}
	if !sameJSON(read(), before) {
		t.Fatal("hook changed the work")
	}
	mode.Store("denied")
	value, text := invoke("codex", "UserPromptSubmit")
	if !sameJSON(value.Package, before.Package) || value.RelatedMemories.State != "partial" || len(value.RelatedMemories.Items) != 0 || strings.Contains(text, "do not expose") {
		t.Fatalf("denied search: %+v", value)
	}
	mode.Store("changing")
	value, _ = invoke("zcode", "SessionStart")
	if !sameJSON(value.Package, before.Package) || value.RelatedMemories.State != "work_changed" || len(value.RelatedMemories.Items) != 0 {
		t.Fatalf("changed work: %+v", value)
	}
	mode.Store("")
	if read().Revision != 2 {
		t.Fatal("concurrent save lost")
	}
	got, err := p.Get(testCtx, colSessionArchives, scopeA, archiveID)
	if err != nil || got.Revision != header.Revision || !got.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("hook renewed original", err)
	}
	var uses int
	if err = p.q().QueryRow(testCtx, `SELECT count(*) FROM memory_records WHERE collection=$1 AND kind=$2`, colJobs, kindSessionArchiveUsage).Scan(&uses); err != nil || uses != 0 {
		t.Fatal("hook recorded usage", err, uses)
	}
}
