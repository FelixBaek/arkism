package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const pgTextSearchExpression = `to_tsvector('simple', COALESCE(doc->>'text','') || ' ' || COALESCE(doc->>'summary','') || ' ' || COALESCE((doc->'keywords')::text,''))`

// pgSearchVector is what the fts lane matches and ranks: the normalized
// search_tokens (see search_tokens.go), or the legacy expression for a row the
// backfill has not reached yet.
const pgSearchVector = `COALESCE(search_tokens, ` + pgTextSearchExpression + `)`

// pgSearchMatch ORs query terms over search_tokens; rows not yet backfilled
// are matched through the legacy expression index with the same terms.
const pgSearchMatch = `(search_tokens @@ $%d::tsquery OR (search_tokens IS NULL AND ` + pgTextSearchExpression + ` @@ $%d::tsquery)) AND cardinality($%d::text[]) > 0`

// pgSearchOrder ranks by how many distinct query terms a row contains, then by
// the cover-density rank the lane used before, then by recency.
func pgSearchOrder(queryParam, termsParam int) string {
	return fmt.Sprintf("(SELECT count(*) FROM unnest(tsvector_to_array(%s)) l WHERE l = ANY($%d::text[])) DESC,ts_rank_cd(%s,$%d::tsquery) DESC,updated_at DESC,id ASC",
		pgSearchVector, termsParam, pgSearchVector, queryParam)
}

// selectRecords keeps the tenant fence in one place. The caller's predicates
// start at $6; this makes it difficult to accidentally omit a scope field.
func (p *Postgres) selectRecords(ctx context.Context, collection string, scope Scope, where string, args []any, order string, limit int) ([]Record, error) {
	if err := checkScope(scope); err != nil || collection == "" || limit <= 0 {
		return nil, ErrInvalid
	}
	if order == "" {
		order = "updated_at DESC, id ASC"
	}
	query := "SELECT payload FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5"
	if where != "" {
		query += " AND (" + where + ")"
	}
	query += " ORDER BY " + order + " LIMIT $" + fmt.Sprint(len(args)+6)
	params := []any{collection, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID}
	params = append(params, args...)
	params = append(params, limit)
	rows, err := p.q().Query(ctx, query, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := make([]Record, 0, limit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, ErrUnavailable
		}
		rec, err := pgDecode(payload)
		if err != nil {
			return nil, ErrUnavailable
		}
		if rec.ID == "" || !sameScope(rec.Scope, scope) {
			continue
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, pgErr(err)
	}
	return out, nil
}

func pgSearchState(criteria SearchCriteria, now time.Time, start int) (string, []any) {
	statuses := []string{statusActive}
	if criteria.history {
		statuses = memorySearchStatuses
	}
	if criteria.Filters != nil && len(criteria.Filters.Statuses) > 0 {
		statuses = criteria.Filters.Statuses
	}
	// Quarantined rows are dropped in SQL so they cannot spend the LIMIT.
	parts := []string{fmt.Sprintf("doc->>'status' = ANY($%d::text[])", start), "COALESCE((doc->>'privacy_quarantined')::boolean,false)=false"}
	args := []any{statuses}
	if criteria.history {
		parts = append(parts, "COALESCE((doc->>'content_erased')::boolean,false)=false", "NULLIF(doc->>'approved_by','') IS NOT NULL")
	} else {
		parts = append(parts, fmt.Sprintf("(doc->'effective_at' IS NULL OR (doc->'effective_at'->>'$date')::timestamptz <= $%d)", start+1), fmt.Sprintf("(doc->'expires_at' IS NULL OR (doc->'expires_at'->>'$date')::timestamptz > $%d)", start+2))
		args = append(args, now, now)
	}
	return strings.Join(parts, " AND "), args
}

func pgCriteria(criteria SearchCriteria, start int) (string, []any) {
	parts := []string{}
	args := []any{}
	next := start
	add := func(sql string, value any) {
		parts = append(parts, fmt.Sprintf(sql, next))
		args = append(args, value)
		next++
	}
	if len(criteria.Kinds) > 0 {
		add("doc->>'kind' = ANY($%d::text[])", criteria.Kinds)
	}
	// ST-824: the same rule as reachVisible, so other projects' memories never spend the LIMIT.
	add("(doc->>'reach' IS DISTINCT FROM 'project' OR ($%[1]d <> '' AND doc->>'reach_project' = $%[1]d))", criteria.ProjectKey)
	if f := criteria.Filters; f != nil {
		if len(f.Tags) > 0 {
			add("doc->'tags' ?| $%d::text[]", f.Tags)
		}
		if len(f.CreatedBy) > 0 {
			add("doc->>'created_by' = ANY($%d::text[])", f.CreatedBy)
		}
		if len(f.GeneratorModels) > 0 {
			add("doc->'generator'->>'model' = ANY($%d::text[])", f.GeneratorModels)
		}
		if f.CreatedFrom != nil {
			add("(doc->'created_at'->>'$date')::timestamptz >= $%d", *f.CreatedFrom)
		}
		if f.CreatedTo != nil {
			add("(doc->'created_at'->>'$date')::timestamptz < $%d", *f.CreatedTo)
		}
		if f.UpdatedFrom != nil {
			add("(doc->'updated_at'->>'$date')::timestamptz >= $%d", *f.UpdatedFrom)
		}
		if f.UpdatedTo != nil {
			add("(doc->'updated_at'->>'$date')::timestamptz < $%d", *f.UpdatedTo)
		}
		// File digests are rechecked after BSON decode; JSON containment handles
		// both artifact and source spans without trusting an unverified hit.
		if len(f.FileDigests) > 0 {
			parts = append(parts, fmt.Sprintf("(doc->'artifact'->>'digest' = ANY($%d::text[]) OR EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(doc->'sources','[]')) s WHERE s->'span'->>'content_hash' = ANY($%d::text[])))", next, next+1))
			args = append(args, f.FileDigests, f.FileDigests)
			next += 2
		}
	}
	return strings.Join(parts, " AND "), args
}

func pgSearchWhere(criteria SearchCriteria, collection string, now time.Time, extra string, args []any) (string, []any) {
	state, stateArgs := pgSearchState(criteria, now, 6)
	// state placeholders are relative to the selectRecords predicate ($6).
	parts := []string{state}
	params := append([]any{}, stateArgs...)
	if collection == colReflections {
		parts = append(parts, "COALESCE((doc->'reflection'->>'verified')::boolean,false)=true", "COALESCE((doc->'reflection'->>'secret_checked')::boolean,false)=true")
	}
	if extra != "" {
		start := 6 + len(params)
		values := make([]any, strings.Count(extra, "%d"))
		for i := range values {
			values[i] = start + i
		}
		parts = append(parts, fmt.Sprintf(extra, values...))
		params = append(params, args...)
	}
	crit, critArgs := pgCriteria(criteria, 6+len(params))
	if crit != "" {
		parts = append(parts, crit)
		params = append(params, critArgs...)
	}
	return strings.Join(parts, " AND "), params
}

func (p *Postgres) Search(ctx context.Context, scope Scope, query string, vector []float64, space string, limit int, modes ...string) ([][]Hit, error) {
	if err := checkScope(scope); err != nil || (len(vector) > 0 && (space == "" || len(vector) != VectorDimension)) || !validSearchModes(modes) {
		return nil, ErrInvalid
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	return p.searchCollections(ctx, []string{colMemories}, scope, query, vector, space, limit, SearchCriteria{}, modes...)
}

func (p *Postgres) SearchCollection(ctx context.Context, collection string, in SearchInput, vector []float64, space string) ([][]Hit, error) {
	if collection != colMemories && collection != colSkillVersions && collection != colReflections || in.IncludeInactive && collection != colMemories {
		return nil, ErrInvalid
	}
	if err := checkScope(in.Scope); err != nil || !validSearchModes(in.Modes) {
		return nil, ErrInvalid
	}
	if len(vector) > 0 && (space == "" || len(vector) != VectorDimension) {
		return nil, ErrInvalid
	}
	c, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return nil, err
	}
	c.history = in.IncludeInactive
	if in.Limit <= 0 {
		in.Limit = defaultSearchLimit
	}
	return p.searchCollections(ctx, []string{collection}, in.Scope, in.Query, vector, space, in.Limit, c, in.Modes...)
}

func (p *Postgres) searchCollections(ctx context.Context, collections []string, scope Scope, query string, vector []float64, space string, limit int, criteria SearchCriteria, modes ...string) ([][]Hit, error) {
	now := time.Now().UTC()
	out := make([][]Hit, 3)
	for _, collection := range collections {
		if searchModeEnabled(modes, "exact") {
			rows, err := p.pgLane(ctx, collection, scope, criteria, now, exactQueryKeywords(query), "exact", "", "", limit)
			if err != nil {
				return nil, err
			}
			out[0] = append(out[0], rows...)
		}
		if terms := searchQueryTerms(query); searchModeEnabled(modes, "fts") && len(terms) > 0 {
			rows, err := p.pgLane(ctx, collection, scope, criteria, now, terms, "fts", "", "", limit)
			if err != nil {
				return nil, err
			}
			out[1] = append(out[1], rows...)
		}
		if searchModeEnabled(modes, "vector") && len(vector) > 0 {
			rows, err := p.pgLane(ctx, collection, scope, criteria, now, nil, "vector", vectorSQL(vector), space, limit)
			if err != nil {
				return nil, err
			}
			out[2] = append(out[2], rows...)
		}
	}
	return out, nil
}

// vectorRelativeCut keeps a vector match only while its similarity stays within 93% of the
// best match for the same query (Letta's bootstrap cut): the nearest neighbours of an
// unrelated question are all far, and the tail of a related one drifts off topic.
const vectorRelativeCut = 0.93

// vectorKeepNearest matches always pass the relative cut (maxDistance still applies): a detailed
// request often has one memory clearly closest and the next relevant one just behind it
// (e.g. a worktree rule lost to a GitHub-flow memory on a "make a branch and commit" task).
const vectorKeepNearest = 3

// pgVectorCut narrows a vector-lane predicate to matches no farther than maxDistance and
// within vectorRelativeCut of the best visible match, or among the vectorKeepNearest closest.
// The nearest are taken over the same tenant and lane predicate, so filters and RLS bound
// them exactly like the rows themselves.
func pgVectorCut(where string, vectorParam int, maxDistance float64) string {
	distance := fmt.Sprintf("embedding <=> $%d::vector", vectorParam)
	nearest := fmt.Sprintf("(SELECT %s AS d FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND (%s) ORDER BY 1 LIMIT %d) nearest",
		distance, where, vectorKeepNearest)
	cut := fmt.Sprintf("(SELECT GREATEST(1-%g*(1-min(d)), max(d)) FROM %s)", vectorRelativeCut, nearest)
	return where + fmt.Sprintf(" AND %s <= LEAST(%g, %s)", distance, maxDistance, cut)
}

func vectorSQL(vector []float64) string {
	vals := make([]string, len(vector))
	for i, v := range vector {
		vals[i] = fmt.Sprintf("%g", v)
	}
	return "[" + strings.Join(vals, ",") + "]"
}

func (p *Postgres) pgLane(ctx context.Context, collection string, scope Scope, criteria SearchCriteria, now time.Time, terms []string, lane, vector, space string, limit int) ([]Hit, error) {
	if lane == "exact" && len(terms) == 0 {
		return nil, nil
	}
	extra, args := "", []any{}
	if lane == "exact" {
		extra = "doc->'keywords_lower' ?| $%d::text[]"
		args = append(args, terms)
	}
	if lane == "fts" {
		// terms are normalized (searchQueryTerms); any one of them matches.
		q := searchTermsQuery(terms)
		extra = pgSearchMatch
		args = append(args, q, q, terms)
	}
	// shortcut: exact vector scan; add a measured ANN index when corpus latency requires it.
	if lane == "vector" {
		extra = "embedding IS NOT NULL AND doc->'embedding'->>'space'=$%d AND (doc->'embedding'->>'dimension')::int=1024 AND embedding <=> $%d::vector < 2"
		args = append(args, space, vector)
	}
	where, params := pgSearchWhere(criteria, collection, now, extra, args)
	if lane == "vector" && p.vectorMaxDistance > 0 {
		_, stateArgs := pgSearchState(criteria, now, 6)
		where = pgVectorCut(where, 6+len(stateArgs)+1, p.vectorMaxDistance)
	}
	order := "updated_at DESC, id ASC"
	_, stateParams := pgSearchState(criteria, now, 6)
	termParam := 6 + len(stateParams)
	if lane == "exact" {
		order = fmt.Sprintf("(SELECT count(DISTINCT k) FROM jsonb_array_elements_text(doc->'keywords_lower') k WHERE k=ANY($%d::text[])) DESC,updated_at DESC,id ASC", termParam)
	}
	if lane == "fts" {
		order = pgSearchOrder(termParam, termParam+2)
	}
	if lane == "vector" {
		_, stateArgs := pgSearchState(criteria, now, 6)
		// Vector is the second lane argument (space is first); criteria
		// arguments are appended after it and must not shift this reference.
		order = fmt.Sprintf("embedding <=> $%d::vector ASC, id ASC", 6+len(stateArgs)+1)
	}
	rows, err := p.selectRecords(ctx, collection, scope, where, params, order, max(limit*3, limit))
	if err != nil {
		return nil, err
	}
	parents := map[string]Record{}
	for _, r := range rows {
		if collection == colSkillVersions && r.SkillID != "" {
			ps, _ := p.selectRecords(ctx, colSkills, scope, "id=$6", []any{r.SkillID}, "id ASC", 1)
			if len(ps) > 0 {
				parents[r.SkillID] = ps[0]
			}
		}
	}
	hits := make([]Hit, 0, limit)
	for _, r := range rows {
		var parent *Record
		if x, ok := parents[r.SkillID]; ok {
			parent = &x
		}
		if !criteria.matchesRecord(r) || !searchableLive(r, collection, space, now, parent, criteria.history) {
			continue
		}
		hits = append(hits, searchHit(r, collection, map[string]string{"exact": reasonExactKeyword, "fts": reasonTextSearch, "vector": reasonVector}[lane]))
		if len(hits) == limit {
			break
		}
	}
	return hits, nil
}

// SearchRelatedMemories uses the anchor's immutable references as a store-side
// filter, then repeats relationMatches after decoding. The second check keeps
// malformed JSON and stale relation snapshots out of the result.
func (p *Postgres) SearchRelatedMemories(ctx context.Context, in SearchInput, anchor Record) ([]Hit, error) {
	if checkScope(in.Scope) != nil || !validSearchRelation(in) || in.RelatedTo == nil || anchor.Scope != in.Scope || anchor.ID != in.RelatedTo.ID || anchor.Revision != in.RelatedTo.Revision || in.Limit < 1 || in.Limit > 50 {
		return nil, ErrInvalid
	}
	criteria, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return nil, err
	}
	criteria.history = in.IncludeInactive
	refs, err := relationReferences(in.RelatedTo.Collection, anchor)
	if err != nil {
		return nil, err
	}
	extra, args := "", []any{}
	if in.RelatedTo.Collection == "sources" {
		extra = "EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(doc->'sources','[]')) s WHERE s->>'source_id'=$%d)"
		args = append(args, anchor.ID)
	} else {
		if len(refs) == 0 {
			return []Hit{}, nil
		}
		parts := make([]string, 0, len(refs))
		for range refs {
			parts = append(parts, "(id=$%d AND (doc->>'revision')::bigint=$%d)")
		}
		extra = "(" + strings.Join(parts, " OR ") + ")"
		for _, ref := range refs {
			args = append(args, ref.ID, ref.Revision)
		}
	}
	// historySearchable runs in SQL too, so the LIMIT is not spent on rows it would drop.
	extra = "COALESCE((doc->>'content_erased')::boolean,false)=false AND COALESCE((doc->>'privacy_quarantined')::boolean,false)=false AND NULLIF(doc->>'approved_by','') IS NOT NULL AND " + extra
	where, params := pgSearchWhere(criteria, colMemories, time.Now().UTC(), extra, args)
	rows, err := p.selectRecords(ctx, colMemories, in.Scope, where, params, "updated_at DESC, id ASC", in.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(rows))
	for _, r := range rows {
		if !historySearchable(r) || (!criteria.history && !live(r)) || !criteria.matchesRecord(r) || !relationMatches(in.RelatedTo.Collection, anchor, r) {
			continue
		}
		out = append(out, searchHit(r, colMemories, "relation"))
	}
	return out, nil
}

// RankVectors ranks an explicit ID allowlist that scope, binding, lifecycle and
// trust filters already produced. Similarity never widens that set: the query
// keeps the tenant fence, matches the exact embedding space and dimension, and
// returns only rows whose vector is present.
func (p *Postgres) RankVectors(ctx context.Context, scope Scope, collection string, ids []string, vector []float64, space string, limit int) ([]VectorRank, error) {
	if err := checkScope(scope); err != nil || collection == "" || space == "" || len(vector) != VectorDimension || limit <= 0 || len(ids) == 0 || len(ids) > knowledgeCandidateLimit {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT id, embedding <=> $7::vector FROM memory_records
WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND id=ANY($6::text[])
AND embedding IS NOT NULL AND embedding_space=$8 AND embedding_dimension=$9
ORDER BY embedding <=> $7::vector ASC, id ASC LIMIT $10`,
		collection, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, ids, vectorSQL(vector), space, VectorDimension, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	out := make([]VectorRank, 0, limit)
	for rows.Next() {
		var rank VectorRank
		if err := rows.Scan(&rank.ID, &rank.Distance); err != nil {
			return nil, ErrUnavailable
		}
		if !allowed[rank.ID] {
			return nil, ErrConflict
		}
		out = append(out, rank)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

const knowledgeVectorIndex = "memory_records_knowledge_embedding"

// InitSemanticIndex adds the pgvector ANN index for knowledge chunks only. It
// is a separate opt-in so that enabling semantic retrieval cannot silently
// change the plan of any other collection's vector lane, and so that the
// benchmark can measure build time and size with and without it.
func (p *Postgres) InitSemanticIndex(ctx context.Context, enable bool) error {
	statement := `DROP INDEX IF EXISTS ` + knowledgeVectorIndex
	if enable {
		statement = `CREATE INDEX IF NOT EXISTS ` + knowledgeVectorIndex + ` ON memory_records USING hnsw (embedding extensions.vector_cosine_ops) WHERE collection='` + colKnowledgeChunks + `'`
	}
	if _, err := p.q().Exec(ctx, statement); err != nil {
		return ErrUnavailable
	}
	return nil
}
