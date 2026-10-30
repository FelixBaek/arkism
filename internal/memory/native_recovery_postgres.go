package memory

import (
	"context"
	"encoding/hex"
	"strings"
)

const nativeRecoveryRowsLimit = recoverySnapshotScopeLimit

var nativeRecoveryRoleField = map[string]string{
	"state": "state", "config": "state", "enrollment": "state", "activation": "state", "catalog": "state",
	"capture": "receipt", "seen": "receipt", "consumption": "consumption", "event": "event_cursor", "checkpoint": "checkpoint", "purge": "dependency_index",
}

type nativeRecoveryRowCommit struct {
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	PayloadSHA  string `json:"payload_sha256"`
	DocumentSHA string `json:"document_sha256"`
}

type nativeRecoveryStoredRow struct {
	record Record
	commit nativeRecoveryRowCommit
	bytes  int
}

func (p *Postgres) nativeRecoveryIdentityTx(ctx context.Context) (string, error) {
	var identity []byte
	if err := p.q().QueryRow(ctx, `SELECT value FROM memory_storage_metadata WHERE key='storage_identity'`).Scan(&identity); err != nil || len(identity) != 32 {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(identity), nil
}

func (p *Postgres) nativeRecoveryRowsTx(ctx context.Context, scope Scope) ([]nativeRecoveryStoredRow, error) {
	if p.tx == nil || !validScope(scope) {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT id,revision,kind,status,payload,doc::text,doc=convert_from(payload,'UTF8')::jsonb FROM memory_records WHERE collection='jobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND left(kind,length($5))=$5 ORDER BY id COLLATE "C" LIMIT $6`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, nativeDynamicsPrefix, nativeRecoveryRowsLimit+1)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []nativeRecoveryStoredRow{}
	budget := 0
	for rows.Next() {
		var id, kind, status, doc string
		var revision int64
		var payload []byte
		var sameDoc bool
		if rows.Scan(&id, &revision, &kind, &status, &payload, &doc, &sameDoc) != nil {
			return nil, ErrUnavailable
		}
		stored, err := nativeRecoveryDecodeRow(scope, id, revision, kind, status, payload, doc, sameDoc)
		if err != nil {
			return nil, err
		}
		budget += stored.bytes
		if budget > recoverySnapshotByteLimit || len(out) == nativeRecoveryRowsLimit {
			return nil, ErrUnavailable
		}
		out = append(out, stored)
	}
	if err := rows.Err(); err != nil {
		return nil, pgErr(err)
	}
	return out, nil
}

func nativeRecoveryDecodeRow(scope Scope, id string, revision int64, kind, status string, payload []byte, doc string, sameDoc bool) (nativeRecoveryStoredRow, error) {
	record, err := pgDecode(payload)
	if err != nil {
		return nativeRecoveryStoredRow{}, err
	}
	role := strings.TrimPrefix(kind, nativeDynamicsPrefix)
	_, known := nativeRecoveryRoleField[role]
	if !sameDoc || !known || !strings.HasPrefix(kind, nativeDynamicsPrefix) || record.Scope != scope || record.ID != id || record.Revision != revision || revision < 1 || record.Kind != kind || record.Status != status || record.NativeDynamics == nil || record.NativeDynamics.Protocol != nativeDynamicsProtocol {
		return nativeRecoveryStoredRow{}, ErrUnavailable
	}
	commit := nativeRecoveryRowCommit{id, revision, kind, status, ContentHash(string(payload)), ContentHash(doc)}
	return nativeRecoveryStoredRow{record, commit, len(payload) + len(doc)}, nil
}

func nativeRecoveryFieldsFor(scope Scope, rows []nativeRecoveryStoredRow) NativeRecoveryFields {
	fields := map[string][]nativeRecoveryRowCommit{"state": {}, "receipt": {}, "consumption": {}, "event_cursor": {}, "checkpoint": {}, "dependency_index": {}}
	for _, row := range rows {
		field := nativeRecoveryRoleField[strings.TrimPrefix(row.record.Kind, nativeDynamicsPrefix)]
		if field != "dependency_index" {
			fields[field] = append(fields[field], row.commit)
		}
		fields["dependency_index"] = append(fields["dependency_index"], row.commit)
	}
	digest := func(field string) string {
		return nativeRecoveryHash(field, struct {
			Scope Scope                     `json:"scope"`
			Rows  []nativeRecoveryRowCommit `json:"rows"`
		}{scope, fields[field]})
	}
	return NativeRecoveryFields{digest("state"), digest("receipt"), digest("consumption"), digest("event_cursor"), digest("checkpoint"), digest("dependency_index")}
}

func (p *Postgres) nativeRecoveryScopeTx(ctx context.Context, scope Scope) (NativeRecoveryScope, []nativeRecoveryStoredRow, int, error) {
	rows, err := p.nativeRecoveryRowsTx(ctx, scope)
	if err != nil {
		return NativeRecoveryScope{}, nil, 0, err
	}
	bytes := 0
	for _, row := range rows {
		bytes += row.bytes
	}
	if len(rows) > 0 {
		catalog, err := nativeCatalogTx(ctx, p, scope)
		if err != nil {
			return NativeRecoveryScope{}, nil, 0, err
		}
		if !catalog {
			return NativeRecoveryScope{}, nil, 0, ErrUnavailable
		}
	}
	return NativeRecoveryScope{scope, len(rows), nativeRecoveryFieldsFor(scope, rows)}, rows, bytes, nil
}

func (p *Postgres) nativeRecoverySnapshotTx(ctx context.Context) (NativeRecoverySnapshot, error) {
	identity, err := p.nativeRecoveryIdentityTx(ctx)
	if err != nil {
		return NativeRecoverySnapshot{}, err
	}
	scopes, err := recoverySnapshotScopes(ctx, p)
	if err != nil {
		return NativeRecoverySnapshot{}, err
	}
	out := NativeRecoverySnapshot{Protocol: nativeRecoveryProtocol, SourceIdentity: identity, Scopes: []NativeRecoveryScope{}}
	budget := 0
	for _, scope := range scopes {
		item, _, cost, err := p.nativeRecoveryScopeTx(ctx, scope)
		if err != nil {
			return NativeRecoverySnapshot{}, err
		}
		budget += cost
		if budget > recoverySnapshotByteLimit {
			return NativeRecoverySnapshot{}, ErrUnavailable
		}
		if item.Rows > 0 {
			out.Scopes = append(out.Scopes, item)
		}
	}
	return out, nil
}
