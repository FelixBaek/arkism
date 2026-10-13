package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func expiryCandidate(t *testing.T, f *fixture, at time.Time) Record {
	t.Helper()
	r := Record{ID: ID(), Scope: scopeA, Kind: "decision", Status: "candidate", Revision: 1, CreatedAt: at, UpdatedAt: at, CreatedBy: "human", Text: "candidate body"}
	if e := f.repo.Insert(testCtx, "memory_candidates", r); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestCandidateExpirySelectionAndIdempotence(t *testing.T) {
	f := newFixture(t)
	before := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Millisecond)
	first := expiryCandidate(t, f, before.Add(-time.Hour))
	second := expiryCandidate(t, f, before)
	excluded := []Record{}
	for _, kind := range []string{"recent", "held", "conflicted", "approved", "other_scope"} {
		r := expiryCandidate(t, f, before.Add(-2*time.Hour))
		switch kind {
		case "recent":
			r.UpdatedAt = time.Now()
		case "held":
			r.ConflictID = ID()
		case "other_scope":
			delete(f.repo.store["memory_candidates"], recKey(scopeA, r.ID))
			r.Scope = scopeB
		default:
			r.Status = kind
		}
		f.repo.store["memory_candidates"][recKey(r.Scope, r.ID)] = r
		excluded = append(excluded, r)
	}
	for _, want := range []string{first.ID, second.ID, ""} {
		out, e := f.svc.ExpireCandidates(testCtx, f.human, ExpireCandidatesInput{Scope: scopeA, Before: before, Limit: 1})
		if e != nil {
			t.Fatal(e)
		}
		if want == "" {
			if len(out.Expired) != 0 {
				t.Fatal(out)
			}
			continue
		}
		if len(out.Expired) != 1 || out.Expired[0].ID != want || out.Expired[0].Revision != 2 {
			t.Fatal(out)
		}
		row, e := f.repo.Get(testCtx, "memory_candidates", scopeA, want)
		if e != nil || row.Status != "expired" || row.Text != "candidate body" {
			t.Fatal(row, e)
		}
	}
	for _, r := range excluded {
		got, e := f.repo.Get(testCtx, "memory_candidates", r.Scope, r.ID)
		if e != nil || got.Status != r.Status || got.Revision != 1 {
			t.Fatal(got, e)
		}
	}
	if len(f.repo.rows("memories")) != 0 || len(f.repo.rows("jobs")) != 0 {
		t.Fatal("expiry created memory/job")
	}
}
func TestCandidateExpiryGuardsAndRollback(t *testing.T) {
	f := newFixture(t)
	before := time.Now().Add(-time.Hour)
	r := expiryCandidate(t, f, before.Add(-time.Hour))
	expiryCandidate(t, f, before.Add(-time.Hour))
	in := ExpireCandidatesInput{Scope: scopeA, Before: before}
	if _, e := f.svc.ExpireCandidates(testCtx, f.agent, in); !errors.Is(e, ErrDenied) {
		t.Fatal("agent expiry", e)
	}
	f.repo.failAudit = true
	out, e := f.svc.ExpireCandidates(testCtx, f.human, in)
	if !errors.Is(e, ErrUnavailable) || len(out.Expired) != 0 {
		t.Fatal(out, e)
	}
	row, _ := f.repo.Get(testCtx, "memory_candidates", scopeA, r.ID)
	if row.Status != "candidate" || row.Revision != 1 {
		t.Fatal("partial expiry")
	}
	f.repo.failAudit = false
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: r.ID}
	if _, e = f.svc.ExpireCandidates(testCtx, f.human, in); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	f.svc.Auth = f.auth
	for _, bad := range []ExpireCandidatesInput{{Scope: scopeA}, {Scope: scopeA, Before: time.Now().Add(time.Hour)}, {Scope: scopeA, Before: before, Limit: 101}} {
		if _, e = f.svc.ExpireCandidates(testCtx, f.human, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	body := `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"before":"` + before.UTC().Format(time.RFC3339Nano) + `","limit":1}`
	req := httptest.NewRequest("POST", "/v1/memory_candidates/expire", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

type expiryChangesRepo struct {
	Repository
	repo *fakeRepo
}

func (r expiryChangesRepo) ExpiringCandidates(ctx context.Context, scope Scope, before time.Time, limit int) ([]Record, error) {
	rows, e := r.Repository.ExpiringCandidates(ctx, scope, before, limit)
	if len(rows) > 0 {
		row := rows[0]
		row.Revision++
		row.ConflictID = ID()
		r.repo.mu.Lock()
		r.repo.store["memory_candidates"][recKey(scope, row.ID)] = row
		r.repo.mu.Unlock()
	}
	return rows, e
}
func TestCandidateExpiryConcurrentReview(t *testing.T) {
	f := newFixture(t)
	before := time.Now().Add(-time.Hour)
	expiryCandidate(t, f, before.Add(-time.Hour))
	f.svc.Repo = expiryChangesRepo{Repository: f.repo, repo: f.repo}
	out, e := f.svc.ExpireCandidates(testCtx, f.human, ExpireCandidatesInput{Scope: scopeA, Before: before})
	if !errors.Is(e, ErrConflict) || len(out.Expired) != 0 {
		t.Fatal(out, e)
	}
}
