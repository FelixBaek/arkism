package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func outcomeFixture(t *testing.T) (*fixture, Record, MemoryOutcomeInput) {
	f := newFixture(t)
	m := reviewedForEvaluation(t, f)
	target := Ref{Collection: "memories", ID: m.ID, Revision: m.Revision}
	raw, _ := json.Marshal(map[string]any{"retrieved": []Ref{target}, "actual_test_exit": 1})
	source := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: string(raw)}))
	source.TraceID = "outcome-trace"
	source.SourceEventID = "outcome-event"
	if e := f.repo.Replace(testCtx, "sources", source, source.Revision); e != nil {
		t.Fatal(e)
	}
	finish := time.Now().UTC()
	run := Record{ID: "outcome-run", Scope: scopeA, Status: "active", Revision: 1, Run: &RunDetails{Outcome: "failed", FinishedAt: &finish}}
	trace := Record{ID: source.TraceID, Scope: scopeA, Status: "active", Revision: 1, CreatedBy: "runner", Sources: []SourceRef{sourceReference(source, "trace")}, Relations: []Ref{{Collection: "agent_runs", ID: run.ID, Revision: 1}}}
	if e := f.repo.Insert(testCtx, "agent_runs", run); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.Insert(testCtx, "execution_traces", trace); e != nil {
		t.Fatal(e)
	}
	f.auth.actors["checker"] = actor("checker", roleService, true, allVerbs...)
	return f, m, MemoryOutcomeInput{Scope: scopeA, Target: target, Trace: Ref{Collection: "execution_traces", ID: trace.ID, Revision: 1}, Evidence: []Ref{{Collection: "sources", ID: source.ID, Revision: 1}}, EventID: "outcome-one", CheckerSHA256: strings.Repeat("a", 64), Dimension: "work_task", Passed: false}
}
func TestMemoryOutcomeIndependentFailureSettlesAndReplayFences(t *testing.T) {
	f, m, in := outcomeFixture(t)
	a, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
	if e != nil {
		t.Fatal(e)
	}
	if evaluationJob(t, f, m).Status != JobStatusDone {
		t.Fatal("obligation not settled")
	}
	ev := f.get(f.human, "evaluations", scopeA, a.ID)
	if ev.Passed || *ev.Evaluation.Passed {
		t.Fatal("failure became success")
	}
	b, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
	if e != nil || a != b || len(f.repo.rows("evaluations")) != 1 {
		t.Fatal("replay", b, e)
	}
	if e = f.repo.Delete(testCtx, "sources", scopeA, in.Evidence[0].ID, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.RecordMemoryOutcome(testCtx, "checker", in); e == nil {
		t.Fatal("deleted replay accepted")
	}
}
func TestMemoryOutcomeDeniesUnboundOrSelfRatings(t *testing.T) {
	for _, mode := range []string{"human", "creator", "approver", "stale", "foreign", "not-delivered", "uncompleted", "changed-request"} {
		t.Run(mode, func(t *testing.T) {
			f, m, in := outcomeFixture(t)
			token := "checker"
			switch mode {
			case "human":
				token = f.human
			case "creator":
				f.auth.actors[token] = actor(m.CreatedBy, roleService, true, allVerbs...)
			case "approver":
				f.auth.actors[token] = actor(m.ApprovedBy, roleService, true, allVerbs...)
			case "stale":
				in.Target.Revision++
			case "foreign":
				in.Scope = scopeB
			case "not-delivered":
				in.Target.ID = "other"
			case "uncompleted":
				f.repo.Delete(testCtx, "agent_runs", scopeA, "outcome-run", 1)
			case "changed-request":
				if _, e := f.svc.RecordMemoryOutcome(testCtx, token, in); e != nil {
					t.Fatal(e)
				}
				in.Passed = true
			}
			if _, e := f.svc.RecordMemoryOutcome(testCtx, token, in); e == nil {
				t.Fatal("unsafe outcome accepted")
			}
		})
	}
}
