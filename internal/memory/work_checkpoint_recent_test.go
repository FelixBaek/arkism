package memory

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

type recentWorkFake struct {
	*fakeRepo
	beforeRead func()
}

func (r *recentWorkFake) RecentWorkRows(_ context.Context, scope Scope, repository, session string, since time.Time, limit int) ([]Record, error) {
	var rows []Record
	for _, row := range r.rows(colWorkCheckpoints) {
		if !sameScope(row.Scope, scope) || !isWorkCheckpoint(row) {
			continue
		}
		pkg := row.WorkCheckpoint.Package
		if pkg.Repository == repository && pkg.SessionID != "" && pkg.SessionID != session && !row.UpdatedAt.Before(since) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	out := []Record{}
	seen := map[string]bool{}
	for _, row := range rows {
		sid := row.WorkCheckpoint.Package.SessionID
		if !seen[sid] && len(out) < limit {
			out = append(out, row)
			seen[sid] = true
		}
	}
	if r.beforeRead != nil {
		r.beforeRead()
	}
	return out, nil
}
func TestRecentWorkFiltersSessionsAndRevalidates(t *testing.T) {
	f := newFixture(t)
	store := &recentWorkFake{fakeRepo: f.repo}
	f.svc.Repo = store
	pkg := workCheckpointPackage()
	pkg.SessionID = "old"
	for _, id := range []string{"a", "z"} {
		if _, err := workCheckpointSave(f, f.agent, id, id, 0, pkg); err != nil {
			t.Fatal(err)
		}
	}
	pkg.SessionID = "current"
	if _, err := workCheckpointSave(f, f.agent, "current", "current", 0, pkg); err != nil {
		t.Fatal(err)
	}
	pkg.SessionID = "other"
	pkg.Repository = "other"
	if _, err := workCheckpointSave(f, f.agent, "other", "other", 0, pkg); err != nil {
		t.Fatal(err)
	}
	in := RecentWorkInput{Scope: scopeA, Repository: workCheckpointPackage().Repository, SessionID: "current"}
	out, err := f.svc.RecentWorkCheckpoints(testCtx, f.agent, in)
	if err != nil || len(out.Items) != 1 || out.Items[0].ID != "z" || len(out.Items[0].Package.Checks) != 0 || len(out.Items[0].Package.Files) != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	store.beforeRead = func() {
		pkg := workCheckpointPackage()
		pkg.SessionID = "old"
		pkg.Goal = "changed"
		if _, err := workCheckpointSave(f, f.agent, "z", "change", 1, pkg); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := f.svc.RecentWorkCheckpoints(testCtx, f.agent, in); !errors.Is(err, ErrConflict) || len(out.Items) != 0 {
		t.Fatalf("changed revision leaked: %+v %v", out, err)
	}
}
func TestRecentWorkBounds(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = &recentWorkFake{fakeRepo: f.repo}
	for _, in := range []RecentWorkInput{{Scope: scopeA, Repository: "repo"}, {Scope: scopeA, Repository: "repo", SessionID: "s", Limit: 11}, {Scope: scopeA, Repository: "repo", SessionID: "s", MaxAgeDays: 31}} {
		if _, err := f.svc.RecentWorkCheckpoints(testCtx, f.agent, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
}

func TestPostgresRecentWorkSessions(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	for _, entry := range []struct{ id, session, repo string }{{"a", "past", "/repo"}, {"z", "past", "/repo"}, {"b", "other", "/repo"}, {"current", "now", "/repo"}, {"foreign", "foreign", "/other"}, {"empty", "", "/repo"}} {
		pkg := workCheckpointPackage()
		pkg.Repository = entry.repo
		pkg.SessionID = entry.session
		if _, err := workCheckpointSave(f, f.agent, entry.id, entry.id, 0, pkg); err != nil {
			t.Fatal(err)
		}
	}
	out, err := f.svc.RecentWorkCheckpoints(testCtx, f.agent, RecentWorkInput{Scope: scopeA, Repository: "/repo", SessionID: "now", Limit: 3, MaxAgeDays: 7})
	if err != nil || len(out.Items) != 2 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, item := range out.Items {
		if item.ID != "z" && item.ID != "b" {
			t.Fatal(item.ID)
		}
	}
	// The time predicate must be applied by SQL, not just the response renderer.
	rows, err := p.RecentWorkRows(testCtx, scopeA, "/repo", "now", time.Now().Add(time.Hour), 3)
	if err != nil || len(rows) != 0 {
		t.Fatalf("future cutoff: %+v %v", rows, err)
	}
}

func TestRecentSessionsDeletedEvidenceNeverLeaks(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = &recentWorkFake{fakeRepo: f.repo}
	src := f.source()
	pkg := workCheckpointPackage()
	pkg.SessionID = "past"
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	if _, err := workCheckpointSave(f, f.agent, "evidence", "event", 0, pkg); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.RecentWorkCheckpoints(testCtx, f.human, RecentWorkInput{Scope: scopeA, Repository: pkg.Repository, SessionID: "now"})
	if err != nil || len(out.Items) != 0 {
		t.Fatalf("deleted evidence leaked: %+v %v", out, err)
	}
}

// A controlled candidate list checks the service independently of SQL ordering.
type recentCandidateRepo struct {
	*fakeRepo
	candidates []Record
	failID     string
	failure    error
	requested  int
}

func (r *recentCandidateRepo) RecentWorkRows(_ context.Context, _ Scope, _ string, _ string, _ time.Time, limit int) ([]Record, error) {
	r.requested = limit
	return r.candidates, nil
}
func (r *recentCandidateRepo) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == colWorkCheckpoints && id == r.failID {
		return Record{}, r.failure
	}
	return r.fakeRepo.Get(ctx, col, scope, id)
}
func TestRecentSessionsRefillsAfterIneligibleCandidates(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	for _, id := range []string{"deleted", "revoked", "good"} {
		pkg := workCheckpointPackage()
		pkg.SessionID = id
		if id == "deleted" {
			pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
		}
		if _, err := workCheckpointSave(f, f.agent, id, id, 0, pkg); err != nil {
			t.Fatal(err)
		}
	}
	byID := map[string]Record{}
	for _, r := range f.repo.rows(colWorkCheckpoints) {
		byID[r.ID] = r
	}
	repo := &recentCandidateRepo{fakeRepo: f.repo, candidates: []Record{byID["deleted"], byID["revoked"], byID["good"]}, failID: "revoked", failure: ErrDenied}
	f.svc.Repo = repo
	if err := f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision); err != nil {
		t.Fatal(err)
	}
	in := RecentWorkInput{Scope: scopeA, Repository: workCheckpointPackage().Repository, SessionID: "current", Limit: 1}
	out, err := f.svc.RecentWorkCheckpoints(testCtx, f.human, in)
	if err != nil || len(out.Items) != 1 || out.Items[0].ID != "good" || repo.requested != 30 {
		t.Fatalf("refill: %+v %v, requested=%d", out, err, repo.requested)
	}
	// Transport/database failures cannot be mistaken for revoked evidence.
	repo.failure = ErrUnavailable
	if out, err := f.svc.RecentWorkCheckpoints(testCtx, f.human, in); !errors.Is(err, ErrUnavailable) || len(out.Items) != 0 {
		t.Fatalf("database failure hidden: %+v %v", out, err)
	}
	// Validate the entire bounded candidate pool, even after enough good results.
	foreign := byID["good"]
	foreign.Scope = scopeB
	repo.candidates = []Record{byID["good"], foreign}
	if out, err := f.svc.RecentWorkCheckpoints(testCtx, f.human, in); !errors.Is(err, ErrConflict) || len(out.Items) != 0 {
		t.Fatalf("foreign candidate accepted: %+v %v", out, err)
	}
}
