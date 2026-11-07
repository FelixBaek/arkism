package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Real ingestion -> canonical run -> independent checker -> SQL aggregation.
// A later-completed success is evaluated first; the earlier-completed failure
// must still win. Repeated judging does not erase raw history or reset its window.
func TestOutcomeTaskFirstCompletionPostgres(t *testing.T) {
	p := isolatedPostgres(t)
	if e := p.initTraceSchema(testCtx); e != nil {
		t.Fatal(e)
	}
	f := newFixture(t)
	f.svc.Repo = p
	f.auth.actors["producer"] = actor("producer", roleService, true, allVerbs...)
	f.auth.actors["other-producer"] = actor("other-producer", roleService, true, allVerbs...)
	f.auth.actors["checker"] = actor("checker", roleService, true, allVerbs...)
	m := reviewedForEvaluation(t, f)
	target := Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}
	payload, _ := json.Marshal(map[string]any{"retrieved": []Ref{target}})
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	for i, item := range []struct {
		task, producer string
		completed      int
		passed         bool
	}{
		{"same-task", "producer", 20, true}, {"same-task", "producer", 10, false},
		{"same-task", "producer", 30, true}, {"same-task", "other-producer", 15, true},
		{"different-task", "producer", 5, true}, {"", "producer", 2, true}, {"", "producer", 1, true},
	} {
		trace, e := f.svc.IngestCompletedTrace(testCtx, item.producer, TraceIngestInput{Scope: scopeA, TraceID: fmt.Sprintf("task-trace-%d", i), ThreadID: fmt.Sprintf("thread-%d", i), AgentRun: CompletedAgentRunInput{TaskKey: item.task, AgentID: "claude_code", TaskType: "test", Outcome: "failed", StartedAt: start, FinishedAt: start.Add(time.Duration(item.completed) * time.Second), Text: "task"}, Events: []TraceEventInput{{ID: fmt.Sprintf("event-%d", i), Kind: "context", OccurredAt: start, Payload: payload}}})
		if e != nil {
			t.Fatal(e)
		}
		ref, e := f.svc.RecordMemoryOutcome(testCtx, "checker", MemoryOutcomeInput{Scope: scopeA, Target: target, Trace: trace.Trace, Evidence: trace.SourceEvents, EventID: fmt.Sprintf("judgment-%d", i), CheckerSHA256: strings.Repeat("a", 64), Dimension: "work_task", Passed: item.passed})
		if e != nil {
			t.Fatal(e)
		}
		row, e := p.Get(testCtx, "evaluations", scopeA, ref.ID)
		if e != nil || (item.task != "" && (row.MemoryOutcomeTaskHash == "" || row.MemoryOutcomeCompletedAt == nil)) {
			t.Fatal(row, e)
		}
	}
	q := MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{m.ID}, IndependentFirst: true, Dimension: "work_task"}
	counts, e := p.MemoryOutcomeCounts(testCtx, q)
	if e != nil || len(counts) != 1 || counts[0].Total != 5 || counts[0].Passed != 4 {
		t.Fatal("first completion or namespace separation", counts, e)
	}
	q.IndependentFirst = false
	counts, e = p.MemoryOutcomeCounts(testCtx, q)
	if e != nil || len(counts) != 1 || counts[0].Total != 7 || counts[0].Passed != 6 {
		t.Fatal("raw history lost", counts, e)
	}
}

func TestOutcomeTaskRejectsAmbiguousOrUnattributedIdentity(t *testing.T) {
	for _, mode := range []string{"ambiguous", "unattributed", "invalid-key"} {
		t.Run(mode, func(t *testing.T) {
			f, _, in := outcomeFixture(t)
			run, _ := f.repo.Get(testCtx, "agent_runs", scopeA, "outcome-run")
			run.Run.TaskKey = "task"
			run.CreatedBy = "runner"
			run.CreatedByType = roleService
			if mode == "unattributed" {
				run.CreatedBy = ""
			}
			if mode == "invalid-key" {
				run.Run.TaskKey = " bad"
			}
			run.ID = "identity-run"
			if e := f.repo.Insert(testCtx, "agent_runs", run); e != nil {
				t.Fatal(e)
			}
			{
				trace, _ := f.repo.Get(testCtx, "execution_traces", scopeA, in.Trace.ID)
				trace.Relations[0].ID = run.ID
				if mode == "ambiguous" {
					trace.Relations = append(trace.Relations, trace.Relations[0])
				}
				trace.Revision++
				in.Trace.Revision = 2
				if e := f.repo.Replace(testCtx, "execution_traces", trace, 1); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in); e == nil {
				t.Fatal("unsafe identity accepted")
			}
		})
	}
}
