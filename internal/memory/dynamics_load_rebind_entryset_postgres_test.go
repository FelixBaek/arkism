package memory

import (
	"context"
	"errors"
	"testing"
)

func TestDynamicsLoadRecoveryRejectsExtraTerminalAfterActualRestorePG(t *testing.T) {
	source := dynamicsPG(t)
	dynamicsUseLogin(t, source)
	source.accept(t)
	dynamicsRebindExpect(t, source.worker.WorkDynamicsOnce(testCtx, source.workerToken), nil, "terminal entry")
	source.in.EventID = "active-at-backup"
	source.accept(t)
	_, err := source.worker.claimDynamics(testCtx, scopeA)
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
	// Corrupt restored ledger with an extra terminal entry outside the coordinator range.
	orphan := dynamicsRow(scopeA, "entry", dynamicsEntryID(scopeA, before.Tail+1), DynamicsLoadPayload{ConfigSHA256: before.ConfigSHA256, StorageIdentity: sourceIdentity, Ordinal: before.Tail + 1, EntryBytes: 100, Reason: "extra restored terminal"})
	orphan.Status = JobStatusDone
	dynamicsRebindExpect(t, restored.Insert(testCtx, colJobs, orphan), nil, "insert extra restored ledger row")
	err = restored.ReconcileDynamicsLoadRecovery(testCtx, source.admin, scopeA, sourceGate.Gate.Generation)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("independent counterexample: clone with extra terminal ledger row rebound: %v", err)
	}
	if got := dynamicsRebindCoordinator(t, restored).DynamicsLoad; got.StorageIdentity != sourceIdentity || got.LeaseGeneration != before.LeaseGeneration {
		t.Fatal("refused rebind mutated coordinator")
	}
}

// Corruptions with matching cardinality must also fail before any binding write.
func TestDynamicsLoadRecoveryEntrySetPG(t *testing.T) {
	source := dynamicsPG(t)
	dynamicsUseLogin(t, source)
	source.accept(t)
	dynamicsRebindExpect(t, source.worker.WorkDynamicsOnce(testCtx, source.workerToken), nil, "terminal entry")
	source.in.EventID = "entry-set-active"
	source.accept(t)
	cases := []struct {
		name, sql string
	}{
		{"duplicate-payload", `INSERT INTO memory_records(collection,id,workspace_id,project_id,user_id,agent_id,revision,status,kind,updated_at,run_at,lease_until,lease_token,attempts,payload,doc,embedding,embedding_space,embedding_dimension,search_tokens,search_context_indexed) SELECT collection, 'sys05_entry_duplicate', workspace_id,project_id,user_id,agent_id,revision,status,kind,updated_at,run_at,lease_until,lease_token,attempts,payload,doc,embedding,embedding_space,embedding_dimension,search_tokens,search_context_indexed FROM memory_records WHERE collection='jobs' AND id=$1`},
		{"missing", `DELETE FROM memory_records WHERE collection='jobs' AND id=$1`},
		{"storage-id", `UPDATE memory_records SET id='unexpected-storage-id' WHERE collection='jobs' AND id=$1`},
		{"payload-id", `UPDATE memory_records SET payload=convert_to((doc || '{"_id":"unexpected-payload-id"}'::jsonb)::text,'UTF8') WHERE collection='jobs' AND id=$1`},
		{"duplicate-ordinal", `UPDATE memory_records SET payload=convert_to(jsonb_set(doc,'{sys05_load,ordinal}','2')::text,'UTF8'), doc=jsonb_set(doc,'{sys05_load,ordinal}','2') WHERE collection='jobs' AND id=$1`},
		{"malformed-payload", `UPDATE memory_records SET payload=convert_to('{','UTF8') WHERE collection='jobs' AND id=$1`},
		{"payload-role", `UPDATE memory_records SET payload=convert_to(jsonb_set(doc,'{sys05_load,role}','"coordinator"')::text,'UTF8') WHERE collection='jobs' AND id=$1`},
		{"sql-kind", `UPDATE memory_records SET kind='unexpected' WHERE collection='jobs' AND id=$1`},
		{"payload-scope", `UPDATE memory_records SET payload=convert_to(jsonb_set(doc,'{scope,workspace_id}','"other"')::text,'UTF8') WHERE collection='jobs' AND id=$1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := source.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				pg := tx.(*Postgres)
				if _, e := pg.q().Exec(ctx, tc.sql, dynamicsEntryID(scopeA, 1)); e != nil {
					t.Fatal(e)
				}
				return dynamicsRecoveryEntrySetTx(ctx, pg, scopeA, 2)
			})
			dynamicsRebindExpect(t, err, ErrConflict, "malformed entry set accepted")
		})
	}
	dynamicsRebindExpect(t, source.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return dynamicsRecoveryEntrySetTx(ctx, tx.(*Postgres), scopeA, 2)
	}), nil, "valid complete entry set")
}
