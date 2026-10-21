package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type panicJobRepo struct{ *fakeRepo }

func (p *panicJobRepo) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == colMemories {
		panic("must never appear in worker logs")
	}
	return p.fakeRepo.Get(ctx, col, scope, id)
}

func TestWorkerPanicConsumesAttemptAndReleasesLease(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	j := Record{ID: "panic-job", Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, Target: &Ref{Collection: colMemories, ID: "target", Revision: 1}, CreatedAt: now, UpdatedAt: now}
	if err := f.repo.Insert(testCtx, colJobs, j); err != nil {
		t.Fatal(err)
	}
	f.svc.Repo = &panicJobRepo{f.repo}
	if err := f.svc.WorkOnce(testCtx, f.work); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	got, err := f.repo.Get(testCtx, colJobs, scopeA, j.ID)
	if err != nil || got.Attempts != 1 || got.Status != JobStatusRetry || got.LeaseToken != "" {
		t.Fatalf("panic job not released: %+v %v", got, err)
	}
}

func TestRetryJitterStaysBounded(t *testing.T) {
	for _, attempt := range []int{-1, 1, 3, 16, 1000} {
		delay := min(baseRetryDelay<<(max(1, min(attempt, 16))-1), maxRetryDelay)
		seen := map[time.Duration]bool{}
		for i := 0; i < 100; i++ {
			got := retryDelay(attempt)
			if got < delay/2 || got > delay {
				t.Fatal("retry outside budget", got)
			}
			seen[got] = true
		}
		if len(seen) < 2 {
			t.Fatal("retry waves remain synchronized")
		}
	}
}

func TestPostgresMalformedJobDoesNotStarveQueue(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC()
	for _, id := range []string{"a-corrupt", "b-valid"} {
		j := Record{ID: id, Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := p.Insert(testCtx, colJobs, j); err != nil {
			t.Fatal(err)
		}
	}
	bad := []byte(`{"attempts":"invalid"}`)
	if _, err := p.q().Exec(testCtx, `UPDATE memory_records SET payload=$1 WHERE collection='jobs' AND id='a-corrupt'`, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ClaimJob(testCtx, now, time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	var status string
	var attempts int
	var kept []byte
	if err := p.q().QueryRow(testCtx, `SELECT status,attempts,payload FROM memory_records WHERE collection='jobs' AND id='a-corrupt'`).Scan(&status, &attempts, &kept); err != nil {
		t.Fatal(err)
	}
	if status != JobStatusFailed || attempts != 1 || string(kept) != string(bad) {
		t.Fatal("corrupt evidence lost or not quarantined")
	}
	j, err := p.ClaimJob(testCtx, now, time.Minute)
	if err != nil || j.ID != "b-valid" {
		t.Fatal("poison job blocked queue", err)
	}
	// A worker killed after claiming loses its lease and can be reclaimed.
	reclaimed, err := p.ClaimJob(testCtx, now.Add(2*time.Minute), time.Minute)
	if err != nil || reclaimed.ID != j.ID || reclaimed.Attempts != 2 || reclaimed.LeaseToken == j.LeaseToken {
		t.Fatal("expired lease not recovered", err)
	}
	if err = p.FinishJob(testCtx, j, true); !errors.Is(err, ErrConflict) {
		t.Fatal("stale worker completed reclaimed job", err)
	}
}

func TestPostgresStructurallyEmptyJobIsQuarantined(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC()
	j := Record{ID: "empty-job", Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := p.Insert(testCtx, colJobs, j); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(testCtx, `UPDATE memory_records SET payload=$1 WHERE collection='jobs' AND id='empty-job'`, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ClaimJob(testCtx, now, time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatal("structurally invalid job not rejected", err)
	}
	var status string
	if err := p.q().QueryRow(testCtx, `SELECT status FROM memory_records WHERE collection='jobs' AND id='empty-job'`).Scan(&status); err != nil || status != JobStatusFailed {
		t.Fatal("empty JSON still starves queue", status, err)
	}
}
