package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMemoryEvaluationErasure(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) { testMemoryEvaluationErasure(t, nil, legacy) })
	}
}
func testMemoryEvaluationErasure(t *testing.T, repo Repository, legacy bool) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "independent evaluator source"}))
	m := Record{ID: "rated-memory", Scope: scopeA, Revision: 1, Status: "active", Sources: refs(src), Proposal: &Ref{Collection: "memory_candidates", ID: "rated-memory", Revision: 1}}
	if e := repo.Insert(testCtx, "memories", m); e != nil {
		t.Fatal(e)
	}
	if e := repo.Insert(testCtx, "memory_candidates", Record{ID: m.ID, Scope: scopeA, Revision: 1, Sources: refs(src)}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 102; i++ {
		col := "memories"
		if i%2 == 1 {
			col = "memory_candidates"
		}
		r := Record{ID: fmt.Sprintf("rating-%03d", i), Scope: scopeA, Revision: 1, Status: "expired", Text: "private old evaluation", Sources: refs(other), Target: &Ref{Collection: col, ID: m.ID, Revision: int64(i + 1)}}
		if e := repo.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
	}
	foreign := []Scope{{WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}}
	for i, scope := range foreign {
		if e := repo.Insert(testCtx, "evaluations", Record{ID: fmt.Sprintf("foreign-%d", i), Scope: scope, Revision: 1, Target: &Ref{Collection: "memories", ID: m.ID, Revision: 1}}); e != nil {
			t.Fatal(e)
		}
	}
	if e := repo.Insert(testCtx, "evaluations", Record{ID: "unrelated", Scope: scopeA, Revision: 1, Target: &Ref{Collection: "agent_runs", ID: m.ID, Revision: 1}}); e != nil {
		t.Fatal(e)
	}
	if e := repo.Insert(testCtx, "memory_relations", Record{ID: "relation", Scope: scopeA, Revision: 1, Sources: refs(other), Relations: []Ref{{Collection: "memories", ID: m.ID, Revision: 1}}}); e != nil {
		t.Fatal(e)
	}
	if e := f.svc.Delete(testCtx, f.human, "memories", m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1}); e != nil {
		t.Fatal(e)
	}
	if legacy {
		job, e := repo.Get(testCtx, "jobs", scopeA, deletionJobID("memories", scopeA, m.ID))
		if e != nil {
			t.Fatal(e)
		}
		job.Kind = memoryRelationErasureKind
		prev := job.Revision
		job.Revision++
		if e = repo.Replace(testCtx, "jobs", job, prev); e != nil {
			t.Fatal(e)
		}
	}
	token := erasureWorker(f)
	if !legacy {
		f.svc.Auth = strictEvaluationErasureAuth{f.auth}
		job, e := repo.ClaimJob(testCtx, time.Now(), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		f.svc.Auth = reviewDenyAuth{Authorizer: f.svc.Auth, denyID: "rating-000"}
		if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrDenied) {
			t.Fatal("evaluation policy", e)
		}
		f.svc.Auth = strictEvaluationErasureAuth{f.auth}
		f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "erase:memory_records_batch"}
		if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrUnavailable) {
			t.Fatal("evaluation audit", e)
		}
		saved, e := repo.Get(testCtx, "jobs", scopeA, job.ID)
		if e != nil || saved.SourceMemoryErasure.DeletedRecords["evaluations"] != 0 {
			t.Fatal("rollback count", saved, e)
		}
		if _, e = repo.Get(testCtx, "evaluations", scopeA, "rating-000"); e != nil {
			t.Fatal("rollback body", e)
		}
		f.svc.Repo = repo
		if e = f.svc.eraseSourceMemories(testCtx, token, job); e != nil {
			t.Fatal(e)
		}
		saved, e = repo.Get(testCtx, "jobs", scopeA, job.ID)
		if e != nil || saved.SourceMemoryErasure.DeletedRecords["evaluations"] != 100 || saved.SourceMemoryErasure.RelationCursor != "" {
			t.Fatal("first evaluation batch", saved, e)
		}
	}

	for i := 0; i < 4; i++ {
		status, e := f.svc.deletionStatus(testCtx, f.human, "memories", scopeA, m.ID)
		if e != nil {
			t.Fatal(e)
		}
		if status.Status == JobStatusDone {
			break
		}
		if e = f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
	}
	status, e := f.svc.deletionStatus(testCtx, f.human, "memories", scopeA, m.ID)
	wantCount := int64(102)
	wantCoverage := "memory_relations_evaluations"
	if legacy {
		wantCount = 0
		wantCoverage = "memory_relations"
	}
	if status.Coverage != wantCoverage || e != nil || status.Status != JobStatusDone || status.DeletedRecords["evaluations"] != wantCount || status.DeletedRecords["memory_relations"] != 1 || status.FullErasureVerified {
		t.Fatal("evaluation erasure incomplete", status, e)
	}
	for i := 0; i < 102; i++ {
		if _, e = repo.Get(testCtx, "evaluations", scopeA, fmt.Sprintf("rating-%03d", i)); (!legacy && !errors.Is(e, ErrNotFound)) || (legacy && e != nil) {
			t.Fatal("rating survival mismatch", i, e)
		}
	}
	for i, scope := range foreign {
		if _, e = repo.Get(testCtx, "evaluations", scope, fmt.Sprintf("foreign-%d", i)); e != nil {
			t.Fatal("foreign rating lost", e)
		}
	}
	if _, e = repo.Get(testCtx, "evaluations", scopeA, "unrelated"); e != nil {
		t.Fatal("unrelated target lost", e)
	}
	for _, s := range []Record{src, other} {
		if _, e = repo.Get(testCtx, "sources", scopeA, s.ID); e != nil {
			t.Fatal("source lost", e)
		}
	}
}

type strictEvaluationErasureAuth struct{ Authorizer }

func (a strictEvaluationErasureAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if strings.HasPrefix(in.Action, "erase:") && in.Action != "erase:memory_records" && in.Action != "erase:evaluations" && in.Action != "erase:memory_relations" {
		return Decision{}, nil
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
