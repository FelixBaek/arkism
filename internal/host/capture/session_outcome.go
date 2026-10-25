package capture

// Finished Claude Code and Codex sessions as execution traces. A recall hook
// appends the exact memory versions it delivered to a per-session ledger (refs only, never text).
// Once a session has been idle for 30 minutes the drain records one trace per 32 refs in the shared
// (agent-less) user scope the memories live in, names the agent in agent_run.agent_id, and spools a
// ref-only outcome receipt for `arkism capture outcomes`. Off unless the session processor runs and
// ARKISM_MEMORY_OUTCOME_SPOOL is set.

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
)

const (
	sessionIdle       = 30 * 60.0   // seconds without a recall before a session counts as finished
	sessionLedgerTTL  = 7 * 86400.0 // a ledger the server keeps refusing is dropped after this
	sessionTraceRefs  = 32          // the outcome drain's per-trace target limit
	sessionLedgerName = "session-recalls"
)

func outcomeSpool() string {
	if !sessionProcessorOn() {
		return ""
	}
	return os.Getenv("ARKISM_MEMORY_OUTCOME_SPOOL")
}

// sharedScope is the trace scope: the capture scope without an agent partition.
func sharedScope() map[string]string {
	s := capScope()
	return map[string]string{"workspace_id": str(s["workspace_id"]), "project_id": str(s["project_id"]), "user_id": str(s["user_id"]), "agent_id": ""}
}

// memoryDeliveries extracts exact refs from returned memory rows without storing their text.
func memoryDeliveries(items []map[string]any, form string) []host.MemoryDelivery {
	var out []host.MemoryDelivery
	for _, item := range items {
		rev, _ := host.Int(item["revision"])
		id, _ := item["id"].(string)
		if (item["collection"] != nil && item["collection"] != "memories") || rev < 1 || id == "" || !truthy(item["text"]) {
			continue
		}
		scope := map[string]string{}
		rowScope, ok := item["scope"].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range scopeKeys {
			scope[key], _ = rowScope[key].(string)
		}
		out = append(out, host.MemoryDelivery{Collection: "memories", ID: id, Revision: rev, Scope: scope, Form: form})
	}
	return out
}

// ledgerRecall supports historical callers whose output form was not recorded.
func ledgerRecall(sid string, items []map[string]any, cwd ...string) {
	ledgerDeliveries(sid, memoryDeliveries(items, "unknown"), cwd...)
}

// ledgerDeliveries logs reference-only context exposure, never an actual-use acknowledgement.
func ledgerDeliveries(sid string, items []host.MemoryDelivery, cwd ...string) {
	if outcomeSpool() == "" || sid == "" {
		return
	}
	scope := sharedScope()
	if len(cwd) > 1 || privatePayload([]any{capOrigin, sid, scope, items, hookTranscript, cwd}) {
		return
	}
	explicitCWD := ""
	if len(cwd) == 1 && strings.TrimSpace(cwd[0]) != "" {
		explicitCWD = cwd[0]
	}
	policy := newArchivePolicy(hookTranscript)
	if explicitCWD != "" {
		// Hooks already carry their working directory. Keep recall bounded:
		// only this directory and the original transcript parent are checked.
		// Full metadata/snapshot inspection stays in the idle trace drain.
		if err := policy.addPath(explicitCWD, false); err != nil {
			return
		}
		if err := evidencePolicyCheck(policy); err != nil {
			return
		}
	} else if err := tracePolicySource(policy, hookTranscript, nil); err != nil {
		return // Unknown historical callers retain their full inspection fallback.
	}
	refs, deliveries := ledgerRefs(items, scope)
	if len(refs) == 0 {
		return
	}
	err := func() error {
		if err := evidencePolicyCheck(policy); err != nil {
			return err
		}
		dir := filepath.Join(capState, sessionLedgerName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, sdigest([]any{capOrigin, sid})[:32]+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		row := map[string]any{"ts": host.NowISO(), "origin": capOrigin, "session": sid, "refs": refs, "scope": scope, "deliveries": deliveries}
		if hookTranscript != "" {
			row["transcript"] = hookTranscript
		}
		if explicitCWD != "" {
			row["cwd"] = explicitCWD
		}
		_, err = f.WriteString(dumps(row) + "\n")
		return errors.Join(err, f.Close())
	}()
	if err != nil {
		metric(kv("event", "hook_error"), kv("hook", "session_recall_ledger"), kv("error", pyType(err)))
	}
}

// ledgerRefs keeps the shared-scope memory deliveries as refs and their delivered forms.
func ledgerRefs(items []host.MemoryDelivery, scope map[string]string) ([]any, []any) {
	refs, deliveries := []any{}, []any{}
	for _, item := range items {
		ok := item.Collection == "memories" && item.ID != "" && item.Revision >= 1
		for _, key := range scopeKeys {
			ok = ok && item.Scope[key] == scope[key]
		}
		if !ok {
			continue
		}
		form := item.Form
		if form != "full" && form != "meta" {
			form = "unknown"
		}
		refs = append(refs, map[string]any{"collection": "memories", "id": item.ID, "revision": item.Revision})
		deliveries = append(deliveries, map[string]any{"collection": "memories", "id": item.ID, "revision": item.Revision, "form": form})
	}
	return refs, deliveries
}

// sessionTraces claims every idle ledger, then records each claimed one. A ledger the server refuses
// (4xx other than 429) or one past its TTL is dropped; any other failure retries on the next drain.
func sessionTraces() {
	spool := outcomeSpool()
	if spool == "" {
		return
	}
	if err := privateCapturePolicy("capture_policy_wait"); err != nil {
		return
	}
	dir := filepath.Join(capState, sessionLedgerName)
	open, _ := filepath.Glob(filepath.Join(globEscape(dir), "*.jsonl"))
	for _, path := range open {
		if st, err := os.Stat(path); err == nil && float64(st.ModTime().UnixNano())/1e9+sessionIdle <= timeNow() {
			policy, err := traceLedgerPolicy(path)
			if capturePolicyPending(err) || evidencePolicyCheck(policy) != nil {
				continue
			}
			// A later recall of the same session starts a new ledger, so a claim never loses one.
			_ = os.Rename(path, strings.TrimSuffix(path, ".jsonl")+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".claimed")
		}
	}
	claimed, _ := filepath.Glob(filepath.Join(globEscape(dir), "*.claimed"))
	for _, path := range claimed {
		n, err := recordSession(path, spool)
		if capturePolicyPending(err) {
			continue
		}
		st, statErr := os.Stat(path)
		expired := statErr == nil && float64(st.ModTime().UnixNano())/1e9+sessionLedgerTTL <= timeNow()
		code := host.HTTPCode(err)
		switch {
		case err == nil:
			metric(kv("event", "session_trace_recorded"), kv("traces", n))
		case code >= 400 && code < 500 && code != 429 || expired || errors.Is(err, errLedgerInvalid):
			metric(kv("event", "session_trace_dropped"), kv("error", errorCode(err)))
		default:
			metric(kv("event", "session_trace_pending"), kv("error", errorCode(err)))
			continue
		}
		os.Remove(path)
	}
}

var errLedgerInvalid = runtimeErr("session_ledger_invalid")

// recordSession posts the ledger's traces and spools their receipts; a replay posts the same bodies.
func recordSession(path, spool string) (int, error) {
	policy, err := traceLedgerPolicy(path)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var origin, sid, first, last, transcript string
	var refs []any
	seen := map[string]bool{}
	forms := map[string][]string{}
	hasDeliveryMetadata := false
	scope := sharedScope()
	lines := bufio.NewScanner(f)
	lines.Buffer(nil, 1<<20)
	inspected := map[string]bool{}
	for lines.Scan() {
		row, err := ledgerRow(lines.Text(), origin, sid, policy, inspected)
		if err != nil {
			return 0, err
		}
		origin, sid = str(row["origin"]), str(row["session"])
		ts := str(row["ts"])
		if first == "" || ts < first {
			first = ts
		}
		last = max(last, ts)
		if t, ok := row["transcript"].(string); ok && t != "" {
			transcript = t
		}
		if refs, hasDeliveryMetadata, err = scanLedgerRow(row, scope, forms, seen, refs, hasDeliveryMetadata); err != nil {
			return 0, err
		}
	}
	if lines.Err() != nil || origin == "" || sid == "" || len(refs) == 0 {
		return 0, errLedgerInvalid
	}
	snapshot, cleanup, err := traceBehaviorSnapshot(transcript)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	if snapshot != "" {
		if err := policy.inspect(snapshot, "jsonl"); err != nil {
			return 0, err
		}
	}
	behavior := ledgerBehavior(snapshot, first, last)
	return postLedgerTraces(policy, spool, scope, origin, sid, first, last, refs, forms, hasDeliveryMetadata, behavior)
}

// ledgerRow parses one ledger line of the same session and checks it against the trace policy.
func ledgerRow(text, origin, sid string, policy *archivePolicy, inspected map[string]bool) (map[string]any, error) {
	row, err := loadsMap(text)
	if err != nil || origin != "" && (row["origin"] != origin || row["session"] != sid) {
		return nil, errLedgerInvalid
	}
	if err := tracePolicyRow(policy, row, inspected); err != nil {
		return nil, err
	}
	return row, nil
}

// scanLedgerRow adds a row's new refs and their delivery forms; a row whose saved scope differs is refused.
func scanLedgerRow(row map[string]any, scope map[string]string, forms map[string][]string, seen map[string]bool, refs []any, hasDeliveryMetadata bool) ([]any, bool, error) {
	if saved, ok := row["scope"].(map[string]any); ok {
		for _, key := range scopeKeys {
			if strField(saved[key]) != scope[key] {
				return nil, false, runtimeErr("session_ledger_scope_changed")
			}
		}
	}
	rowForms := map[string][]string{}
	for _, raw := range anyList(row["deliveries"]) {
		d, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		form := strField(d["form"])
		if form != "full" && form != "meta" && form != "unknown" {
			continue
		}
		key := sdigest(map[string]any{"collection": d["collection"], "id": d["id"], "revision": d["revision"]})
		rowForms[key] = append(rowForms[key], form)
		hasDeliveryMetadata = true
	}
	for _, r := range anyList(row["refs"]) {
		key := sdigest(r)
		fs := rowForms[key]
		if len(fs) == 0 {
			fs = []string{"unknown"}
		}
		for _, form := range fs {
			if !slices.Contains(forms[key], form) {
				forms[key] = append(forms[key], form)
			}
		}
		if k := sdigest(r); !seen[k] {
			seen[k] = true
			refs = append(refs, r)
		}
	}
	return refs, hasDeliveryMetadata, nil
}

// ledgerBehavior is the session's behavior over the ledger's window, nil when either end is unreadable.
func ledgerBehavior(snapshot, first, last string) map[string]any {
	if t, err := time.Parse(time.RFC3339Nano, first); err == nil {
		// The prompt line is written around its recall, so the window opens a minute early.
		if t2, err := time.Parse(time.RFC3339Nano, last); err == nil {
			return sessionBehavior(snapshot, t.Add(-time.Minute).Format(time.RFC3339Nano), t2.Add(time.Duration(sessionIdle)*time.Second).Format(time.RFC3339Nano))
		}
	}
	return nil
}

// postLedgerTraces posts one trace per sessionTraceRefs refs and reports how many were recorded.
func postLedgerTraces(policy *archivePolicy, spool string, scope map[string]string, origin, sid, first, last string, refs []any, forms map[string][]string, hasDeliveryMetadata bool, behavior map[string]any) (int, error) {
	for i := 0; i < len(refs); i += sessionTraceRefs {
		if err := evidencePolicyCheck(policy); err != nil {
			return i / sessionTraceRefs, err
		}
		chunk := refs[i:min(i+sessionTraceRefs, len(refs))]
		payload := map[string]any{"retrieved": chunk}
		identity := []any{origin, sid, first, last, chunk}
		if hasDeliveryMetadata {
			deliveries, err := chunkDeliveries(chunk, forms, payload)
			if err != nil {
				return 0, err
			}
			identity = append(identity, deliveries)
		}
		id := sdigest(identity)[:32]
		body := map[string]any{"scope": scope, "trace_id": "session-trace-" + id, "thread_id": sid,
			"agent_run": map[string]any{"agent_id": origin, "task_type": "agent_session", "outcome": "partial", "started_at": first, "finished_at": last,
				"text": "agent session " + origin}, // the run's real result is unknown here: a checker decides
			"events": []any{map[string]any{"id": "session-event-" + id, "kind": "context", "occurred_at": last,
				"payload": payload}}}
		context := body["events"]
		if behavior != nil {
			body["events"] = append(context.([]any), map[string]any{"id": "session-behavior-" + id, "kind": "behavior", "occurred_at": last, "payload": behavior})
		}
		if err := postSessionTrace(policy, spool, scope, body, context, behavior != nil); err != nil {
			return i / sessionTraceRefs, err
		}
	}
	return (len(refs) + sessionTraceRefs - 1) / sessionTraceRefs, nil
}

// chunkDeliveries lists a chunk's delivery forms into payload, with the observation they support.
func chunkDeliveries(chunk []any, forms map[string][]string, payload map[string]any) ([]any, error) {
	var deliveries []any
	known := 0
	for _, raw := range chunk {
		r, ok := raw.(map[string]any)
		if !ok {
			return nil, errLedgerInvalid
		}
		for _, form := range forms[sdigest(raw)] {
			deliveries = append(deliveries, map[string]any{"collection": r["collection"], "id": r["id"], "revision": r["revision"], "form": form})
		}
		if fs := forms[sdigest(raw)]; slices.Contains(fs, "full") || slices.Contains(fs, "meta") {
			known++
		}
	}
	payload["deliveries"] = deliveries
	if known == len(chunk) {
		payload["observation"] = "context_delivery"
	} else if known > 0 {
		payload["observation"] = "context_delivery_mixed"
	}
	return deliveries, nil
}

// postSessionTrace posts one trace, retrying without its behavior event when the server refuses that,
// and spools the stored trace's receipt.
func postSessionTrace(policy *archivePolicy, spool string, scope map[string]string, body map[string]any, context any, withBehavior bool) error {
	if privatePayload(body) {
		return errLedgerInvalid
	}
	stored, err := capPost("/v1/execution_traces/completed", body, 30*time.Second)
	if code := host.HTTPCode(err); withBehavior && code >= 400 && code < 500 && code != 429 {
		// A refused behavior event (e.g. a secret in a command) must not cost the delivery record.
		metric(kv("event", "session_behavior_refused"), kv("error", errorCode(err)))
		body["events"] = context
		if err := evidencePolicyCheck(policy); err != nil {
			return err
		}
		stored, err = capPost("/v1/execution_traces/completed", body, 30*time.Second)
	}
	if err != nil {
		return err
	}
	trace, _ := stored["trace"].(map[string]any)
	rev, _ := host.Int(trace["revision"])
	if trace["collection"] != "execution_traces" || str(trace["id"]) == "" || rev < 1 {
		return runtimeErr("trace_receipt_invalid")
	}
	if privateIdentityFields(trace, evidenceIdentityKeys) {
		return sessionWait("session_private_identity")
	}
	if err := evidencePolicyCheck(policy); err != nil {
		return err
	}
	return crewrun.EnqueueReceipt(spool, crewrun.TraceReceipt{Scope: scope,
		Trace: crewrun.Ref{Collection: "execution_traces", ID: str(trace["id"]), Revision: int64(rev)}})
}
