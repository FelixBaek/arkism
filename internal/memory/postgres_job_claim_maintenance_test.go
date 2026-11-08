package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func queueMaintenanceRequest(scope Scope, key string) PrivacyDeletionRequest {
	return PrivacyDeletionRequest{Scope: scope, TargetType: "subject", TargetIDs: []string{scope.UserID}, IdempotencyKey: key, Reason: "owner_request", RetentionPolicyRef: retentionSynthetic}
}

// Raw owner reads inspect durable state while the repository gate is closed;
// they are test observations, not a claim that the gate fences arbitrary SQL.
func queueMaintenanceSnapshot(t *testing.T, p *Postgres) []byte {
	t.Helper()
	var raw []byte
	if err := p.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]'::jsonb) FROM arkism.memory_records r WHERE collection='jobs'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func waitQueueGateBlocker(t *testing.T, p *Postgres, ctx context.Context, blocker int32, query string) int32 {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int32
		err := p.pool.QueryRow(ctx, `SELECT COALESCE(min(pid),0) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND position($2 in query)>0`, blocker, query).Scan(&pid)
		if err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no observed lock wait for %s: %v", query, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestPostgresJobClaimMaintenanceBlocksOwnerAndRestrictedWorker(t *testing.T) {
	p, svc, scope, ownerToken := durableMaintenanceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	past := now.Add(-time.Hour)
	queueTestJob(t, p, "queue-approved", approvedEntitiesJobKind, JobStatusPending, &past, time.Time{})
	foreign := Record{ID: "queue-foreign", Scope: scopeB, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, RunAt: &past}
	if err := p.Insert(ctx, colJobs, foreign); err != nil {
		t.Fatal(err)
	}
	workerToken := testPrincipalToken("queue-maintenance-restricted-worker")
	if err := p.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: "queue-worker", Role: roleAgent, Token: workerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write"}}); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, p)
	worker, err := app.WithToken(workerToken)
	if err != nil {
		t.Fatal(err)
	}
	ownerErasure := *p
	ownerErasure.erasureOnly = true
	workerErasure := *worker
	workerErasure.erasureOnly = true
	before := queueMaintenanceSnapshot(t, p)
	st, err := svc.BeginPrivacyMaintenance(ctx, ownerToken, queueMaintenanceRequest(scope, "queue-active"))
	if err != nil || !st.Active {
		t.Fatalf("maintenance: %+v %v", st, err)
	}
	for _, entry := range []struct {
		name string
		repo *Postgres
	}{{"owner", p}, {"restricted", worker}, {"owner-erasure-empty", &ownerErasure}, {"restricted-erasure-empty", &workerErasure}} {
		if got, err := entry.repo.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrUnavailable) || got.ID != "" {
			t.Fatalf("%s bypassed active gate: %s %v", entry.name, got.ID, err)
		}
	}
	// The restricted pool has one connection, so check the exact session used by
	// each blocked claim. The gate must reject before changing its preference.
	for _, setting := range []string{"on", "off"} {
		if _, err = app.pool.Exec(ctx, "SELECT set_config('enable_sort',$1,false)", setting); err != nil {
			t.Fatal(err)
		}
		if _, err = worker.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		var got string
		if err = app.pool.QueryRow(ctx, "SELECT current_setting('enable_sort')").Scan(&got); err != nil || got != setting {
			t.Fatalf("blocked claim changed session setting %s -> %s: %v", setting, got, err)
		}
	}
	if _, err = app.pool.Exec(ctx, "RESET enable_sort"); err != nil {
		t.Fatal(err)
	}
	if after := queueMaintenanceSnapshot(t, p); !bytes.Equal(before, after) {
		t.Fatal("blocked claims changed durable queue fields or payload")
	}
	if _, err = p.CancelMemoryMaintenance(ctx, st.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = workerErasure.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("erasure-only claimed approved entity work: %v", err)
	}
	got, err := worker.ClaimJob(ctx, now, time.Minute)
	if err != nil || got.ID != "queue-approved" || got.Kind != approvedEntitiesJobKind || got.Attempts != 1 || got.Revision != 2 {
		t.Fatalf("resumed approved-entity claim: %+v %v", got, err)
	}
	queueTestJob(t, p, "queue-erasure", erasureJobKinds[0], JobStatusPending, nil, time.Time{})
	got, err = workerErasure.ClaimJob(ctx, now, time.Minute)
	if err != nil || got.ID != "queue-erasure" {
		t.Fatalf("resumed erasure claim: %s %v", got.ID, err)
	}
	if _, err = worker.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restricted worker escaped its project: %v", err)
	}
}

func TestPostgresJobClaimMaintenanceDrainsCallerTransaction(t *testing.T) {
	for _, mode := range []struct{ name, isolation, sort string }{{"read-committed", "read committed", "on"}, {"serializable", "serializable", "off"}} {
		t.Run(mode.name, func(t *testing.T) {
			p, svc, scope, token := durableMaintenanceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			now := time.Now().UTC()
			queueTestJob(t, p, "queue-admitted", "embedding", JobStatusPending, nil, time.Time{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			admitted := make(chan int32, 1)
			claimDone := make(chan error, 1)
			wrapper := p.ReadTransaction
			if mode.isolation == "serializable" {
				wrapper = p.Transaction
			}
			go func() {
				claimDone <- wrapper(ctx, func(ctx context.Context, tx Tx) error {
					child := tx.(*Postgres)
					if _, err := child.q().Exec(ctx, "SELECT set_config('enable_sort',$1,true)", mode.sort); err != nil {
						return err
					}
					r, err := child.ClaimJob(ctx, now, time.Minute)
					if err != nil {
						return err
					}
					if r.ID != "queue-admitted" {
						return fmt.Errorf("wrong admitted job: %s", r.ID)
					}
					var isolation, sort string
					var pid int32
					if err = child.q().QueryRow(ctx, "SELECT current_setting('transaction_isolation'),current_setting('enable_sort'),pg_backend_pid()").Scan(&isolation, &sort, &pid); err != nil {
						return err
					}
					if isolation != mode.isolation || sort != mode.sort {
						return fmt.Errorf("caller state changed: isolation=%s sort=%s", isolation, sort)
					}
					admitted <- pid
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			var pid int32
			select {
			case pid = <-admitted:
			case err := <-claimDone:
				t.Fatalf("claim was not admitted: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			type result struct {
				status MemoryMaintenanceStatus
				err    error
			}
			maintenanceDone := make(chan result, 1)
			go func() {
				st, err := svc.BeginPrivacyMaintenance(ctx, token, queueMaintenanceRequest(scope, "queue-drain"))
				maintenanceDone <- result{st, err}
			}()
			waitQueueGateBlocker(t, p, ctx, pid, "memory_maintenance_gate")
			select {
			case r := <-maintenanceDone:
				t.Fatalf("maintenance passed an open claim transaction: %+v", r)
			default:
			}
			unblock()
			if err := <-claimDone; err != nil {
				t.Fatal(err)
			}
			r := <-maintenanceDone
			if r.err != nil || !r.status.Active {
				t.Fatalf("maintenance after drain: %+v", r)
			}
			var attempts int
			var status string
			if err := p.pool.QueryRow(ctx, "SELECT attempts,status FROM memory_records WHERE collection='jobs' AND id='queue-admitted'").Scan(&attempts, &status); err != nil || attempts != 1 || status != JobStatusRunning {
				t.Fatalf("admitted claim did not commit exactly once: %d %s %v", attempts, status, err)
			}
			if _, err := p.ClaimJob(ctx, now, time.Minute); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("new claim passed active maintenance: %v", err)
			}
			if _, err := p.CancelMemoryMaintenance(ctx, r.status.Revision); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresJobClaimWaitsForMaintenanceCommit(t *testing.T) {
	p, svc, scope, token := durableMaintenanceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC()
	queueTestJob(t, p, "queue-waiting", "embedding", JobStatusPending, nil, time.Time{})
	before := queueMaintenanceSnapshot(t, p)
	// Pause the real maintenance operation after it owns the gate row and before
	// its state commits. A new claim must wait, then see the committed active gate.
	schemaExec(t, p, `CREATE FUNCTION queue_pause_maintenance_gate() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(710010); RETURN NEW; END $$;
 CREATE TRIGGER queue_pause_maintenance_gate BEFORE UPDATE ON arkism.memory_maintenance_gate FOR EACH ROW EXECUTE FUNCTION queue_pause_maintenance_gate()`)
	barrier, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(710010)"); err != nil {
		t.Fatal(err)
	}
	var barrierPID int32
	if err = barrier.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&barrierPID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status MemoryMaintenanceStatus
		err    error
	}
	maintenanceDone := make(chan result, 1)
	go func() {
		st, err := svc.BeginPrivacyMaintenance(ctx, token, queueMaintenanceRequest(scope, "queue-wait"))
		maintenanceDone <- result{st, err}
	}()
	maintenancePID := waitQueueGateBlocker(t, p, ctx, barrierPID, "UPDATE arkism.memory_maintenance_gate")
	claimDone := make(chan error, 1)
	go func() { _, err := p.ClaimJob(ctx, now, time.Minute); claimDone <- err }()
	waitQueueGateBlocker(t, p, ctx, maintenancePID, "check_maintenance_access")
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-maintenanceDone
	if r.err != nil || !r.status.Active {
		t.Fatalf("maintenance commit: %+v", r)
	}
	if err = <-claimDone; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("waiting claim ignored active gate: %v", err)
	}
	if after := queueMaintenanceSnapshot(t, p); !bytes.Equal(before, after) {
		t.Fatal("waiting claim mutated a job")
	}
	if _, err = p.CancelMemoryMaintenance(ctx, r.status.Revision); err != nil {
		t.Fatal(err)
	}
}
