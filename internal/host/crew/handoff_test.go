package crew

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

type handoffStore struct {
	mu           sync.Mutex
	saved        map[string]map[string]any
	parentReads  int
	changeParent bool
	denyParent   bool
	failResult   bool
}

func newHandoffStore() *handoffStore {
	return &handoffStore{saved: map[string]map[string]any{"parent": {"id": "parent", "revision": 1, "package": map[string]any{"title": "Parent", "goal": "Inspect repository", "repository": "repo", "origin": "codex", "status": "in_progress", "source_refs": []any{map[string]any{"collection": "sources", "id": "source-1", "revision": 1}}}}}}
}
func (s *handoffStore) request(path string, p map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "/v1/work-checkpoints/authorize-handoff" {
		archives := s.saved["parent"]["archive_messages"]
		if archives == nil {
			archives = []any{}
		}
		return map[string]any{"authorized": true, "content_hash": digest([]byte(sOr(p["content"]))), "source_refs": s.saved["parent"]["package"].(map[string]any)["source_refs"], "archive_messages": archives}, nil
	}
	id := p["id"].(string)
	if strings.HasSuffix(path, "/get") {
		if id == "parent" {
			s.parentReads++
			if s.parentReads > 1 && s.denyParent {
				return nil, errors.New("denied")
			}
			if s.parentReads > 1 && s.changeParent {
				s.saved[id]["revision"] = 2
			}
		}
		if v, ok := s.saved[id]; ok {
			raw, _ := json.Marshal(v)
			var cp map[string]any
			json.Unmarshal(raw, &cp)
			return cp, nil
		}
		return nil, errors.New("not found")
	}
	expected, _ := host.Int(p["expected_revision"])
	current := 0
	if v := s.saved[id]; v != nil {
		n, _ := host.Int(v["revision"])
		current = int(n)
	}
	if int(expected) != current {
		return nil, errors.New("conflict")
	}
	if expected > 0 && s.failResult {
		return nil, errors.New("network failed")
	}
	raw, _ := json.Marshal(p["package"])
	var pkg map[string]any
	json.Unmarshal(raw, &pkg)
	s.saved[id] = map[string]any{"id": id, "revision": current + 1, "package": pkg}
	return map[string]any{"id": id, "revision": current + 1}, nil
}
func handoffInput(t *testing.T) string {
	t.Helper()
	// a parent agent's provider override (e.g. ANTHROPIC_BASE_URL under Claude Code) would make every handoff refuse
	for _, key := range []string{"OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "ANTHROPIC_BEDROCK_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(key, "")
	}
	b, _ := json.Marshal(handoffRequest{ID: "child", ParentWorkID: "parent", Sender: "codex", Recipient: "claude", SessionID: "session-1", Task: "Inspect the current repository", Model: "opus", CWD: t.TempDir(), TimeoutSeconds: 10})
	return string(b)
}
func TestHandoffRealProcessPersistsResultAndBlocksReplay(t *testing.T) {
	store := newHandoffStore()
	input := handoffInput(t)
	cli := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n/bin/cat >/dev/null\nprintf 'Inspected source-1; review required.\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_CLAUDE_BIN", cli)
	out, err := runHandoff(strings.NewReader(input), store.request, runNode)
	if err != nil || out["ok"] != true {
		t.Fatalf("%v %v", out, err)
	}
	pkg := store.saved["child"]["package"].(map[string]any)
	if pkg["status"] != "completed" || !strings.Contains(dumpLine(pkg["completed"]), "source-1") || !strings.Contains(dumpLine(pkg["source_refs"]), "source-1") {
		t.Fatal(pkg)
	}
	calls := 0
	_, err = runHandoff(strings.NewReader(input), store.request, func(map[string]any) (map[string]any, error) { calls++; return nil, nil })
	if err == nil || calls != 0 {
		t.Fatalf("replayed execution: %v %d", err, calls)
	}
}
func TestHandoffFencesParentAndPersistsBlockedState(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "denied"}[deny], func(t *testing.T) {
			store := newHandoffStore()
			store.changeParent = !deny
			store.denyParent = deny
			called := false
			_, err := runHandoff(strings.NewReader(handoffInput(t)), store.request, func(map[string]any) (map[string]any, error) { called = true; return nil, nil })
			if err == nil || called || store.saved["child"]["package"].(map[string]any)["status"] != "blocked" {
				t.Fatalf("%v %v", err, called)
			}
		})
	}
}
func TestHandoffConcurrentReservationRunsOnlyOnce(t *testing.T) {
	store := newHandoffStore()
	input := handoffInput(t)
	var count atomic.Int32
	var wg sync.WaitGroup
	run := func(spec map[string]any) (map[string]any, error) {
		count.Add(1)
		if spec["access"] != "read" || spec["once"] != false {
			t.Error(spec)
		}
		return map[string]any{"ok": true, "output": "unverified result"}, nil
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); runHandoff(strings.NewReader(input), store.request, run) }()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("%d executions", count.Load())
	}
}
func TestHandoffAmbiguousCompletionNeverReruns(t *testing.T) {
	store := newHandoffStore()
	store.failResult = true
	input := handoffInput(t)
	calls := 0
	run := func(map[string]any) (map[string]any, error) {
		calls++
		return map[string]any{"ok": true, "output": "result"}, nil
	}
	for i := 0; i < 2; i++ {
		if _, err := runHandoff(strings.NewReader(input), store.request, run); err == nil {
			t.Fatal("expected failure")
		}
	}
	if calls != 1 || store.saved["child"]["package"].(map[string]any)["status"] != "in_progress" {
		t.Fatal(calls, store.saved)
	}
}
func TestHandoffFailuresAreRecoverableAndBounded(t *testing.T) {
	for _, result := range []map[string]any{{"ok": false, "error": "timeout"}, {"ok": true, "output": strings.Repeat("x", 8193)}} {
		store := newHandoffStore()
		out, err := runHandoff(strings.NewReader(handoffInput(t)), store.request, func(map[string]any) (map[string]any, error) { return result, nil })
		if err != nil || out["ok"] != false || out["status"] != "blocked" {
			t.Fatal(out, err)
		}
	}
}
func TestHandoffRejectsImplicitAuthorityAndUnboundedRequests(t *testing.T) {
	for _, extra := range []string{`,"access":"full"`, `,"timeout_s":601`, `,"model":"auto"`} {
		input := strings.TrimSuffix(handoffInput(t), "}") + extra + "}"
		called := false
		_, err := runHandoff(strings.NewReader(input), func(string, map[string]any) (map[string]any, error) { called = true; return nil, nil }, nil)
		if err == nil || called {
			t.Fatal(err, called)
		}
	}
}

func TestHandoffResolvesSkillsWithCallerAndRechecksBeforeDispatch(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "approved", true: "revoked"}[revoke], func(t *testing.T) {
			store := newHandoffStore()
			var input map[string]any
			json.Unmarshal([]byte(handoffInput(t)), &input)
			input["skills"] = []string{"review"}
			raw, _ := json.Marshal(input)
			reads := 0
			request := func(path string, p map[string]any) (map[string]any, error) {
				if path != "/v1/skills/resolve" {
					return store.request(path, p)
				}
				reads++
				if revoke && reads == 2 {
					return nil, errors.New("access revoked")
				}
				eq(t, p["refs"], []string{"review@latest-approved"})
				return map[string]any{"items": []any{map[string]any{"ref": "review@latest-approved", "skill_id": "review", "version_id": "version-one", "instructions": "Check evidence first.", "content_hash": digest([]byte("Check evidence first.")), "latest_approved": true}}}, nil
			}
			called := false
			out, err := runHandoff(strings.NewReader(string(raw)), request, func(spec map[string]any) (map[string]any, error) {
				called = true
				if _, ok := spec["skills"]; ok {
					t.Fatal("runner would resolve with a different credential")
				}
				if !strings.Contains(sOr(spec["instructions"]), "Check evidence first.") {
					t.Fatal(spec)
				}
				return map[string]any{"ok": true, "output": "review result"}, nil
			})
			if revoke {
				if err == nil || called {
					t.Fatal(out, err, called)
				}
			} else if err != nil || !called {
				t.Fatal(out, err)
			}
		})
	}
}

func TestHandoffChildCannotDelegateAgain(t *testing.T) {
	t.Setenv("ARKISM_CAPTURE_CHILD", "1")
	if _, err := runHandoff(strings.NewReader(handoffInput(t)), nil, nil); err == nil {
		t.Fatal("nested handoff allowed")
	}
}

func TestHandoffWithholdsResultWhenParentChangesDuringExecution(t *testing.T) {
	store := newHandoffStore()
	out, err := runHandoff(strings.NewReader(handoffInput(t)), store.request, func(map[string]any) (map[string]any, error) {
		store.changeParent = true
		return map[string]any{"ok": true, "output": "stale result must be withheld"}, nil
	})
	if err != nil || out["ok"] != false || out["status"] != "blocked" {
		t.Fatal(out, err)
	}
	if _, exists := store.saved["child"]["package"].(map[string]any)["completed"]; exists {
		t.Fatal("stale result persisted")
	}
}

func TestHandoffWithholdsResultRejectedByCheckpointValidation(t *testing.T) {
	store := newHandoffStore()
	request := func(path string, p map[string]any) (map[string]any, error) {
		if pkg, ok := p["package"].(map[string]any); ok && pkg["completed"] != nil {
			return nil, errors.New("invalid")
		}
		return store.request(path, p)
	}
	out, err := runHandoff(strings.NewReader(handoffInput(t)), request, func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "output": "result rejected by secret scan"}, nil
	})
	if err != nil || out["ok"] != false || out["status"] != "blocked" {
		t.Fatal(out, err)
	}
	if pkg := store.saved["child"]["package"].(map[string]any); pkg["status"] != "blocked" || pkg["completed"] != nil {
		t.Fatal(pkg)
	}
}

func TestHandoffWriteUsesCodexWorkspaceSandbox(t *testing.T) {
	store := newHandoffStore()
	var input map[string]any
	json.Unmarshal([]byte(handoffInput(t)), &input)
	input["access"] = "write"
	input["model"] = "gpt-5"
	store.saved["parent"]["package"].(map[string]any)["repository"] = input["cwd"]
	raw, _ := json.Marshal(input)
	called := false
	out, err := runHandoff(strings.NewReader(string(raw)), store.request, func(spec map[string]any) (map[string]any, error) {
		called = true
		argv, _ := buildCommand(row(t, sOr(spec["model"])), sOr(spec["model"]), "medium", sOr(spec["instructions"]), sOr(spec["task"]), "", false, sOr(spec["access"]))
		if index(argv, "workspace-write") < 0 || index(argv, "--dangerously-bypass-approvals-and-sandbox") >= 0 {
			t.Fatal(argv)
		}
		return map[string]any{"ok": true, "output": "Changed workspace; verification pending"}, nil
	})
	if err != nil || !called || out["ok"] != true {
		t.Fatal(out, err, called)
	}
	input["id"] = "wrong-workspace"
	input["cwd"] = t.TempDir()
	raw, _ = json.Marshal(input)
	if _, err := runHandoff(strings.NewReader(string(raw)), store.request, nil); err == nil {
		t.Fatal("write outside checkpoint repository allowed")
	}
	input["id"] = "child-two"
	input["model"] = "opus"
	raw, _ = json.Marshal(input)
	if _, err := runHandoff(strings.NewReader(string(raw)), store.request, nil); err == nil {
		t.Fatal("non-sandboxed write allowed")
	}
}

func TestHandoffPinsArchiveProvenanceForReservationAndPublication(t *testing.T) {
	store := newHandoffStore()
	refs := []any{map[string]any{"archive_id": strings.Repeat("a", 64), "sha256": strings.Repeat("b", 64), "chunk_index": 0, "chunk_offset": 0, "message_sha256": strings.Repeat("c", 64)}}
	store.saved["parent"]["archive_messages"] = refs
	saves := 0
	request := func(path string, p map[string]any) (map[string]any, error) {
		if strings.HasSuffix(path, "/save") {
			saves++
			eq(t, p["archive_messages"], refs)
		}
		return store.request(path, p)
	}
	out, err := runHandoff(strings.NewReader(handoffInput(t)), request, func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "output": "result based on archived evidence"}, nil
	})
	if err != nil || out["ok"] != true || saves != 2 {
		t.Fatal(out, err, saves)
	}
}

func TestHandoffRequiresTransferAuthorizationForExactPrompt(t *testing.T) {
	for _, mode := range []string{"denied", "wrong-hash", "allowed"} {
		t.Run(mode, func(t *testing.T) {
			store := newHandoffStore()
			approved := ""
			called := false
			request := func(path string, p map[string]any) (map[string]any, error) {
				if path != "/v1/work-checkpoints/authorize-handoff" {
					return store.request(path, p)
				}
				approved = sOr(p["content"])
				if p["destination"] != "external:anthropic" {
					t.Fatal(p)
				}
				if mode == "denied" {
					return nil, errors.New("denied")
				}
				hash := digest([]byte(approved))
				if mode == "wrong-hash" {
					hash = "wrong"
				}
				return map[string]any{"authorized": true, "content_hash": hash, "source_refs": store.saved["parent"]["package"].(map[string]any)["source_refs"], "archive_messages": []any{}}, nil
			}
			out, err := runHandoff(strings.NewReader(handoffInput(t)), request, func(spec map[string]any) (map[string]any, error) {
				called = true
				raw, _ := json.Marshal(map[string]any{"task": spec["task"], "instructions": spec["instructions"]})
				if string(raw) != approved {
					t.Fatal("prompt changed after authorization")
				}
				if spec["handoff_isolated"] != true {
					t.Fatal("unisolated runner")
				}
				return map[string]any{"ok": true, "output": "result"}, nil
			})
			if mode == "allowed" {
				if err != nil || !called || out["ok"] != true {
					t.Fatal(out, err)
				}
			} else if err == nil || called {
				t.Fatal(out, err, called)
			}
		})
	}
}

func TestHandoffResultFactsRespectCheckpointStringContract(t *testing.T) {
	facts, ok := handoffResultFacts("# Observations\r\n\n\tSource source-1 confirms the issue.\n  Next: review the fix. \n")
	if !ok {
		t.Fatal("ordinary multiline result rejected")
	}
	eq(t, facts, []string{"# Observations", "Source source-1 confirms the issue.", "Next: review the fix."})
	for _, output := range []string{"\x00hidden", strings.Repeat("line\n", 101), string([]byte{0xff}), strings.Repeat("x", 8193)} {
		if _, ok := handoffResultFacts(output); ok {
			t.Fatal("invalid fact accepted")
		}
	}
	store := newHandoffStore()
	out, err := runHandoff(strings.NewReader(handoffInput(t)), store.request, func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "output": "Observation\n\tSource source-1.\n"}, nil
	})
	if err != nil || out["ok"] != true {
		t.Fatal(out, err)
	}
	pkg := store.saved["child"]["package"].(map[string]any)
	eq(t, pkg["completed"], []string{"Observation", "Source source-1."})
}

func TestHandoffPinsAllAuthorizedEvidenceBeforeReservation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "changed"}[changed], func(t *testing.T) {
			store := newHandoffStore()
			calls, saves := 0, 0
			ran := false
			canonical := []any{map[string]any{"collection": "sources", "id": "source-1", "revision": 1}, map[string]any{"collection": "skill_versions", "id": "skill-version", "revision": 2}, map[string]any{"collection": "sources", "id": "skill-source", "revision": 1}}
			request := func(path string, p map[string]any) (map[string]any, error) {
				if path == "/v1/work-checkpoints/authorize-handoff" {
					calls++
					refs := canonical
					if changed && calls == 2 {
						refs = canonical[:1]
					}
					return map[string]any{"authorized": true, "content_hash": digest([]byte(sOr(p["content"]))), "source_refs": refs, "archive_messages": []any{}}, nil
				}
				if strings.HasSuffix(path, "/save") {
					saves++
					eq(t, p["package"].(map[string]any)["source_refs"], canonical)
				}
				return store.request(path, p)
			}
			out, err := runHandoff(strings.NewReader(handoffInput(t)), request, func(spec map[string]any) (map[string]any, error) {
				ran = true
				if sOr(spec["task"]) != strings.TrimSpace(sOr(spec["task"])) || sOr(spec["instructions"]) != strings.TrimSpace(sOr(spec["instructions"])) {
					t.Fatal("runner would change authorized bytes")
				}
				return map[string]any{"ok": true, "output": "result"}, nil
			})
			if changed {
				if err == nil || ran {
					t.Fatal(out, err)
				}
			} else if err != nil || !ran {
				t.Fatal(out, err)
			}
			if saves != 2 || calls != 2 {
				t.Fatal(saves, calls)
			}
		})
	}
}

func TestHandoffRejectedResultFallsBackToMetadataWithoutRerun(t *testing.T) {
	store := newHandoffStore()
	input := handoffInput(t)
	calls := 0
	var events []string
	request := func(path string, p map[string]any) (map[string]any, error) {
		if strings.HasSuffix(path, "/save") && p["expected_revision"] != 0 {
			events = append(events, sOr(p["event_id"]))
			if _, hasOutput := p["package"].(map[string]any)["completed"]; hasOutput {
				return nil, errors.New("result rejected")
			}
			if p["expected_revision"] != 1 {
				t.Fatal("fallback rebased onto another writer", p["expected_revision"])
			}
		}
		return store.request(path, p)
	}
	run := func(map[string]any) (map[string]any, error) {
		calls++
		return map[string]any{"ok": true, "output": "Rejected result must not be copied into fallback metadata"}, nil
	}
	out, err := runHandoff(strings.NewReader(input), request, run)
	if err != nil || out["ok"] != false || out["status"] != "blocked" || calls != 1 {
		t.Fatal(out, err, calls)
	}
	pkg := store.saved["child"]["package"].(map[string]any)
	if _, exists := pkg["completed"]; exists || strings.Contains(dumpLine(pkg), "Rejected result must") {
		t.Fatal("result body persisted in fallback")
	}
	if len(events) != 2 || events[0] == events[1] || !strings.HasSuffix(events[1], "-result-withheld") {
		t.Fatal(events)
	}
	if _, err := runHandoff(strings.NewReader(input), request, run); err == nil || calls != 1 {
		t.Fatal("replayed model execution", err, calls)
	}
}

func TestHandoffAmbiguousCommittedResultCannotBeOverwrittenByFallback(t *testing.T) {
	store := newHandoffStore()
	originalErr := errors.New("commit response lost")
	calls := 0
	fallbacks := 0
	request := func(path string, p map[string]any) (map[string]any, error) {
		if strings.HasSuffix(path, "/save") && strings.HasSuffix(sOr(p["event_id"]), "-result") {
			if _, err := store.request(path, p); err != nil {
				t.Fatal(err)
			}
			return nil, originalErr
		}
		if strings.HasSuffix(sOr(p["event_id"]), "-result-withheld") {
			fallbacks++
			if p["expected_revision"] != 1 {
				t.Fatal("unsafe fallback revision")
			}
		}
		return store.request(path, p)
	}
	out, err := runHandoff(strings.NewReader(handoffInput(t)), request, func(map[string]any) (map[string]any, error) {
		calls++
		return map[string]any{"ok": true, "output": "Already committed result"}, nil
	})
	if !errors.Is(err, originalErr) || out != nil || calls != 1 || fallbacks != 1 {
		t.Fatal(out, err, calls, fallbacks)
	}
	pkg := store.saved["child"]["package"].(map[string]any)
	if pkg["status"] != "completed" || store.saved["child"]["revision"] != 2 {
		t.Fatal("fallback overwrote committed state", store.saved["child"])
	}
	eq(t, pkg["completed"], []string{"Already committed result"})
}
