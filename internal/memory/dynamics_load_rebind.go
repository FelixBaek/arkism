package memory

import (
	"context"
	"math"

	"github.com/jackc/pgx/v5"
)

// dynamicsLedgerRecoveryLimit bounds terminal history read in one offline pass.
const dynamicsLedgerRecoveryLimit = 4096

// ReconcileDynamicsLoadRecovery is the offline operator step for a restored
// copy whose queue still names the original storage identity. The current
// original stays paused and read-only while the restored ledger (terminal
// history, active entries and their captures) must equal it byte for byte.
// Only then are the coordinator and active entries rebound to the restored
// identity and every copied lease fenced. The restored copy is open for this
// maintenance write (the write gate refuses it while paused), but must still
// hold the general restore review: its queue cannot admit or claim before the
// rebind, nor publish after it. No hold is cleared here.
func (p *Postgres) ReconcileDynamicsLoadRecovery(ctx context.Context, source *Postgres, scope Scope, sourceGeneration string) error {
	if p == nil || source == nil || p == source || p.pool == nil || source.pool == nil || p.tx != nil || source.tx != nil || !validScope(scope) || !sourceHashPattern.MatchString(sourceGeneration) {
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
	sourceIdentity, want, err := dynamicsRecoverySourceLedger(ctx, current, scope, sourceGeneration)
	if err != nil {
		return err
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return pgErr(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = disableSnapshotRowSecurity(ctx, tx); err != nil {
		return err
	}
	restored := &Postgres{pool: p.pool, tx: tx}
	identity, err := dynamicsRecoveryRestoredMatch(ctx, restored, scope, sourceIdentity, want)
	if err != nil {
		return err
	}
	// No success is inferred if the current original became unreadable.
	if again, e := current.BlobStorageIdentity(ctx); e != nil || again != sourceIdentity {
		return ErrConflict
	}
	if err = dynamicsRebindTx(ctx, restored, scope, identity); err != nil {
		return err
	}
	return pgErr(tx.Commit(ctx))
}

// dynamicsRecoverySourceLedger reads the paused original's identity and ledger digest.
func dynamicsRecoverySourceLedger(ctx context.Context, current *Postgres, scope Scope, generation string) (string, string, error) {
	if err := current.checkPausedRecoveryGate(ctx, generation); err != nil {
		return "", "", err
	}
	if _, err := current.recoverySnapshotManifest(ctx, false); err != nil {
		return "", "", err
	}
	identity, err := current.BlobStorageIdentity(ctx)
	if err != nil {
		return "", "", err
	}
	want, err := dynamicsLedgerDigestTx(ctx, current, scope, identity)
	return identity, want, err
}

// dynamicsRecoveryRestoredMatch returns the restored copy's own identity once its
// ledger, still bound to the original identity, equals the original's digest.
func dynamicsRecoveryRestoredMatch(ctx context.Context, restored *Postgres, scope Scope, sourceIdentity, want string) (string, error) {
	if _, err := restored.recoverySnapshotManifest(ctx, true); err != nil {
		return "", err
	}
	identity, err := restored.BlobStorageIdentity(ctx)
	if err != nil {
		return "", err
	}
	if identity == sourceIdentity {
		return "", ErrConflict
	}
	got, err := dynamicsLedgerDigestTx(ctx, restored, scope, sourceIdentity)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", ErrConflict
	}
	return identity, nil
}

// dynamicsLedgerDigestTx commits the coordinator, every terminal entry and the
// active queue (with captures). The coordinator must name the expected identity.
func dynamicsLedgerDigestTx(ctx context.Context, tx *Postgres, scope Scope, identity string) (string, error) {
	c, err := dynamicsLockCoordinator(ctx, tx, scope)
	if err != nil {
		return "", err
	}
	p := c.DynamicsLoad
	if !dynamicsCoordinatorCounters(p) || p.StorageIdentity != identity || p.Head > dynamicsLedgerRecoveryLimit {
		return "", ErrConflict
	}
	raw, err := storeMarshal(c)
	if err != nil {
		return "", err
	}
	active, err := dynamicsRecoveryQueueDigestTx(ctx, tx, scope, p, string(raw))
	if err != nil {
		return "", err
	}
	commitments := []string{active}
	for ordinal := int64(1); ordinal <= p.Head; ordinal++ {
		row, e := tx.DynamicsLoadLock(ctx, scope, dynamicsEntryID(scope, ordinal))
		if e != nil {
			return "", e
		}
		if e = dynamicsValidateRow(row, scope, "entry"); e != nil {
			return "", e
		}
		if row.DynamicsLoad.Ordinal != ordinal || row.Status != JobStatusDone && row.Status != JobStatusFailed {
			return "", ErrConflict
		}
		entry, e := storeMarshal(row)
		if e != nil {
			return "", e
		}
		commitments = append(commitments, string(entry))
	}
	return ContentHash(structuredText(commitments)), nil
}

// dynamicsRebindTx moves the live queue to the restored identity. A new lease
// generation and revisions reject any claim copied by the dump.
func dynamicsRebindTx(ctx context.Context, tx *Postgres, scope Scope, identity string) error {
	c, err := dynamicsLockCoordinator(ctx, tx, scope)
	if err != nil {
		return err
	}
	p := c.DynamicsLoad
	for ordinal := p.Head + 1; ordinal <= p.Tail; ordinal++ {
		row, e := tx.DynamicsLoadLock(ctx, scope, dynamicsEntryID(scope, ordinal))
		if e != nil {
			return e
		}
		row.DynamicsLoad.StorageIdentity = identity
		row.DynamicsLoad.LeaseToken = ""
		row.DynamicsLoad.LeaseUntilNS = 0
		if e = dynamicsReplace(ctx, tx, row); e != nil {
			return e
		}
	}
	if p.LeaseGeneration == math.MaxInt64 {
		return ErrConflict
	}
	p.StorageIdentity = identity
	p.LeaseGeneration++
	p.LeaseToken = ""
	p.LeaseUntilNS = 0
	return dynamicsReplace(ctx, tx, c)
}
