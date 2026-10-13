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
	if err := decoder.Decode(new(any)); err != io.EOF {
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
func (p *Postgres) initRecoveryWriteGate(ctx context.Context) error {
	gate, err := newRecoveryWriteGate("open")
	if err != nil {
		return err
	}
	data, err := json.Marshal(gate)
	if err != nil {
		return ErrUnavailable
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return pgErr(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO memory_storage_metadata(key,value) VALUES('recovery_write_gate',$1) ON CONFLICT(key) DO NOTHING`, data); err != nil {
		return pgErr(err)
	}
	if _, err = tx.Exec(ctx, recoveryWriteGateSQL); err != nil {
		return pgErr(err)
	}
	for _, table := range recoveryWriteTables {
		name := pgx.Identifier{"arkism", table}.Sanitize()
		if _, err = tx.Exec(ctx, `DROP TRIGGER IF EXISTS recovery_write_gate ON `+name+`; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON `+name+` FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate()`); err != nil {
			return pgErr(err)
		}
	}
	if _, err = tx.Exec(ctx, `REVOKE ALL ON FUNCTION arkism.check_recovery_write_gate() FROM PUBLIC`); err != nil {
		return pgErr(err)
	}
	return pgErr(tx.Commit(ctx))
}

const recoveryWriteGateSQL = `CREATE OR REPLACE FUNCTION arkism.check_recovery_write_gate() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
DECLARE raw bytea; gate jsonb;
BEGIN
 SELECT value INTO raw FROM arkism.memory_storage_metadata WHERE key='recovery_write_gate' FOR SHARE;
 IF raw IS NULL THEN RAISE EXCEPTION 'recovery write gate unavailable' USING ERRCODE='55000'; END IF;
 gate := convert_from(raw,'UTF8')::jsonb;
 IF jsonb_typeof(gate)<>'object' OR NOT (gate ?& ARRAY['version','state','generation','changed_at'])
    OR gate-'version'-'state'-'generation'-'changed_at'<>'{}'::jsonb
    OR jsonb_typeof(gate->'version')<>'number' OR gate->>'version'<>'1' OR gate->>'state' NOT IN ('open','paused')
    OR jsonb_typeof(gate->'state')<>'string' OR jsonb_typeof(gate->'generation')<>'string'
    OR gate->>'generation' !~ '^[0-9a-f]{64}$' OR jsonb_typeof(gate->'changed_at')<>'string'
    OR gate->>'changed_at' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$'
    OR (gate->>'changed_at')::timestamptz IS NULL THEN
  RAISE EXCEPTION 'recovery write gate invalid' USING ERRCODE='55000';
 END IF;
 IF gate->>'state'<>'open' THEN RAISE EXCEPTION 'recovery writes paused' USING ERRCODE='55000'; END IF;
 RETURN NULL;
END $$`

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
	if p == nil || p.pool == nil || p.tx != nil || !sourceHashPattern.MatchString(expectedIdentity) ||
		(action != "status" && action != "pause" && action != "resume") ||
		(action == "status" && expectedGeneration != "") || (action != "status" && !sourceHashPattern.MatchString(expectedGeneration)) {
		return RecoveryWriteGateStatus{}, ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RecoveryWriteGateStatus{}, pgErr(err)
	}
	defer tx.Rollback(ctx)
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
		state := "paused"
		if action == "resume" {
			state = "open"
		}
		if status.Gate.State != state {
			status.Gate, err = newRecoveryWriteGate(state)
			if err != nil {
				return RecoveryWriteGateStatus{}, err
			}
			data, err := json.Marshal(status.Gate)
			if err != nil {
				return RecoveryWriteGateStatus{}, ErrUnavailable
			}
			if _, err = tx.Exec(ctx, `UPDATE memory_storage_metadata SET value=$1 WHERE key='recovery_write_gate'`, data); err != nil {
				return RecoveryWriteGateStatus{}, pgErr(err)
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return RecoveryWriteGateStatus{}, pgErr(err)
	}
	return status, nil
}
