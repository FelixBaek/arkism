package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCandidateErasure(t *testing.T) {
	for _, source := range []bool{false, true} {
		t.Run(fmt.Sprintf("source=%t", source), func(t *testing.T) { testCandidateErasure(t, nil, source) })
	}
}
func testCandidateErasure(t *testing.T, repo Repository, source bool) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "independent retained source"}))
	c := Record{ID: "candidate-only", Scope: scopeA, Revision: 1, Status: "candidate", Sources: refs(src)}
	if e := repo.Insert(testCtx, "memory_candidates", c); e != nil {
		t.Fatal(e)
	}
	m := c
	m.Status = "active"
	m.Sources = refs(other)
	if e := repo.Insert(testCtx, "memories", m); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 102; i++ {
		r := Record{ID: fmt.Sprintf("candidate-eval-%03d", i), Scope: scopeA, Revision: 1, Sources: refs(other), Target: &Ref{Collection: "memory_candidates", ID: c.ID, Revision: int64(i + 1)}}
		if e := repo.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
	}
	keep := Record{ID: "keep-memory-eval", Scope: scopeA, Revision: 1, Sources: refs(other), Target: &Ref{Collection: "memories", ID: c.ID, Revision: 1}}
	if e := repo.Insert(testCtx, "evaluations", keep); e != nil {
		t.Fatal(e)
	}
	rel := Record{ID: "candidate-relation", Scope: scopeA, Revision: 1, Kind: "supports", Sources: refs(other), Relations: []Ref{{Collection: "memory_candidates", ID: c.ID, Revision: 1}}}
	if e := repo.Insert(testCtx, "memory_relations", rel); e != nil {
		t.Fatal(e)
	}
	foreignScopes := []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}}
	for i, scope := range foreignScopes {
		r := keep
		r.ID = fmt.Sprintf("foreign-candidate-%d", i)
		r.Scope = scope
		r.Target = &Ref{Collection: "memory_candidates", ID: c.ID, Revision: 1}
		if e := repo.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
	}
	token := erasureWorker(f)
	if source {
		if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
			t.Fatal(e)
		}
		job, e := repo.ClaimJob(testCtx, time.Now(), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "erase:source_memory_dependents_batch"}
		if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrUnavailable) {
			t.Fatal("source audit rollback", e)
		}
		if _, e = repo.Get(testCtx, "memory_candidates", scopeA, c.ID); e != nil {
			t.Fatal("failed source batch deleted candidate", e)
		}
		if _, e = repo.Get(testCtx, "jobs", scopeA, deletionJobID("memory_candidates", scopeA, c.ID)); !errors.Is(e, ErrNotFound) {
			t.Fatal("failed batch queued child", e)
		}
		f.svc.Repo = repo
		if e = f.svc.eraseSourceMemories(testCtx, token, job); e != nil {
			t.Fatal(e)
		}
	} else {
		f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "delete:memory_candidates"}
		if e := f.svc.Delete(testCtx, f.human, "memory_candidates", c.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1}); !errors.Is(e, ErrUnavailable) {
			t.Fatal("delete audit rollback", e)
		}
		if _, e := repo.Get(testCtx, "memory_candidates", scopeA, c.ID); e != nil {
			t.Fatal("failed delete lost candidate", e)
		}
		if _, e := repo.Get(testCtx, "jobs", scopeA, deletionJobID("memory_candidates", scopeA, c.ID)); !errors.Is(e, ErrNotFound) {
			t.Fatal("failed delete left job", e)
		}
		f.svc.Repo = repo
		body, _ := json.Marshal(ChangeInput{Scope: scopeA, ExpectedRevision: 1})
		req := httptest.NewRequest("DELETE", "/v1/memory_candidates/"+c.ID, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 202 || !strings.Contains(w.Body.String(), "candidate_relations_evaluations") {
			t.Fatal("candidate DELETE", w.Code, w.Body.String())
		}
		job, e := repo.Get(testCtx, "jobs", scopeA, deletionJobID("memory_candidates", scopeA, c.ID))
		if e != nil {
			t.Fatal(e)
		}
		prev := job.Revision
		job.Revision++
		job.Status = JobStatusFailed
		if e = repo.Replace(testCtx, "jobs", job, prev); e != nil {
			t.Fatal(e)
		}
		body, _ = json.Marshal(RetrySourceDeletionInput{Scope: scopeA, ExpectedRevision: job.Revision})
		f.auth.actors["retry-human"] = actor("retry-reviewer", "human", false, "retry")
		req = httptest.NewRequest("POST", "/v1/memory_candidates/"+c.ID+"/deletion/retry", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer retry-human")
		w = httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal("candidate retry", w.Code, w.Body.String())
		}

	}
	for i := 0; i < 10; i++ {
		e := f.svc.WorkOnce(testCtx, token)
		if errors.Is(e, ErrNotFound) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 102; i++ {
		if _, e := repo.Get(testCtx, "evaluations", scopeA, fmt.Sprintf("candidate-eval-%03d", i)); !errors.Is(e, ErrNotFound) {
			t.Fatal("candidate evaluation remains", i, e)
		}
	}
	if _, e := repo.Get(testCtx, "memory_relations", scopeA, rel.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("candidate relation remains", e)
	}
	if _, e := repo.Get(testCtx, "memories", scopeA, m.ID); e != nil {
		t.Fatal("same ID memory lost", e)
	}
	if _, e := repo.Get(testCtx, "evaluations", scopeA, keep.ID); e != nil {
		t.Fatal("memory evaluation lost", e)
	}
	for i, scope := range foreignScopes {
		if _, e := repo.Get(testCtx, "evaluations", scope, fmt.Sprintf("foreign-candidate-%d", i)); e != nil {
			t.Fatal("foreign candidate evaluation lost", e)
		}
	}
	status, e := f.svc.deletionStatus(testCtx, f.human, "memory_candidates", scopeA, c.ID)
	if e != nil || status.Status != JobStatusDone || status.Coverage != "candidate_relations_evaluations" || status.DeletedRecords["evaluations"] != 102 || status.DeletedRecords["memory_relations"] != 1 || status.FullErasureVerified {
		t.Fatal("candidate progress", status, e)
	}
	req := httptest.NewRequest("GET", "/v1/memory_candidates/"+c.ID+"/deletion?workspace_id=ws-a&project_id=proj-a", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("candidate status", w.Code, w.Body.String())
	}
	if source {
		root, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
		if e != nil || root.Status != JobStatusDone || root.QueuedCandidateJobs != 1 || root.QueuedMemoryJobs != 0 {
			t.Fatal("source counters", root, e)
		}
	}
	c.Sources = refs(other)
	if e = repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return insertSourceRecord(ctx, tx, "memory_candidates", c) }); !errors.Is(e, ErrConflict) {
		t.Fatal("candidate ID resurrected", e)
	}
}
