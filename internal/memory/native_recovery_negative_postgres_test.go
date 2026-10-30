package memory

import (
	"errors"
	"testing"
	"time"
)

func TestDynamicsRecoveryActualReadbackAndAuthorityNegativesPG(t *testing.T) {
	for _, name := range []string{"missing_readback", "stale_source_generation", "wrong_source_identity", "changed_authorization", "changed_live_value", "unknown_native_role", "missing_catalog", "source_query_failure"} {
		t.Run(name, func(t *testing.T) { sys04CheckRecoveryAuthorityNegative(t, name) })
	}
}

func TestDynamicsRecoveryActualIncompleteProjectionIsNotEmptyPG(t *testing.T) {
	for _, name := range []string{"unknown_role", "column_disagreement", "doc_disagreement", "missing_catalog"} {
		t.Run(name, func(t *testing.T) {
			p := nativePGSetup(t)
			p.activate(t, "bad-projection")
			_, capture := p.accept(t)
			p.process(t, capture)

			var err error
			switch name {
			case "unknown_role":
				err = p.admin.Insert(testCtx, colJobs, nativeJob(p.in.Scope, "unrecognized", nativeID("unrecognized", name), NativeDynamicsPayload{Protocol: nativeDynamicsProtocol}))
			case "column_disagreement":
				_, err = p.admin.q().Exec(testCtx, `UPDATE memory_records SET revision=revision+1 WHERE collection='jobs' AND kind=$1`, nativeDynamicsPrefix+"state")
			case "doc_disagreement":
				_, err = p.admin.q().Exec(testCtx, `UPDATE memory_records SET doc=jsonb_set(doc,'{status}','"tampered"') WHERE collection='jobs' AND kind=$1`, nativeDynamicsPrefix+"checkpoint")
			case "missing_catalog":
				_, err = p.admin.q().Exec(testCtx, `DELETE FROM memory_records WHERE collection='jobs' AND id=$1`, nativeCatalogID(p.in.Scope))
			}
			if err != nil {
				t.Fatal("actual malformed projection preparation", err)
			}
			paused := sys04Pause(t, p.admin)
			if raw, err := p.admin.RecoveryNativeReviewSnapshot(testCtx, false, paused.Gate.Generation); err == nil || raw != nil {
				t.Fatal("incomplete native projection was exported as known", err)
			}
		})
	}
}

func sys04MutateRecoveryNegative(t *testing.T, name string, source *nativePGFixture, restored *Postgres, generation string) (string, error) {
	t.Helper()
	var err error
	switch name {
	case "stale_source_generation":
		generation = ContentHash("stale")
	case "wrong_source_identity":
		_, err = source.admin.q().Exec(testCtx, `UPDATE memory_storage_metadata SET value=decode(repeat('ab',32),'hex') WHERE key='storage_identity'`)
	case "changed_authorization":
		_, err = source.admin.q().Exec(testCtx, `DELETE FROM principal_permissions WHERE principal_id='native-pg-worker'`)
	case "changed_live_value":
		states := source.rows(t, "state")
		r, e := restored.Get(testCtx, colJobs, source.in.Scope, states[0].ID)
		if e != nil {
			t.Fatal(e)
		}
		changed := 0.99
		r.NativeDynamics.State.Value = &changed
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		err = restored.Replace(testCtx, colJobs, r, r.Revision-1)
	case "unknown_native_role":
		r := nativeJob(source.in.Scope, "unknown", nativeID("unknown", name), NativeDynamicsPayload{Protocol: nativeDynamicsProtocol})
		err = restored.Insert(testCtx, colJobs, r)
	case "missing_catalog":
		_, err = restored.q().Exec(testCtx, `DELETE FROM memory_records WHERE collection='jobs' AND id=$1`, nativeCatalogID(source.in.Scope))
	case "source_query_failure":
		// Inject the actual source outage only after obtaining a paused generation below.
	}
	return generation, err
}

func sys04CheckRecoveryAuthorityNegative(t *testing.T, name string) {
	t.Helper()
	source := nativePGSetup(t)
	source.activate(t, "roundtrip")
	_, capture := source.accept(t)
	source.process(t, capture)
	sourceGate := sys04Pause(t, source.admin)
	snapshot := sys04Snapshot(t, source.admin, false, sourceGate.Gate.Generation)
	restored := isolatedPostgresSchema(t, false)
	sys04DumpAndRestore(t, source.admin, restored)
	targetGate := gateStatus(t, restored)
	manifest := ContentHash("negative " + name)
	if err := restored.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	if name != "missing_readback" {
		if err := restored.VerifyNativeRecoveryReadback(testCtx, manifest, targetGate.Gate.Generation, *snapshot.NativeDynamics); err != nil {
			t.Fatal("positive actual readback", err)
		}
	}
	sys04Resume(t, source.admin)
	sys04Resume(t, restored)
	generation := sourceGate.Gate.Generation
	var err error
	generation, err = sys04MutateRecoveryNegative(t, name, source, restored, generation)

	if err != nil {
		t.Fatal("negative-control preparation", err)
	}
	sourceGate = sys04Pause(t, source.admin)
	targetGate = sys04Pause(t, restored)
	if name == "source_query_failure" {
		if _, err = source.admin.q().Exec(testCtx, `ALTER TABLE principal_permissions RENAME TO sys04_unavailable_permissions`); err != nil {
			t.Fatal(err)
		}
	}
	if name != "stale_source_generation" {
		generation = sourceGate.Gate.Generation
	}
	if err = restored.ReconcileNativeRecovery(testCtx, source.admin, manifest, generation, targetGate.Gate.Generation); err == nil {
		t.Fatal("untrusted actual state cleared publication fence")
	}
	if err = restored.checkNativeRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("failed review removed hold", err)
	}
}
