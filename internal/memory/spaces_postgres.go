package memory

import "context"

// SpaceCounts counts project-level records per collection for up to limit
// projects of one workspace after the given project id. RLS keeps it to the
// caller's permitted projects; the service authorizes each space again.
func (p *Postgres) SpaceCounts(ctx context.Context, workspace, after string, collections []string, limit int) ([]SpaceCount, error) {
	if !scopeName(workspace) || limit < 1 || limit > spaceListMax+1 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `WITH p AS (SELECT DISTINCT project_id FROM memory_records WHERE workspace_id=$1 AND user_id='' AND agent_id='' AND project_id>$2 AND collection=ANY($3) ORDER BY project_id LIMIT $4)
SELECT m.project_id, m.collection, count(*) FROM memory_records m JOIN p USING (project_id) WHERE m.workspace_id=$1 AND m.user_id='' AND m.agent_id='' AND m.collection=ANY($3) GROUP BY 1,2 ORDER BY 1,2`, workspace, after, collections, limit)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []SpaceCount{}
	for rows.Next() {
		var c SpaceCount
		if err := rows.Scan(&c.ProjectID, &c.Collection, &c.Count); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, c)
	}
	return out, pgErr(rows.Err())
}

// SpaceAliasOwners returns the space configs of one workspace that carry the alias (at most two).
func (p *Postgres) SpaceAliasOwners(ctx context.Context, workspace, alias string) ([]Record, error) {
	if !scopeName(workspace) || !spaceAliasPattern.MatchString(alias) {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND user_id='' AND agent_id='' AND id=$3 AND doc->'space_config'->'aliases' ? $4 ORDER BY project_id LIMIT 2`, colSpaceConfigs, workspace, spaceConfigID, alias)
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
		if err != nil || rec.Scope.WorkspaceID != workspace {
			return nil, ErrUnavailable
		}
		out = append(out, rec)
	}
	return out, pgErr(rows.Err())
}
