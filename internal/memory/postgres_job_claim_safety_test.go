package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresJobClaimRechecksCommittedLease(t *testing.T) {
	for _, change := range []string{"lease", "due_reschedule", "future_reschedule", "unscheduled"} {
		t.Run(change, func(t *testing.T) {
			p := isolatedPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			now := time.Now().UTC().Truncate(time.Millisecond)
			past := now.Add(-time.Hour)
			for _, id := range []string{"a", "b"} {
				queueTestJob(t, p, id, "embedding", JobStatusPending, &past, time.Time{})
			}
			// Pause after the statement snapshot exists but before LockRows. Another
			// transaction commits a lease or reschedule against the old candidate.
			schemaExec(t, p, `CREATE FUNCTION queue_claim_pause() RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$
   BEGIN PERFORM pg_advisory_xact_lock(710005); RETURN true; END $$`)
			barrier, err := p.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(710005)"); err != nil {
				t.Fatal(err)
			}
			conn, err := p.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			pid := conn.Conn().PgConn().PID()
			result := make(chan []any, 1)
			failure := make(chan error, 1)
			done := make(chan struct{})
			query := strings.Replace(claimJobSelectSQL(1), "AND job.kind=", "AND queue_claim_pause() AND job.kind=", 1)
			go func() {
				defer close(done)
				rows, err := conn.Query(ctx, query, []string{"embedding"}, now)
				if err != nil {
					failure <- err
					return
				}
				defer rows.Close()
				if !rows.Next() {
					failure <- rows.Err()
					return
				}
				values, err := rows.Values()
				if err != nil {
					failure <- err
					return
				}
				result <- values
			}()
			defer func() { cancel(); <-done }()
			for {
				var waiting bool
				if err = p.pool.QueryRow(ctx, "SELECT COALESCE(wait_event='advisory',false) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-failure:
					t.Fatalf("query stopped before barrier: %v", err)
				case <-ctx.Done():
					t.Fatal("query never reached barrier")
				case <-time.After(time.Millisecond):
				}
			}
			if change == "lease" {
				claimed, err := p.ClaimJob(ctx, now, time.Minute)
				if err != nil || claimed.ID != "a" {
					t.Fatalf("concurrent commit claim %s: %v", claimed.ID, err)
				}
			} else {
				r, err := p.Get(ctx, colJobs, scopeA, "a")
				if err != nil {
					t.Fatal(err)
				}
				next := now.Add(-30 * time.Minute)
				if change == "future_reschedule" {
					next = now.Add(time.Hour)
				}
				r.RunAt = &next
				if change == "unscheduled" {
					r.RunAt = nil
				}
				previous := r.Revision
				r.Revision++
				if err = p.Replace(ctx, colJobs, r, previous); err != nil {
					t.Fatal(err)
				}
			}
			if err = barrier.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var values []any
			select {
			case values = <-result:
			case err := <-failure:
				t.Fatalf("candidate recheck: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if change == "lease" || change == "future_reschedule" {
				if values[0] != "b" {
					t.Fatalf("ineligible stale candidate selected: %s", values[0])
				}
			} else {
				if values[0] != "a" {
					t.Fatalf("reschedule race did not expose its old candidate: %v", values[0])
				}
				optionalTime := func(value any) *time.Time {
					if value == nil {
						return nil
					}
					v := value.(time.Time)
					return &v
				}
				var retry transientTransactionError
				if err = checkJobClaimPriority(optionalTime(values[10]), optionalTime(values[11])); !errors.As(err, &retry) {
					t.Fatalf("reschedule did not require a fresh snapshot: %v", err)
				}
				// The query has released its lock without mutating the candidate. A retry
				// must choose b at its current priority, not lease a at its old position.
				<-done
				claimed, err := p.ClaimJob(ctx, now, time.Minute)
				if err != nil || claimed.ID != "b" {
					t.Fatalf("fresh priority %s: %v", claimed.ID, err)
				}
			}
		})
	}
}

func TestPostgresJobClaimRestrictedTokenAndRecoveryGate(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	ctx := context.Background()
	// These earlier jobs are the same claimed kind, but another project. This
	// deliberately measures the row-policy filter, not merely unrelated kinds.
	seedQueuePlanJobs(t, p, "foreign-", "embedding", 1000, now)
	schemaExec(t, p, "UPDATE memory_records SET project_id='foreign-project',doc=jsonb_set(doc,'{scope,project_id}','\"foreign-project\"'::jsonb) WHERE collection='jobs'")
	schemaExec(t, p, "UPDATE memory_records SET payload=convert_to(doc::text,'UTF8') WHERE collection='jobs'")
	queueTestJob(t, p, "visible", "embedding", JobStatusPending, nil, time.Time{})
	token := testPrincipalToken("queue-claim-restricted-test")
	if err := p.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: "queue-agent", Role: roleAgent, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write"}}); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, p)
	worker, err := app.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	schemaExec(t, p, "ANALYZE memory_records")
	readQueuePlan(t, worker, "claim-rls-1000-denied", claimJobSelectSQL(1), []string{"embedding"}, now)
	got, err := worker.ClaimJob(ctx, now, time.Minute)
	if err != nil || got.ID != "visible" {
		t.Fatalf("restricted worker claim %s: %v", got.ID, err)
	}
	if _, err = worker.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restricted worker claimed foreign job: %v", err)
	}
	var touched int
	if err = p.pool.QueryRow(ctx, "SELECT count(*) FROM memory_records WHERE collection='jobs' AND project_id='foreign-project' AND (status<>'pending' OR attempts<>0 OR revision<>1)").Scan(&touched); err != nil || touched != 0 {
		t.Fatalf("foreign jobs touched=%d: %v", touched, err)
	}
	queueTestJob(t, p, "gate-job", "embedding", JobStatusPending, nil, time.Time{})
	initial := gateStatus(t, p)
	paused := gateChange(t, p, "pause", initial)
	if _, err = worker.ClaimJob(ctx, now, time.Minute); err == nil {
		t.Fatal("claim bypassed paused recovery gate")
	}
	var attempts int
	if err = p.pool.QueryRow(ctx, "SELECT attempts FROM memory_records WHERE collection='jobs' AND id='gate-job'").Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("paused claim changed attempts=%d: %v", attempts, err)
	}
	gateChange(t, p, "resume", paused)
	got, err = worker.ClaimJob(ctx, now, time.Minute)
	if err != nil || got.ID != "gate-job" {
		t.Fatalf("resumed claim %s: %v", got.ID, err)
	}
	if err = p.FinishJob(ctx, got, true); err != nil {
		t.Fatal(err)
	}
	if err = p.FinishJob(ctx, got, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease accepted: %v", err)
	}
	// Explicitly verify the caller's transaction isolation remains unchanged.
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	child := &Postgres{pool: p.pool, tx: tx}
	if _, err = child.ClaimJob(ctx, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	var isolation string
	if err = tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&isolation); err != nil || isolation != "repeatable read" {
		t.Fatalf("caller isolation %s: %v", isolation, err)
	}
}

func TestPostgresJobClaimPlanPreferenceRestoredAndCachedPlanBypassed(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC()
	kinds := append([]string{"embedding", webhookDeliveryKind, autoDeriveJobKind, deriveScheduleKind, captureMergeGateKind, approvedEntitiesJobKind}, erasureJobKinds...)
	queueTestJob(t, p, "a", "embedding", JobStatusPending, nil, time.Time{})
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SET plan_cache_mode=force_generic_plan; SET enable_sort=on"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "RESET plan_cache_mode; RESET enable_sort")
	// Deliberately retain a named generic plan created with sorting enabled.
	// Production must neither reuse nor replan that cached statement.
	sql := claimJobSelectSQL(len(kinds))
	for i := 0; i < 6; i++ {
		rows, err := conn.Query(ctx, sql, kinds, now)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	var before int64
	if err = conn.QueryRow(ctx, "SELECT generic_plans FROM pg_prepared_statements WHERE statement=$1", sql).Scan(&before); err != nil || before < 6 {
		t.Fatalf("generic warmup=%d: %v", before, err)
	}
	for _, setting := range []string{"on", "off"} {
		t.Run(setting, func(t *testing.T) {
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, "SELECT set_config('enable_sort',$1,true)", setting); err != nil {
				t.Fatal(err)
			}
			child := &Postgres{pool: p.pool, tx: tx}
			got, err := child.ClaimJob(ctx, now, time.Minute)
			if err != nil || got.ID != "a" {
				t.Fatalf("claim %s: %v", got.ID, err)
			}
			for _, wantError := range []bool{false, true} {
				if wantError {
					if _, err = child.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrNotFound) {
						t.Fatalf("empty claim: %v", err)
					}
				}
				var actual string
				if err = tx.QueryRow(ctx, "SELECT current_setting('enable_sort')").Scan(&actual); err != nil || actual != setting {
					t.Fatalf("setting leaked: %s want %s: %v", actual, setting, err)
				}
			}
			// Rollback restores the same pending job for the other original setting.
		})
	}
	var after int64
	if err = conn.QueryRow(ctx, "SELECT generic_plans FROM pg_prepared_statements WHERE statement=$1", sql).Scan(&after); err != nil || after != before {
		t.Fatalf("claim reused named generic plan: %d -> %d: %v", before, after, err)
	}
}

func TestPostgresJobClaimPlanPreferenceCancelledContext(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if _, err := p.preferOrderedJobClaim(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nontransaction setting change: %v", err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL enable_sort=on"); err != nil {
		t.Fatal(err)
	}
	child := &Postgres{pool: p.pool, tx: tx}
	cancelled, cancel := context.WithCancel(ctx)
	restore, err := child.preferOrderedJobClaim(cancelled)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for i := 0; i < 2; i++ {
		if err = restore(); err != nil {
			t.Fatalf("restore after cancellation: %v", err)
		}
	}
	var setting string
	if err = tx.QueryRow(ctx, "SELECT current_setting('enable_sort')").Scan(&setting); err != nil || setting != "on" {
		t.Fatalf("cancelled setting leaked: %s %v", setting, err)
	}
	if _, err = tx.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("caller transaction unusable: %v", err)
	}
}
