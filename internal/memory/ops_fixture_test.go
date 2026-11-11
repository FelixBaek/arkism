package memory

import (
	"testing"
	"time"
)

func newOpsFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.svc.Repo = opsFakeRepo{f.repo}
	f.auth.actors[f.human].verbs["ops"] = true
	return f
}

func (f *fixture) put(col string, r Record) Record {
	f.t.Helper()
	if r.Scope == (Scope{}) {
		r.Scope = scopeA
	}
	if r.Revision == 0 {
		r.Revision = 1
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	r.UpdatedAt = r.CreatedAt
	if err := f.repo.Insert(testCtx, col, r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) job(id string) Record {
	f.t.Helper()
	r, err := f.repo.Get(testCtx, colJobs, scopeA, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}
