package memory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	if err != nil || len(ids) == 0 || len(ids) > memoryGraphMapNodes || limit < 1 || limit > memoryGraphMaxNodes {
		return nil, ErrInvalid
	}
	w, args := memoryGraphWhere(in, "m.", 6)
	params := append([]any{in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, colMemories}, args...)
	params = append(params, ids, limit)
	// sessionTags in SQL: tags starting with "session:" and longer than it. Both sides are
	// MATERIALIZED and joined on the tag: a lateral per asking row re-read each large doc (7 s at 600 memories).
	rows, err := p.q().Query(ctx, `WITH asking AS MATERIALIZED (
SELECT c.id, `+pgReach("c.")+` AS reach, COALESCE(c.doc->>'reach_project','') AS rp, t FROM memory_records c, jsonb_array_elements_text(c.doc->'tags') t
WHERE c.collection=$5 AND c.workspace_id=$1 AND c.project_id=$2 AND c.user_id=$3 AND c.agent_id=$4 AND c.id=ANY($8::text[]) AND jsonb_typeof(c.doc->'tags')='array' AND t LIKE 'session:_%'
), cand AS MATERIALIZED (
SELECT m.id, `+pgReach("m.")+` AS reach, COALESCE(m.doc->>'reach_project','') AS rp, t FROM memory_records m, jsonb_array_elements_text(m.doc->'tags') t
WHERE m.collection=$5 AND m.workspace_id=$1 AND m.project_id=$2 AND m.user_id=$3 AND m.agent_id=$4 AND jsonb_typeof(m.doc->'tags')='array' AND t LIKE 'session:_%' AND `+w+`
), pairs AS (
SELECT DISTINCT a.id AS cid, m.id AS mid FROM asking a JOIN cand m ON m.t=a.t AND m.id<>a.id AND m.reach=a.reach AND m.rp=a.rp
), ranked AS (
SELECT cid, mid, row_number() OVER (PARTITION BY cid ORDER BY mid) AS rn FROM pairs
)
SELECT r.cid, r.mid, n.payload FROM ranked r JOIN memory_records n ON n.collection=$5 AND n.workspace_id=$1 AND n.project_id=$2 AND n.user_id=$3 AND n.agent_id=$4 AND n.id=r.mid
WHERE r.rn <= $9 ORDER BY r.cid, r.mid`, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []MemoryGraphSessionHit{}
	decoded := map[string]Record{} // one row is the same payload for every asking memory: decode it once (#166)
	for rows.Next() {
		var h MemoryGraphSessionHit
		var id string
		var payload []byte
		if err = rows.Scan(&h.From, &id, &payload); err != nil {
			return nil, ErrUnavailable
		}
		var ok bool
		if h.Memory, ok = decoded[id]; !ok {
			if h.Memory, err = pgDecode(payload); err != nil || !sameScope(h.Memory.Scope, in.Scope) || h.Memory.ID != id {
				return nil, ErrUnavailable
			}
			decoded[id] = h.Memory
		}
		out = append(out, h)
	}
	return out, pgErr(rows.Err())
}

// MemoryGraphSimilar compares stored vectors only (no embedding call): same scope, same
// embedding space and dimension, same reach and reach_project as the asking memory.
func (p *Postgres) MemoryGraphSimilar(ctx context.Context, in MemoryGraphQuery, ids []string, maxDistance float64, limit int) ([]MemoryGraphSimilarPair, error) {
	in, err := normalizeMemoryGraphQuery(in)
	if err != nil || len(ids) == 0 || len(ids) > memoryGraphMapNodes || maxDistance < 0 || maxDistance > 1 || limit < 1 || limit > memoryGraphMaxNodes {
		return nil, ErrInvalid
	}
	query, params := memoryGraphSimilarQuery(in, ids, maxDistance, limit)
	var out []MemoryGraphSimilarPair
	err = p.transactionWithIsolation(ctx, pgx.ReadCommitted, func(ctx context.Context, tx Tx) error {
		q := tx.(*Postgres).q()
		if _, e := q.Exec(ctx, memoryGraphSimilarSettings); e != nil {
			return pgErr(e)
		}
		rows, e := q.Query(ctx, query, params...)
		if e != nil {
			return pgErr(e)
		}
		defer rows.Close()
		out = []MemoryGraphSimilarPair{}
		for rows.Next() {
			var pair MemoryGraphSimilarPair
			if e = rows.Scan(&pair.From, &pair.To, &pair.Distance); e != nil {
				return ErrUnavailable
			}
			out = append(out, pair)
		}
		return pgErr(rows.Err())
	})
	return out, err
}

// The filters (jsonb visibility, reach) make the planner expect one row, so it sorts a full scan per
// asking memory (16 s for 200 at 2,000 memories). Sorting off leaves the ordered index scan (0.4 s);
// the iterative scan keeps reading the index until LIMIT rows pass those filters. The index spans
// every scope, so the scan cap is raised from 20,000: other scopes' nearby vectors must not use it up.
// shortcut: a scope beyond 200,000 closer memories of other scopes can still lose lines; give each scope its own index then.
const memoryGraphSimilarSettings = `SET LOCAL hnsw.iterative_scan = relaxed_order; SET LOCAL hnsw.ef_search = 100; SET LOCAL hnsw.max_scan_tuples = 200000; SET LOCAL enable_sort = off`

// memoryGraphSimilarQuery asks the memory_records_memory_graph_vector index (migration 0004) for each
// asking memory's nearest neighbours instead of comparing every pair (#175: 12.8 s at 5,202 memories).
// The halfvec distance is approximate to about 1e-3; the map only draws lines from it.
func memoryGraphSimilarQuery(in MemoryGraphQuery, ids []string, maxDistance float64, limit int) (string, []any) {
	w, args := memoryGraphWhere(in, "m.", 6)
	params := append([]any{in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, colMemories}, args...)
	params = append(params, ids, maxDistance, limit, VectorDimension)
	return `WITH asking AS MATERIALIZED (
SELECT c.id, c.embedding::halfvec(1024) AS hv, c.embedding_space, ` + pgReach("c.") + ` AS reach, COALESCE(c.doc->>'reach_project','') AS rp FROM memory_records c
WHERE c.collection=$5 AND c.workspace_id=$1 AND c.project_id=$2 AND c.user_id=$3 AND c.agent_id=$4 AND c.id=ANY($8::text[]) AND c.embedding IS NOT NULL AND c.embedding_dimension=$11
)
SELECT c.id, n.id, n.d FROM asking c CROSS JOIN LATERAL (
SELECT m.id, m.embedding::halfvec(1024) <=> c.hv AS d FROM memory_records m
WHERE m.collection='memories' AND m.workspace_id=$1 AND m.project_id=$2 AND m.user_id=$3 AND m.agent_id=$4 AND m.embedding IS NOT NULL AND m.embedding_dimension=$11 AND ` + w + `
AND m.id<>c.id AND m.embedding_space=c.embedding_space AND ` + pgReach("m.") + `=c.reach AND COALESCE(m.doc->>'reach_project','')=c.rp
ORDER BY m.embedding::halfvec(1024) <=> c.hv LIMIT $10) n
WHERE n.d <= $9 ORDER BY c.id, n.d, n.id`, params
}
