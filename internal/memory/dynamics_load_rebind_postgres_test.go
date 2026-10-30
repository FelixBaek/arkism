package memory

import (
	"context"
	"errors"
	"testing"
)

func dynamicsRebindCoordinator(t *testing.T, p *Postgres) Record {
	t.Helper()
	var c Record
	if err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		var e error
		c, e = dynamicsLockCoordinator(ctx, tx, scopeA)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func dynamicsRebindIdentity(t *testing.T, p *Postgres) string {
	t.Helper()
	identity, err := p.BlobStorageIdentity(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func dynamicsRebindExpect(t *testing.T, err, want error, what string) {
	t.Helper()
	if want == nil && err != nil || want != nil && !errors.Is(err, want) {
		t.Fatal(what, err)
	}
}

// dynamicsRebindClone takes an actual pg_dump/pg_restore copy held for review.
func dynamicsRebindClone(t *testing.T, source *nativePGFixture, manifest string) (*Postgres, RecoveryWriteGateStatus) {
	t.Helper()
	restored := isolatedPostgresSchema(t, false)
	sys04DumpAndRestore(t, source.admin, restored)
	gate := gateStatus(t, restored)
	dynamicsRebindExpect(t, restored.RequireRestoreReview(testCtx, manifest), nil, "restore review")
	return restored, gate
}

func dynamicsRebindWorker(t *testing.T, source *nativePGFixture, restored *Postgres) *nativePGFixture {
	t.Helper()
	var role string
	dynamicsRebindExpect(t, source.app.q().QueryRow(testCtx, `SELECT current_user`).Scan(&role), nil, "app role")
	app := sys04OpenHelperDatabase(t, sys04DatabaseDSN(restored), source.admin.pool.Config().ConnConfig.Port, role)
	repo, err := app.WithToken(source.workerToken)
	dynamicsRebindExpect(t, err, nil, "worker token")
	auth, err := NewPostgresAuthorizer(app)
	dynamicsRebindExpect(t, err, nil, "authorizer")
	worker := &Service{Repo: repo, Auth: auth, NativeDynamics: &NativeDynamicsRuntime{Enabled: true}, DynamicsLoad: dynamicsFixtureRuntime(t, dynamicsFixtureConfig())}
	return &nativePGFixture{admin: restored, app: app, workerRepo: repo, worker: worker, in: source.in, workerToken: source.workerToken}
}

func dynamicsRebindGeneralHold(t *testing.T, restored *Postgres, manifest string, held bool) {
	t.Helper()
	query, args := `DELETE FROM memory_storage_metadata WHERE key='restore_review_pending'`, []any{}
	if held {
		query, args = `INSERT INTO memory_storage_metadata(key,value) VALUES('restore_review_pending',$1)`, []any{[]byte(manifest)}
	}
	_, err := restored.q().Exec(testCtx, query, args...)
	dynamicsRebindExpect(t, err, nil, "general hold")
}

// Each rebound entry is done and its own capture carries a native receipt.
func dynamicsRebindAssertApplied(t *testing.T, restored *Postgres, from, to int64) {
	t.Helper()
	for ordinal := from; ordinal <= to; ordinal++ {
		var entry, capture Record
		dynamicsRebindExpect(t, restored.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			var e error
			if entry, e = tx.(*Postgres).DynamicsLoadLock(ctx, scopeA, dynamicsEntryID(scopeA, ordinal)); e != nil {
				return e
			}
			capture, e = nativeGetTx(ctx, tx, scopeA, entry.DynamicsLoad.Capture.ID, "capture")
			return e
		}), nil, "applied entry")
		if entry.Status != JobStatusDone || capture.NativeDynamics.Receipt == nil || capture.NativeDynamics.Receipt.ProcessingSeq == nil {
			t.Fatalf("entry %d not applied: %s", ordinal, entry.Status)
		}
	}
}

// Actual dump/restore with a terminal entry plus an active, leased entry. The
// clone's queue stays bound to the original until the offline rebind; refusals
// change nothing; publication stays held until the existing reviews release it.
func TestDynamicsLoadRecoveryRebindAfterActualRestorePG(t *testing.T) {
	source := dynamicsPG(t)
	source.accept(t)
	dynamicsRebindExpect(t, source.worker.WorkDynamicsOnce(testCtx, source.workerToken), nil, "terminal entry")
	source.in.EventID = "active-at-backup"
	source.accept(t)
	copiedLease, err := source.worker.claimDynamics(testCtx, scopeA)
	dynamicsRebindExpect(t, err, nil, "lease copied by the dump")
	sourceGate := sys04Pause(t, source.admin)
	manifest := ContentHash("sys05 queue rebind after actual restore")
	restored, _ := dynamicsRebindClone(t, source, manifest)
	sourceIdentity := dynamicsRebindIdentity(t, source.admin)
	if sourceIdentity == dynamicsRebindIdentity(t, restored) || dynamicsRebindCoordinator(t, restored).DynamicsLoad.StorageIdentity != sourceIdentity {
		t.Fatal("restore did not produce a distinct storage identity")
	}
	_, err = dynamicsRebindWorker(t, source, restored).worker.claimDynamics(testCtx, scopeA)
	dynamicsRebindExpect(t, err, ErrDynamicsRuntimeBinding, "clone accepted the original binding")

	dynamicsRebindExpect(t, restored.ReconcileDynamicsLoadRecovery(testCtx, restored, scopeA, sourceGate.Gate.Generation), ErrInvalid, "self rebind")
	dynamicsRebindExpect(t, restored.ReconcileDynamicsLoadRecovery(testCtx, source.admin, scopeA, ContentHash("not the paused generation")), ErrConflict, "wrong source generation")
	sys04Resume(t, restored)
	sys04Resume(t, source.admin)
	source.in.EventID = "after-backup"
	source.accept(t)
	sourceGate = sys04Pause(t, source.admin)
	dynamicsRebindExpect(t, restored.ReconcileDynamicsLoadRecovery(testCtx, source.admin, scopeA, sourceGate.Gate.Generation), ErrConflict, "diverged ledger rebound")
	if dynamicsRebindCoordinator(t, restored).DynamicsLoad.StorageIdentity != sourceIdentity {
		t.Fatal("refused rebind mutated the clone")
	}

	// A fresh clone of the current ledger: native review while paused (writes refused), then rebind once while open and held.
	restored, restoredGate := dynamicsRebindClone(t, source, manifest)
	snapshot := sys04Snapshot(t, source.admin, false, sourceGate.Gate.Generation)
	dynamicsRebindExpect(t, restored.VerifyNativeRecoveryReadback(testCtx, manifest, restoredGate.Gate.Generation, *snapshot.NativeDynamics), nil, "native readback")
	dynamicsRebindExpect(t, restored.ReconcileNativeRecovery(testCtx, source.admin, manifest, sourceGate.Gate.Generation, restoredGate.Gate.Generation), nil, "native reconcile")
	if restored.ReconcileDynamicsLoadRecovery(testCtx, source.admin, scopeA, sourceGate.Gate.Generation) == nil {
		t.Fatal("paused clone accepted a queue write")
	}
	sys04Resume(t, restored)
	dynamicsRebindGeneralHold(t, restored, manifest, false)
	dynamicsRebindExpect(t, restored.ReconcileDynamicsLoadRecovery(testCtx, source.admin, scopeA, sourceGate.Gate.Generation), ErrUnavailable, "rebind without general review")
	dynamicsRebindGeneralHold(t, restored, manifest, true)
	before := dynamicsRebindCoordinator(t, restored).DynamicsLoad
	dynamicsRebindExpect(t, restored.ReconcileDynamicsLoadRecovery(testCtx, source.admin, scopeA, sourceGate.Gate.Generation), nil, "rebind")
	after := dynamicsRebindCoordinator(t, restored).DynamicsLoad
	if after.StorageIdentity != dynamicsRebindIdentity(t, restored) || after.LeaseGeneration != before.LeaseGeneration+1 || after.LeaseToken != "" || after.Head != before.Head || after.Tail != before.Tail || after.RepairStartNS != nil {
		t.Fatalf("rebind result %+v", after)
	}
	if dynamicsRebindCoordinator(t, source.admin).DynamicsLoad.StorageIdentity != sourceIdentity {
		t.Fatal("original was modified")
	}
	dynamicsRebindExpect(t, restored.ReconcileDynamicsLoadRecovery(testCtx, source.admin, scopeA, sourceGate.Gate.Generation), ErrConflict, "second rebind")
	dynamicsRebindExpect(t, restored.CheckRestorePublication(testCtx), ErrUnavailable, "rebind released the general hold")

	// Copied lease is fenced; the live queue is held until review release.
	target := dynamicsRebindWorker(t, source, restored)
	events := len(target.rows(t, "event"))
	dynamicsRebindExpect(t, target.worker.applyDynamicsClaim(testCtx, source.workerToken, scopeA, copiedLease), ErrConflict, "copied lease applied")
	dynamicsRebindExpect(t, target.worker.WorkDynamicsOnce(testCtx, source.workerToken), ErrUnavailable, "held clone was not refused")
	if len(target.rows(t, "event")) != events {
		t.Fatal("held clone mutated native state")
	}

	// Release by the existing review, then the rebound queue drains in order.
	dynamicsRebindGeneralHold(t, restored, manifest, false)
	for step := 0; step < 8 && dynamicsRebindCoordinator(t, restored).DynamicsLoad.Count > 0; step++ {
		dynamicsRebindExpect(t, target.worker.WorkDynamicsOnce(testCtx, source.workerToken), nil, "drain after release")
	}
	if final := dynamicsRebindCoordinator(t, restored).DynamicsLoad; final.Count != 0 || final.Head != after.Tail || final.Withheld {
		t.Fatalf("rebound queue did not drain %+v", final)
	}
	dynamicsRebindAssertApplied(t, restored, before.Head+1, before.Tail)
	if dynamicsRebindCoordinator(t, source.admin).DynamicsLoad.Head != before.Head {
		t.Fatal("clone work leaked into the original")
	}
}
