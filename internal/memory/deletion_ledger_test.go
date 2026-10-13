package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeletionLedger(t *testing.T) { testDeletionLedger(t, nil) }
func testDeletionLedger(t *testing.T, repo Repository) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	f.auth.actors[f.human].verbs["export"] = true
	src := f.source()
	for _, col := range []string{"memories", "memory_candidates"} {
		if e := repo.Insert(testCtx, col, Record{ID: col, Scope: scopeA, Revision: 1, Text: "private ledger sentinel", Sources: refs(src)}); e != nil {
			t.Fatal(e)
		}
		if e := f.svc.Delete(testCtx, f.human, col, col, ChangeInput{Scope: scopeA, ExpectedRevision: 1}); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	scopes := []Scope{scopeA, scopeA, scopeA, scopeA}
	scopes[0].WorkspaceID = "other"
	scopes[1].ProjectID = "other"
	scopes[2].UserID = "other"
	scopes[3].AgentID = "other"
	for _, scope := range scopes {
		r := Record{ID: sourceDeletionID(scope, "foreign"), Scope: scope, Kind: "source_deletion", Status: "done", Revision: 1, CreatedAt: time.Now().Add(-time.Second), Target: &Ref{Collection: "sources", ID: "foreign", Revision: 1}}
		if e := repo.Insert(testCtx, "jobs", r); e != nil {
			t.Fatal(e)
		}
	}
	ledger, e := f.svc.ExportDeletions(testCtx, f.human, scopeA)
	if e != nil || ledger.Version != 1 || len(ledger.Targets) != 3 || len(ledger.Scope) != 4 {
		t.Fatalf("ledger: %+v %v", ledger, e)
	}
	raw, _ := json.Marshal(ledger)
	for _, word := range []string{"sentinel", "foreign", "revision", "job_id", "text"} {
		if strings.Contains(string(raw), word) {
			t.Fatalf("unexpected payload %s", word)
		}
	}
	req := httptest.NewRequest("POST", "/v1/deletions/export", strings.NewReader(`{"scope":{"workspace_id":"`+scopeA.WorkspaceID+`","project_id":"`+scopeA.ProjectID+`","user_id":"`+scopeA.UserID+`","agent_id":"`+scopeA.AgentID+`"}}`))
	req.Header.Set("Authorization", "Bearer "+f.human)
	response := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("HTTP export %d %s", response.Code, response.Body.String())
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: src.ID}
	if out, e := f.svc.ExportDeletions(testCtx, f.human, scopeA); !errors.Is(e, ErrDenied) || len(out.Targets) != 0 {
		t.Fatalf("partial denied export: %+v %v", out, e)
	}
	f.svc.Auth = f.auth
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "export:deletions"}
	if out, e := f.svc.ExportDeletions(testCtx, f.human, scopeA); !errors.Is(e, ErrUnavailable) || len(out.Targets) != 0 {
		t.Fatalf("partial unaudited export: %+v %v", out, e)
	}
	f.svc.Repo = repo
	f.auth.actors[f.agent].verbs["export"] = true
	if _, e := f.svc.ExportDeletions(testCtx, f.agent, scopeA); !errors.Is(e, ErrDenied) {
		t.Fatalf("agent export: %v", e)
	}
	// A body revived beside a receipt must block export, even before worker runs.
	if e := repo.Insert(testCtx, "sources", Record{ID: src.ID, Scope: scopeA, Revision: 1}); e != nil {
		t.Fatal(e)
	}
	if out, e := f.svc.ExportDeletions(testCtx, f.human, scopeA); !errors.Is(e, ErrConflict) || len(out.Targets) != 0 {
		t.Fatalf("inconsistent export: %+v %v", out, e)
	}
}

func TestDeletionLedgerEmptyAndOverflow(t *testing.T) {
	f := newFixture(t)
	f.auth.actors[f.human].verbs["export"] = true
	out, e := f.svc.ExportDeletions(testCtx, f.human, scopeA)
	if e != nil || out.Targets == nil || len(out.Targets) != 0 {
		t.Fatalf("empty ledger %+v %v", out, e)
	}
	for i := 0; i <= deletionLedgerLimit; i++ {
		id := fmt.Sprint(i)
		if e := f.repo.Insert(testCtx, "jobs", Record{ID: sourceDeletionID(scopeA, id), Scope: scopeA, Kind: "source_deletion", Status: "done", Revision: 1, CreatedAt: time.Now().Add(-time.Second), Target: &Ref{Collection: "sources", ID: id, Revision: 1}}); e != nil {
			t.Fatal(e)
		}
	}
	if out, e = f.svc.ExportDeletions(testCtx, f.human, scopeA); !errors.Is(e, ErrUnavailable) || len(out.Targets) != 0 {
		t.Fatalf("truncated ledger %+v %v", out, e)
	}
}

func TestDeletionLedgerRejectsMalformedReceipt(t *testing.T) {
	for _, mode := range []string{"target", "id", "status", "time"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.auth.actors[f.human].verbs["export"] = true
			r := Record{ID: sourceDeletionID(scopeA, "deleted"), Scope: scopeA, Kind: "source_deletion", Status: "done", Revision: 1, CreatedAt: time.Now().Add(-time.Second), Target: &Ref{Collection: "sources", ID: "deleted", Revision: 1}}
			switch mode {
			case "target":
				r.Target.Collection = "memories"
			case "id":
				r.ID = "wrong"
			case "status":
				r.Status = "pending"
			case "time":
				r.CreatedAt = time.Now().Add(time.Hour)
			}
			if e := f.repo.Insert(testCtx, "jobs", r); e != nil {
				t.Fatal(e)
			}
			if out, e := f.svc.ExportDeletions(testCtx, f.human, scopeA); !errors.Is(e, ErrUnavailable) || len(out.Targets) != 0 {
				t.Fatalf("malformed export %+v %v", out, e)
			}
		})
	}
}
