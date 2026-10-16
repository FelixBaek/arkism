package capture

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func traceScopeEnv(t *testing.T) map[string]any {
	t.Setenv("ARKISM_WORKSPACE", "w")
	t.Setenv("ARKISM_PROJECT", "p")
	t.Setenv("ARKISM_USER", "u")
	t.Setenv("ARKISM_AGENT", "crew")
	t.Setenv("ARKISM_SHARED_AUDIENCE_SCOPE", "")
	return host.Scope()
}

func TestSharedAudienceScopeKeepsWorkspaceProjectUser(t *testing.T) {
	origin := map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u", "agent_id": "a"}
	if got, err := audienceScope(origin, ""); err != nil || !eq(got, origin) {
		t.Fatal(got, err)
	}
	if got, err := audienceScope(origin, `{"workspace_id":"w","project_id":"p","user_id":"u","agent_id":"team"}`); err != nil || got["agent_id"] != "team" {
		t.Fatal(got, err)
	}
	for _, bad := range []string{`{"workspace_id":"w","project_id":"p","user_id":"other","agent_id":"team"}`,
		`{"workspace_id":"w","project_id":"p","user_id":"u","agent_id":""}`, `{"workspace_id":"w","project_id":"p","user_id":"u"}`, `[]`} {
		if _, err := audienceScope(origin, bad); err == nil {
			t.Fatal(bad)
		}
	}
}

// A canonical run receipt reaches the processor without bodies in the spool; the claim carries
// Personal/Work memory_context, the canonical source ref, and an agent statement becomes an inference.
func TestSessionCanonicalReceiptCarriesMemoryContextAndSourceRefs(t *testing.T) {
	capFake(t)
	scope := traceScopeEnv(t)
	fakeUsage(t, nil, errors.New("no reading"))
	trace := map[string]any{"collection": "execution_traces", "id": "tr", "revision": int64(1)}
	run := map[string]any{"collection": "agent_runs", "id": "run", "revision": int64(1)}
	tool := map[string]any{"collection": "sources", "id": "ev-tool", "revision": int64(1)}
	msg := map[string]any{"collection": "sources", "id": "ev-msg", "revision": int64(1)}
	event := map[string]any{"scope": scope, "session_id": "crew-run-1", "occurred_at": "2026-10-14T00:00:00.000Z",
		"receipt": map[string]any{"trace": trace, "run": run, "source_events": []any{tool, msg}}}
	if err := EnqueueSessionTrace(event); !pending(err) || errorCode(err) != "session_processor_not_enabled" {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	for range 2 { // a replay is a no-op
		if err := EnqueueSessionTrace(event); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := os.ReadDir(spoolDir())
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	path := spoolDir() + "/" + paths[0].Name()
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "go test ok") {
		t.Fatal("body in spool")
	}
	changed := map[string]any{"scope": scope, "session_id": "crew-run-1", "occurred_at": "2026-10-14T00:00:00.000Z",
		"receipt": map[string]any{"trace": trace, "run": run, "source_events": []any{tool}}}
	if err := EnqueueSessionTrace(changed); err != nil { // a different receipt is a different event
		t.Fatal(err)
	}

	rows := map[string]map[string]any{
		"/v1/execution_traces/tr": {"id": "tr", "revision": int64(1), "status": "active", "scope": scope, "text": "LangGraph execution trace crew-run-1",
			"relations": []any{run}, "sources": []any{map[string]any{"source_id": "ev-tool"}, map[string]any{"source_id": "ev-msg"}}},
		"/v1/agent_runs/run": {"id": "run", "revision": int64(1), "status": "active", "scope": scope, "trace_id": "tr"},
		"/v1/sources/ev-tool": {"id": "ev-tool", "revision": int64(1), "status": "active", "scope": scope, "trace_id": "tr", "source_event_id": "ev-tool",
			"kind": "tool_result", "text": `{"output":"go test ok"}`},
		"/v1/sources/ev-msg": {"id": "ev-msg", "revision": int64(1), "status": "active", "scope": scope, "trace_id": "tr", "source_event_id": "ev-msg",
			"kind": "message", "text": `{"output":"발표 자료는 짧게"}`},
	}
	capGet = func(p string, _ time.Duration) (map[string]any, error) {
		row, ok := rows[strings.SplitN(p, "?", 2)[0]]
		if !ok {
			t.Fatal(p)
		}
		return row, nil
	}
	quote := `발표 자료는 짧게`
	output := mustLoad(t, `{"session_state": {"goal": "발표", "constraints": [], "decisions": [], "completed": [], "next_steps": [],
		"uncertainties": [], "artifacts": [], "checks": [], "source_refs": [{"source_id": "ev-msg", "revision": 1, "quote": "`+quote+`"}]},
		"memory_candidates": [{"statement": "발표 자료는 짧게", "kind": "lesson", "domains": ["work", "personal"], "applicability": "업무 발표", "observed_at": "",
			"valid_from": "", "evidence_kind": "agent_statement", "source_refs": [{"source_id": "ev-msg", "revision": 1, "quote": "`+quote+`"}], "uncertainty": ""}],
		"coverage": {"start": 0, "end": 2, "expected_revision": 0, "schema_version": "`+sessionSchemaVersion+`", "prompt_version": "`+sessionPromptVersion+`", "source_ids": ["ev-tool", "ev-msg"]},
		"unresolved_refs": []}`)
	llm = func(string, string, string, time.Duration, string, string) (any, error) { return output, nil }
	var captures, authorized, saves []any
	capPost = func(p string, body any, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasSuffix(p, "/get"):
			return nil, &host.HTTPError{Code: 404}
		case strings.HasSuffix(p, "/authorize-transfer"):
			authorized = append(authorized, body)
			return map[string]any{"authorized": true, "content_hash": sha256Hex([]byte(str(body.(map[string]any)["content"])))}, nil
		case p == "/v1/capture":
			captures = append(captures, body)
			return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
		case strings.HasSuffix(p, "/save"):
			saves = append(saves, body)
			return map[string]any{"id": body.(map[string]any)["id"], "revision": 1}, nil
		}
		t.Fatal(p)
		return nil, nil
	}
	job, err := readJob(path)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := queuedOrigin(job)
	if err != nil || origin != "langgraph" {
		t.Fatal(origin, err)
	}
	if err := sessionProcess(job, func() error { return nil }, origin); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 1 || len(saves) != 1 || len(authorized) != 1 || job["session_processor_complete"] != true {
		t.Fatal(captures, saves, authorized, job)
	}
	claim := captures[0].(host.Obj)
	claims, _ := claim.Get("claims")
	c := claims.([]any)[0].(map[string]any)
	context := c["memory_context"].(map[string]any)
	if context["evidence_kind"] != "inference" || !eq(context["domains"], []any{"work", "personal"}) || context["semantic_kind"] != "lesson" ||
		c["basis"] != "inferred" || !eq(c["source_refs"], []any{msg}) || len(anyList(c["archive_messages"])) != 0 {
		t.Fatal(c)
	}
	if !eq(authorized[0].(map[string]any)["source_refs"], []any{tool, msg}) {
		t.Fatal(authorized[0])
	}
	pkg := saves[0].(map[string]any)["package"].(map[string]any)
	if !eq(pkg["source_refs"], []any{tool, msg}) {
		t.Fatal(pkg["source_refs"])
	}

	// A source that changed after the run is never read as evidence.
	rows["/v1/sources/ev-tool"]["revision"] = int64(2)
	if _, _, err := sessionCanonical(job); !pending(err) || errorCode(err) != "session_trace_source_changed" {
		t.Fatal(err)
	}
}

// A drain holding its lock past the bound makes the sink give up, so a crew run's Wait never hangs on it.
func TestSessionTraceGivesUpOnAHeldDrainLock(t *testing.T) {
	capFake(t)
	scope := traceScopeEnv(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	old := sessionTraceLockWait
	sessionTraceLockWait = 300 * time.Millisecond
	t.Cleanup(func() { sessionTraceLockWait = old })
	if err := os.MkdirAll(capState, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := lockFile(filepath.Join(capState, "drain.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ref := map[string]any{"collection": "execution_traces", "id": "tr", "revision": int64(1)}
	event := map[string]any{"scope": scope, "session_id": "crew-run-1", "occurred_at": "2026-10-14T00:00:00.000Z",
		"receipt": map[string]any{"trace": ref, "run": ref, "source_events": []any{}}}
	start := time.Now()
	if err := EnqueueSessionTrace(event); !errors.Is(err, errBusy) || time.Since(start) > 5*time.Second {
		t.Fatal(err, time.Since(start))
	}
}
