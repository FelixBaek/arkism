package memory

import "context"

// NativeDynamicsRows runs only in its caller's current transaction and scope.
func (p *Postgres) NativeDynamicsRows(ctx context.Context, scope Scope, role, dependency, after string, limit int) ([]Record, error) {
	return p.nativeDynamicsRows(ctx, scope, role, dependency, after, limit, false)
}

func (p *Postgres) NativeDynamicsPendingCaptures(ctx context.Context, scope Scope, limit int) ([]Record, error) {
	return p.nativeDynamicsRows(ctx, scope, "capture", "", "", limit, true)
}

func (p *Postgres) NativeDynamicsPendingPurges(ctx context.Context, scope Scope, limit int) ([]Record, error) {
	return p.nativeDynamicsRows(ctx, scope, "purge", "", "", limit, true)
}

func (p *Postgres) nativeDynamicsRows(ctx context.Context, scope Scope, role, dependency, after string, limit int, pendingOnly bool) ([]Record, error) {
	if p.tx == nil || checkScope(scope) != nil || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT id,revision,kind,payload FROM memory_records WHERE collection='jobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind=$5 AND ($6='' OR COALESCE(doc->'native_dynamics'->'dependency_keys','[]'::jsonb) ? $6) AND id>$7 AND (NOT $9::boolean OR doc->>'status' IN ('pending','retry')) ORDER BY id LIMIT $8`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, nativeDynamicsPrefix+role, dependency, after, limit, pendingOnly)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var id, kind string
		var revision int64
		var b []byte
		if rows.Scan(&id, &revision, &kind, &b) != nil {
			return nil, ErrUnavailable
		}
		r, e := pgDecode(b)
		if e != nil {
			return nil, e
		}
		if r.ID != id || r.Scope != scope || r.Revision != revision || r.Kind != kind || r.NativeDynamics == nil || r.NativeDynamics.Protocol != nativeDynamicsProtocol {
			return nil, ErrConflict
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, pgErr(err)
	}
	return out, nil
}
