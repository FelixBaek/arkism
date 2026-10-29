package memory

import (
	"errors"
	"testing"
)

func TestLLMJudgeEvaluationValidation(t *testing.T) {
	cal := &Ref{Collection: colJudgeCalibrations, ID: "cal", Revision: 1}
	for _, x := range []*Evaluation{
		{EvalType: "llm_judge"},
		{EvalType: "user_rating", CalibrationRef: cal},
		{EvalType: "llm_judge", CalibrationRef: &Ref{Collection: colEvalTasks, ID: "cal", Revision: 1}},
		{EvalType: "llm_judge", CalibrationRef: &Ref{Collection: colJudgeCalibrations, ID: "cal"}},
		{EvalType: "automated_check", SpecRef: &Ref{Collection: colEvalTrials, ID: "t", Revision: 1}},
		{EvalType: "automated_check", TrialRef: &Ref{Collection: colEvalTasks, ID: "t", Revision: 1}},
	} {
		if validEvaluation(x) {
			t.Fatalf("accepted invalid evaluation: %+v", x)
		}
	}
	if !validEvaluation(&Evaluation{EvalType: "llm_judge", CalibrationRef: cal}) || !validEvaluation(&Evaluation{EvalType: "automated_check", SpecRef: &Ref{Collection: colEvalTasks, ID: "t", Revision: 1}}) {
		t.Fatal("rejected valid evaluation")
	}
	if !evaluationRole("llm_judge", "pipeline") || evaluationRole("llm_judge", "human") || evaluationRole("llm_judge", "agent") {
		t.Fatal("llm_judge roles")
	}
}

func TestEvalRecordsAndJudgedEvaluation(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	ref := func(col string, r Record) *Ref { return &Ref{Collection: col, ID: r.ID, Revision: r.Revision} }
	task := f.create(f.human, colEvalTasks, CreateInput{Scope: scopeA, Sources: refs(src), Kind: "recall", Text: `{"query":"q","expected":["m1"]}`})
	run := f.create(f.human, colEvalRuns, CreateInput{Scope: scopeA, Sources: refs(src), Kind: "recall", Text: `{"dataset":"recall-quality-v1"}`})
	if _, e := f.svc.Create(testCtx, f.human, colEvalTrials, CreateInput{Scope: scopeA, Sources: refs(src), Text: "trial", Relations: []Ref{*ref(colEvalTasks, task)}}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("trial without run: %v", e)
	}
	trial := f.create(f.human, colEvalTrials, CreateInput{Scope: scopeA, Sources: refs(src), Text: "trial", Relations: []Ref{*ref(colEvalTasks, task), *ref(colEvalRuns, run)}})
	cal := f.create(f.human, colJudgeCalibrations, CreateInput{Scope: scopeA, Sources: refs(src), Text: `{"agreement":0.9,"labels":40}`})
	if got, e := f.svc.Get(testCtx, f.human, colEvalTrials, scopeA, trial.ID); e != nil || got.ID != trial.ID {
		t.Fatalf("get trial: %+v %v", got, e)
	}

	f.auth.actors["judge"] = actor("judge-1", "pipeline", true, allVerbs...)
	target := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)}))
	in := func(x Evaluation) CreateInput {
		return CreateInput{Scope: scopeA, Text: "judged", Sources: refs(src), Target: &Ref{Collection: "agent_runs", ID: target.ID, Revision: target.Revision}, Evaluation: &x}
	}
	judged := Evaluation{EvalType: "llm_judge", Passed: boolValue(true), SpecRef: ref(colEvalTasks, task), TrialRef: ref(colEvalTrials, trial), CalibrationRef: ref(colJudgeCalibrations, cal)}
	stale := judged
	stale.CalibrationRef = &Ref{Collection: colJudgeCalibrations, ID: cal.ID, Revision: cal.Revision + 1}
	if _, e := f.svc.Create(testCtx, "judge", "evaluations", in(stale)); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale calibration: %v", e)
	}
	if _, e := f.svc.Create(testCtx, f.human, "evaluations", in(judged)); !errors.Is(e, ErrDenied) {
		t.Fatalf("human llm_judge: %v", e)
	}
	got, e := f.svc.Create(testCtx, "judge", "evaluations", in(judged))
	if e != nil || got.Evaluation.CalibrationRef.ID != cal.ID || got.Evaluation.TrialRef.ID != trial.ID {
		t.Fatalf("judged evaluation: %+v %v", got, e)
	}
	if independentEvaluation(got) {
		t.Fatal("llm_judge must not verify skills or reflections")
	}
	sum, e := f.svc.SummarizeEvaluations(testCtx, f.human, EvaluationSummaryInput{Scope: scopeA, Target: *got.Target})
	if e != nil || sum.Groups["llm_judge"].Count != 1 || len(sum.PreferredTypes) != 0 || sum.Passed != nil {
		t.Fatalf("summary: %+v %v", sum, e)
	}
}
