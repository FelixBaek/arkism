package memory

import (
	"context"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ReconcileNativeRecovery checks explicit operator-provided current and
// restored databases. The original source's durable paused generation stays
// locked while the restored transaction checks canonical deletion/proof/ACL
// state. It clears only the native marker; general publication review remains.
func (p *Postgres) ReconcileNativeRecovery(ctx context.Context, source *Postgres, manifest, sourceGeneration, restoredGeneration string) error {
	if p == nil || source == nil || p == source || p.pool == nil || source.pool == nil || p.tx != nil || source.tx != nil || !sourceHashPattern.MatchString(manifest) || !sourceHashPattern.MatchString(sourceGeneration) || !sourceHashPattern.MatchString(restoredGeneration) {
		return ErrInvalid
	}
	lease, err := source.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return pgErr(err)
	}
	defer func() { _ = lease.Rollback(ctx) }() // Source is read-only; no cross-database commit is asserted.
	if err = disableSnapshotRowSecurity(ctx, lease); err != nil {
		return err
	}
	current := &Postgres{pool: source.pool, tx: lease}
	if err = current.checkPausedRecoveryGate(ctx, sourceGeneration); err != nil {
		return err
	}
	return p.nativeRecoveryTransaction(ctx, restoredGeneration, func(restored *Postgres) error {
		return nativeReconcileRecoveryTx(ctx, current, restored, manifest)
	})
}

func nativeReconcileRecoveryTx(ctx context.Context, source, restored *Postgres, manifest string) error {
	marker, err := restored.recoverySnapshotManifest(ctx, true)
	if err != nil {
		return err
	}
	if string(marker) != manifest {
		return ErrConflict
	}
	proof, err := restored.nativeRecoveryReadbackTx(ctx, manifest)
	if err != nil {
		return err
	}
	identity, err := source.nativeRecoveryIdentityTx(ctx)
	if err != nil {
		return err
	}
	if proof.SourceIdentity != identity {
		return ErrConflict
	}
	if err = restored.nativeRecoveryCheckWitnessesTx(ctx, source, proof); err != nil {
		return err
	}
	if err = nativeRecoverySameAuthorizationTx(ctx, source, restored); err != nil {
		return err
	}
	scopes, err := recoverySnapshotScopes(ctx, restored)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if err = nativeRecoveryCurrentScopeTx(ctx, source, restored, scope, proof); err != nil {
			return err
		}
	}
	// Source-only scope deletions must not disappear because a restored database
	// happens to have no ordinary record there.
	sourceScopes, err := recoverySnapshotScopes(ctx, source)
	if err != nil {
		return err
	}
	for _, scope := range sourceScopes {
		if err = nativeRecoveryAuthoritativeDeletionsTx(ctx, source, restored, scope); err != nil {
			return err
		}
	}
	// Prove the explicit current connection is still usable immediately before
	// changing the native review marker. No success is inferred on connection loss.
	if _, err = source.nativeRecoveryIdentityTx(ctx); err != nil {
		return err
	}
	_, err = restored.q().Exec(ctx, `DELETE FROM memory_storage_metadata WHERE key=$1 AND value=$2`, nativeRecoveryPendingKey, []byte(manifest))
	return pgErr(err)
}

func nativeRecoveryAuthoritativeDeletionsTx(ctx context.Context, source, restored *Postgres, scope Scope) error {
	if _, err := buildDeletionLedger(ctx, source, scope, 2, func(string, string) error { return nil }); err != nil {
		return err
	}
	rows, err := source.DeletionRecords(ctx, scope)
	if err != nil {
		return err
	}
	if len(rows) > deletionLedgerLimit {
		return ErrUnavailable
	}
	for _, row := range rows {
		if !validDeletionLedgerRow(row, scope) {
			return ErrUnavailable
		}
		if row.Kind == workDeletionKind {
			if err = verifyWorkPrivacyImpact(ctx, restored, scope, PrivacyImpact{Records: []Ref{*row.Target}}); err != nil {
				return err
			}
		} else if err = erasedTargetAbsent(ctx, restored, row); err != nil {
			return err
		}
	}
	return nil
}

func nativeRecoveryCurrentScopeTx(ctx context.Context, source, restored *Postgres, scope Scope, proof nativeRecoveryReadback) error {
	causes, err := nativeRecoveryDeletionEvidenceTx(ctx, source, restored, scope)
	if err != nil {
		return err
	}
	witnesses := nativeRecoveryWitnessMap(proof)
	_, rows, _, err := restored.nativeRecoveryScopeTx(ctx, scope)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err = nativeRecoveryCurrentRowTx(ctx, source, restored, scope, row.record, witnesses[nativeRecoveryWitnessKey(nativeRecoveryHash("scope", scope), row.record.ID)], causes); err != nil {
			return err
		}
	}
	return nil
}

func nativeRecoveryCurrentRowTx(ctx context.Context, source, restored *Postgres, scope Scope, row Record, witness nativeRecoveryWitness, causes map[string]bool) error {
	role := strings.TrimPrefix(row.Kind, nativeDynamicsPrefix)
	p := row.NativeDynamics
	if role == "purge" {
		return nativeRecoveryPurgeCauseTx(ctx, source, scope, row, causes)
	}
	if handled, err := nativeRecoverySpecialRow(scope, row, role); handled {
		return err
	}
	if nativeRecoveryTombstone(p) {
		return nativeRecoveryScrubCauseTx(ctx, source, scope, row, witness, causes)
	}
	if p.Key.Scope != scope || p.Key.Target.Collection != colMemories || !validWorkCheckpointRef(p.Key.Target) || !nativeDimension(p.Key.Dimension) || !validRunLabel(p.Key.Version, 128) || len(p.Dependencies) == 0 {
		return ErrUnavailable
	}
	if err := nativeValidateProofTx(ctx, source, scope, p.Dependencies); err != nil {
		return err
	}
	if err := nativeValidateProofTx(ctx, restored, scope, p.Dependencies); err != nil {
		return err
	}
	if role == "capture" {
		if p.Input == nil || p.Input.Scope != scope || p.Stamp == nil || p.Evaluation.Collection != "evaluations" || row.ID != nativeCaptureID(scope, p.Evaluation) {
			return ErrUnavailable
		}
		if err := nativeValidateCaptureSourceTx(ctx, source, scope, p); err != nil {
			return err
		}
		return nativeValidateCaptureSourceTx(ctx, restored, scope, p)
	}
	return nil
}

func nativeRecoveryTombstone(p *NativeDynamicsPayload) bool {
	if p == nil || p.Protocol != nativeDynamicsProtocol || p.Reason == "" {
		return false
	}
	expected := NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, IdentitySHA: p.IdentitySHA, ClaimSHA: p.ClaimSHA, Reason: p.Reason}
	if p.State != nil {
		if p.State.Epoch < 1 {
			return false
		}
		expected.State = &NativeDynamicsState{Status: "invalidated", Epoch: p.State.Epoch, Anchor: "invalidated"}
	}
	return reflect.DeepEqual(p, &expected)
}

var nativeRecoveryAuthorizationTables = []string{"principals", "organization_memberships", "project_memberships", "principal_permissions", "principal_token_history"}

func nativeRecoveryAuthorizationDigestTx(ctx context.Context, p *Postgres) (string, error) {
	commits := map[string][]string{}
	budget := 0
	for _, table := range nativeRecoveryAuthorizationTables {
		// Table is the fixed local allowlist above, never input or a database value.
		rows, err := p.q().Query(ctx, `SELECT to_jsonb(t)::text FROM `+pgx.Identifier{table}.Sanitize()+` t ORDER BY to_jsonb(t)::text COLLATE "C" LIMIT $1`, nativeRecoveryRowsLimit+1)
		if err != nil {
			return "", pgErr(err)
		}
		values := []string{}
		for rows.Next() {
			var raw string
			if rows.Scan(&raw) != nil {
				rows.Close()
				return "", ErrUnavailable
			}
			budget += len(raw)
			if budget > recoverySnapshotByteLimit || len(values) == nativeRecoveryRowsLimit {
				rows.Close()
				return "", ErrUnavailable
			}
			values = append(values, ContentHash(raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", pgErr(err)
		}
		commits[table] = values
	}
	return nativeRecoveryHash("current_authorization", commits), nil
}

func nativeRecoverySameAuthorizationTx(ctx context.Context, source, restored *Postgres) error {
	a, err := nativeRecoveryAuthorizationDigestTx(ctx, source)
	if err != nil {
		return err
	}
	b, err := nativeRecoveryAuthorizationDigestTx(ctx, restored)
	if err != nil {
		return err
	}
	if a != b {
		return ErrConflict
	}
	return nil
}

func (p *Postgres) nativeRecoveryWitnessesTx(ctx context.Context, scopes []NativeRecoveryScope) ([]nativeRecoveryWitness, error) {
	out := []nativeRecoveryWitness{}
	for _, item := range scopes {
		rows, err := p.nativeRecoveryRowsTx(ctx, item.Scope)
		if err != nil {
			return nil, err
		}
		scopeSHA := nativeRecoveryHash("scope", item.Scope)
		for _, row := range rows {
			out = append(out, nativeRecoveryWitness{ScopeSHA: scopeSHA, Commit: row.commit, Dependencies: nativeRecoveryDependencyWitnesses(item.Scope, row.record)})
		}
	}
	return out, nil
}

func nativeRecoveryWitnessKey(scopeSHA, id string) string { return scopeSHA + "\x00" + id }

func nativeRecoveryWitnessValid(w nativeRecoveryWitness) bool {
	role := strings.TrimPrefix(w.Commit.Kind, nativeDynamicsPrefix)
	_, known := nativeRecoveryRoleField[role]
	return nativeRecoveryDependencyWitnessesValid(w.Dependencies) && sourceHashPattern.MatchString(w.ScopeSHA) && known && strings.HasPrefix(w.Commit.Kind, nativeDynamicsPrefix) && validRunLabel(w.Commit.ID, 256) && w.Commit.Revision > 0 && sourceHashPattern.MatchString(w.Commit.PayloadSHA) && sourceHashPattern.MatchString(w.Commit.DocumentSHA)
}

func (p *Postgres) nativeRecoveryCheckWitnessesTx(ctx context.Context, source *Postgres, proof nativeRecoveryReadback) error {
	expected := map[string]nativeRecoveryWitness{}
	for _, w := range proof.Witnesses {
		key := nativeRecoveryWitnessKey(w.ScopeSHA, w.Commit.ID)
		if !nativeRecoveryWitnessValid(w) {
			return ErrUnavailable
		}
		if _, exists := expected[key]; exists {
			return ErrUnavailable
		}
		expected[key] = w
	}
	scopes, err := recoverySnapshotScopes(ctx, p)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if err = p.nativeRecoveryCheckScopeWitnessesTx(ctx, source, scope, expected); err != nil {
			return err
		}
	}
	if len(expected) != 0 {
		return ErrConflict
	}
	return nil
}

func nativeRecoverySpecialRow(scope Scope, row Record, role string) (bool, error) {
	p := row.NativeDynamics
	switch role {
	case "catalog":
		if row.ID != nativeCatalogID(scope) || p.Reason != "native_feature_catalog_v1" {
			return true, ErrUnavailable
		}
		return true, nil
	case "config":
		if p.Config == nil || !nativeConfigValid(*p.Config) || row.ID != nativeConfigID(scope, p.Config.Version) || p.ConfigHash != nativeConfigHash(*p.Config) {
			return true, ErrUnavailable
		}
		return true, nil
	case "purge":
		if p.Purge == nil || !p.Purge.Done || row.Status != JobStatusDone || row.ID != nativePurgeKeyID(scope, p.Purge.DependencyKey, p.Purge.ThroughRevision) {
			return true, ErrConflict
		}
		return true, nil
	}
	return false, nil
}

func (p *Postgres) nativeRecoveryCheckScopeWitnessesTx(ctx context.Context, source *Postgres, scope Scope, expected map[string]nativeRecoveryWitness) error {
	rows, err := p.nativeRecoveryRowsTx(ctx, scope)
	if err != nil {
		return err
	}
	causes, err := nativeRecoveryDeletionEvidenceTx(ctx, source, p, scope)
	if err != nil {
		return err
	}
	scopeSHA := nativeRecoveryHash("scope", scope)
	for _, row := range rows {
		key := nativeRecoveryWitnessKey(scopeSHA, row.record.ID)
		old, exists := expected[key]
		if exists && old.Commit == row.commit {
			delete(expected, key)
			continue
		}
		if row.record.Kind == nativeDynamicsPrefix+"purge" {
			if err = nativeRecoveryPurgeCauseTx(ctx, source, scope, row.record, causes); err != nil {
				return err
			}
		} else {
			if !exists {
				return ErrConflict
			}
			if err = nativeRecoveryScrubCauseTx(ctx, source, scope, row.record, old, causes); err != nil {
				return err
			}
		}
		delete(expected, key)
	}
	return nil
}
