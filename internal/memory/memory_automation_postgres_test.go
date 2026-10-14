package memory

import (
	"errors"
	"sync"
	"testing"
)

func TestPostgresMemoryAutomationAtomicReplay(t *testing.T) {
	p := isolatedPostgres(t)
	f, _, in := autoReviewFixture(t)
	f.svc.Repo = p
	src := f.source()
	c := f.create(f.agent, "memory_candidates", generatedCandidate(scopeA, refs(src)))
	in.ExpectedRevision = c.Revision
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in)
			if e != nil || r.ID != c.ID || r.Status != "active" {
				t.Errorf("concurrent review: %+v %v", r, e)
			}
		}()
	}
	wg.Wait()
	counts := map[string]int{}
	for _, job := range pgCollection(t, p, "jobs") {
		counts[job.Kind]++
	}
	if counts["memory_review_receipt"] != 1 || counts[memoryEvaluationKind] != 1 || counts["embedding"] != 1 {
		t.Fatal("non-atomic/repeated effects", counts)
	}
	m := f.get(f.human, colMemories, scopeA, c.ID)
	f.auth.actors["evaluator"] = actor("independent-checker", roleService, true, allVerbs...)
	ev := f.create("evaluator", "evaluations", CreateInput{Scope: scopeA, Text: "actual check failed", Sources: refs(src), Target: &Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, Evaluation: &Evaluation{EvalType: "automated_check", Passed: boolValue(false)}})
	job, e := p.Get(testCtx, "jobs", scopeA, memoryEvaluationID(scopeA, *ev.Target))
	if e != nil || job.Status != JobStatusDone || len(job.Relations) != 1 || job.Relations[0].ID != ev.ID {
		t.Fatal(job, e)
	}
	if _, e = f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); e != nil {
		t.Fatal("evaluation completion changed approval receipt", e)
	}
}

func TestPostgresMemoryEvaluationApprovalRollback(t *testing.T) {
	p := isolatedPostgres(t)
	f, _, in := autoReviewFixture(t)
	f.svc.Repo = p
	src := f.source()
	c := f.create(f.agent, "memory_candidates", generatedCandidate(scopeA, refs(src)))
	in.ExpectedRevision = c.Revision
	f.svc.Repo = evaluationFailureRepo{p, "enqueue"}
	if _, e := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if len(pgCollection(t, p, colMemories)) != 0 {
		t.Fatal("memory escaped rollback")
	}
	for _, job := range pgCollection(t, p, "jobs") {
		if job.Kind == "memory_review_receipt" || job.Kind == memoryEvaluationKind || job.Kind == "embedding" {
			t.Fatal("job escaped rollback", job.Kind)
		}
	}
	f.svc.Repo = p
	if r := f.get(f.human, "memory_candidates", scopeA, c.ID); r.Revision != c.Revision || r.Status != "candidate" {
		t.Fatal(r)
	}
	if _, e := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); e != nil {
		t.Fatal("recovery retry", e)
	}
}

func TestPostgresCandidateConflictReviewAtomicRecovery(t *testing.T) {
	p := isolatedPostgres(t)
	f, _, in := autoReviewFixture(t)
	f.svc.Repo = p
	src := f.source()
	c := f.create(f.agent, "memory_candidates", generatedCandidate(scopeA, refs(src)))
	old := reviewedForEvaluation(t, f)
	mark, err := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "mark", Reason: "user correction", Memories: []Ref{{Collection: colMemories, ID: old.ID, Revision: old.Revision}, {Collection: "memory_candidates", ID: c.ID, Revision: c.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range mark.Memories {
		if row.ID == c.ID {
			c = row
		} else {
			old = row
		}
	}
	in.ExpectedRevision = c.Revision
	in.Conflict = &CandidateReviewConflict{ID: mark.Relation.ID, ExpectedRevision: mark.Relation.Revision, Memory: Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}, Resolution: "replace_old", Reason: "changed preference"}
	f.svc.Repo = evaluationFailureRepo{p, "enqueue"}
	if _, err = f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	stored, err := p.Get(testCtx, colMemories, scopeA, old.ID)
	if err != nil || stored.Status != old.Status || stored.Revision != old.Revision {
		t.Fatal("partial retirement", err)
	}
	if len(pgCollection(t, p, colMemories)) != 1 {
		t.Fatal("partial activation")
	}
	f.svc.Repo = p
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := f.svc.ReviewCandidate(testCtx, "reviewer", c.ID, in); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	stored, err = p.Get(testCtx, colMemories, scopeA, old.ID)
	if err != nil || stored.Status != "superseded" || stored.SupersededBy == nil {
		t.Fatal("replacement missing", err)
	}
	m, err := p.Get(testCtx, colMemories, scopeA, c.ID)
	if err != nil || m.Status != "active" || len(m.Supersedes) != 1 {
		t.Fatal("new memory missing", err)
	}
	job, err := p.Get(testCtx, "jobs", scopeA, memoryEvaluationID(scopeA, Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}))
	if err != nil || job.Status != JobStatusPending {
		t.Fatal("evaluation missing", err)
	}
	count := 0
	for _, row := range pgCollection(t, p, "jobs") {
		if row.Kind == "memory_review_receipt" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate receipt", count)
	}
}
