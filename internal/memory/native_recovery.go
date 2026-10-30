package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"

	"github.com/jackc/pgx/v5"
)

const nativeRecoveryProtocol = "arkism-native-recovery-v1"
const nativeRecoveryPendingKey = "native_restore_review_pending_v1"
const nativeRecoveryReadbackKey = "native_restore_readback_v2"
const nativeRecoveryReadbackProtocol = "arkism-native-recovery-readback-v2"

// NativeRecoveryFields retain the six distinct restoration domains. A digest
// is a database observation, not authority to publish or a physical erase claim.
type NativeRecoveryFields struct {
	State           string `json:"state"`
	Receipt         string `json:"receipt"`
	Consumption     string `json:"consumption"`
	EventCursor     string `json:"event_cursor"`
	Checkpoint      string `json:"checkpoint"`
	DependencyIndex string `json:"dependency_index"`
}

type NativeRecoveryScope struct {
	Scope  Scope                `json:"scope"`
	Rows   int                  `json:"rows"`
	Fields NativeRecoveryFields `json:"fields"`
}

type NativeRecoverySnapshot struct {
	Protocol       string                `json:"protocol"`
	SourceIdentity string                `json:"source_identity"`
	Scopes         []NativeRecoveryScope `json:"scopes"`
}

type nativeRecoveryWitness struct {
	ScopeSHA     string                            `json:"scope_sha256"`
	Commit       nativeRecoveryRowCommit           `json:"row"`
	Dependencies []nativeRecoveryDependencyWitness `json:"dependencies"`
}

type nativeRecoveryReadback struct {
	Protocol       string                  `json:"protocol"`
	Manifest       string                  `json:"manifest"`
	SourceIdentity string                  `json:"source_identity"`
	SnapshotSHA    string                  `json:"snapshot_sha256"`
	Witnesses      []nativeRecoveryWitness `json:"witnesses"`
}

func nativeRecoveryHash(domain string, value any) string {
	return ContentHash(nativeRecoveryProtocol + "\x00" + domain + "\x00" + structuredText(value))
}

func nativeRecoverySnapshotValid(snapshot NativeRecoverySnapshot) bool {
	if snapshot.Protocol != nativeRecoveryProtocol || !sourceHashPattern.MatchString(snapshot.SourceIdentity) || snapshot.Scopes == nil || len(snapshot.Scopes) > recoverySnapshotScopeLimit {
		return false
	}
	seen := map[Scope]bool{}
	for _, item := range snapshot.Scopes {
		if !validScope(item.Scope) || seen[item.Scope] || item.Rows < 1 || item.Rows > nativeRecoveryRowsLimit {
			return false
		}
		seen[item.Scope] = true
		for _, digest := range []string{item.Fields.State, item.Fields.Receipt, item.Fields.Consumption, item.Fields.EventCursor, item.Fields.Checkpoint, item.Fields.DependencyIndex} {
			if !sourceHashPattern.MatchString(digest) {
				return false
			}
		}
	}
	return true
}

// RecoveryNativeReviewSnapshot explicitly opts into source v7/restored v8.
// Both include the existing v5/v6 receipt-causes projection; legacy exports
// remain byte-compatible and cannot clear a native restore review requirement.
func (p *Postgres) RecoveryNativeReviewSnapshot(ctx context.Context, restored bool, generation string) ([]byte, error) {
	if !sourceHashPattern.MatchString(generation) {
		return nil, ErrInvalid
	}
	return p.exportRecoverySnapshotMode(ctx, restored, generation, true, true)
}

// nativeRecoveryTransaction is an offline operator boundary. The existing
// privilege and paused-generation checks are mandatory, never inferred from
// a scoped application query returning no rows.
func (p *Postgres) nativeRecoveryTransaction(ctx context.Context, generation string, fn func(*Postgres) error) error {
	if p == nil || p.pool == nil || p.tx != nil || !sourceHashPattern.MatchString(generation) {
		return ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return pgErr(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = disableSnapshotRowSecurity(ctx, tx); err != nil {
		return err
	}
	view := &Postgres{pool: p.pool, tx: tx}
	if err = view.checkPausedRecoveryGate(ctx, generation); err != nil {
		return err
	}
	if err = fn(view); err != nil {
		return err
	}
	return pgErr(tx.Commit(ctx))
}

// VerifyNativeRecoveryReadback compares the actual restored database before
// deletion replay with the native projection recorded alongside its dump.
// It does not clear either publication marker or grant any service permission.
func (p *Postgres) VerifyNativeRecoveryReadback(ctx context.Context, manifest, generation string, expected NativeRecoverySnapshot) error {
	if !sourceHashPattern.MatchString(manifest) || !nativeRecoverySnapshotValid(expected) {
		return ErrInvalid
	}
	return p.nativeRecoveryTransaction(ctx, generation, func(view *Postgres) error {
		recorded, err := view.recoverySnapshotManifest(ctx, true)
		if err != nil {
			return err
		}
		if string(recorded) != manifest {
			return ErrConflict
		}
		actual, err := view.nativeRecoverySnapshotTx(ctx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, expected) {
			return ErrConflict
		}
		witnesses, err := view.nativeRecoveryWitnessesTx(ctx, actual.Scopes)
		if err != nil {
			return err
		}
		proof := nativeRecoveryReadback{Protocol: nativeRecoveryReadbackProtocol, Manifest: manifest, SourceIdentity: expected.SourceIdentity, SnapshotSHA: nativeRecoveryHash("readback", expected), Witnesses: witnesses}
		raw, err := json.Marshal(proof)
		if err != nil || len(raw) > recoverySnapshotByteLimit {
			return ErrUnavailable
		}
		_, err = view.q().Exec(ctx, `INSERT INTO memory_storage_metadata(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, nativeRecoveryReadbackKey, raw)
		return pgErr(err)
	})
}

func (p *Postgres) nativeRecoveryReadbackTx(ctx context.Context, manifest string) (nativeRecoveryReadback, error) {
	var raw []byte
	if err := p.q().QueryRow(ctx, `SELECT value FROM memory_storage_metadata WHERE key=$1`, nativeRecoveryReadbackKey).Scan(&raw); err != nil {
		return nativeRecoveryReadback{}, ErrUnavailable
	}
	if len(raw) > recoverySnapshotByteLimit {
		return nativeRecoveryReadback{}, ErrUnavailable
	}
	var proof nativeRecoveryReadback
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&proof) != nil || decoder.Decode(new(any)) != io.EOF || proof.Protocol != nativeRecoveryReadbackProtocol || proof.Manifest != manifest || !sourceHashPattern.MatchString(proof.SourceIdentity) || !sourceHashPattern.MatchString(proof.SnapshotSHA) || proof.Witnesses == nil {
		return proof, ErrUnavailable
	}
	return proof, nil
}

func (p *Postgres) nativeRestoreReviewRequiredTx(ctx context.Context) (bool, error) {
	var privileged bool
	if err := p.q().QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged); err != nil {
		return false, pgErr(err)
	}
	if !privileged {
		return true, nil
	} // An RLS-filtered empty result is not proof of absence.
	var present bool
	err := p.q().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_records WHERE collection='jobs' AND left(kind,length($1))=$1)`, nativeDynamicsPrefix).Scan(&present)
	return present, pgErr(err)
}

func (p *Postgres) requireNativeRestoreReviewTx(ctx context.Context, manifest string) error {
	required, err := p.nativeRestoreReviewRequiredTx(ctx)
	if err != nil || !required {
		return err
	}
	tag, err := p.q().Exec(ctx, `INSERT INTO memory_storage_metadata(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value WHERE memory_storage_metadata.value=EXCLUDED.value`, nativeRecoveryPendingKey, []byte(manifest))
	if err != nil {
		return pgErr(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (p *Postgres) checkNativeRestorePublication(ctx context.Context) error {
	var pending bool
	if err := p.q().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_storage_metadata WHERE key=$1)`, nativeRecoveryPendingKey).Scan(&pending); err != nil {
		return pgErr(err)
	}
	if pending {
		return ErrUnavailable
	}
	return nil
}
