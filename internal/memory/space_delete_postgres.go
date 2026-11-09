package memory

import (
	"context"
)

// SpaceArtifacts returns up to limit attachment rows of the space, every sub-scope.
func (p *Postgres) SpaceArtifacts(ctx context.Context, space Scope, limit int) ([]Record, error) {
	if !validScope(space) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 ORDER BY id LIMIT $4`, colArtifacts, space.WorkspaceID, space.ProjectID, limit)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, ErrUnavailable
		}
		rec, err := pgDecode(payload)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, pgErr(rows.Err())
}

// EraseSpace deletes the space's rows inside the caller's transaction (see spaceEraser).
func (p *Postgres) EraseSpace(ctx context.Context, space Scope, only *spaceClear) (map[string]int64, error) {
	if !validScope(space) {
		return nil, ErrInvalid
	}
	if only != nil {
		return p.clearSpaceRecords(ctx, space, *only)
	}
	if err := p.checkSpaceErasable(ctx, space); err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	// Graph, traces and code index first: the record trigger then finds nothing left to update.
	var rel, ent, ev, tr, ce, cs, cr int64
	err := p.q().QueryRow(ctx, `WITH r AS (DELETE FROM relations WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 e AS (DELETE FROM entities WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 s AS (DELETE FROM source_events WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 t AS (DELETE FROM execution_traces WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 ce AS (DELETE FROM code_edges WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 cs AS (DELETE FROM code_symbols WHERE organization_id=$1 AND project_id=$2 RETURNING 1),
 cr AS (DELETE FROM code_repos WHERE organization_id=$1 AND project_id=$2 RETURNING 1)
 SELECT (SELECT count(*) FROM r),(SELECT count(*) FROM e),(SELECT count(*) FROM s),(SELECT count(*) FROM t),(SELECT count(*) FROM ce),(SELECT count(*) FROM cs),(SELECT count(*) FROM cr)`,
		space.WorkspaceID, space.ProjectID).Scan(&rel, &ent, &ev, &tr, &ce, &cs, &cr)
	if err != nil {
		return nil, pgErr(err)
	}
	for name, n := range map[string]int64{"relations": rel, "entities": ent, "source_events": ev, "execution_traces": tr, "code_edges": ce, "code_symbols": cs, "code_repos": cr} {
		if n > 0 {
			counts[name] = n
		}
	}
	err = p.countDeleted(ctx, counts, `WITH d AS (DELETE FROM memory_records WHERE workspace_id=$1 AND project_id=$2 RETURNING collection) SELECT collection, count(*) FROM d GROUP BY 1`, space.WorkspaceID, space.ProjectID)
	return counts, err
}

// checkSpaceErasable locks the space's jobs so no worker claims one meanwhile,
// then refuses a running job or a privacy deletion under legal hold.
func (p *Postgres) checkSpaceErasable(ctx context.Context, space Scope) error {
	if _, err := p.q().Exec(ctx, `SELECT 1 FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 FOR UPDATE`, colJobs, space.WorkspaceID, space.ProjectID); err != nil {
		return pgErr(err)
	}
	var busy bool
	err := p.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3
 AND (lease_until > now() OR kind=$4 AND (doc#>>'{privacy_deletion,state}'=$5 OR doc#>>'{privacy_deletion,legal_hold_status}'=$5)))`,
		colJobs, space.WorkspaceID, space.ProjectID, privacyDeletionKind, privacyHoldHeld).Scan(&busy)
	if err != nil {
		return pgErr(err)
	}
	if busy {
		return ErrConflict
	}
	return nil
}

// clearSpaceRecords deletes the memory rows, the causal links naming a removed
// memory, and the queued (not running) embedding jobs of removed rows.
func (p *Postgres) clearSpaceRecords(ctx context.Context, space Scope, c spaceClear) (map[string]int64, error) {
	counts := map[string]int64{}
	err := p.countDeleted(ctx, counts, `WITH d AS (DELETE FROM memory_records WHERE workspace_id=$1 AND project_id=$2 AND collection=ANY($3) AND ($4='' OR kind=$4) RETURNING collection, id),
 l AS (DELETE FROM memory_records m WHERE m.collection=$5 AND m.workspace_id=$1 AND m.project_id=$2 AND $4<>''
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(m.doc->'relations','[]'::jsonb)) x JOIN d ON d.collection=$6 AND d.id=x->>'id') RETURNING collection),
 j AS (DELETE FROM memory_records m WHERE m.collection=$7 AND m.workspace_id=$1 AND m.project_id=$2 AND m.kind='embedding' AND (m.lease_until IS NULL OR m.lease_until <= now())
  AND EXISTS (SELECT 1 FROM d WHERE d.collection=m.doc->'target'->>'collection' AND d.id=m.doc->'target'->>'id') RETURNING collection)
 SELECT collection, count(*) FROM (SELECT collection FROM d UNION ALL SELECT collection FROM l UNION ALL SELECT collection FROM j) x GROUP BY 1`,
		space.WorkspaceID, space.ProjectID, c.Collections, c.Kind, colMemoryRelations, colMemories, colJobs)
	return counts, err
}

func (p *Postgres) countDeleted(ctx context.Context, counts map[string]int64, sql string, args ...any) error {
	rows, err := p.q().Query(ctx, sql, args...)
	if err != nil {
		return pgErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		var n int64
		if err := rows.Scan(&col, &n); err != nil {
			return ErrUnavailable
		}
		counts[col] += n
	}
	return pgErr(rows.Err())
}
