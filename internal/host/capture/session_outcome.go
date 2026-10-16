package capture

// Finished Claude Code and Codex sessions as execution traces (ST-623, FR-012, OI-25). A recall hook
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

// ledgerRecall records the delivered memories of the shared scope; anything else could not be judged.
func ledgerRecall(sid string, items []map[string]any) {
	if outcomeSpool() == "" || sid == "" {
		return
	}
	scope, refs := sharedScope(), []any{}
	for _, i := range items {
		rev, _ := host.Int(i["revision"])
		rowScope, _ := i["scope"].(map[string]any)
		// Memory list rows carry no collection and the server omits an empty agent_id/user_id.
		ok := (i["collection"] == nil || i["collection"] == "memories") && rev >= 1 && truthy(i["text"]) && str(i["id"]) != ""
		for _, k := range scopeKeys {
			v, _ := rowScope[k].(string)
			ok = ok && v == scope[k]
		}
		if ok {
			refs = append(refs, map[string]any{"collection": "memories", "id": i["id"], "revision": rev})
		}
	}
	if len(refs) == 0 {
		return
	}
	err := func() error {
		dir := filepath.Join(capState, sessionLedgerName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, sdigest([]any{capOrigin, sid})[:32]+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		row := map[string]any{"ts": host.NowISO(), "origin": capOrigin, "session": sid, "refs": refs}
		if hookTranscript != "" {
			row["transcript"] = hookTranscript
		}
		_, err = f.WriteString(dumps(row) + "\n")
		return err
	}()
	if err != nil {
		metric(kv("event", "hook_error"), kv("hook", "session_recall_ledger"), kv("error", pyType(err)))
	}
}

// sessionTraces claims every idle ledger, then records each claimed one. A ledger the server refuses
// (4xx other than 429) or one past its TTL is dropped; any other failure retries on the next drain.
func sessionTraces() {
	spool := outcomeSpool()
	if spool == "" {
		return
	}
	dir := filepath.Join(capState, sessionLedgerName)
	open, _ := filepath.Glob(filepath.Join(globEscape(dir), "*.jsonl"))
	for _, path := range open {
		if st, err := os.Stat(path); err == nil && float64(st.ModTime().UnixNano())/1e9+sessionIdle <= timeNow() {
			// A later recall of the same session starts a new ledger, so a claim never loses one.
			os.Rename(path, strings.TrimSuffix(path, ".jsonl")+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".claimed")
		}
	}
	claimed, _ := filepath.Glob(filepath.Join(globEscape(dir), "*.claimed"))
	for _, path := range claimed {
		n, err := recordSession(path, spool)
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
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var origin, sid, first, last, transcript string
	var refs []any
	seen := map[string]bool{}
	lines := bufio.NewScanner(f)
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		row, err := loadsMap(lines.Text())
		if err != nil || origin != "" && (row["origin"] != origin || row["session"] != sid) {
			return 0, errLedgerInvalid
		}
		origin, sid = str(row["origin"]), str(row["session"])
		ts := str(row["ts"])
		if first == "" || ts < first {
			first = ts
		}
		last = max(last, ts)
		if t := str(row["transcript"]); t != "" {
			transcript = t
		}
		for _, r := range anyList(row["refs"]) {
			if k := sdigest(r); !seen[k] {
				seen[k] = true
				refs = append(refs, r)
			}
		}
	}
	if lines.Err() != nil || origin == "" || sid == "" || len(refs) == 0 {
		return 0, errLedgerInvalid
	}
	scope := sharedScope()
	var behavior map[string]any
	if t, err := time.Parse(time.RFC3339Nano, first); err == nil {
		// The prompt line is written around its recall, so the window opens a minute early.
		if t2, err := time.Parse(time.RFC3339Nano, last); err == nil {
			behavior = sessionBehavior(transcript, t.Add(-time.Minute).Format(time.RFC3339Nano), t2.Add(time.Duration(sessionIdle)*time.Second).Format(time.RFC3339Nano))
		}
	}
	for i := 0; i < len(refs); i += sessionTraceRefs {
		chunk := refs[i:min(i+sessionTraceRefs, len(refs))]
		id := sdigest([]any{origin, sid, first, last, chunk})[:32]
		body := map[string]any{"scope": scope, "trace_id": "session-trace-" + id, "thread_id": sid,
			"agent_run": map[string]any{"agent_id": origin, "task_type": "agent_session", "outcome": "partial", "started_at": first, "finished_at": last,
				"text": "agent session " + origin}, // the run's real result is unknown here: a checker decides
			"events": []any{map[string]any{"id": "session-event-" + id, "kind": "context", "occurred_at": last,
				"payload": map[string]any{"retrieved": chunk}}}}
		context := body["events"]
		if behavior != nil {
			body["events"] = append(context.([]any), map[string]any{"id": "session-behavior-" + id, "kind": "behavior", "occurred_at": last, "payload": behavior})
		}
		stored, err := capPost("/v1/execution_traces/completed", body, 30*time.Second)
		if code := host.HTTPCode(err); behavior != nil && code >= 400 && code < 500 && code != 429 {
			// A refused behavior event (e.g. a secret in a command) must not cost the delivery record.
			metric(kv("event", "session_behavior_refused"), kv("error", errorCode(err)))
			body["events"] = context
			stored, err = capPost("/v1/execution_traces/completed", body, 30*time.Second)
		}
		if err != nil {
			return i / sessionTraceRefs, err
		}
		trace, _ := stored["trace"].(map[string]any)
		rev, _ := host.Int(trace["revision"])
		if trace["collection"] != "execution_traces" || str(trace["id"]) == "" || rev < 1 {
			return i / sessionTraceRefs, runtimeErr("trace_receipt_invalid")
		}
		if err := crewrun.EnqueueReceipt(spool, crewrun.TraceReceipt{Scope: scope,
			Trace: crewrun.Ref{Collection: "execution_traces", ID: str(trace["id"]), Revision: int64(rev)}}); err != nil {
			return i / sessionTraceRefs, err
		}
	}
	return (len(refs) + sessionTraceRefs - 1) / sessionTraceRefs, nil
}
