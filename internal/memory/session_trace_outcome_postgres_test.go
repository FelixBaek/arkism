package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTraceAgentLivesInRunOrScope(t *testing.T) {
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	trace := func(scopeAgent, runAgent string) TraceIngestInput {
		scope := scopeA
		scope.AgentID = scopeAgent
		return TraceIngestInput{Scope: scope, TraceID: "trace-agent", ThreadID: "thread-agent",
			AgentRun: CompletedAgentRunInput{AgentID: runAgent, TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "agent session"},
			Events:   []TraceEventInput{{ID: "event-agent", Kind: "context", OccurredAt: start.Add(time.Second), Payload: json.RawMessage(`{"retrieved":[]}`)}}}
	}
	for _, ok := range [][2]string{{"", "claude_code"}, {"crew", ""}, {"crew", "crew"}} {
		if _, _, e := prepareCompletedTrace(trace(ok[0], ok[1])); e != nil {
			t.Fatal("valid agent placement rejected:", ok, e)
		}
	}
	for _, bad := range [][2]string{{"", ""}, {"crew", "claude_code"}, {"", " claude_code"}} {
		if _, _, e := prepareCompletedTrace(trace(bad[0], bad[1])); !errors.Is(e, ErrInvalid) {
			t.Fatal("trace without one executing agent accepted:", bad, e)
		}
	}
}

// A session trace in the shared user scope reaches the memories it delivered
// and settles their evaluation obligation; the run still names its agent.
func TestPostgresSharedScopeSessionTraceSettlesMemoryOutcome(t *testing.T) {
	p := isolatedPostgres(t)
	if err := p.initTraceSchema(testCtx); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.svc.Repo = p
	m := reviewedForEvaluation(t, f)
	target := Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}
	f.auth.actors["session-producer"] = actor("session-producer", roleService, true, allVerbs...)
	f.auth.actors["checker"] = actor("checker", roleService, true, allVerbs...)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	payload, _ := json.Marshal(map[string]any{"retrieved": []Ref{target}})
	out, e := f.svc.IngestCompletedTrace(testCtx, "session-producer", TraceIngestInput{Scope: scopeA, TraceID: "session-trace-1", ThreadID: "session-1",
		AgentRun: CompletedAgentRunInput{AgentID: "claude_code", TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "agent session"},
		Events:   []TraceEventInput{{ID: "session-event-1", Kind: "context", OccurredAt: start.Add(time.Second), Payload: payload}}})
	if e != nil {
		t.Fatal(e)
	}
	run, e := p.Get(testCtx, "agent_runs", scopeA, out.Run.ID)
	if e != nil || run.Run == nil || run.Run.AgentID != "claude_code" {
		t.Fatalf("run agent: %+v %v", run.Run, e)
	}
	if _, e = f.svc.RecordMemoryOutcome(testCtx, "checker", MemoryOutcomeInput{Scope: scopeA, Target: target, Trace: out.Trace, Evidence: out.SourceEvents,
		EventID: "session-outcome-1", CheckerSHA256: strings.Repeat("b", 64), Dimension: "personal_compliance", Passed: true}); e != nil {
		t.Fatal(e)
	}
	job, e := p.Get(testCtx, "jobs", scopeA, memoryEvaluationID(scopeA, target))
	if e != nil || job.Status != JobStatusDone {
		t.Fatalf("obligation not settled: %+v %v", job, e)
	}
}
