package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type sys04CauseFixture struct {
	source, target         *nativePGFixture
	restored               *Postgres
	sourceGate, targetGate RecoveryWriteGateStatus
	manifest               string
	stateID                string
}

func sys04NewCauseFixture(t *testing.T) *sys04CauseFixture {
	t.Helper()
	return sys04NewCauseFixtureMode(t, false)
}
func sys04NewCauseFixtureMode(t *testing.T, preScrub bool) *sys04CauseFixture {
	t.Helper()
	p := nativePGSetup(t)
	p.activate(t, "roundtrip")
	_, capture := p.accept(t)
	p.process(t, capture)
	if preScrub {
		sys04DeleteActualMemory(t, p.admin, p.in.Scope, p.in.Target)
		sys04DrainNativePurges(t, p)
	}
	sg := sys04Pause(t, p.admin)
	snapshot := sys04Snapshot(t, p.admin, false, sg.Gate.Generation)
	restored := isolatedPostgresSchema(t, false)
	sys04DumpAndRestore(t, p.admin, restored)
	rg := gateStatus(t, restored)
	manifest := ContentHash("v2 hash-only current deletion cause")
	if err := restored.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	if err := restored.VerifyNativeRecoveryReadback(testCtx, manifest, rg.Gate.Generation, *snapshot.NativeDynamics); err != nil {
		t.Fatal("actual v2 readback", err)
	}
	input := sys04ProcessInput(t, p, restored, *snapshot.NativeDynamics, sg, rg, manifest)
	app := sys04OpenHelperDatabase(t, input.RestoredDSN, input.ExpectedPort, input.AppRole)
	repo, err := app.WithToken(p.workerToken)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	target := &nativePGFixture{admin: restored, app: app, workerRepo: repo, worker: &Service{Repo: repo, Auth: auth, NativeDynamics: &NativeDynamicsRuntime{Enabled: true}}, in: p.in, workerToken: p.workerToken}
	return &sys04CauseFixture{p, target, restored, sg, rg, manifest, p.rows(t, "state")[0].ID}
}
func (c *sys04CauseFixture) reconcile() error {
	return c.restored.ReconcileNativeRecovery(testCtx, c.source.admin, c.manifest, c.sourceGate.Gate.Generation, c.targetGate.Gate.Generation)
}
func (c *sys04CauseFixture) deleteBoth(t *testing.T) {
	t.Helper()
	sys04Resume(t, c.source.admin)
	sys04Resume(t, c.restored)
	for _, p := range []*nativePGFixture{c.source, c.target} {
		sys04DeleteActualMemory(t, p.admin, p.in.Scope, p.in.Target)
		sys04DrainNativePurges(t, p)
	}
	c.sourceGate = sys04Pause(t, c.source.admin)
	c.targetGate = sys04Pause(t, c.restored)
}
func (c *sys04CauseFixture) held(t *testing.T) {
	t.Helper()
	if err := c.reconcile(); err == nil {
		t.Fatal("unproven source deletion cause cleared native review")
	}
	if err := c.restored.checkNativeRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("failed review lost native marker", err)
	}
}
func sys04MutateReadbackProof(t *testing.T, c *sys04CauseFixture, fn func(*nativeRecoveryReadback)) {
	t.Helper()
	var raw []byte
	if err := c.restored.q().QueryRow(testCtx, `SELECT value FROM memory_storage_metadata WHERE key=$1`, nativeRecoveryReadbackKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var proof nativeRecoveryReadback
	if err := json.Unmarshal(raw, &proof); err != nil {
		t.Fatal(err)
	}
	fn(&proof)
	raw, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.restored.q().Exec(testCtx, `UPDATE memory_storage_metadata SET value=$2 WHERE key=$1`, nativeRecoveryReadbackKey, raw); err != nil {
		t.Fatal(err)
	}
}
func sys04AlterWitnessDependency(c *sys04CauseFixture, proof *nativeRecoveryReadback, mode string) {
	for i := range proof.Witnesses {
		w := &proof.Witnesses[i]
		if w.Commit.ID != c.stateID {
			continue
		}
		if mode == "missing_dependency_witness" {
			w.Dependencies = nil
			return
		}
		wanted := nativeDependencyKey(c.source.in.Scope, c.source.in.Target)
		for j := range w.Dependencies {
			d := &w.Dependencies[j]
			if d.Key != wanted {
				continue
			}
			if mode == "wrong_witness_revision" {
				d.Revision++
				return
			}
			foreign := c.source.in.Scope
			foreign.UserID = "other-witness-scope"
			d.Key = nativeDependencyKey(foreign, c.source.in.Target)
			return
		}
	}
}
func sys04ScrubActualState(t *testing.T, p *nativePGFixture, id string) {
	t.Helper()
	if err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		row, err := tx.Get(ctx, colJobs, p.in.Scope, id)
		if err != nil {
			return err
		}
		return nativeScrubTx(ctx, tx, row, "target_deleted")
	}); err != nil {
		t.Fatal("actual malformed stored state preparation", err)
	}
}
func sys04ModifySourcePurge(t *testing.T, c *sys04CauseFixture, mode string) {
	t.Helper()
	sys04Resume(t, c.source.admin)
	id := nativePurgeID(c.source.in.Scope, c.source.in.Target)
	if mode == "source_purge_absent" {
		if _, err := c.source.admin.q().Exec(testCtx, `DELETE FROM memory_records WHERE collection='jobs' AND id=$1`, id); err != nil {
			t.Fatal(err)
		}
	} else {
		row, err := c.source.admin.Get(testCtx, colJobs, c.source.in.Scope, id)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "source_purge_pending" {
			row.NativeDynamics.Purge.Done = false
			row.Status = JobStatusPending
		} else {
			row.NativeDynamics.Purge.ThroughRevision++
		}
		row.Revision++
		if err = c.source.admin.Replace(testCtx, colJobs, row, row.Revision-1); err != nil {
			t.Fatal(err)
		}
	}
	c.sourceGate = sys04Pause(t, c.source.admin)
}
func sys04CauseNegative(t *testing.T, mode string) {
	t.Helper()
	c := sys04NewCauseFixture(t)
	if mode == "fake_done_without_ledger" || mode == "unrelated_actual_cause" || mode == "other_scope_actual_cause" {
		sys04UnrelatedOrFakeCause(t, c, mode)
		c.held(t)
		return
	}
	if mode == "source_active_no_cause" {
		sys04Resume(t, c.restored)
		sys04ScrubActualState(t, c.target, c.stateID)
		c.targetGate = sys04Pause(t, c.restored)
		c.held(t)
		return
	}
	c.deleteBoth(t)
	switch mode {
	case "wrong_witness_scope", "wrong_witness_revision", "missing_dependency_witness":
		sys04MutateReadbackProof(t, c, func(p *nativeRecoveryReadback) { sys04AlterWitnessDependency(c, p, mode) })
	case "old_readback_protocol":
		sys04MutateReadbackProof(t, c, func(p *nativeRecoveryReadback) { p.Protocol = nativeRecoveryProtocol })
	case "old_readback_key_only":
		if _, err := c.restored.q().Exec(testCtx, `UPDATE memory_storage_metadata SET key='native_restore_readback_v1' WHERE key=$1`, nativeRecoveryReadbackKey); err != nil {
			t.Fatal(err)
		}
	case "source_purge_absent", "source_purge_pending", "source_purge_wrong_revision":
		sys04ModifySourcePurge(t, c, mode)
	case "wrong_source_identity":
		if _, err := c.source.admin.q().Exec(testCtx, `UPDATE memory_storage_metadata SET value=decode(repeat('cd',32),'hex') WHERE key='storage_identity'`); err != nil {
			t.Fatal(err)
		}
	case "restored_wrong_epoch", "retained_body", "retained_ref":
		sys04Resume(t, c.restored)
		row, err := c.restored.Get(testCtx, colJobs, c.target.in.Scope, c.stateID)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "retained_body" {
			row.Text = "body must not remain in retained SQL metadata"
		} else if mode == "retained_ref" {
			row.Target = &c.source.in.Target
		} else {
			row.NativeDynamics.State.Epoch++
		}
		row.Revision++
		if err = c.restored.Replace(testCtx, colJobs, row, row.Revision-1); err != nil {
			t.Fatal(err)
		}
		c.targetGate = sys04Pause(t, c.restored)
	}
	c.held(t)
}
func TestDynamicsRecoveryV2DeletionCauseNegativesPG(t *testing.T) {
	for _, mode := range []string{"source_active_no_cause", "fake_done_without_ledger", "unrelated_actual_cause", "other_scope_actual_cause", "wrong_witness_scope", "wrong_witness_revision", "missing_dependency_witness", "old_readback_protocol", "old_readback_key_only", "source_purge_absent", "source_purge_pending", "source_purge_wrong_revision", "wrong_source_identity", "restored_wrong_epoch", "retained_body", "retained_ref"} {
		t.Run(mode, func(t *testing.T) { sys04CauseNegative(t, mode) })
	}
}
func TestDynamicsRecoveryV2ActualDeletionAndRetainedMetadataPG(t *testing.T) {
	c := sys04NewCauseFixture(t)
	c.deleteBoth(t)
	// Existing canonical deletion contract, not absence of every SQL row.
	if _, err := c.source.admin.Get(testCtx, colMemories, c.source.in.Scope, c.source.in.Target.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("source serving record not erased", err)
	}
	if _, err := c.restored.Get(testCtx, colMemories, c.target.in.Scope, c.target.in.Target.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("restored serving record not erased", err)
	}
	row, err := c.restored.Get(testCtx, colJobs, c.target.in.Scope, c.stateID)
	if err != nil || row.NativeDynamics.State == nil || row.NativeDynamics.State.Value != nil || len(row.NativeDynamics.Dependencies) != 0 {
		t.Fatal("retained metadata cleanup", err)
	}
	if err = c.reconcile(); err != nil {
		t.Fatal("supported actual deletion replay rejected", err)
	}
	if err = c.restored.checkNativeRestorePublication(testCtx); err != nil {
		t.Fatal("native marker not cleared", err)
	}
	if err = c.restored.checkRestorePublicationMarker(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("general marker was automatically cleared", err)
	}
}
func TestDynamicsRecoveryV2CorrectionWithoutDeletionRemainsHeldPG(t *testing.T) {
	c := sys04NewCauseFixture(t)
	for _, p := range []*nativePGFixture{c.source, c.target} {
		sys04Resume(t, p.admin)
		row, err := p.admin.Get(testCtx, colMemories, p.in.Scope, p.in.Target.ID)
		if err != nil {
			t.Fatal(err)
		}
		row.Summary = "actual revision, not deletion"
		row.Revision++
		row.UpdatedAt = time.Now().UTC()
		if err = p.admin.Replace(testCtx, colMemories, row, row.Revision-1); err != nil {
			t.Fatal(err)
		}
		sys04DrainNativePurges(t, p)
	}
	c.sourceGate = sys04Pause(t, c.source.admin)
	c.targetGate = sys04Pause(t, c.restored)
	c.held(t)
}

func sys04UnrelatedOrFakeCause(t *testing.T, c *sys04CauseFixture, mode string) {
	t.Helper()
	sys04Resume(t, c.source.admin)
	sys04Resume(t, c.restored)
	if mode == "fake_done_without_ledger" {
		ref := c.source.in.Target
		key := nativeDependencyKey(c.source.in.Scope, ref)
		row := nativeJob(c.source.in.Scope, "purge", nativePurgeID(c.source.in.Scope, ref), NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Purge: &NativeDynamicsPurge{DependencyKey: key, ThroughRevision: ref.Revision, RoleIndex: len(nativePurgeRoles), Done: true}, DependencyKeys: []string{key}, Reason: "target_deleted"})
		if err := c.source.admin.Insert(testCtx, colJobs, row); err != nil {
			t.Fatal(err)
		}
	} else {
		scope := c.source.in.Scope
		if mode == "other_scope_actual_cause" {
			scope.UserID = "unrelated-cause-user"
			scope.AgentID = "unrelated-cause-agent"
		}
		other, _ := sys04ScopedNativeFixture(t, c.source, scope, "unrelated-cause")
		other.activate(t, "other-cause")
		sys04DeleteActualMemory(t, other.admin, other.in.Scope, other.in.Target)
		sys04DrainNativePurges(t, other)
	}
	// Even a source-side lookalike and a done marker must not replace actual
	// deletion of one of THIS retained row's witnessed dependencies.
	for _, p := range []*nativePGFixture{c.source, c.target} {
		sys04ScrubActualState(t, p, c.stateID)
	}
	if row, err := c.source.admin.Get(testCtx, colMemories, c.source.in.Scope, c.source.in.Target.ID); err != nil || row.Status != "active" {
		t.Fatal("test target source must remain active", err)
	}
	c.sourceGate = sys04Pause(t, c.source.admin)
	c.targetGate = sys04Pause(t, c.restored)
}
func TestDynamicsRecoveryV2PreScrubWithoutOriginalDependencyHeldPG(t *testing.T) {
	c := sys04NewCauseFixtureMode(t, true)
	c.held(t)
}
