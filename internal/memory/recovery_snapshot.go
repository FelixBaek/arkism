package memory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const recoverySnapshotScopeLimit = 4096
const recoverySnapshotByteLimit = 128 * 1024 * 1024

// RecoverySnapshot is one database snapshot, not a publication authorization or
// a fence against deletions committed after that snapshot.
type RecoverySnapshot struct {
	Version        int                     `json:"version"`
	SourceIdentity string                  `json:"source_identity"`
	CapturedAt     time.Time               `json:"captured_at"`
	Coverage       string                  `json:"coverage"`
	Ledgers        []DeletionLedger        `json:"ledgers"`
	ReceiptCauses  *[]RecoveryReceiptScope `json:"receipt_causes,omitempty"`
}

// ExportRecoverySnapshot is for the offline backup operator only. No HTTP/MCP
// route calls it. A scoped application connection is explicitly insufficient.
func (p *Postgres) ExportRecoverySnapshot(ctx context.Context) ([]byte, error) {
	return p.exportRecoverySnapshot(ctx, false, "", false)
}

func (p *Postgres) InspectRestoredRecovery(ctx context.Context) ([]byte, error) {
	return p.exportRecoverySnapshot(ctx, true, "", false)
}

// RecoverySnapshotAtWriteGate holds the paused generation while reading. The
// unchanged v3/v4 artifact alone is not proof the gate stayed paused afterward.
func (p *Postgres) RecoverySnapshotAtWriteGate(ctx context.Context, restored bool, generation string) ([]byte, error) {
	if !sourceHashPattern.MatchString(generation) {
		return nil, ErrInvalid
	}
	return p.exportRecoverySnapshot(ctx, restored, generation, false)
}

type RestoredRecoverySnapshot struct {
	Version          int                     `json:"version"`
	RestoredIdentity string                  `json:"restored_identity"`
	RestoreManifest  string                  `json:"restore_manifest"`
	CapturedAt       time.Time               `json:"captured_at"`
	Coverage         string                  `json:"coverage"`
	Ledgers          []DeletionLedger        `json:"ledgers"`
	ReplayKeys       map[string]string       `json:"replay_keys"`
	ReceiptCauses    *[]RecoveryReceiptScope `json:"receipt_causes,omitempty"`
}

var restoreReplayKeyPattern = regexp.MustCompile(`^restore-(request|work|source)-[0-9a-f]{64}$`)

// RecoveryReviewSnapshot adds verified or explicitly unresolved receipt origins.
func (p *Postgres) RecoveryReviewSnapshot(ctx context.Context, restored bool, generation string) ([]byte, error) {
	if generation != "" && !sourceHashPattern.MatchString(generation) {
		return nil, ErrInvalid
	}
	return p.exportRecoverySnapshot(ctx, restored, generation, true)
}

func (p *Postgres) exportRecoverySnapshot(ctx context.Context, restored bool, generation string, causes bool) ([]byte, error) {
	if p == nil || p.pool == nil || p.tx != nil {
		return nil, ErrInvalid
	}
	mode := pgx.ReadOnly
	if generation != "" {
		mode = pgx.ReadWrite // Row locks are forbidden in PostgreSQL ReadOnly.
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: mode})
	if err != nil {
		return nil, pgErr(err)
	}
	defer tx.Rollback(ctx)
	var privileged bool
	if err = tx.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged); err != nil {
		return nil, pgErr(err)
	}
	if !privileged {
		return nil, ErrDenied
	}
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return nil, pgErr(err)
	}
	view := &Postgres{pool: p.pool, tx: tx}
	if generation != "" {
		if err := view.checkRecoveryWriteGateSchema(ctx); err != nil {
			return nil, err
		}
		gate, err := view.recoveryWriteGate(ctx, " FOR SHARE OF g")
		if err != nil {
			return nil, err
		}
		if gate.Gate.State != "paused" || gate.Gate.Generation != generation {
			return nil, ErrConflict
		}
	}
	var manifest []byte
	if restored {
		if err = tx.QueryRow(ctx, `SELECT value FROM memory_storage_metadata WHERE key='restore_review_pending'`).Scan(&manifest); err != nil || !sourceHashPattern.Match(manifest) {
			return nil, ErrUnavailable
		}
	} else if err = view.checkRestorePublicationMarker(ctx); err != nil {
		return nil, err
	}
	replayKeys := map[string]string{}
	var identity []byte
	var captured time.Time
	if err = tx.QueryRow(ctx, `SELECT value,transaction_timestamp() FROM memory_storage_metadata WHERE key='storage_identity'`).Scan(&identity, &captured); err != nil || len(identity) != 32 {
		return nil, ErrUnavailable
	}
	scopes, err := recoverySnapshotScopes(ctx, view)
	if err != nil {
		return nil, err
	}
	out := RecoverySnapshot{Version: 3, SourceIdentity: hex.EncodeToString(identity), CapturedAt: captured.UTC(), Coverage: "arkism_canonical_tables_v1", Ledgers: make([]DeletionLedger, 0, len(scopes))}
	if causes {
		items := []RecoveryReceiptScope{}
		out.ReceiptCauses = &items
		out.Version = 5
	}
	budget := 0
	for _, scope := range scopes {
		ledger, err := buildDeletionLedger(ctx, view, scope, 2, func(string, string) error { return nil })
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(ledger)
		if err != nil {
			return nil, ErrUnavailable
		}
		budget += len(encoded) + 1
		if budget > recoverySnapshotByteLimit {
			return nil, ErrUnavailable
		}
		if restored || causes {
			rows, err := view.RecoveryPrivacyRecords(ctx, scope)
			if err != nil {
				return nil, err
			}
			if causes {
				item, err := recoveryReceiptCauses(ctx, view, scope, ledger, rows)
				if err != nil {
					return nil, err
				}
				encoded, err := json.Marshal(item)
				if err != nil {
					return nil, ErrUnavailable
				}
				budget += len(encoded) + 1
				if budget > recoverySnapshotByteLimit {
					return nil, ErrUnavailable
				}
				*out.ReceiptCauses = append(*out.ReceiptCauses, item)
			}
			for _, row := range rows {
				pd := row.PrivacyDeletion
				if restored && pd != nil && pd.RequesterType == "human" && pd.RetentionPolicyRef == "external_backup_default_v1" && restoreReplayKeyPattern.MatchString(pd.IdempotencyKey) {
					replayKeys[row.ID] = ContentHash(pd.IdempotencyKey)
					budget += len(row.ID) + 70
				}
			}
			if budget > recoverySnapshotByteLimit {
				return nil, ErrUnavailable
			}
		}
		out.Ledgers = append(out.Ledgers, ledger)
	}
	var artifact any = out
	if restored {
		version := 4
		if causes {
			version = 6
		}
		artifact = RestoredRecoverySnapshot{Version: version, RestoredIdentity: out.SourceIdentity, RestoreManifest: string(manifest), CapturedAt: out.CapturedAt, Coverage: out.Coverage, Ledgers: out.Ledgers, ReplayKeys: replayKeys, ReceiptCauses: out.ReceiptCauses}
	}
	encoded, err := json.Marshal(artifact)
	if err != nil || len(encoded) > recoverySnapshotByteLimit {
		return nil, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, pgErr(err)
	}
	return encoded, nil
}

func recoverySnapshotScopes(ctx context.Context, p *Postgres) ([]Scope, error) {
	rows, err := p.q().Query(ctx, `SELECT workspace_id,project_id,user_id,agent_id FROM (
 SELECT workspace_id,project_id,user_id,agent_id FROM memory_records
 UNION SELECT organization_id,project_id,user_id,agent_id FROM execution_traces
 UNION SELECT organization_id,project_id,user_id,agent_id FROM source_events
 UNION SELECT organization_id,project_id,user_id,agent_id FROM entities
 UNION SELECT organization_id,project_id,user_id,agent_id FROM relations
 ) scopes ORDER BY workspace_id COLLATE "C",project_id COLLATE "C",user_id COLLATE "C",agent_id COLLATE "C" LIMIT $1`, recoverySnapshotScopeLimit+1)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []Scope{}
	for rows.Next() {
		var scope Scope
		if err = rows.Scan(&scope.WorkspaceID, &scope.ProjectID, &scope.UserID, &scope.AgentID); err != nil {
			return nil, pgErr(err)
		}
		if !validScope(scope) {
			return nil, ErrUnavailable
		}
		for _, value := range []string{scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID} {
			if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return nil, ErrUnavailable
			}
		}
		out = append(out, scope)
		if len(out) > recoverySnapshotScopeLimit {
			return nil, ErrUnavailable
		}
	}
	return out, pgErr(rows.Err())
}
