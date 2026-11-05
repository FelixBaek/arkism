package memory

import (
	"context"
	"sort"
	"time"
)

// recallWindowSQL keeps rows with any event-time field in the window, a superset;
// windowHits then applies hitEventTime's precedence exactly.
const recallWindowSQL = `((doc->'memory_context'->>'observed_at')::timestamptz >= $%d AND (doc->'memory_context'->>'observed_at')::timestamptz < $%d
 OR left(doc->'memory_context'->>'valid_from',10) BETWEEN $%d AND $%d
 OR (doc->>'effective_at')::timestamptz >= $%d AND (doc->>'effective_at')::timestamptz < $%d
 OR (doc->>'created_at')::timestamptz >= $%d AND (doc->>'created_at')::timestamptz < $%d)`

func (p *Postgres) SearchMemoriesInWindow(ctx context.Context, in SearchInput, from, to time.Time) ([]Hit, error) {
	if checkScope(in.Scope) != nil || !from.Before(to) || in.Limit < 1 || in.Limit > 50 {
		return nil, ErrInvalid
	}
	criteria, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	loc := from.Location()
	args := []any{from, to, from.Format(time.DateOnly), to.Format(time.DateOnly), from, to, from, to}
	where, params := pgSearchWhere(criteria, colMemories, now, recallWindowSQL, args)
	rows, err := p.selectRecords(ctx, colMemories, in.Scope, where, params, "updated_at DESC, id ASC", in.Limit*5)
	if err != nil {
		return nil, err
	}
	return windowHits(rows, criteria, now, timeWindow{From: from, To: to}, loc, in.Limit), nil
}

// windowHits keeps the live approved memories whose event time is in w, newest event first.
func windowHits(rows []Record, criteria SearchCriteria, now time.Time, w timeWindow, loc *time.Location, limit int) []Hit {
	hits := []Hit{}
	for _, r := range rows {
		if r.ApprovedBy == "" || !criteria.matchesRecord(r) || !searchableLive(r, colMemories, "", now, nil, false) {
			continue
		}
		if h := searchHit(r, colMemories, reasonTemporal); inWindow(hitEventTime(h, loc), w) {
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hitEventTime(hits[i], loc).After(hitEventTime(hits[j], loc)) })
	return hits[:min(limit, len(hits))]
}

// memoryNeighborsSQL walks one relations step from the seed memories: outgoing and
// incoming memory edges of any kind, and memory -mentions-> X <-mentions- memory.
// Each branch is LATERAL-limited per seed so a hub node cannot blow up the query.
const memoryNeighborsSQL = `WITH seed AS (SELECT id, pos FROM unnest($5::text[]) WITH ORDINALITY AS s(id, pos)),
edge AS (
 SELECT n.id, s.pos FROM seed s CROSS JOIN LATERAL (SELECT r.target_id AS id FROM relations r
  WHERE r.organization_id=$1 AND r.project_id=$2 AND r.user_id=$3 AND r.agent_id=$4
  AND r.source_kind IN ('memory','lesson') AND r.source_id=s.id AND r.target_kind IN ('memory','lesson') LIMIT $7) n
 UNION ALL
 SELECT n.id, s.pos FROM seed s CROSS JOIN LATERAL (SELECT r.source_id AS id FROM relations r
  WHERE r.organization_id=$1 AND r.project_id=$2 AND r.user_id=$3 AND r.agent_id=$4
  AND r.target_kind IN ('memory','lesson') AND r.target_id=s.id AND r.source_kind IN ('memory','lesson') LIMIT $7) n
 UNION ALL
 SELECT n.id, s.pos FROM seed s CROSS JOIN LATERAL (SELECT m.target_kind, m.target_id FROM relations m
  WHERE m.organization_id=$1 AND m.project_id=$2 AND m.user_id=$3 AND m.agent_id=$4
  AND m.source_kind IN ('memory','lesson') AND m.source_id=s.id AND m.relation_kind='mentions' LIMIT $7) x
 CROSS JOIN LATERAL (SELECT o.source_id AS id FROM relations o
  WHERE o.organization_id=$1 AND o.project_id=$2 AND o.user_id=$3 AND o.agent_id=$4
  AND o.target_kind=x.target_kind AND o.target_id=x.target_id AND o.relation_kind='mentions' AND o.source_kind IN ('memory','lesson') LIMIT $7) n)
SELECT id FROM edge WHERE id <> ALL($5::text[]) GROUP BY id ORDER BY count(*) DESC, min(pos) ASC, id ASC LIMIT $6`

func (p *Postgres) MemoryNeighbors(ctx context.Context, scope Scope, seeds []string, limit int) ([]string, error) {
	if checkScope(scope) != nil || len(seeds) == 0 || len(seeds) > 50 || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, memoryNeighborsSQL, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, seeds, limit, graphFanOut)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, pgErr(err)
		}
		out = append(out, id)
	}
	return out, pgErr(rows.Err())
}
