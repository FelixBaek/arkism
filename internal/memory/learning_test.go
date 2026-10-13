package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func learningFixture(t *testing.T, outcome string, passed bool) (fixture *fixture, run, evaluation Record) {
	t.Helper()
	f := newFixture(t)
	src := f.source()
	runInput := completedRunInput(CreateInput{Scope: scopeA, Text: "local learning run", Sources: refs(src)})
	runInput.Run.Outcome = outcome
	run = f.create(f.agent, "agent_runs", runInput)
	target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
	evaluation = f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "independent check", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
	return f, run, evaluation
}

func TestLearningCreatesReviewableRecordsAndSignalsOnlyAfterThreeRuns(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		src := f.source()
		run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "learning run", Sources: refs(src)}))
		passed := true
		target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
		ev := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "check", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
		got, err := f.svc.Learn(testCtx, f.agent, LearningInput{Scope: scopeA, Run: target, Evaluation: Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}, TaskType: "index", Lesson: "verify the local index", VerificationMethod: "query check"})
		if err != nil || got.ReviewSuggested != (i == 2) || got.CompatibleRuns != i+1 {
			t.Fatalf("iteration %d: result=%+v err=%v", i, got, err)
		}
	}
	if len(f.repo.rows("memories")) != 0 {
		t.Fatal("learning auto-promoted a candidate")
	}
	if len(f.repo.rows("reflections")) != 3 || len(f.repo.rows("memory_candidates")) != 3 {
		t.Fatal("learning records missing")
	}
}

type learningPolicy struct {
	Authorizer
	deny       string
	recordOnly bool
	seen       []Authorization
}

func (a *learningPolicy) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	a.seen = append(a.seen, in)
	d, err := a.Authorizer.Authorize(ctx, token, in)
	if err == nil && in.Action == a.deny && (!a.recordOnly || in.ResourceID != "") {
		d.Allowed, d.Context, d.DecisionID = false, in.Context, "learning-test-deny"
	}
	return d, err
}

func TestLearningRequiresEvidenceReadsAndAuthorizesCompletePayloads(t *testing.T) {
	f, run, evaluation := learningFixture(t, "failed", false)
	policy := &learningPolicy{Authorizer: f.auth}
	f.svc.Auth = policy
	in := LearningInput{Scope: scopeA, Run: Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}, TaskType: "repair", Lesson: "retry after checking state", FailureReason: "state was stale", VerificationMethod: "replay the query"}
	if _, err := f.svc.Learn(testCtx, f.agent, in); err != nil {
		t.Fatal(err)
	}
	writes := map[string]string{}
	reads := map[string]bool{}
	for _, request := range policy.seen {
		if strings.HasPrefix(request.Action, "write:") {
			writes[request.Action] = request.Content
		}
		if strings.HasPrefix(request.Action, "read:") {
			reads[request.Action] = true
		}
	}
	for _, action := range []string{"read:agent_runs", "read:evaluations", "read:memory_candidates"} {
		if !reads[action] {
			t.Fatalf("missing %s authorization", action)
		}
	}
	for _, action := range []string{"write:reflections", "write:memory_candidates"} {
		if !strings.Contains(writes[action], in.FailureReason) || !strings.Contains(writes[action], in.VerificationMethod) {
			t.Fatalf("%s did not inspect the complete payload: %s", action, writes[action])
		}
	}

	f, run, evaluation = learningFixture(t, "success", true)
	f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:memory_candidates"}
	in.Run, in.Evaluation = Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}
	if _, err := f.svc.Learn(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatalf("write-only caller read candidate counts: %v", err)
	}
	if len(f.repo.rows("reflections")) != 0 || len(f.repo.rows("memory_candidates")) != 0 {
		t.Fatal("denied evidence read wrote learning records")
	}

	f = newFixture(t)
	makeEvidence := func(label string) (Record, Record) {
		source := f.source()
		run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: label + " policy run", Sources: refs(source)}))
		passed := true
		target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
		return run, f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: label + " policy check", Sources: refs(source), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
	}
	firstRun, firstEvaluation := makeEvidence("first")
	in.Run, in.Evaluation = Ref{Collection: "agent_runs", ID: firstRun.ID, Revision: firstRun.Revision}, Ref{Collection: "evaluations", ID: firstEvaluation.ID, Revision: firstEvaluation.Revision}
	if _, err := f.svc.Learn(testCtx, f.agent, in); err != nil {
		t.Fatal(err)
	}
	secondRun, secondEvaluation := makeEvidence("second")
	f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:memory_candidates", recordOnly: true}
	in.Run, in.Evaluation = Ref{Collection: "agent_runs", ID: secondRun.ID, Revision: secondRun.Revision}, Ref{Collection: "evaluations", ID: secondEvaluation.ID, Revision: secondEvaluation.Revision}
	if _, err := f.svc.Learn(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatalf("per-record candidate read bypassed: %v", err)
	}
	if len(f.repo.rows("reflections")) != 1 || len(f.repo.rows("memory_candidates")) != 1 {
		t.Fatal("per-record denial wrote learning records")
	}
}

func TestLearningPreservesSensitiveEvidenceClassification(t *testing.T) {
	f, run, evaluation := learningFixture(t, "success", true)
	for _, source := range f.repo.rows("sources") {
		source.Sensitivity = "restricted"
		f.repo.store["sources"][recKey(scopeA, source.ID)] = source
	}
	in := LearningInput{Scope: scopeA, Run: Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}, TaskType: "classify", Lesson: "retain the evidence class", VerificationMethod: "inspect records"}
	result, err := f.svc.Learn(testCtx, f.agent, in)
	if err != nil {
		t.Fatal(err)
	}
	reflection, _ := f.repo.Get(testCtx, "reflections", scopeA, result.Reflection.ID)
	candidate, _ := f.repo.Get(testCtx, "memory_candidates", scopeA, result.Lesson.ID)
	for _, row := range []Record{reflection, candidate} {
		if row.Sensitivity != "restricted" || row.Inspection == nil || row.Inspection.RulesVersion != inspectionVersion {
			t.Fatalf("learning declassified or skipped inspection: %+v", row)
		}
	}
}

func TestLearningCountsDistinctLiveValidatedRuns(t *testing.T) {
	f := newFixture(t)
	makeEvidence := func() (Record, Record) {
		source := f.source()
		run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "counted run", Sources: refs(source)}))
		passed := true
		target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
		evaluation := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "counted check", Sources: refs(source), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
		return run, evaluation
	}
	learn := func(run, evaluation Record) LearningResult {
		t.Helper()
		result, err := f.svc.Learn(testCtx, f.agent, LearningInput{Scope: scopeA, Run: Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}, TaskType: "count", Lesson: "count only valid runs", VerificationMethod: "replay"})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	run1, evaluation1 := makeEvidence()
	first := learn(run1, evaluation1)
	duplicate, _ := f.repo.Get(testCtx, "memory_candidates", scopeA, first.Lesson.ID)
	duplicate.ID = ID()
	if err := f.repo.Insert(testCtx, "memory_candidates", duplicate); err != nil {
		t.Fatal(err)
	}
	run2, evaluation2 := makeEvidence()
	if result := learn(run2, evaluation2); result.CompatibleRuns != 2 || result.ReviewSuggested {
		t.Fatalf("duplicate run inflated count: %+v", result)
	}
	revoked, _ := f.repo.Get(testCtx, "evaluations", scopeA, evaluation1.ID)
	revoked.Status = "expired"
	f.repo.store["evaluations"][recKey(scopeA, evaluation1.ID)] = revoked
	run3, evaluation3 := makeEvidence()
	if result := learn(run3, evaluation3); result.CompatibleRuns != 2 || result.ReviewSuggested {
		t.Fatalf("revoked evidence inflated count: %+v", result)
	}
}

func TestLearningHTTPRejectsCrossScopeAndRevokedEvidence(t *testing.T) {
	f, run, ev := learningFixture(t, "failed", false)
	in := LearningInput{Scope: scopeA, Run: Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}, TaskType: "repair", Lesson: "check the failed step", VerificationMethod: "replay"}
	payload, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", "/v1/learning", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+f.agent)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("failed run should produce a candidate: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.svc.Learn(testCtx, f.agent, LearningInput{Scope: scopeB, Run: in.Run, Evaluation: in.Evaluation, TaskType: in.TaskType, Lesson: in.Lesson, VerificationMethod: in.VerificationMethod}); !errors.Is(err, ErrDenied) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope access = %v", err)
	}
	if err := f.svc.Delete(testCtx, f.human, "sources", f.repo.rows("sources")[0].ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Learn(testCtx, f.agent, in); !errors.Is(err, ErrDenied) && !errors.Is(err, ErrConflict) {
		t.Fatalf("revoked evidence accepted: %v", err)
	}
}

func TestLearningIsAgentGeneratedAndIdempotent(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "idempotent run", Sources: refs(src)}))
	passed := true
	target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
	ev := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "check", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
	in := LearningInput{Scope: scopeA, Run: target, Evaluation: Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}, TaskType: "index", Lesson: "verify once", VerificationMethod: "query"}
	if _, err := f.svc.Learn(testCtx, f.human, in); !errors.Is(err, ErrDenied) {
		t.Fatalf("human generated learning accepted: %v", err)
	}
	first, err := f.svc.Learn(testCtx, f.agent, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Learn(testCtx, f.agent, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeat learning = %v", err)
	}
	if len(f.repo.rows("reflections")) != 1 || len(f.repo.rows("memory_candidates")) != 1 || first.Lesson.ID == "" {
		t.Fatal("repeat changed learning records")
	}
}

func TestLearningCandidateQuotaRollsBackBothRecords(t *testing.T) {
	f := newFixture(t)
	f.svc.CandidateDailyLimit = 1
	src := f.source()
	makeRun := func() (Record, Record) {
		run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "quota run", Sources: refs(src)}))
		passed := true
		target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
		ev := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "check", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
		return run, ev
	}
	run, ev := makeRun()
	in := LearningInput{Scope: scopeA, Run: Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}, TaskType: "quota", Lesson: "one lesson", VerificationMethod: "query"}
	if _, err := f.svc.Learn(testCtx, f.agent, in); err != nil {
		t.Fatal(err)
	}
	run, ev = makeRun()
	in.Run, in.Evaluation = Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}
	if _, err := f.svc.Learn(testCtx, f.agent, in); !errors.Is(err, ErrCandidateQuota) {
		t.Fatalf("quota error = %v", err)
	}
	if len(f.repo.rows("reflections")) != 1 || len(f.repo.rows("memory_candidates")) != 1 {
		t.Fatal("quota failure committed partial learning")
	}
}
