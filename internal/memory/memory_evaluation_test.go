package memory

import (
	"context"
	"errors"
	"testing"
)

func reviewedForEvaluation(t *testing.T, f *fixture) Record {
	t.Helper()
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "설계 검토는 자세히, 상태 보고는 간단히", Sources: refs(src)}))
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func evaluationJob(t *testing.T, f *fixture, m Record) Record {
	t.Helper()
	job, err := f.repo.Get(testCtx, "jobs", m.Scope, memoryEvaluationID(m.Scope, Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}))
	if err != nil {
		t.Fatal(err)
	}
	if job.Text != "" || job.Summary != "" || len(job.Sources) != 0 || job.Target == nil || job.Target.Revision != m.Revision {
		t.Fatal("invalid tracking record", job)
	}
	return job
}

func TestMemoryEvaluationScheduledWithApproval(t *testing.T) {
	f := newFixture(t)
	m := reviewedForEvaluation(t, f)
	job := evaluationJob(t, f, m)
	if job.Status != JobStatusPending {
		t.Fatal(job.Status)
	}
	in := EvaluationSummaryInput{Scope: m.Scope, Target: *job.Target}
	summary, err := f.svc.SummarizeEvaluations(testCtx, f.human, in)
	if err != nil || summary.Tracking == nil || summary.Tracking.Status != JobStatusPending || summary.Passed != nil {
		t.Fatal(summary, err)
	}
	if _, err = f.svc.ReviewCandidate(testCtx, f.human, m.ID, CandidateReviewInput{Scope: m.Scope, ExpectedRevision: m.Revision - 1, Action: "approve"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	count := 0
	for _, r := range f.repo.rows("jobs") {
		if r.Kind == memoryEvaluationKind && r.Target.ID == m.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatal("retry duplicated tracking", count)
	}
}

type evaluationFailureRepo struct {
	Repository
	fail string
}
type evaluationFailureTx struct {
	Tx
	fail string
}

func (r evaluationFailureRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, evaluationFailureTx{tx, r.fail}) })
}
func (tx evaluationFailureTx) Insert(ctx context.Context, col string, r Record) error {
	if tx.fail == "enqueue" && col == "jobs" && r.Kind == memoryEvaluationKind {
		return ErrUnavailable
	}
	return tx.Tx.Insert(ctx, col, r)
}
func (tx evaluationFailureTx) Replace(ctx context.Context, col string, r Record, rev int64) error {
	if tx.fail == "completion" && col == "jobs" && r.Kind == memoryEvaluationKind {
		return ErrUnavailable
	}
	return tx.Tx.Replace(ctx, col, r, rev)
}

func TestMemoryEvaluationApprovalRollback(t *testing.T) {
	for _, fail := range []string{"enqueue", "audit"} {
		t.Run(fail, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "approval must be atomic", Sources: refs(src)}))
			before := len(f.repo.rows("jobs"))
			f.svc.Repo = evaluationFailureRepo{f.repo, fail}
			f.repo.failAudit = fail == "audit"
			_, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			f.repo.failAudit = false
			if len(f.repo.rows("memories")) != 0 || len(f.repo.rows("jobs")) != before {
				t.Fatal("partial approval committed")
			}
			c = f.get(f.human, "memory_candidates", scopeA, c.ID)
			if c.Status != "candidate" || c.Revision != 1 {
				t.Fatal("candidate changed", c)
			}
		})
	}
}

func TestMemoryEvaluationObservations(t *testing.T) {
	for _, kind := range []string{"automated_check", "user_rating", "model_self", "unrated"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			// An agent creates the proposal; approval preserves that origin.
			src := f.source()
			c := f.create(f.agent, "memory_candidates", generatedCandidate(scopeA, refs(src)))
			m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
			if err != nil {
				t.Fatal(err)
			}
			token, role, who := "evaluator", roleService, "evaluator"
			evalType := kind
			if kind == "user_rating" {
				role = roleHuman
			}
			if kind == "model_self" {
				role, who = roleAgent, m.CreatedBy
			}
			passed := boolValue(false)
			if kind == "unrated" {
				evalType, passed = "automated_check", nil
			}
			f.auth.actors[token] = actor(who, role, true, allVerbs...)
			ev := f.create(token, "evaluations", CreateInput{Scope: scopeA, Text: "observed result", Sources: refs(src), Target: &Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, Evaluation: &Evaluation{EvalType: evalType, Passed: passed}})
			job := evaluationJob(t, f, m)
			want := JobStatusDone
			if kind == "model_self" || kind == "unrated" {
				want = JobStatusPending
			}
			if job.Status != want {
				t.Fatal(job.Status, want)
			}
			if want == JobStatusDone && (len(job.Relations) != 1 || job.Relations[0].ID != ev.ID || job.Passed) {
				t.Fatal("failed observation turned into success", job)
			}
		})
	}
}

func TestMemoryEvaluationCompletionRollbackAndStaleTarget(t *testing.T) {
	f := newFixture(t)
	m := reviewedForEvaluation(t, f)
	f.auth.actors["evaluator"] = actor("evaluator", roleService, true, allVerbs...)
	in := CreateInput{Scope: m.Scope, Text: "check", Sources: m.Sources, Target: &Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, Evaluation: &Evaluation{EvalType: "automated_check", Passed: boolValue(true)}}
	f.svc.Repo = evaluationFailureRepo{f.repo, "completion"}
	if _, err := f.svc.Create(testCtx, "evaluator", "evaluations", in); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if len(f.repo.rows("evaluations")) != 0 || evaluationJob(t, f, m).Status != JobStatusPending {
		t.Fatal("partial completion")
	}
	f.svc.Repo = f.repo
	f.transition(f.human, colMemories, m.ID, ChangeInput{Scope: m.Scope, ExpectedRevision: m.Revision, Status: "expired"})
	if _, err := f.svc.Create(testCtx, "evaluator", "evaluations", in); !errors.Is(err, ErrConflict) {
		t.Fatal("stale target", err)
	}
	if evaluationJob(t, f, m).Status != JobStatusPending {
		t.Fatal("stale evaluation settled tracking")
	}
}
