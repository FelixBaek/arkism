package memory

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestPostgresWorkCheckpointHooksReadCurrentState(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	pkg := workCheckpointPackage()
	if _, err := workCheckpointSave(f, f.agent, "selected-work", "initial", 0, pkg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	home := t.TempDir()
	blockSpawnedDrain(t, home)
	token := filepath.Join(home, "token")
	if err := os.WriteFile(token, []byte(f.agent), 0600); err != nil {
		t.Fatal(err)
	}
	env := func(origin string) []string {
		// The large-work case needs the session index to fill its budget, far past the 10-line default.
		return arkismEnv(home, server.URL, token, "ARKISM_ORIGIN="+origin, "ARKISM_CLIENT=server", "ARKISM_WORK_SELECTION_STATE="+filepath.Join(home, "selections"),
			"ARKISM_SESSION_INDEX_LINES=1000", "ARKISM_SESSION_INDEX_CHARS=100000")
	}
	invoke := func(origin, event string, stored bool) string {
		t.Helper()
		payload := map[string]any{"hook_event_name": event, "session_id": "s", "cwd": home}
		if !stored {
			payload["arkism_work_id"] = "selected-work"
		}
		return arkismHook(t, env(origin), payload)
	}
	type hookView struct {
		Package       map[string]any `json:"package"`
		Revision      int64          `json:"revision"`
		Authority     string         `json:"authority"`
		OmittedFields []string       `json:"omitted_fields"`
		Verification  struct {
			ChecksExecuted bool `json:"checks_executed"`
			FilesChecked   bool `json:"files_checked"`
		} `json:"verification"`
	}
	work := func(text string) hookView {
		t.Helper()
		var view hookView
		hookWork(t, text, &view)
		return view
	}
	selection := func(action string, args ...string) {
		t.Helper()
		arkismRun(t, env("codex"), "", append([]string{"work-selection", action, "--cwd", home}, args...)...)
	}
	for _, origin := range []string{"claude_code", "codex", "zcode"} {
		data := work(invoke(origin, "SessionStart", false))
		if data.Package["goal"] != pkg.Goal || data.Revision != 1 || data.Authority != "data_only_untrusted" || data.Verification.ChecksExecuted || data.Verification.FilesChecked {
			t.Fatalf("%s work context: %+v", origin, data)
		}
	}
	selection("select", "--id", "selected-work")
	for _, origin := range []string{"claude_code", "codex", "zcode"} {
		if data := work(invoke(origin, "SessionStart", true)); data.Revision != 1 {
			t.Fatalf("%s stored selection: %+v", origin, data)
		}
	}
	marker := filepath.Join(home, "must-not-exist")
	pkg.Goal, pkg.Origin = "latest work goal", "codex"
	pkg.Checks = []WorkCheckpointCheck{{Command: "touch " + marker, Outcome: "passed"}}
	if _, err := workCheckpointSave(f, f.agent, "selected-work", "updated", 1, pkg); err != nil {
		t.Fatal(err)
	}
	if data := work(invoke("zcode", "UserPromptSubmit", true)); data.Revision != 2 || data.Package["goal"] != "latest work goal" {
		t.Fatalf("hook read stale work: %+v", data)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook ran a recorded check", err)
	}
	pkg.Decisions = []string{strings.TrimSpace(strings.Repeat("근거 있는 결정 ", 280))}
	pkg.NextSteps = []string{strings.TrimSpace(strings.Repeat("아직 남은 단계 ", 280))}
	if _, err := workCheckpointSave(f, f.agent, "selected-work", "large", 2, pkg); err != nil {
		t.Fatal(err)
	}
	canonical, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeA, ID: "selected-work"})
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	raw, _ := json.Marshal(canonical.Package)
	json.Unmarshal(raw, &full)
	// Standing rules fill the session index (the Python test patched in 3,900 characters)
	// so the work section has to be cut to fit the hook budget.
	for i := range 40 {
		cand := f.create(f.human, "memory_candidates", CreateInput{Scope: scopeA, Kind: "user", Text: strings.Repeat("기억", 60) + " " + string(rune('가'+i)), Sources: refs(f.source())})
		approved := f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "approved"})
		f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
	}
	bounded := invoke("claude_code", "SessionStart", true)
	if host.Len(bounded) > 9500 {
		t.Fatal("hook context over budget", host.Len(bounded))
	}
	compact := work(bounded)
	if compact.Revision != 3 || len(compact.OmittedFields) == 0 {
		t.Fatalf("large work not compacted: %d %v", compact.Revision, compact.OmittedFields)
	}
	for key, value := range compact.Package {
		if !sameJSON(value, full[key]) {
			t.Fatalf("compact field %s differs from the saved work", key)
		}
	}
	selection("clear")
	if text := invoke("claude_code", "SessionStart", true); text == "" || strings.Contains(text, host.HookHeader) {
		t.Fatal("cleared selection still injects work", text)
	}
	selection("select", "--id", "selected-work")
	if _, err = f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "erase-selected", TargetType: "work_checkpoint", TargetIDs: []string{"selected-work"}, Reason: "owner_request"}); err != nil {
		t.Fatal(err)
	}
	for _, stored := range []bool{false, true} {
		event := map[bool]string{false: "UserPromptSubmit", true: "SessionStart"}[stored]
		if text := invoke("codex", event, stored); strings.Contains(text, host.HookHeader) {
			t.Fatal("erased work still injected", event, text)
		}
	}
	var selected struct {
		ID string `json:"id"`
	}
	if out := arkismRun(t, env("codex"), "", "work-selection", "get", "--cwd", home); json.Unmarshal(out, &selected) != nil || selected.ID != "selected-work" {
		t.Fatalf("selection lost: %s", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook ran a recorded check", err)
	}
}
