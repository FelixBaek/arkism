package memory

import (
	"context"
	"errors"
	"math"
)

var nativePurgeRoles = []string{"state", "capture", "event", "seen", "consumption", "activation", "checkpoint", "enrollment"}

func nativePurgeKeyID(scope Scope, key string, rev int64) string {
	return nativeID("purge", struct {
		Scope    Scope
		Key      string
		Revision int64
	}{scope, key, rev})
}
func nativePurgeID(scope Scope, ref Ref) string {
	return nativePurgeKeyID(scope, nativeDependencyKey(scope, ref), ref.Revision)
}

// The invalidation fence and resumable purge intent commit in the source owner's transaction.
func invalidateNativeDynamicsDependencyTx(ctx context.Context, tx Tx, scope Scope, ref Ref, reason string) error {
	enabled, e := nativeCatalogTx(ctx, tx, scope)
	if e != nil {
		return e
	}
	if !enabled {
		return nil
	}
	if _, ok := tx.(nativeDynamicsTxQueries); !ok {
		return ErrUnavailable
	}
	id := nativePurgeID(scope, ref)
	old, e := nativeGetTx(ctx, tx, scope, id, "purge")
	if e == nil {
		if old.NativeDynamics.Purge == nil || old.NativeDynamics.Purge.DependencyKey != nativeDependencyKey(scope, ref) || old.NativeDynamics.Purge.ThroughRevision != ref.Revision {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(e, ErrNotFound) {
		return e
	}
	row := nativeJob(scope, "purge", id, NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Purge: &NativeDynamicsPurge{DependencyKey: nativeDependencyKey(scope, ref), ThroughRevision: ref.Revision}, DependencyKeys: []string{nativeDependencyKey(scope, ref)}, Reason: reason})
	row.Status = JobStatusPending
	return nativeInsertTx(ctx, tx, row)
}
func nativeRejectInvalidatedTx(ctx context.Context, tx Tx, scope Scope, ref Ref) error {
	row, e := nativeGetTx(ctx, tx, scope, nativePurgeID(scope, ref), "purge")
	if errors.Is(e, ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if row.NativeDynamics.Purge == nil || row.NativeDynamics.Purge.DependencyKey != nativeDependencyKey(scope, ref) || row.NativeDynamics.Purge.ThroughRevision != ref.Revision {
		return ErrUnavailable
	}
	return ErrConflict
}
func nativePurgePendingTx(ctx context.Context, tx Tx, scope Scope) (bool, error) {
	enabled, e := nativeCatalogTx(ctx, tx, scope)
	if e != nil {
		return false, e
	}
	if !enabled {
		return false, nil
	}
	q, ok := tx.(nativeDynamicsTxQueries)
	if !ok {
		return false, ErrUnavailable
	}
	rows, e := q.NativeDynamicsPendingPurges(ctx, scope, 1)
	return len(rows) > 0, e
}
func nativePurgeMatches(p *NativeDynamicsPayload, scope Scope, key string, revision int64) bool {
	for _, d := range p.Dependencies {
		if nativeDependencyKey(scope, d.Ref) == key && d.Ref.Revision <= revision {
			return true
		}
	}
	return false
}
func nativeScrubTx(ctx context.Context, tx Tx, row Record, reason string) error {
	p, e := nativePayload(row)
	if e != nil {
		return e
	}
	tomb := NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, IdentitySHA: p.IdentitySHA, ClaimSHA: p.ClaimSHA, Reason: reason}
	if p.State != nil {
		epoch := p.State.Epoch
		if epoch < math.MaxInt64 {
			epoch++
		}
		tomb.State = &NativeDynamicsState{Status: "invalidated", Epoch: epoch, Anchor: "invalidated"}
	}
	row.NativeDynamics = &tomb
	row.Status = JobStatusDone
	row.Target = nil
	row.Sources = nil
	row.Relations = nil
	row.Text = ""
	row.Summary = ""
	return nativeReplaceTx(ctx, tx, row)
}

// Each call commits at most one bounded role page and its cursor. No nested pool use.
func (s *Service) ProcessNativeDynamicsPurge(ctx context.Context, token string, scope Scope, purge Ref, limit int) (bool, error) {
	if purge.Collection != colJobs || !validRunLabel(purge.ID, 256) || purge.Revision < 1 || limit < 1 || limit > 64 || !validScope(scope) {
		return false, ErrInvalid
	}
	ctx, e := s.nativeMaintenanceActor(ctx, token, scope, "write:jobs")
	if e != nil {
		return false, e
	}
	done := false
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		done = false
		var transactionErr error
		done, transactionErr = nativeProcessPurgeTx(ctx, tx, scope, purge, limit)
		return transactionErr
	})
	if e != nil {
		return false, e
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	return done, nil
}

func nativeCatalogID(scope Scope) string { return nativeID("catalog", scope) }
func nativeCatalogTx(ctx context.Context, tx Tx, scope Scope) (bool, error) {
	row, e := tx.Get(ctx, colJobs, scope, nativeCatalogID(scope))
	if errors.Is(e, ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if row.Kind != nativeDynamicsPrefix+"catalog" || row.NativeDynamics == nil || row.NativeDynamics.Protocol != nativeDynamicsProtocol || row.NativeDynamics.Reason != "native_feature_catalog_v1" {
		return false, ErrUnavailable
	}
	return true, nil
}
func nativeEnsureCatalogTx(ctx context.Context, tx Tx, scope Scope) error {
	exists, e := nativeCatalogTx(ctx, tx, scope)
	if e != nil {
		return e
	}
	if exists {
		return nil
	}
	return nativeInsertTx(ctx, tx, nativeJob(scope, "catalog", nativeCatalogID(scope), NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Reason: "native_feature_catalog_v1"}))
}

func nativeProcessPurgeTx(ctx context.Context, tx Tx, scope Scope, purge Ref, limit int) (done bool, resultErr error) {
	row, e := nativeGetTx(ctx, tx, scope, purge.ID, "purge")
	if e != nil {
		return done, e
	}
	p := row.NativeDynamics
	cursor := p.Purge
	if cursor == nil || row.Revision < purge.Revision || nativePurgeKeyID(scope, cursor.DependencyKey, cursor.ThroughRevision) != row.ID || cursor.RoleIndex < 0 || cursor.RoleIndex > len(nativePurgeRoles) {
		return done, ErrConflict
	}
	if cursor.Done {
		done = true
		return done, nil
	}
	if cursor.RoleIndex == len(nativePurgeRoles) {
		return done, ErrConflict
	}
	q, ok := tx.(nativeDynamicsTxQueries)
	if !ok {
		return done, ErrUnavailable
	}
	rows, e := q.NativeDynamicsRows(ctx, scope, nativePurgeRoles[cursor.RoleIndex], cursor.DependencyKey, cursor.After, limit)
	if e != nil {
		return done, e
	}
	if e := nativeScrubPurgePageTx(ctx, tx, scope, p, cursor, rows); e != nil {
		return done, e
	}
	if len(rows) < limit {
		cursor.RoleIndex++
		cursor.After = ""
	}
	if cursor.RoleIndex == len(nativePurgeRoles) {
		cursor.Done = true
		row.Status = JobStatusDone
		done = true
	}
	return done, nativeReplaceTx(ctx, tx, row)
}

func nativeScrubPurgePageTx(ctx context.Context, tx Tx, scope Scope, p *NativeDynamicsPayload, cursor *NativeDynamicsPurge, rows []Record) error {
	for _, r := range rows {
		if nativePurgeMatches(r.NativeDynamics, scope, cursor.DependencyKey, cursor.ThroughRevision) {
			if e := nativeScrubTx(ctx, tx, r, p.Reason); e != nil {
				return e
			}
		}
		cursor.After = r.ID
	}
	return nil
}
