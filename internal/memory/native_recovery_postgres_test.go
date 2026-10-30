package memory

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func sys04Pause(t *testing.T, p *Postgres) RecoveryWriteGateStatus {
	t.Helper()
	return gateChange(t, p, "pause", gateStatus(t, p))
}
func sys04Resume(t *testing.T, p *Postgres) RecoveryWriteGateStatus {
	t.Helper()
	return gateChange(t, p, "resume", gateStatus(t, p))
}
func sys04Snapshot(t *testing.T, p *Postgres, restored bool, generation string) RecoverySnapshot {
	t.Helper()
	raw, err := p.RecoveryNativeReviewSnapshot(testCtx, restored, generation)
	if err != nil {
		t.Fatal("native recovery snapshot", err)
	}
	var out RecoverySnapshot
	if err = json.Unmarshal(raw, &out); err != nil || out.NativeDynamics == nil || out.ReceiptCauses == nil {
		t.Fatal("native recovery projection incomplete", err)
	}
	expected := 7
	if restored {
		expected = 8
	}
	if out.Version != expected {
		t.Fatal("explicit snapshot version", out.Version)
	}
	return out
}

func TestDynamicsRecoveryExplicitSnapshotAndNativeGatePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "recovery-version")
	_, capture := p.accept(t)
	p.process(t, capture)
	paused := sys04Pause(t, p.admin)
	legacy, err := p.admin.RecoveryReviewSnapshot(testCtx, false, paused.Gate.Generation)
	if err != nil {
		t.Fatal(err)
	}
	var old RecoverySnapshot
	if json.Unmarshal(legacy, &old) != nil || old.Version != 5 || old.NativeDynamics != nil {
		t.Fatal("legacy v5 export changed")
	}
	exported := sys04Snapshot(t, p.admin, false, paused.Gate.Generation)
	if !nativeRecoverySnapshotValid(*exported.NativeDynamics) || len(exported.NativeDynamics.Scopes) != 1 {
		t.Fatal("native six domains missing")
	}
	if exported.SourceIdentity != old.SourceIdentity {
		t.Fatal("source identity changed")
	}
	t.Run("operator_grants", func(t *testing.T) {
		raw, err := p.app.RecoveryNativeReviewSnapshot(testCtx, false, paused.Gate.Generation)
		if !errors.Is(err, ErrDenied) || raw != nil {
			t.Fatal("scoped role exported partial recovery", err)
		}
	})
	t.Run("generation", func(t *testing.T) {
		raw, err := p.admin.RecoveryNativeReviewSnapshot(testCtx, false, ContentHash("wrong generation"))
		if !errors.Is(err, ErrConflict) || raw != nil {
			t.Fatal("stale generation accepted", err)
		}
	})
	manifest := ContentHash("own synthetic restore")
	if err = p.admin.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	sys04Snapshot(t, p.admin, true, paused.Gate.Generation)
	if err = p.admin.VerifyNativeRecoveryReadback(testCtx, manifest, paused.Gate.Generation, *exported.NativeDynamics); err != nil {
		t.Fatal("real native readback", err)
	}
	t.Run("direct_native_read", func(t *testing.T) {
		out, err := p.worker.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, "recovery-version", time.Now().UnixNano()})
		if !errors.Is(err, ErrUnavailable) || out.Value != nil {
			t.Fatal("restore snapshot became a native read grant", err)
		}
	})
	if _, err = p.admin.q().Exec(testCtx, `DELETE FROM memory_storage_metadata WHERE key='restore_review_pending'`); err != nil {
		t.Fatal(err)
	}
	sys04Resume(t, p.admin)
	if err = p.admin.CheckRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("legacy review removal bypassed native review", err)
	}
	if _, err = p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, capture); !errors.Is(err, ErrUnavailable) {
		t.Fatal("old capture retry bypassed native review", err)
	}
}

func TestDynamicsRecoveryReadbackRejectsEachSixDomainPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "six-fields")
	_, capture := p.accept(t)
	p.process(t, capture)
	paused := sys04Pause(t, p.admin)
	exported := sys04Snapshot(t, p.admin, false, paused.Gate.Generation)
	manifest := ContentHash("six required actual fields")
	if err := p.admin.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"state", "receipt", "consumption", "event_cursor", "checkpoint", "dependency_index"} {
		t.Run(field, func(t *testing.T) {
			raw, err := json.Marshal(exported.NativeDynamics)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if json.Unmarshal(raw, &object) != nil {
				t.Fatal("fixture encoding")
			}
			scopes := object["scopes"].([]any)
			fields := scopes[0].(map[string]any)["fields"].(map[string]any)
			fields[field] = ContentHash("different " + field)
			raw, err = json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			var changed NativeRecoverySnapshot
			if json.Unmarshal(raw, &changed) != nil {
				t.Fatal("fixture decoding")
			}
			if err = p.admin.VerifyNativeRecoveryReadback(testCtx, manifest, paused.Gate.Generation, changed); !errors.Is(err, ErrConflict) {
				t.Fatal("different native restoration domain accepted", err)
			}
		})
	}
	if err := p.admin.VerifyNativeRecoveryReadback(testCtx, manifest, paused.Gate.Generation, *exported.NativeDynamics); err != nil {
		t.Fatal("valid positive after rejected inputs", err)
	}
}

func TestDynamicsRecoveryLegacyEmptyAndUnknownRemainDistinctPG(t *testing.T) {
	p := isolatedPostgres(t)
	paused := sys04Pause(t, p)
	exported := sys04Snapshot(t, p, false, paused.Gate.Generation)
	if len(exported.NativeDynamics.Scopes) != 0 {
		t.Fatal("empty synthetic database was not known empty")
	}
	manifest := ContentHash("empty native restore")
	if err := p.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	var nativePending bool
	if err := p.q().QueryRow(testCtx, `SELECT EXISTS(SELECT 1 FROM memory_storage_metadata WHERE key=$1)`, nativeRecoveryPendingKey).Scan(&nativePending); err != nil || nativePending {
		t.Fatal("verified legacy absence changed normal marker semantics", err)
	}
	if err := p.VerifyNativeRecoveryReadback(testCtx, manifest, paused.Gate.Generation, *exported.NativeDynamics); err != nil {
		t.Fatal(err)
	}
	unknown := *exported.NativeDynamics
	unknown.Scopes = nil
	if err := p.VerifyNativeRecoveryReadback(testCtx, manifest, paused.Gate.Generation, unknown); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown projection accepted as empty", err)
	}
}
