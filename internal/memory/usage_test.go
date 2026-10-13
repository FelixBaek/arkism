package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestContextUsageAndRecent(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("usage sentinel body", "usage")
	run := f.create(f.human, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "private task output", Sources: m.Sources}))
	for i := 0; i < 12; i++ {
		out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "usage", RunID: run.ID, MaxChars: 5})
		if e != nil || out.ContextID == "" || len(out.Items) != 1 {
			t.Fatal(out, e)
		}
		receipt, e := f.repo.Get(testCtx, "jobs", scopeA, out.ContextID)
		if e != nil || receipt.Kind != "context_usage" || receipt.Status != "done" || len(receipt.Usage.Items) != 1 || receipt.Usage.Items[0].Rank != 1 || receipt.Usage.Items[0].Memory.Revision != m.Revision || receipt.Usage.Run.ID != run.ID {
			t.Fatal(receipt, e)
		}
		raw, _ := json.Marshal(receipt)
		if strings.Contains(string(raw), m.Text) || strings.Contains(string(raw), run.Text) || receipt.Text != "" || len(receipt.Sources) != 0 {
			t.Fatal("content stored", string(raw))
		}
	}
	page, e := f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, m.ID)
	if e != nil || len(page.Items) != 10 || page.Authority != AuthorityDataOnly {
		t.Fatal(page, e)
	}
	for i, item := range page.Items {
		if item.Memory.ID != m.ID || item.Rank != 1 || item.RequestedBy == "" {
			t.Fatal(item)
		}
		if i > 0 && (item.CreatedAt.After(page.Items[i-1].CreatedAt) || item.CreatedAt.Equal(page.Items[i-1].CreatedAt) && item.ContextID > page.Items[i-1].ContextID) {
			t.Fatal("unordered usage")
		}
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: page.Items[0].ContextID}
	if denied, e := f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, m.ID); !errors.Is(e, ErrDenied) || len(denied.Items) != 0 {
		t.Fatal(denied, e)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: run.ID}
	if _, e = f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, m.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("run visibility", e)
	}
	f.svc.Auth = f.auth
	for _, scope := range []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		if _, e = f.svc.RecentMemoryUsage(testCtx, f.human, scope, m.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("scope leak", e)
		}
	}
}

type usageFailureTx struct{ Tx }

func (x usageFailureTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "jobs" && r.Kind == "context_usage" {
		return ErrUnavailable
	}
	return x.Tx.Insert(ctx, col, r)
}

type usageFailureRepo struct{ Repository }

func (r usageFailureRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, usageFailureTx{tx}) })
}
func TestUsageFailureAndEmpty(t *testing.T) {
	f := newFixture(t)
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "no match"})
	if e != nil || out.ContextID == "" || len(out.Items) != 0 {
		t.Fatal(out, e)
	}
	receipt, e := f.repo.Get(testCtx, "jobs", scopeA, out.ContextID)
	if e != nil || receipt.Usage == nil || receipt.Usage.Run != nil || len(receipt.Usage.Items) != 0 {
		t.Fatal(receipt, e)
	}
	before := len(f.repo.rows("jobs"))
	audits := len(f.repo.rows("audit_events"))
	f.repo.failAudit = true
	out, e = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "no match"})
	if !errors.Is(e, ErrUnavailable) || out.ContextID != "" || len(out.Items) != 0 || len(f.repo.rows("jobs")) != before {
		t.Fatal("audit rollback", out, e)
	}
	f.repo.failAudit = false
	f.svc.Repo = usageFailureRepo{f.repo}
	out, e = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "no match"})
	if !errors.Is(e, ErrUnavailable) || out.ContextID != "" || len(f.repo.rows("audit_events")) != audits {
		t.Fatal("receipt failure", out, e)
	}
}

func TestUsageRunAndHTTP(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("http usage")
	if _, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "usage", RunID: ID()}); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "/v1/memories/"+m.ID+"/usage?workspace_id=ws-a&project_id=proj-a", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	f.repo.failAudit = true
	if page, e := f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, m.ID); !errors.Is(e, ErrUnavailable) || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
}

func TestUsageRanksFollowFinalTruncation(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("first usage", "usage")
	f.activeMemory("second usage", "usage")
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "usage", MaxChars: 1})
	if e != nil || len(out.Items) != 1 {
		t.Fatal(out, e)
	}
	r, e := f.repo.Get(testCtx, "jobs", scopeA, out.ContextID)
	if e != nil || len(r.Usage.Items) != 1 || r.Usage.Items[0].Memory.ID != out.Items[0].ID || r.Usage.Items[0].Rank != 1 {
		t.Fatal(r, e)
	}
	if r.CreatedAt.After(time.Now()) {
		t.Fatal("future time")
	}
}

type runChangesRepo struct {
	Repository
	repo *fakeRepo
	run  Record
}

func (r runChangesRepo) SearchCollection(ctx context.Context, col string, in SearchInput, v []float64, space string) ([][]Hit, error) {
	out, e := r.Repository.SearchCollection(ctx, col, in, v, space)
	r.repo.mu.Lock()
	row := r.run
	row.Revision++
	r.repo.store["agent_runs"][recKey(row.Scope, row.ID)] = row
	r.repo.mu.Unlock()
	return out, e
}
func TestUsageRunRevisionFence(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("run fence")
	run := f.create(f.human, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "run", Sources: m.Sources}))
	f.svc.Repo = runChangesRepo{Repository: f.repo, repo: f.repo, run: run}
	before := len(f.repo.rows("jobs"))
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "run", RunID: run.ID})
	if !errors.Is(e, ErrConflict) || out.ContextID != "" || len(out.Items) != 0 || len(f.repo.rows("jobs")) != before {
		t.Fatal("stale run accepted", out, e)
	}
}

type usagePrincipalAuth struct {
	Authorizer
	action string
}

func (a usagePrincipalAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if in.Action == a.action {
		d.PrincipalID = "another"
	}
	return d, e
}
func TestUsagePrincipalGuard(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("principal usage")
	run := f.create(f.human, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "run", Sources: m.Sources}))
	f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "read:agent_runs"}
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "usage", RunID: run.ID})
	if !errors.Is(e, ErrDenied) || out.ContextID != "" {
		t.Fatal(out, e)
	}
	f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "read:usage"}
	page, e := f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, m.ID)
	if !errors.Is(e, ErrDenied) || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
}
