package memory

import (
	"context"
	"reflect"
	"strconv"
)

// Internal readback v2 binds each original row commit to its actual dependency
// set without retaining original bodies, IDs or Ref lists in recovery metadata.
type nativeRecoveryDependencyWitness struct {
	Key       string `json:"dependency_key"`
	Revision  int64  `json:"revision"`
	RecordSHA string `json:"record_sha256"`
}

func nativeRecoveryDependencyWitnesses(scope Scope, row Record) []nativeRecoveryDependencyWitness {
	out := []nativeRecoveryDependencyWitness{}
	for _, d := range row.NativeDynamics.Dependencies {
		out = append(out, nativeRecoveryDependencyWitness{nativeDependencyKey(scope, d.Ref), d.Ref.Revision, d.Hash})
	}
	return out
}
func nativeRecoveryDependencyWitnessesValid(deps []nativeRecoveryDependencyWitness) bool {
	if deps == nil || len(deps) > 500 {
		return false
	}
	seen := map[string]bool{}
	for _, d := range deps {
		key := nativeRecoveryCauseKey(d.Key, d.Revision)
		if !sourceHashPattern.MatchString(d.Key) || d.Revision < 1 || !sourceHashPattern.MatchString(d.RecordSHA) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func nativeRecoveryWitnessMap(proof nativeRecoveryReadback) map[string]nativeRecoveryWitness {
	out := map[string]nativeRecoveryWitness{}
	for _, w := range proof.Witnesses {
		out[nativeRecoveryWitnessKey(w.ScopeSHA, w.Commit.ID)] = w
	}
	return out
}
func nativeRecoveryCauseKey(key string, revision int64) string {
	return key + ":" + strconv.FormatInt(revision, 10)
}

// This map comes exclusively from actual source ledger validation and the
// existing erasure contracts on BOTH databases. SQL metadata rows may remain;
// an empty native payload or row count is never proof of canonical erasure.
func nativeRecoveryDeletionEvidenceTx(ctx context.Context, source, restored *Postgres, scope Scope) (map[string]bool, error) {
	if err := nativeRecoveryAuthoritativeDeletionsTx(ctx, source, restored, scope); err != nil {
		return nil, err
	}
	rows, err := source.DeletionRecords(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, row := range rows {
		if !validDeletionLedgerRow(row, scope) {
			return nil, ErrUnavailable
		}
		if row.Target.Revision > 0 {
			out[nativeRecoveryCauseKey(nativeDependencyKey(scope, *row.Target), row.Target.Revision)] = true
		}
	}
	return out, nil
}
func nativeRecoveryPurgeCauseTx(ctx context.Context, source *Postgres, scope Scope, row Record, causes map[string]bool) error {
	if row.NativeDynamics == nil {
		return ErrConflict
	}
	p := row.NativeDynamics.Purge
	if !nativeRecoveryCompletePurge(scope, row) || !causes[nativeRecoveryCauseKey(p.DependencyKey, p.ThroughRevision)] {
		return ErrConflict
	}
	original, err := nativeGetTx(ctx, source, scope, row.ID, "purge")
	if err != nil {
		return err
	}
	if !nativeRecoveryCompletePurge(scope, original) {
		return ErrConflict
	}
	cause := original.NativeDynamics.Purge
	if cause.DependencyKey != p.DependencyKey || cause.ThroughRevision != p.ThroughRevision {
		return ErrConflict
	}
	return nil
}
func nativeRecoveryCompletePurge(scope Scope, row Record) bool {
	if row.Scope != scope || row.Kind != nativeDynamicsPrefix+"purge" || row.Status != JobStatusDone || row.NativeDynamics == nil {
		return false
	}
	p := row.NativeDynamics.Purge
	return p != nil && p.Done && p.RoleIndex == len(nativePurgeRoles) && p.After == "" && row.ID == nativePurgeKeyID(scope, p.DependencyKey, p.ThroughRevision)
}
func nativeRecoveryScrubCauseTx(ctx context.Context, source *Postgres, scope Scope, row Record, witness nativeRecoveryWitness, causes map[string]bool) error {
	if !nativeRecoveryWitnessValid(witness) || witness.ScopeSHA != nativeRecoveryHash("scope", scope) || witness.Commit.ID != row.ID || witness.Commit.Kind != row.Kind || witness.Commit.Revision >= row.Revision || !nativeRecoveryTombstone(row.NativeDynamics) || row.Status != JobStatusDone {
		return ErrConflict
	}
	// Compare actual current original scrub data, not a supplied Reason or Done.
	original, err := source.Get(ctx, colJobs, scope, row.ID)
	if err != nil {
		return err
	}
	if original.Scope != scope || original.Kind != row.Kind || original.Status != JobStatusDone || !nativeRecoveryTombstone(original.NativeDynamics) || !nativeRecoverySameScrubbedRecord(original, row) {
		return ErrConflict
	}
	return nativeRecoveryMatchScrubDependencyTx(ctx, source, scope, witness.Dependencies, causes)
}
func nativeRecoveryMatchScrubDependencyTx(ctx context.Context, source *Postgres, scope Scope, deps []nativeRecoveryDependencyWitness, causes map[string]bool) error {
	for _, d := range deps {
		if !causes[nativeRecoveryCauseKey(d.Key, d.Revision)] {
			continue
		}
		purge, err := nativeGetTx(ctx, source, scope, nativePurgeKeyID(scope, d.Key, d.Revision), "purge")
		if err != nil {
			return err
		}
		if err = nativeRecoveryPurgeCauseTx(ctx, source, scope, purge, causes); err != nil {
			return err
		}
		return nil
	}
	// A prior scrub or revision-only correction has no reconstructable exact
	// deletion cause in this protocol. Keep review held; never invent one.
	return ErrConflict
}

func nativeRecoverySameScrubbedRecord(original, current Record) bool {
	if !nativeRecoveryClearServingData(original) || !nativeRecoveryClearServingData(current) {
		return false
	}
	// Independent replay changes the transaction observation timestamp/revision,
	// but must not rewrite other retained data or preserve serving bodies/refs.
	current.Revision = original.Revision
	current.UpdatedAt = original.UpdatedAt
	return reflect.DeepEqual(original, current)
}
func nativeRecoveryClearServingData(row Record) bool {
	return row.Text == "" && row.Summary == "" && row.Target == nil && len(row.Sources) == 0 && len(row.Relations) == 0 && len(row.Keywords) == 0 && len(row.KeywordsLower) == 0 && row.Embedding == nil
}
