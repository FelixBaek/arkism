package capture

// Canonical execution receipts as session processor input (ST-302, FR-012). A finished agent run's
// trace is already canonical on the server; the spool keeps only its refs, never a body. Sources of
// kind tool_result are tool observations; every other trace event is an assistant claim.

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var scopeKeys = []string{"workspace_id", "project_id", "user_id", "agent_id"}

// audienceScope is shared_work.audience_scope: an explicit named agent partition of the same
// workspace, project and user. It selects a partition only; the server still authorizes it.
func audienceScope(origin map[string]any, configured string) (map[string]any, error) {
	if configured == "" {
		return origin, nil
	}
	var scope map[string]any
	if json.Unmarshal([]byte(configured), &scope) != nil || len(scope) != 4 {
		return nil, runtimeErr("shared_audience_scope_invalid")
	}
	for _, k := range scopeKeys {
		v, ok := scope[k].(string)
		if !ok || k != "user_id" && (strings.TrimSpace(v) == "" || len(v) > 256 || strings.ContainsFunc(v, func(r rune) bool { return r < 32 })) {
			return nil, runtimeErr("shared_audience_scope_invalid")
		}
	}
	workspace := origin["workspace_id"]
	if !truthy(workspace) {
		workspace = origin["organization_id"]
	}
	user, _ := origin["user_id"].(string)
	if !eq(workspace, scope["workspace_id"]) || !eq(origin["project_id"], scope["project_id"]) || user != scope["user_id"] {
		return nil, runtimeErr("shared_audience_scope_changed")
	}
	return scope, nil
}

// sessionScope is the processor's input scope: the origin scope, or ARKISM_SHARED_AUDIENCE_SCOPE.
func sessionScope() (map[string]any, error) {
	return audienceScope(host.Scope(), os.Getenv("ARKISM_SHARED_AUDIENCE_SCOPE"))
}

// sessionCanonical reads the receipt's trace, run and mirrored sources, each exact, live and in scope.
func sessionCanonical(job map[string]any) (records []any, refs map[string][]any, err error) {
	defer recovered(&err)
	receipt := job["execution_receipt"].(map[string]any)
	scope := capScope()
	fetch := func(ref any) (map[string]any, error) {
		collection := autosaveGet(ref, "collection")
		id, isStr := autosaveGet(ref, "id").(string)
		if !isStr || collection != "execution_traces" && collection != "agent_runs" && collection != "sources" {
			return nil, sessionWait("session_trace_ref_invalid")
		}
		row, err := capGet("/v1/"+collection.(string)+"/"+url.PathEscape(id)+"?"+scopeQuery(false), 8*time.Second)
		if err != nil {
			return nil, err
		}
		changed := row["id"] != id || !eq(row["revision"], autosaveGet(ref, "revision")) || row["status"] != "active" ||
			truthy(row["content_erased"]) || truthy(row["privacy_quarantined"])
		rowScope, _ := row["scope"].(map[string]any)
		for _, k := range scopeKeys {
			got, _ := rowScope[k].(string)
			want, _ := scope[k].(string)
			changed = changed || got != want
		}
		if changed {
			return nil, sessionWait("session_trace_source_changed")
		}
		return row, nil
	}
	trace, err := fetch(receipt["trace"])
	if err != nil {
		return nil, nil, err
	}
	run, err := fetch(receipt["run"])
	if err != nil {
		return nil, nil, err
	}
	related := false
	for _, r := range anyList(trace["relations"]) {
		related = related || eq(r, receipt["run"])
	}
	if trace["text"] != "LangGraph execution trace "+str(job["session_id"]) || !related || run["trace_id"] != trace["id"] {
		return nil, nil, sessionWait("session_trace_binding_invalid")
	}
	bound := map[any]bool{}
	for _, ref := range anyList(trace["sources"]) {
		bound[autosaveGet(ref, "source_id")] = true
	}
	records, refs = []any{}, map[string][]any{}
	for _, ref := range anyList(receipt["source_events"]) {
		if autosaveGet(ref, "collection") != "sources" || !bound[autosaveGet(ref, "id")] {
			return nil, nil, sessionWait("session_trace_source_unbound")
		}
		source, err := fetch(ref)
		if err != nil {
			return nil, nil, err
		}
		if source["trace_id"] != trace["id"] || source["source_event_id"] != autosaveGet(ref, "id") {
			return nil, nil, sessionWait("session_trace_source_unbound")
		}
		role := "assistant"
		if source["kind"] == "tool_result" {
			role = "tool"
		}
		id := str(autosaveGet(ref, "id"))
		records = append(records, map[string]any{"source_id": id, "revision": autosaveGet(ref, "revision"), "role": role, "text": source["text"]})
		refs[id] = []any{}
	}
	return records, refs, nil
}

var sessionTraceLockWait = 90 * time.Second

// lockFileWithin takes path's exclusive lock, giving up with errBusy after d.
func lockFileWithin(path string, d time.Duration) (*os.File, error) {
	deadline := time.Now().Add(d)
	for {
		f, err := lockFile(path, false)
		if !errors.Is(err, errBusy) || !time.Now().Before(deadline) {
			return f, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// EnqueueSessionTrace is the completion sink: it spools one canonical receipt
// {scope, session_id, receipt{trace, run, source_events}, occurred_at} for the session processor.
// A replay of the same event is a no-op; a different receipt under the same id is refused.
func EnqueueSessionTrace(event map[string]any) error {
	scope, err := sessionScope()
	if err != nil {
		return err
	}
	if !eq(event["scope"], scope) {
		return sessionWait("session_trace_scope_mismatch")
	}
	if !sessionProcessorOn() {
		return sessionWait("session_processor_not_enabled")
	}
	given, _ := event["receipt"].(map[string]any)
	receipt := map[string]any{}
	for _, k := range []string{"trace", "run", "source_events"} {
		v, has := given[k]
		if !has {
			return sessionWait("session_trace_ref_invalid")
		}
		receipt[k] = v
	}
	eventID := "langgraph-session-" + sdigest([]any{event["scope"], receipt})
	job := map[string]any{"type": "task_finished", "origin": "langgraph", "session_id": event["session_id"], "users": []any{},
		"event_id": eventID, "queued_at": event["occurred_at"], "session_scope": event["scope"], "execution_receipt": receipt,
		"occurred_at": event["occurred_at"]}
	if err := os.MkdirAll(capState, 0o700); err != nil {
		return err
	}
	// Bounded: a crew run's background recorder waits here, and Engine.Wait must not hang on a long drain.
	lock, err := lockFileWithin(filepath.Join(capState, "drain.lock"), sessionTraceLockWait)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := os.MkdirAll(spoolDir(), 0o700); err != nil {
		return err
	}
	path := filepath.Join(spoolDir(), eventID+".json")
	existing, err := readJob(path)
	switch {
	case err == nil:
		if !eq(existing["execution_receipt"], receipt) || !eq(existing["session_scope"], event["scope"]) {
			return sessionWait("session_trace_replay_mismatch")
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	return atomicWrite(path, dumps(job))
}
