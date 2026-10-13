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

func TestMemoryRelationErasure(t *testing.T) { testMemoryRelationErasure(t, nil) }
func testMemoryRelationErasure(t *testing.T, repo Repository) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	control := Record{ID: "a-control", Scope: scopeA, Kind: "conflicts_with", Status: "active", Revision: 1, Sources: refs(src), Text: "remove conflict explanation", Conflict: &ConflictDetails{Hold: "both"}, Relations: []Ref{{Collection: "memories", ID: "deleted", Revision: 1}, {Collection: "memories", ID: "survivor", Revision: 1}}}
	for _, id := range []string{"deleted", "survivor"} {
		r := Record{ID: id, Scope: scopeA, Status: "conflicted", ConflictID: control.ID, Revision: 1, ApprovedBy: "human-1", Sources: refs(src)}
		if id == "deleted" {
			r.Proposal = &Ref{Collection: "memory_candidates", ID: id, Revision: 1}
		}
		if e := repo.Insert(testCtx, "memories", r); e != nil {
			t.Fatal(e)
		}
	}
	if e := repo.Insert(testCtx, "memory_candidates", Record{ID: "deleted", Scope: scopeA, Revision: 1, Status: "approved", Sources: refs(src)}); e != nil {
		t.Fatal(e)
	}
	if e := repo.Insert(testCtx, "memory_relations", control); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 102; i++ {
		col := "memories"
		if i%2 == 1 {
			col = "memory_candidates"
		}
		r := Record{ID: fmt.Sprintf("relation-%03d", i), Scope: scopeA, Revision: 1, Status: "expired", Sources: refs(src), Text: "remove relation body", Relations: []Ref{{Collection: col, ID: "deleted", Revision: 1}}}
		if e := repo.Insert(testCtx, "memory_relations", r); e != nil {
			t.Fatal(e)
		}
	}
	scopes := []Scope{{WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}}
	for i, scope := range scopes {
		r := control
		r.ID = fmt.Sprintf("foreign-%d", i)
		r.Scope = scope
		if e := repo.Insert(testCtx, "memory_relations", r); e != nil {
			t.Fatal(e)
		}
	}
	body, _ := json.Marshal(ChangeInput{Scope: scopeA, ExpectedRevision: 1})
	req := httptest.NewRequest("DELETE", "/v1/memories/deleted", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"coverage":"memory_relations_evaluations"`) {
		t.Fatal("delete HTTP", w.Code, w.Body.String())
	}
	token := erasureWorker(f)
	f.svc.Auth = strictRelationErasureAuth{f.svc.Auth}
	job, e := repo.ClaimJob(testCtx, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "erase:memory_records_batch"}
	if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrUnavailable) {
		t.Fatal("audit guard", e)
	}
	kept, e := repo.Get(testCtx, "memory_relations", scopeA, control.ID)
	if e != nil || kept.ContentErased || kept.Text != control.Text {
		t.Fatal("lost body on rollback", kept, e)
	}
	saved, e := repo.Get(testCtx, "jobs", scopeA, job.ID)
	if e != nil || saved.SourceMemoryErasure.RelationCursor != "" || saved.SourceMemoryErasure.RedactedConflicts != 0 {
		t.Fatal("advanced cursor on rollback", saved, e)
	}
	f.svc.Repo = repo
	if e = f.svc.eraseSourceMemories(testCtx, token, job); e != nil {
		t.Fatal(e)
	}
	saved, e = repo.Get(testCtx, "jobs", scopeA, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if saved.SourceMemoryErasure.RelationCursor == "" || saved.SourceMemoryErasure.DeletedRecords["memory_relations"] != 99 || saved.SourceMemoryErasure.RedactedConflicts != 1 {
		t.Fatal("first batch", saved.SourceMemoryErasure)
	}
	cursor := saved.SourceMemoryErasure.RelationCursor
	saved.Status = JobStatusFailed
	saved.Attempts = 5
	old := saved.Revision
	saved.Revision++
	if e = repo.Replace(testCtx, "jobs", saved, old); e != nil {
		t.Fatal(e)
	}
	saved, e = repo.Get(testCtx, "jobs", scopeA, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	f.auth.actors["retry-human"] = actor("reviewer", "human", false, "retry")
	body, _ = json.Marshal(RetrySourceDeletionInput{Scope: scopeA, ExpectedRevision: saved.Revision})
	req = httptest.NewRequest("POST", "/v1/memories/deleted/deletion/retry", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer retry-human")
	w = httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("retry HTTP", w.Code, w.Body.String())
	}
	saved, e = repo.Get(testCtx, "jobs", scopeA, job.ID)
	if e != nil || saved.SourceMemoryErasure.RelationCursor != cursor {
		t.Fatal("retry lost cursor", e)
	}
	for i := 0; i < 2; i++ {
		if e = f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
	}
	req = httptest.NewRequest("GET", "/v1/memories/deleted/deletion?workspace_id=ws-a&project_id=proj-a", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w = httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var status SourceErasureStatus
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Status != JobStatusDone || status.DeletedRecords["memory_relations"] != 102 || status.RedactedConflicts != 1 || status.FullErasureVerified || strings.Contains(w.Body.String(), cursor) {
		t.Fatal("status", w.Code, w.Body.String())
	}
	kept, e = f.svc.Get(testCtx, f.human, "memory_relations", scopeA, control.ID)
	if e != nil || !kept.ContentErased || kept.Text != "" {
		t.Fatal("control", kept, e)
	}
	survivor, e := repo.Get(testCtx, "memories", scopeA, "survivor")
	if e != nil || survivor.Status != "conflicted" || survivor.Revision != 1 {
		t.Fatal("survivor changed", survivor, e)
	}
	for i, scope := range scopes {
		if _, e = repo.Get(testCtx, "memory_relations", scope, fmt.Sprintf("foreign-%d", i)); e != nil {
			t.Fatal("foreign removed", e)
		}
	}
	if _, e = repo.Get(testCtx, "sources", scopeA, src.ID); e != nil {
		t.Fatal("source removed", e)
	}
	if e = f.svc.Delete(testCtx, f.human, "memories", survivor.ID, ChangeInput{Scope: scopeA, ExpectedRevision: survivor.Revision}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = repo.Get(testCtx, "memory_relations", scopeA, control.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("orphan control survived", e)
	}
	if len(f.emb.calls) != 0 {
		t.Fatal("erasure embedded")
	}
}

// Policy failures and replaced roots must not advance the cursor or erase rows.
func TestMemoryRelationErasureGuards(t *testing.T) {
	for _, mode := range []string{"denied", "principal", "restored", "audit_delete"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			member := Record{ID: "member", Scope: scopeA, Revision: 1, Sources: refs(src)}
			if e := f.repo.Insert(testCtx, "memories", member); e != nil {
				t.Fatal(e)
			}
			rel := Record{ID: "relation", Scope: scopeA, Revision: 1, Relations: []Ref{{Collection: "memories", ID: member.ID, Revision: 1}}, Sources: refs(src)}
			if e := f.repo.Insert(testCtx, "memory_relations", rel); e != nil {
				t.Fatal(e)
			}
			if mode == "audit_delete" {
				f.repo.failAudit = true
			}
			e := f.svc.Delete(testCtx, f.human, "memories", member.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1})
			if mode == "audit_delete" {
				if !errors.Is(e, ErrUnavailable) {
					t.Fatal(e)
				}
				if _, e = f.repo.Get(testCtx, "memories", scopeA, member.ID); e != nil {
					t.Fatal(e)
				}
				if _, e = f.repo.Get(testCtx, "jobs", scopeA, deletionJobID("memories", scopeA, member.ID)); !errors.Is(e, ErrNotFound) {
					t.Fatal("job escaped rollback", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			job, e := f.repo.ClaimJob(testCtx, time.Now(), time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			token := erasureWorker(f)
			want := ErrDenied
			switch mode {
			case "denied":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: rel.ID}
			case "principal":
				f.svc.Auth = relationPrincipalAuth{Authorizer: f.auth, id: rel.ID}
			case "restored":
				if e = f.repo.Insert(testCtx, "memories", member); e != nil {
					t.Fatal(e)
				}
				want = ErrConflict
			}
			if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, want) {
				t.Fatal(e, want)
			}
			if _, e = f.repo.Get(testCtx, "memory_relations", scopeA, rel.ID); e != nil {
				t.Fatal("relation removed", e)
			}
			saved, e := f.repo.Get(testCtx, "jobs", scopeA, job.ID)
			if e != nil || saved.SourceMemoryErasure.RelationCursor != "" {
				t.Fatal("cursor advanced", e)
			}
		})
	}
}

type relationPrincipalAuth struct {
	Authorizer
	id string
}

func (a relationPrincipalAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if in.ResourceID == a.id && in.Action == "erase:memory_relations" {
		d.PrincipalID = "other"
	}
	return d, e
}

func TestMemoryRelationErasureDoesNotInferProposal(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	if e := f.repo.Insert(testCtx, "memories", Record{ID: "legacy", Scope: scopeA, Revision: 1, Sources: refs(src)}); e != nil {
		t.Fatal(e)
	}
	for _, col := range []string{"memories", "memory_candidates"} {
		r := Record{ID: col, Scope: scopeA, Revision: 1, Relations: []Ref{{Collection: col, ID: "legacy", Revision: 1}}, Sources: refs(src)}
		if e := f.repo.Insert(testCtx, "memory_relations", r); e != nil {
			t.Fatal(e)
		}
		r.Target = &Ref{Collection: col, ID: "legacy", Revision: 1}
		if e := f.repo.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.svc.Delete(testCtx, f.human, "memories", "legacy", ChangeInput{Scope: scopeA, ExpectedRevision: 1}); e != nil {
		t.Fatal(e)
	}
	token := erasureWorker(f)
	for i := 0; i < 3; i++ {
		if e := f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := f.repo.Get(testCtx, "memory_relations", scopeA, "memories"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e := f.repo.Get(testCtx, "memory_relations", scopeA, "memory_candidates"); e != nil {
		t.Fatal("unproven candidate relationship deleted", e)
	}
	if _, e := f.repo.Get(testCtx, "evaluations", scopeA, "memories"); !errors.Is(e, ErrNotFound) {
		t.Fatal("memory evaluation retained", e)
	}
	if _, e := f.repo.Get(testCtx, "evaluations", scopeA, "memory_candidates"); e != nil {
		t.Fatal("unproven candidate evaluation deleted", e)
	}
}

func TestMemoryErasureBlocksLegacyReapproval(t *testing.T) {
	testMemoryErasureBlocksLegacyReapproval(t, nil)
}
func testMemoryErasureBlocksLegacyReapproval(t *testing.T, repo Repository) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "legacy unlinked proposal", Sources: refs(src)}))
	if e := repo.Insert(testCtx, "memories", Record{ID: c.ID, Scope: scopeA, Revision: 1, Status: "active", ApprovedBy: "human-1", Sources: refs(src)}); e != nil {
		t.Fatal(e)
	}
	if e := f.svc.Delete(testCtx, f.human, "memories", c.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1}); e != nil {
		t.Fatal(e)
	}
	_, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if !errors.Is(e, ErrConflict) {
		t.Fatal("deleted memory recreated", e)
	}
	if _, e = repo.Get(testCtx, "memories", scopeA, c.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("memory resurrected", e)
	}
	kept, e := repo.Get(testCtx, "memory_candidates", scopeA, c.ID)
	if e != nil || kept.Status != c.Status || kept.Revision != c.Revision {
		t.Fatal("proposal changed", kept, e)
	}
}
