package memory

import (
	"context"
	"fmt"
)

// pgReach reads a memory's reach with memoryReach's default for rows written before ST-824.
func pgReach(alias string) string {
	return fmt.Sprintf(`COALESCE(%[1]sdoc->>'reach', CASE WHEN %[1]skind='user' THEN 'personal' ELSE 'general' END)`, alias)
}

// memoryGraphWhere is the one ST-820 node filter (memoryGraphVisible in SQL): visible,
// not erased, active-and-current or superseded, in the asked reaches, project reach only
// for the asked key. alias is "" or "m."; placeholders start at $first.
func memoryGraphWhere(in MemoryGraphQuery, alias string, first int) (string, []any) {
	a := alias
	w := fmt.Sprintf(`COALESCE((%[1]sdoc->>'content_erased')::boolean,false)=false AND COALESCE((%[1]sdoc->>'privacy_quarantined')::boolean,false)=false AND %[1]srevision>0
AND (%[1]sstatus='superseded' OR %[1]sstatus='active' AND (%[1]sdoc->'effective_at' IS NULL OR (%[1]sdoc->'effective_at'->>'$date')::timestamptz <= now()) AND (%[1]sdoc->'expires_at' IS NULL OR (%[1]sdoc->'expires_at'->>'$date')::timestamptz > now()))
AND %[2]s = ANY($%[3]d::text[]) AND (%[2]s <> 'project' OR ($%[4]d <> '' AND %[1]sdoc->>'reach_project' = $%[4]d))`, a, pgReach(a), first, first+1)
	return w, []any{in.Reaches, in.ProjectKey}
}

func (p *Postgres) MemoryGraphNodes(ctx context.Context, in MemoryGraphQuery, ids []string) ([]Record, error) {
	in, err := normalizeMemoryGraphQuery(in)
	if err != nil || len(ids) == 0 || len(ids) > memoryGraphScan {
		return nil, ErrInvalid
	}
	w, args := memoryGraphWhere(in, "", 6)
	w += " AND id=ANY($8::text[])"
	return p.selectRecords(ctx, colMemories, in.Scope, w, append(args, ids), "id ASC", len(ids))
}

func (p *Postgres) MemoryGraphMapNodes(ctx context.Context, in MemoryGraphQuery, after string, limit int) ([]Record, error) {
	in, err := normalizeMemoryGraphQuery(in)
	if err != nil || in.Mode != MemoryGraphModeAll || limit < 1 || limit > memoryGraphMapPage {
		return nil, ErrInvalid
	}
	w, args := memoryGraphWhere(in, "", 6)
	return p.selectRecords(ctx, colMemories, in.Scope, w+" AND id > $8", append(args, after), "id ASC", limit)
}

func (p *Postgres) MemoryGraphLinks(ctx context.Context, in MemoryGraphQuery, ids []string) ([]Record, error) {
	in, err := normalizeMemoryGraphQuery(in)
	if err != nil || len(ids) == 0 || len(ids) > memoryGraphMaxNodes {
		return nil, ErrInvalid
	}
	w := `status='active' AND COALESCE((doc->>'content_erased')::boolean,false)=false AND COALESCE((doc->>'privacy_quarantined')::boolean,false)=false
AND jsonb_typeof(doc->'relations')='array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(doc->'relations') e WHERE e->>'collection'='memories' AND e->>'id'=ANY($6::text[]))`
	return p.selectRecords(ctx, "memory_relations", in.Scope, w, []any{ids}, "id ASC", memoryGraphScan+1)
}

func (p *Postgres) MemoryGraphSessions(ctx context.Context, in MemoryGraphQuery, ids []string, limit int) ([]MemoryGraphSessionHit, error) {
	in, err := normalizeMemoryGraphQuery(in)
	if err != nil || len(ids) == 0 || len(ids) > memoryGraphMaxNodes || limit < 1 || limit > memoryGraphMaxNodes {
		return nil, ErrInvalid
	}
	w, args := memoryGraphWhere(in, "m.", 6)
	params := append([]any{in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, colMemories}, args...)
	params = append(params, ids, limit)
	// sessionTags in SQL: tags starting with "session:" and longer than it.
	rows, err := p.q().Query(ctx, `SELECT c.id, n.payload FROM memory_records c CROSS JOIN LATERAL (
SELECT m.id, m.payload FROM memory_records m WHERE m.collection=$5 AND m.workspace_id=$1 AND m.project_id=$2 AND m.user_id=$3 AND m.agent_id=$4
AND m.id<>c.id AND `+pgReach("m.")+`=`+pgReach("c.")+` AND COALESCE(m.doc->>'reach_project','')=COALESCE(c.doc->>'reach_project','')
AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(c.doc->'tags') t WHERE t LIKE 'session:_%' AND m.doc->'tags' ? t)
AND `+w+` ORDER BY m.id LIMIT $9) n
WHERE c.collection=$5 AND c.workspace_id=$1 AND c.project_id=$2 AND c.user_id=$3 AND c.agent_id=$4 AND c.id=ANY($8::text[]) AND jsonb_typeof(c.doc->'tags')='array'
ORDER BY c.id, n.id`, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []MemoryGraphSessionHit{}
	for rows.Next() {
		var h MemoryGraphSessionHit
		var payload []byte
		if err = rows.Scan(&h.From, &payload); err != nil {
			return nil, ErrUnavailable
		}
		if h.Memory, err = pgDecode(payload); err != nil || !sameScope(h.Memory.Scope, in.Scope) {
			return nil, ErrUnavailable
		}
		out = append(out, h)
	}
	return out, pgErr(rows.Err())
}

// MemoryGraphSimilar compares stored vectors only (no embedding call): same scope, same
// embedding space and dimension, same reach and reach_project as the asking memory.
func (p *Postgres) MemoryGraphSimilar(ctx context.Context, in MemoryGraphQuery, ids []string, maxDistance float64, limit int) ([]MemoryGraphSimilarPair, error) {
	in, err := normalizeMemoryGraphQuery(in)
	if err != nil || len(ids) == 0 || len(ids) > memoryGraphMaxNodes || maxDistance < 0 || maxDistance > 1 || limit < 1 || limit > memoryGraphMaxNodes {
		return nil, ErrInvalid
	}
	w, args := memoryGraphWhere(in, "m.", 6)
	params := append([]any{in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, colMemories}, args...)
	params = append(params, ids, maxDistance, limit, VectorDimension)
	rows, err := p.q().Query(ctx, `SELECT c.id, n.id, n.d FROM memory_records c CROSS JOIN LATERAL (
SELECT m.id, m.embedding <=> c.embedding AS d FROM memory_records m WHERE m.collection=$5 AND m.workspace_id=$1 AND m.project_id=$2 AND m.user_id=$3 AND m.agent_id=$4
AND m.id<>c.id AND m.embedding IS NOT NULL AND m.embedding_space=c.embedding_space AND m.embedding_dimension=$11
AND `+pgReach("m.")+`=`+pgReach("c.")+` AND COALESCE(m.doc->>'reach_project','')=COALESCE(c.doc->>'reach_project','')
AND `+w+` AND (m.embedding <=> c.embedding) <= $9 ORDER BY 2, 1 LIMIT $10) n
WHERE c.collection=$5 AND c.workspace_id=$1 AND c.project_id=$2 AND c.user_id=$3 AND c.agent_id=$4 AND c.id=ANY($8::text[]) AND c.embedding IS NOT NULL AND c.embedding_dimension=$11
ORDER BY c.id, n.d, n.id`, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []MemoryGraphSimilarPair{}
	for rows.Next() {
		var pair MemoryGraphSimilarPair
		if err = rows.Scan(&pair.From, &pair.To, &pair.Distance); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, pair)
	}
	return out, pgErr(rows.Err())
}
