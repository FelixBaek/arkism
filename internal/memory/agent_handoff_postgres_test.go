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

// Real PostgreSQL + HTTP + MCP/CLI processes. The external model CLI is a stub:
// this verifies transport, provenance, access, and reservation, not model quality.
func TestPostgresAgentHandoffMCPAndCLI(t *testing.T) {
	f, now := archiveSearchFixture(t, isolatedPostgres(t))
	f.auth.actors[f.agent].verbs["transfer"] = true
	src := f.source()
	pkg := workCheckpointPackage()
	pkg.Files = nil
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	archiveID := ContentHash("handoff-origin")
	archiveRaw := jsonLine(map[string]string{"role": "user", "content": "Source evidence for handoff"})
	archiveSearchUpload(t, f, *now, archiveID, archiveRaw, "jsonl")
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "handoff-parent", EventID: "initial", Package: pkg, ArchiveMessages: []ArchiveMessageRef{{ArchiveID: archiveID, SHA256: ContentHash(string(archiveRaw))}}}); err != nil {
		t.Fatal(err)
	}
	skill, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "handoff-review", Path: "/skills/handoff-review/SKILL.md", Content: "# Handoff review\nCheck the original source ID before answering.", Analysis: detailedSkillAnalysis()})
	if err != nil {
		t.Fatal(err)
	}
	var wrongCredential atomic.Bool
	handler := Handler(f.svc)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.agent {
			wrongCredential.Store(true)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	token := filepath.Join(root, "agent.token")
	stub := filepath.Join(root, "claude")
	marker := filepath.Join(root, "executions")
	args := filepath.Join(root, "argv")
	for path, content := range map[string]string{token: f.agent, stub: "#!/bin/sh\nprintf 'ran\\n' >> '" + marker + "'\nprintf '%s\\n' \"$@\" > '" + args + "'\n/bin/cat >/dev/null\nprintf 'Stub observation.\\n\\tReview source evidence before integrating.\\n'\n"} {
		if err := os.WriteFile(path, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := arkismEnv(root, server.URL, token, "ARKISM_CLAUDE_BIN="+stub, "HANDOFF_MARKER="+marker, "HANDOFF_ARGS="+args, "ARKISM_RUNNER_TOKEN_FILE="+filepath.Join(root, "must-not-use"))
	input := map[string]any{"id": "handoff-child", "parent_work_id": "handoff-parent", "sender": "codex", "recipient": "claude", "session_id": "session-cross-agent", "task": "Review the current repository using the selected evidence.", "model": "opus", "cwd": root, "skills": []string{"handoff-review"}, "timeout_s": 10}
	replies := arkismMCP(t, env, mcpCall{"handoff_work", input}, mcpCall{"resume_work", map[string]any{"id": "handoff-child"}})
	var result map[string]any
	mcpResult(t, replies[0], &result)
	if result["ok"] != true || result["authority"] != AuthorityDataOnly || result["verification_status"] != "unverified" {
		t.Fatal(result)
	}
	var view WorkCheckpointView
	mcpResult(t, replies[1], &view)
	if len(view.ArchiveMessages) != 1 || view.ArchiveMessages[0].ArchiveID != archiveID || view.ArchiveMessages[0].MessageSHA256 == "" {
		t.Fatal("archive provenance was dropped", view.ArchiveMessages)
	}
	if view.Revision != 2 || view.Package.Status != "completed" || len(view.Package.SourceRefs) != 3 || view.Package.SourceRefs[0].ID != src.ID || len(view.Package.Completed) != 2 {
		t.Fatalf("saved result: %+v", view)
	}
	if len(view.Package.Constraints) != 2 || !strings.Contains(view.Package.Constraints[1], skill.Record.ID) || !strings.Contains(view.Package.Constraints[1], "session-cross-agent") {
		t.Fatal("identity/skill version missing", view.Package.Constraints)
	}
	argv, err := os.ReadFile(args)
	if err != nil || !strings.Contains(string(argv), "Check the original source ID") || !strings.Contains(string(argv), "dontAsk") {
		t.Fatal("approved skill/read permission absent", err, string(argv))
	}
	// Retry from another program/process cannot start the model a second time.
	raw, _ := json.Marshal(input)
	if _, err := arkismExec(t, env, string(raw), "crew", "handoff"); err == nil {
		t.Fatal("duplicate reservation executed")
	}
	// Real skill-source deletion must fail the live resolver before any dispatch.
	skillSource, err := f.svc.Repo.Get(testCtx, "sources", scopeA, skill.Record.Sources[0].SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(testCtx, f.human, "sources", skillSource.ID, ChangeInput{Scope: scopeA, ExpectedRevision: skillSource.Revision}); err != nil {
		t.Fatal(err)
	}
	if mcpError(arkismMCP(t, env, mcpCall{"resume_work", map[string]any{"id": "handoff-child"}})[0]) == "" {
		t.Fatal("child result survived deletion of used skill evidence")
	}
	input["id"] = "handoff-skill-deleted"
	if mcpError(arkismMCP(t, env, mcpCall{"handoff_work", input})[0]) == "" {
		t.Fatal("deleted skill dispatched")
	}
	// Original evidence is checked independently of skills.
	delete(input, "skills")
	input["id"] = "handoff-source-deleted"
	if err := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); err != nil {
		t.Fatal(err)
	}
	if mcpError(arkismMCP(t, env, mcpCall{"handoff_work", input})[0]) == "" {
		t.Fatal("deleted source dispatched")
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "ran\n" {
		t.Fatal("execution count", err, string(calls))
	}
	if wrongCredential.Load() {
		t.Fatal("handoff used a different credential")
	}
}
