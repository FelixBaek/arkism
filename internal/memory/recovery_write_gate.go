package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

var recoveryWriteTables = []string{
	"memory_records", "execution_traces", "source_events", "entities", "relations",
	"principals", "organization_memberships", "project_memberships", "principal_permissions", "candidate_daily_usage",
	"principal_token_history", "decision_daily_counts",
}

type RecoveryWriteGate struct {
	Version    int       `json:"version"`
	State      string    `json:"state"`
	Generation string    `json:"generation"`
	ChangedAt  time.Time `json:"changed_at"`
}

type RecoveryWriteGateStatus struct {
	StorageIdentity string            `json:"storage_identity"`
	Gate            RecoveryWriteGate `json:"write_gate"`
}

func newRecoveryWriteGate(state string) (RecoveryWriteGate, error) {
	var generation [32]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return RecoveryWriteGate{}, ErrUnavailable
	}
	return RecoveryWriteGate{Version: 1, State: state, Generation: hex.EncodeToString(generation[:]), ChangedAt: time.Now().UTC()}, nil
}

func decodeRecoveryWriteGate(data []byte) (RecoveryWriteGate, error) {
	var gate RecoveryWriteGate
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&gate); err != nil {
		return gate, ErrUnavailable
	}
	if decoder.Decode(new(any)) != io.EOF {
		return gate, ErrUnavailable
	}
	if gate.Version != 1 || (gate.State != "open" && gate.State != "paused") || !sourceHashPattern.MatchString(gate.Generation) || gate.ChangedAt.IsZero() {
		return gate, ErrUnavailable
	}
	return gate, nil
}

// Installed atomically after the canonical and authorization schemas exist.
// A data write holds a shared row lock until its transaction ends. Changing the
// gate takes an exclusive lock, so successful pause drains earlier writes.

func (p *Postgres) recoveryWriteGate(ctx context.Context, lock string) (RecoveryWriteGateStatus, error) {
	var identity, data []byte
	// lock is internal, never supplied by a caller/CLI.
	err := p.q().QueryRow(ctx, `SELECT i.value,g.value FROM memory_storage_metadata i CROSS JOIN memory_storage_metadata g
WHERE i.key='storage_identity' AND g.key='recovery_write_gate'`+lock).Scan(&identity, &data)
	if err != nil || len(identity) != 32 {
		return RecoveryWriteGateStatus{}, ErrUnavailable
	}
	gate, err := decodeRecoveryWriteGate(data)
	if err != nil {
		return RecoveryWriteGateStatus{}, err
	}
	return RecoveryWriteGateStatus{hex.EncodeToString(identity), gate}, nil
}

func (p *Postgres) checkRecoveryWriteGateSchema(ctx context.Context) error {
	var count int
	err := p.q().QueryRow(ctx, `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='arkism' AND c.relname=ANY($1)
AND t.tgname='recovery_write_gate' AND NOT t.tgisinternal AND t.tgtype=62 AND t.tgenabled IN ('O','A')
AND t.tgfoid=to_regprocedure('arkism.check_recovery_write_gate()')`, recoveryWriteTables).Scan(&count)
	if err != nil || count != len(recoveryWriteTables) {
		return ErrUnavailable
	}
	return nil
}

// ManageRecoveryWrites is an offline administrator operation, never an API
// permission. Resuming writes is independent of the restoration publication gate.
func (p *Postgres) ManageRecoveryWrites(ctx context.Context, action, expectedIdentity, expectedGeneration string) (RecoveryWriteGateStatus, error) {
	if p == nil || p.pool == nil || p.tx != nil || !sourceHashPattern.MatchString(expectedIdentity) || !validRecoveryWriteAction(action, expectedGeneration) {
		return RecoveryWriteGateStatus{}, ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecoveryWriteGateStatus{}, pgErr(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after Commit
	var privileged bool
	if err = tx.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged); err != nil {
		return RecoveryWriteGateStatus{}, pgErr(err)
	}
	if !privileged {
		return RecoveryWriteGateStatus{}, ErrDenied
	}
	lock := ""
	if action != "status" {
		lock = " FOR UPDATE OF g"
	}
	view := &Postgres{pool: p.pool, tx: tx}
	if err := view.checkRecoveryWriteGateSchema(ctx); err != nil {
		return RecoveryWriteGateStatus{}, err
	}
	status, err := view.recoveryWriteGate(ctx, lock)
	if err != nil {
		return RecoveryWriteGateStatus{}, err
	}
	if status.StorageIdentity != expectedIdentity || (action != "status" && status.Gate.Generation != expectedGeneration) {
		return RecoveryWriteGateStatus{}, ErrConflict
	}
	if action != "status" {
		if err = setRecoveryWriteGate(ctx, tx, action, &status); err != nil {
			return RecoveryWriteGateStatus{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return RecoveryWriteGateStatus{}, pgErr(err)
	}
	return status, nil
}

func validRecoveryWriteAction(action, expectedGeneration string) bool {
	return !((action != "status" && action != "pause" && action != "resume") ||
		(action == "status" && expectedGeneration != "") || (action != "status" && !sourceHashPattern.MatchString(expectedGeneration)))
}

// setRecoveryWriteGate replaces the gate with a fresh generation when a pause
// or resume changes its state.
func setRecoveryWriteGate(ctx context.Context, tx pgx.Tx, action string, status *RecoveryWriteGateStatus) error {
	state := "paused"
	if action == "resume" {
		state = "open"
	}
	if status.Gate.State == state {
		return nil
	}
	var err error
	status.Gate, err = newRecoveryWriteGate(state)
	if err != nil {
		return err
	}
	data, err := json.Marshal(status.Gate)
	if err != nil {
		return ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE memory_storage_metadata SET value=$1 WHERE key='recovery_write_gate'`, data); err != nil {
		return pgErr(err)
	}
	return nil
}
