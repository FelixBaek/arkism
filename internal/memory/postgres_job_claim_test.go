package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func queueTestJob(t *testing.T, p *Postgres, id, kind, status string, runAt *time.Time, leaseUntil time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := p.Insert(context.Background(), colJobs, Record{ID: id, Scope: scopeA, Kind: kind, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now, RunAt: runAt, LeaseUntil: leaseUntil}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresJobClaimPriorityAndEligibility(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	oldest, old, future := now.Add(-2*time.Hour), now.Add(-time.Hour), now.Add(time.Hour)
	queueTestJob(t, p, "outside", "memory_evaluation", JobStatusPending, &oldest, time.Time{})
	// Lease order is deliberately opposite to run_at priority.
	queueTestJob(t, p, "running-old", webhookDeliveryKind, JobStatusRunning, &oldest, now.Add(-time.Second))
	queueTestJob(t, p, "a-pending", "embedding", JobStatusPending, &old, time.Time{})
	queueTestJob(t, p, "b-retry", captureMergeGateKind, JobStatusRetry, &old, time.Time{})
	queueTestJob(t, p, "c-running", autoDeriveJobKind, JobStatusRunning, &old, now.Add(-time.Hour))
	queueTestJob(t, p, "future-running", deriveScheduleKind, JobStatusRunning, &future, now.Add(-time.Second))
	queueTestJob(t, p, "0-null", "embedding", JobStatusPending, nil, time.Time{})
	queueTestJob(t, p, "future-retry", "embedding", JobStatusRetry, &future, time.Time{})
	queueTestJob(t, p, "busy", "embedding", JobStatusRunning, &oldest, future)
	queueTestJob(t, p, "null-lease", "embedding", JobStatusRunning, &oldest, time.Time{})
	if _, err := p.pool.Exec(ctx, "UPDATE memory_records SET lease_until=NULL WHERE collection='jobs' AND id='null-lease'"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"running-old", "a-pending", "b-retry", "c-running", "future-running", "0-null"} {
		got, err := p.ClaimJob(ctx, now, time.Minute)
		if err != nil || got.ID != want {
			t.Fatalf("want %s, got %s: %v", want, got.ID, err)
		}
		if got.Status != JobStatusRunning || got.Attempts != 1 || got.Revision != 2 || got.LeaseToken == "" || !got.LeaseUntil.Equal(now.Add(time.Minute)) {
			t.Fatalf("invalid lease: %+v", got)
		}
	}
	if _, err := p.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ineligible job claimed: %v", err)
	}
	queueTestJob(t, p, "erasure", erasureJobKinds[0], JobStatusPending, nil, time.Time{})
	queueTestJob(t, p, "normal-older", "embedding", JobStatusPending, &oldest, time.Time{})
	restricted := *p
	restricted.erasureOnly = true
	got, err := restricted.ClaimJob(ctx, now, time.Minute)
	if err != nil || got.ID != "erasure" {
		t.Fatalf("erasure-only claim %s: %v", got.ID, err)
	}
	if _, err = restricted.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("erasure-only escaped: %v", err)
	}
}

func TestPostgresJobClaimSkipsLockedHeadWithoutLockingLosers(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	past := now.Add(-time.Hour)
	for _, id := range []string{"a", "b"} {
		queueTestJob(t, p, id, "embedding", JobStatusPending, &past, time.Time{})
	}
	queueTestJob(t, p, "c", webhookDeliveryKind, JobStatusPending, &past, time.Time{})
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, claimJobSelectSQL(2), []string{"embedding", webhookDeliveryKind}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("missing locked head", rows.Err())
	}
	values, err := rows.Values()
	rows.Close()
	if err != nil || values[0] != "a" {
		t.Fatalf("head %v: %v", values, err)
	}
	got, err := p.ClaimJob(ctx, now, time.Minute)
	if err != nil || got.ID != "b" {
		t.Fatalf("head hid next same-kind job: %s %v", got.ID, err)
	}
	other, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(ctx)
	if _, err = other.Exec(ctx, "SELECT id FROM memory_records WHERE collection='jobs' AND id='c' FOR UPDATE NOWAIT"); err != nil {
		t.Fatalf("losing kind was locked: %v", err)
	}
}

func TestPostgresJobClaimKeepsCallerIsolation(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC()
	queueTestJob(t, p, "isolated", "embedding", JobStatusPending, nil, time.Time{})
	err := p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		child := tx.(*Postgres)
		var before, after string
		if err := child.q().QueryRow(ctx, "SHOW transaction_isolation").Scan(&before); err != nil {
			return err
		}
		if _, err := child.ClaimJob(ctx, now, time.Minute); err != nil {
			return err
		}
		if err := child.q().QueryRow(ctx, "SHOW transaction_isolation").Scan(&after); err != nil {
			return err
		}
		if before != "serializable" || before != after {
			return fmt.Errorf("caller isolation changed: %s -> %s", before, after)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Run every worker from the same barrier; record all errors instead of hiding
// contention behind test retries. Leases stay live throughout the measurement.
func claimQueueConcurrently(t *testing.T, p *Postgres, workers, total int, now time.Time) time.Duration {
	t.Helper()
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan Record, total)
	failures := make(chan error, total)
	var wg sync.WaitGroup
	started := time.Now()
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for n := 0; n < total/workers; n++ {
				r, err := p.ClaimJob(ctx, now, 10*time.Minute)
				if err != nil {
					failures <- err
				} else {
					results <- r
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	elapsed := time.Since(started)
	close(results)
	close(failures)
	for err := range failures {
		t.Errorf("concurrent claim: %v", err)
	}
	ids := map[string]bool{}
	tokens := map[string]bool{}
	for r := range results {
		if ids[r.ID] || tokens[r.LeaseToken] || r.LeaseToken == "" {
			t.Errorf("duplicate job/lease: %s", r.ID)
		}
		ids[r.ID] = true
		tokens[r.LeaseToken] = true
		if r.Revision != 2 || r.Attempts != 1 {
			t.Errorf("claim changed more than once: %s revision=%d attempts=%d", r.ID, r.Revision, r.Attempts)
		}
	}
	if len(ids) != total {
		t.Fatalf("claimed %d unique jobs, want %d", len(ids), total)
	}
	t.Logf("workers=%d claims=%d wall_ms=%.3f claims_per_second=%.2f", workers, total, float64(elapsed)/float64(time.Millisecond), float64(total)/elapsed.Seconds())
	return elapsed
}
