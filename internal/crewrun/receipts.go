package crewrun

// Finished runs as canonical execution traces. When a run ends, the engine records
// one execution trace for it under a separate producer credential and hands the ref-only receipt to
// the configured sinks: the memory outcome spool (`arkism capture outcomes` drains it) and,
// optionally, the session processor. Bodies stay on the server; a receipt holds refs only. Crew
// runs never judge memories: the outcome drain rejects their receipts and the server
// refuses a crew_run trace as outcome evidence.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Ref is an exact canonical record version.
type Ref struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
}

// TraceReceipt is what a finished run hands its sinks: scope and refs, never bodies.
type TraceReceipt struct {
	Scope        map[string]string `json:"scope"`
	SessionID    string            `json:"session_id"`
	Trace        Ref               `json:"trace"`
	Run          Ref               `json:"run"`
	SourceEvents []Ref             `json:"source_events"`
	OccurredAt   string            `json:"occurred_at"`
}

// ReceiptSink takes one receipt; an error is logged and leaves the trace stored.
type ReceiptSink func(TraceReceipt) error

func scopeMap(s Scope) map[string]string {
	return map[string]string{"workspace_id": s.WorkspaceID, "project_id": s.ProjectID, "user_id": s.UserID, "agent_id": s.AgentID}
}

func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// eventKinds: steps that observe the world are tool results; AI answers are assistant messages.
var eventKinds = map[string]string{"code": "tool_result", "http": "tool_result", "github": "tool_result",
	"ai": "message", "ai_once": "message", "subflow": "message", "expert": "message", "team": "message", "n8n": "tool_result"}

// traceInput is the deterministic final-node projection of a finished run, so a
// retry replays the same request. Its 64-event cap is not an attempt ledger or
// proof that every model call was captured.
func traceInput(scope Scope, run Run, nodes map[string]Node) (map[string]any, bool) {
	var started, finished time.Time
	ids := make([]string, 0, len(run.State.Nodes))
	for id := range run.State.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var events []any
	for _, id := range ids {
		st := run.State.Nodes[id]
		kind := eventKinds[nodes[id].Type]
		if st.StartedAt == nil || st.FinishedAt == nil || kind == "" || (st.Status != "done" && st.Status != "failed") {
			continue
		}
		if started.IsZero() || st.StartedAt.Before(started) {
			started = *st.StartedAt
		}
		if st.FinishedAt.After(finished) {
			finished = *st.FinishedAt
		}
		payload, _ := json.Marshal(map[string]any{"crew_run": run.ID, "crew_id": run.State.CrewID, "node_id": id, "node_type": nodes[id].Type,
			"status": st.Status, "port": st.Port, "output": clip(st.Output, 12000), "error": clip(st.Error, 2000)})
		events = append(events, map[string]any{"id": "crew-event-" + shortHash(run.ID, id), "kind": kind,
			"occurred_at": st.FinishedAt.UTC().Format("2006-01-02T15:04:05.000Z"), "payload": json.RawMessage(payload)})
		if len(events) == 64 {
			break
		}
	}
	if len(events) == 0 {
		return nil, false
	}
	outcome := map[string]string{"succeeded": "success", "stopped": "partial"}[run.State.Status]
	if outcome == "" {
		outcome = "failed"
	}
	ms := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	return map[string]any{"scope": scopeMap(scope), "trace_id": "crew-trace-" + shortHash(run.ID), "thread_id": "crew-run-" + run.ID,
		"agent_run": map[string]any{"task_type": "crew_run", "outcome": outcome, "started_at": ms(started), "finished_at": ms(finished),
			"text": "crew run " + run.ID + " " + run.State.Status}, "events": events}, true
}

// recordFinished stores the run's trace and feeds the receipt to every sink. It never fails the run.
// The caller holds the run's lock, so the trace is built here and the POST and sinks (a sink may wait
// on the capture drain's lock) run in the background; Wait waits for them.
// shortcut: a failed trace or sink write is logged, not retried; add a journal when outcomes go live.
func (e *Engine) recordFinished(ctx context.Context, run Run, nodes map[string]Node) {
	if e.Traces == nil || len(e.Sinks) == 0 {
		if AttemptObserverEnabled(ctx) && traceObservationApplicable(run, nodes) {
			MarkAttemptObservationIncomplete(ctx, "trace_missing")
		}
		return
	}
	body, ok := traceInput(e.Traces.Scope, run, nodes)
	if !ok {
		return
	}
	completed := observationDeliveryPending(ctx)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer completed()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute) // a shutdown still records the end
		defer cancel()
		e.storeTrace(ctx, run.ID, body)
	}()
}

// Empty/start-only runs have no final event eligible for this producer. They are
// not missing traces, and never become personal-memory outcome evidence.
func traceObservationApplicable(run Run, nodes map[string]Node) bool {
	for id, state := range run.State.Nodes {
		if state.StartedAt != nil && state.FinishedAt != nil && eventKinds[nodes[id].Type] != "" &&
			(state.Status == "done" || state.Status == "failed") {
			return true
		}
	}
	return false
}

func (e *Engine) storeTrace(ctx context.Context, runID string, body map[string]any) {
	var stored struct {
		Trace        Ref   `json:"trace"`
		Run          Ref   `json:"run"`
		SourceEvents []Ref `json:"source_events"`
	}
	if err := e.Traces.do(ctx, "POST", "/v1/execution_traces/completed", body, &stored); err != nil {
		MarkAttemptObservationIncomplete(ctx, "trace_store_failed")
		e.log().Warn("crew run trace", "run_id", runID, "error", err.Error())
		return
	}
	if stored.Trace.Collection != "execution_traces" || stored.Trace.Revision < 1 {
		MarkAttemptObservationIncomplete(ctx, "trace_receipt_invalid")
		e.log().Warn("crew run trace", "run_id", runID, "error", "trace_receipt_invalid")
		return
	}
	if AttemptObserverEnabled(ctx) {
		expectedID, _ := body["trace_id"].(string)
		if stored.Trace.ID == "" || (expectedID != "" && stored.Trace.ID != expectedID) {
			MarkAttemptObservationIncomplete(ctx, "trace_receipt_invalid")
		}
	}
	receipt := TraceReceipt{Scope: scopeMap(e.Traces.Scope), SessionID: body["thread_id"].(string), Trace: stored.Trace, Run: stored.Run,
		SourceEvents: stored.SourceEvents, OccurredAt: body["agent_run"].(map[string]any)["finished_at"].(string)}
	for _, sink := range e.Sinks {
		if err := sink(receipt); err != nil {
			MarkAttemptObservationIncomplete(ctx, "sink_failed")
			e.log().Warn("crew run receipt", "run_id", runID, "error", err.Error())
		}
	}
}

// EnqueueReceipt is the outcome spool sink (enqueue_receipt): {scope, trace} only, written once
// under its content hash with fsync, so a replay never duplicates and a crash never tears a file.
func EnqueueReceipt(dir string, r TraceReceipt) error {
	if r.Trace.Collection != "execution_traces" || r.Trace.ID == "" || r.Trace.Revision < 1 {
		return errors.New("outcome_receipt_invalid")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"scope": r.Scope, "trace": r.Trace}); err != nil {
		return err
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	name := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	tmp, err := os.CreateTemp(dir, ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return publishReceipt(tmp.Name(), name, dir)
}

// tracesFromEnv is the producer client: ARKISM_CREW_TRACE_TOKEN_FILE, a credential of its own.
// It is off when unset or when it holds the engine's own token (never the owner's on this path).
func tracesFromEnv(base *Client) *Client {
	file := os.Getenv("ARKISM_CREW_TRACE_TOKEN_FILE")
	if file == "" {
		return nil
	}
	own, _ := readSecretFile(base.TokenFile)
	mine, err := readSecretFile(file)
	if err != nil || len(bytes.TrimSpace(mine)) == 0 || bytes.Equal(bytes.TrimSpace(own), bytes.TrimSpace(mine)) {
		return nil
	}
	c := *base
	c.TokenFile = file
	// Crew traces keep their own agent partition: they stay out of memory outcome matching.
	c.Scope.AgentID = cmp.Or(c.Scope.AgentID, "crew")
	return &c
}
