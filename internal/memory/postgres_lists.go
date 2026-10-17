package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const pgDate = `((doc->'created_at'->>'$date')::timestamptz)`
const pgUpdated = `((doc->'updated_at'->>'$date')::timestamptz)`
const pgTrust = `COALESCE((doc->>'trust')::double precision,0)`
const pgEvidence = `(SELECT count(DISTINCT source->>'source_id') FROM jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'sources')='array' THEN doc->'sources' ELSE '[]'::jsonb END) source WHERE COALESCE(source->>'source_id','')<>'')`

func (p *Postgres) ListRecords(ctx context.Context, scope Scope, collection string, limit int) ([]Record, error) {
	if !knownCollection(collection) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, collection, scope, "", nil, "updated_at DESC, id ASC", limit)
}

func (p *Postgres) ListRecordsByStatus(ctx context.Context, scope Scope, collection, status string, limit int) ([]Record, error) {
	if !knownCollection(collection) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, collection, scope, `status=$6`, []any{status}, "updated_at DESC, id ASC", limit)
}

func (p *Postgres) ListCrewRunsByStatus(ctx context.Context, scope Scope, status string, limit int) ([]Record, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colCrewRuns, scope, `doc->'crew_run'->>'status'=$6`, []any{status}, "updated_at DESC, id ASC", limit)
}

func (p *Postgres) ListSources(ctx context.Context, in SourceListInput) ([]Record, error) {
	in, after, err := normalizeSourceList(in)
	if err != nil {
		return nil, err
	}
	w := `status='active' AND (doc->'effective_at' IS NULL OR (doc->'effective_at'->>'$date')::timestamptz <= now()) AND (doc->'expires_at' IS NULL OR (doc->'expires_at'->>'$date')::timestamptz > now()) AND (doc->'collected_at'->>'$date')::timestamptz > '0001-01-01T00:00:00Z'::timestamptz AND COALESCE((doc->>'privacy_quarantined')::boolean,false)=false`
	args := []any{}
	if in.Query != "" {
		w += ` AND strpos(COALESCE(doc->>'text',''),$6)>0`
		args = append(args, in.Query)
	}
	if in.Kind != "" {
		w += fmt.Sprintf(` AND kind=$%d`, len(args)+6)
		args = append(args, in.Kind)
	}
	if in.OriginProduct != "" {
		w += fmt.Sprintf(` AND doc->>'origin_product'=$%d`, len(args)+6)
		args = append(args, in.OriginProduct)
	}
	if in.CollectedFrom != nil {
		w += fmt.Sprintf(` AND (doc->'collected_at'->>'$date')::timestamptz >= $%d`, len(args)+6)
		args = append(args, *in.CollectedFrom)
	}
	if in.CollectedTo != nil {
		w += fmt.Sprintf(` AND (doc->'collected_at'->>'$date')::timestamptz < $%d`, len(args)+6)
		args = append(args, *in.CollectedTo)
	}
	if after != nil {
		w += fmt.Sprintf(` AND (%s < $%d OR (%s = $%d AND id < $%d))`, `(doc->'collected_at'->>'$date')::timestamptz`, len(args)+6, `(doc->'collected_at'->>'$date')::timestamptz`, len(args)+6, len(args)+7)
		args = append(args, after.CollectedAt, after.ID)
	}
	return p.selectRecords(ctx, "sources", in.Scope, w, args, `(doc->'collected_at'->>'$date')::timestamptz DESC,id DESC`, in.Limit+1)
}

func (p *Postgres) ListMemories(ctx context.Context, in MemoryListInput) ([]Record, error) {
	in, after, err := normalizeMemoryList(in)
	if err != nil {
		return nil, err
	}
	w, args := `COALESCE((doc->>'content_erased')::boolean,false)=false AND COALESCE((doc->>'privacy_quarantined')::boolean,false)=false`, []any{}
	if in.Query != "" {
		w += ` AND strpos(COALESCE(doc->>'text',''),$6)>0`
		args = append(args, in.Query)
	}
	if in.Kind != "" {
		w += fmt.Sprintf(` AND kind=$%d`, len(args)+6)
		args = append(args, in.Kind)
	}
	if in.Status != "" {
		w += fmt.Sprintf(` AND status=$%d`, len(args)+6)
		args = append(args, in.Status)
	}
	if !in.IncludeInactive {
		w += ` AND status='active' AND (doc->'effective_at' IS NULL OR (doc->'effective_at'->>'$date')::timestamptz <= now()) AND (doc->'expires_at' IS NULL OR (doc->'expires_at'->>'$date')::timestamptz > now())`
	}
	if after != "" {
		w += fmt.Sprintf(" AND id>$%d", len(args)+6)
		args = append(args, after)
	}
	return p.selectRecords(ctx, "memories", in.Scope, w, args, "id ASC", in.Limit+1)
}

func (p *Postgres) ListCandidates(ctx context.Context, in ReviewInput) ([]Record, error) {
	in, after, err := normalizeReview(in)
	if err != nil {
		return nil, err
	}
	w, args := `status=$6 AND COALESCE((doc->>'privacy_quarantined')::boolean,false)=false`, []any{in.Status}
	if in.Kind != "" {
		w += fmt.Sprintf(" AND kind=$%d", len(args)+6)
		args = append(args, in.Kind)
	}
	if in.Proposer != "" {
		w += fmt.Sprintf(" AND doc->>'created_by'=$%d", len(args)+6)
		args = append(args, in.Proposer)
	}
	if in.CreatedFrom != nil {
		w += fmt.Sprintf(" AND %s >= $%d", pgDate, len(args)+6)
		args = append(args, *in.CreatedFrom)
	}
	if in.CreatedTo != nil {
		w += fmt.Sprintf(" AND %s < $%d", pgDate, len(args)+6)
		args = append(args, *in.CreatedTo)
	}
	if after != nil {
		w += fmt.Sprintf(" AND (%s < $%d OR (%s=$%d AND %s < $%d) OR (%s=$%d AND %s=$%d AND %s < $%d) OR (%s=$%d AND %s=$%d AND %s=$%d AND id<$%d))", pgTrust, len(args)+6, pgTrust, len(args)+6, pgEvidence, len(args)+7, pgTrust, len(args)+6, pgEvidence, len(args)+7, pgDate, len(args)+8, pgTrust, len(args)+6, pgEvidence, len(args)+7, pgDate, len(args)+8, len(args)+9)
		args = append(args, after.Trust, after.Evidence, after.CreatedAt, after.ID)
	}
	return p.selectRecords(ctx, "memory_candidates", in.Scope, w, args, pgTrust+" DESC,"+pgEvidence+" DESC,"+pgDate+" DESC,id DESC", in.Limit+1)
}

func (p *Postgres) ListSkillRecords(ctx context.Context, s Scope, limit int, after string) ([]Record, error) {
	if !validScope(s) || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	w := `status='active' AND doc->>'active_version' <> ''`
	args := []any{}
	if after != "" {
		w += ` AND id>$6`
		args = append(args, after)
	}
	return p.selectRecords(ctx, "skills", s, w, args, "id ASC", limit+1)
}

// ListDeprecatedSkillRecords lists retired skills (스킬 › 휴지통) in id order, one page past the cursor.
func (p *Postgres) ListDeprecatedSkillRecords(ctx context.Context, s Scope, limit int, after string) ([]Record, error) {
	if !validScope(s) || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	w := `status='deprecated' AND doc->>'active_version' <> ''`
	args := []any{}
	if after != "" {
		w += ` AND id>$6`
		args = append(args, after)
	}
	return p.selectRecords(ctx, "skills", s, w, args, "id ASC", limit+1)
}

// Containment is written against the whole doc so memory_records_document (GIN jsonb_path_ops)
// serves it; `doc->'x' @> ...` forces a scan of every record in the collection.
func (p *Postgres) ListSourceArtifacts(ctx context.Context, sourceID string, in ArtifactListInput) ([]Record, error) {
	in, err := normalizeArtifactList(in)
	if err != nil {
		return nil, err
	}
	if !validRunLabel(sourceID, 256) || containsSecret(sourceID) {
		return nil, ErrInvalid
	}
	w := `doc @> $6::jsonb`
	args := []any{fmt.Sprintf(`{"sources":[{"source_id":%q}]}`, sourceID)}
	if in.Cursor != "" {
		w += ` AND id>$7`
		args = append(args, in.Cursor)
	}
	return p.selectRecords(ctx, "artifacts", in.Scope, w, args, "id ASC", in.Limit+1)
}

func (p *Postgres) ListSourceDerived(ctx context.Context, sourceID string, in DerivedListInput) ([]Record, error) {
	in, err := normalizeDerivedList(in)
	if err != nil {
		return nil, err
	}
	if !validRunLabel(sourceID, 256) || containsSecret(sourceID) {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, in.Collection, in.Scope, `doc->'sources' @> $6::jsonb AND ($7='' OR id>$7)`, []any{fmt.Sprintf(`[{"source_id":%q}]`, sourceID), in.Cursor}, "id ASC", in.Limit+1)
}

func (p *Postgres) SourceDerivedReferences(ctx context.Context, sourceID string, in DerivedCountInput) ([]Ref, error) {
	in, err := normalizeDerivedCount(in)
	if err != nil {
		return nil, err
	}
	if !validRunLabel(sourceID, 256) || containsSecret(sourceID) {
		return nil, ErrInvalid
	}
	rows, err := p.selectRecords(ctx, in.Collection, in.Scope, `doc @> $6::jsonb`, []any{fmt.Sprintf(`{"sources":[{"source_id":%q}]}`, sourceID)}, "id ASC", sourceDerivedCountLimit+1)
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(rows))
	for _, r := range rows {
		out = append(out, Ref{Collection: in.Collection, ID: r.ID, Revision: r.Revision})
	}
	return out, nil
}

func (p *Postgres) MemoryUsage(ctx context.Context, s Scope, id string) ([]Record, error) {
	if checkScope(s) != nil || id == "" {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, "jobs", s, `kind='context_usage' AND doc @> $6::jsonb`, []any{fmt.Sprintf(`{"usage":{"items":[{"memory":{"collection":"memories","id":%q}}]}}`, id)}, pgDate+" DESC,id DESC", 10)
}

func (p *Postgres) ExpiringCandidates(ctx context.Context, s Scope, before time.Time, limit int) ([]Record, error) {
	if checkScope(s) != nil || before.IsZero() || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, "memory_candidates", s, `status='candidate' AND (doc->>'conflict_id' IS NULL OR doc->>'conflict_id'='') AND `+pgDate+` > '0001-01-01T00:00:00Z'::timestamptz AND `+pgDate+` <= $6 AND `+pgUpdated+` > '0001-01-01T00:00:00Z'::timestamptz AND `+pgUpdated+` <= $6`, []any{before}, pgDate+` ASC,id ASC`, limit)
}

func (p *Postgres) OperationalMetrics(ctx context.Context, s Scope) (StoreMetrics, error) {
	out := StoreMetrics{EmbeddingJobs: map[string]QueueMetric{}, SourceMemoryErasureJobs: map[string]QueueMetric{}}
	if checkScope(s) != nil {
		return out, ErrInvalid
	}
	now := time.Now().UTC()
	statuses := []string{JobStatusPending, JobStatusRetry, JobStatusRunning, JobStatusDone, JobStatusFailed}
	collect := func(collection, predicate string, args []any) (map[string]QueueMetric, error) {
		query := `SELECT status,count(*),min(` + pgDate + `) FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND (` + predicate + `) GROUP BY status`
		params := []any{collection, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID}
		params = append(params, args...)
		rows, err := p.q().Query(ctx, query, params...)
		if err != nil {
			return nil, pgErr(err)
		}
		defer rows.Close()
		result := map[string]QueueMetric{}
		for rows.Next() {
			var m QueueMetric
			if err = rows.Scan(&m.Status, &m.Count, &m.Oldest); err != nil {
				return nil, ErrUnavailable
			}
			if m.Oldest.Before(now) {
				m.OldestAgeSeconds = now.Sub(m.Oldest).Seconds()
			}
			result[m.Status] = m
		}
		return result, pgErr(rows.Err())
	}
	candidates, err := collect("memory_candidates", `status='candidate' AND (doc->'expires_at' IS NULL OR (doc->'expires_at'->>'$date')::timestamptz>$6)`, []any{now})
	if err != nil {
		return StoreMetrics{}, err
	}
	out.Candidates = candidates["candidate"]
	if out.EmbeddingJobs, err = collect(colJobs, `kind='embedding' AND status=ANY($6)`, []any{statuses}); err != nil {
		return StoreMetrics{}, err
	}
	if out.SourceMemoryErasureJobs, err = collect(colJobs, `kind=ANY($6) AND status=ANY($7)`, []any{erasureJobKinds, statuses}); err != nil {
		return StoreMetrics{}, err
	}
	for _, status := range statuses {
		if _, ok := out.EmbeddingJobs[status]; !ok {
			out.EmbeddingJobs[status] = QueueMetric{}
		}
		if _, ok := out.SourceMemoryErasureJobs[status]; !ok {
			out.SourceMemoryErasureJobs[status] = QueueMetric{}
		}
	}
	err = p.q().QueryRow(ctx, `SELECT
COALESCE(sum((doc->'cost'->>'requests')::bigint),0),
COALESCE(sum((doc->'cost'->>'requests')::bigint) FILTER (WHERE status='done'),0),
COALESCE(sum((doc->'cost'->>'requests')::bigint) FILTER (WHERE status='failed'),0),
COALESCE(sum((doc->'cost'->>'requests')::bigint) FILTER (WHERE status='pending'),0),
COALESCE(sum((doc->'cost'->>'requests')::bigint) FILTER (WHERE status='done' AND COALESCE((doc->'cost'->>'pricing_available')::boolean,false)=false),0),
COALESCE(sum((doc->'cost'->>'input_tokens')::bigint),0),
COALESCE(sum((doc->'cost'->>'output_tokens')::bigint),0),
COALESCE(sum((doc->'cost'->>'total_tokens')::bigint),0),
COALESCE(sum((doc->'cost'->>'estimated_cost_pico_usd')::bigint),0)
FROM memory_records WHERE collection='provider_usage' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4`, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID).Scan(&out.Cost.Requests, &out.Cost.CompletedRequests, &out.Cost.FailedRequests, &out.Cost.PendingRequests, &out.Cost.UnpricedRequests, &out.Cost.InputTokens, &out.Cost.OutputTokens, &out.Cost.TotalTokens, &out.Cost.EstimatedCostPicoUSD)
	if err != nil {
		return StoreMetrics{}, pgErr(err)
	}
	out.Cost.Available = true
	return out, nil
}

func (p *Postgres) EvaluationsFor(ctx context.Context, s Scope, target Ref) ([]Record, error) {
	if checkScope(s) != nil || target.ID == "" || !knownCollection(target.Collection) || target.Revision < 1 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, "evaluations", s, `doc->'target'->>'collection'=$6 AND doc->'target'->>'id'=$7 AND (doc->'target'->>'revision')::bigint=$8 AND status='active' AND (doc->'expires_at' IS NULL OR (doc->'expires_at'->>'$date')::timestamptz > now())`, []any{target.Collection, target.ID, target.Revision}, "id ASC", evaluationSummaryLimit+1)
}

// BlobReferences is a global, body-free count used only under the local file
// lock for integrity/GC. It intentionally crosses artifact scopes; public CRUD
// cannot call it.
func (p *Postgres) BlobReferences(ctx context.Context, digest string) (int64, error) {
	if !sourceHashPattern.MatchString(digest) {
		return 0, ErrInvalid
	}
	var n int64
	err := p.q().QueryRow(ctx, `SELECT count(*) FROM memory_records WHERE collection='artifacts' AND doc->'artifact'->>'digest'=$1`, digest).Scan(&n)
	return n, pgErr(err)
}

func (p *Postgres) BlobStorageIdentity(ctx context.Context) (string, error) {
	var db string
	var oid uint32
	var seed []byte
	if err := p.q().QueryRow(ctx, `SELECT current_database(),d.oid,m.value FROM pg_database d CROSS JOIN memory_storage_metadata m WHERE d.datname=current_database() AND m.key='storage_identity'`).Scan(&db, &oid, &seed); err != nil {
		return "", pgErr(err)
	}
	if len(seed) != sha256.Size {
		return "", ErrUnavailable
	}
	h := sha256.New()
	_, _ = h.Write([]byte(db + "\n" + fmt.Sprint(oid) + "\n"))
	_, _ = h.Write(seed)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *Postgres) BlobGCBatch(ctx context.Context, after string) ([]string, error) {
	if after != "" && !sourceHashPattern.MatchString(after) {
		return nil, ErrInvalid
	}
	s := blobScope()
	rows, err := p.q().Query(ctx, `SELECT id FROM memory_records WHERE collection='blobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind='blob' AND id>$5 AND (doc->'blob'->>'references')::bigint=0 AND status IN ('active','pending') ORDER BY id LIMIT 64`, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID, after)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, ErrUnavailable
		}
		if !sourceHashPattern.MatchString(id) {
			return nil, ErrUnavailable
		}
		out = append(out, id)
	}
	return out, pgErr(rows.Err())
}

func (p *Postgres) DeletionRecords(ctx context.Context, s Scope) ([]Record, error) {
	if checkScope(s) != nil {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, "jobs", s, "kind=ANY($6)", []any{deletionLedgerKinds}, "id ASC", deletionLedgerLimit+1)
}

func (p *Postgres) ErasureChildren(ctx context.Context, s Scope, parent string) ([]Record, error) {
	if checkScope(s) != nil || !validRunLabel(parent, 256) {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colJobs, s, "doc->>'erasure_parent_id'=$6 AND kind=ANY($7)", []any{parent, []string{memoryEvaluationErasureKind, candidateEvaluationErasureKind}}, "id ASC", 4097)
}

func (p *Postgres) DeletionReferences(ctx context.Context, col string, s Scope, target Ref, limit int) ([]Ref, error) {
	if checkScope(s) != nil || (!knownCollection(col) && col != colArtifacts) || col == "audit_events" || (target.Collection != "sources" && target.Collection != "memories") || !validRunLabel(target.ID, 256) || limit < 1 || limit > deletionPreviewLimit+1 {
		return nil, ErrInvalid
	}
	ref := fmt.Sprintf(`[{"collection":%q,"id":%q}]`, target.Collection, target.ID)
	usage := fmt.Sprintf(`[{"memory":{"collection":%q,"id":%q}}]`, target.Collection, target.ID)
	clauses := []string{
		`(doc->'target'->>'collection'=$6 AND doc->'target'->>'id'=$7)`,
		`(doc->'proposal'->>'collection'=$6 AND doc->'proposal'->>'id'=$7)`,
		`(doc->'superseded_by'->>'collection'=$6 AND doc->'superseded_by'->>'id'=$7)`,
		`(doc->'conflict'->'replacement'->>'collection'=$6 AND doc->'conflict'->'replacement'->>'id'=$7)`,
		`(doc->'usage'->'run'->>'collection'=$6 AND doc->'usage'->'run'->>'id'=$7)`,
		`doc->'relations' @> $8::jsonb`,
		`doc->'supersedes' @> $8::jsonb`,
		`doc->'conflict'->'missing' @> $8::jsonb`,
		`doc->'usage'->'items' @> $9::jsonb`,
	}
	args := []any{target.Collection, target.ID, ref, usage}
	if target.Collection == "sources" {
		clauses = append(clauses, `doc->'sources' @> $10::jsonb`)
		args = append(args, fmt.Sprintf(`[{"source_id":%q}]`, target.ID))
	}
	w := "(" + strings.Join(clauses, " OR ") + ")"
	if col == target.Collection {
		w += fmt.Sprintf(" AND id<>$%d", len(args)+6)
		args = append(args, target.ID)
	}
	rows, err := p.selectRecords(ctx, col, s, w, args, "id ASC", limit)
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(rows))
	for _, r := range rows {
		out = append(out, Ref{Collection: col, ID: r.ID, Revision: r.Revision})
	}
	return out, nil
}

func (p *Postgres) SourceErasureBatch(ctx context.Context, s Scope, id, kind string) ([]Ref, error) {
	if checkScope(s) != nil || id == "" {
		return nil, ErrInvalid
	}
	cols := sourceErasureCollections(kind)
	if cols == nil {
		return nil, ErrInvalid
	}
	out := make([]Ref, 0, sourceMemoryErasureBatch)
	for _, col := range cols {
		if len(out) == sourceMemoryErasureBatch {
			break
		}
		// Some records (synthetic consultations) cite their source only by source_event_id.
		rows, e := p.selectRecords(ctx, col, s, `(doc @> $6::jsonb OR (doc->>'source_event_id'=$7 AND id<>$7))`, []any{fmt.Sprintf(`{"sources":[{"source_id":%q}]}`, id), id}, "id ASC", sourceMemoryErasureBatch-len(out))
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			if col == colSkills {
				children, e := p.selectRecords(ctx, colSkillVersions, s, `doc->>'skill_id'=$6`, []any{r.ID}, "id ASC", sourceMemoryErasureBatch-len(out))
				if e != nil {
					return nil, e
				}
				if len(children) > 0 {
					for _, child := range children {
						ref := Ref{Collection: colSkillVersions, ID: child.ID, Revision: child.Revision}
						found := false
						for _, existing := range out {
							found = found || existing == ref
						}
						if !found {
							out = append(out, ref)
						}
					}
					return out, nil
				}
			}
			out = append(out, Ref{Collection: col, ID: r.ID, Revision: r.Revision})
			if len(out) == sourceMemoryErasureBatch {
				break
			}
		}
	}
	return out, nil
}

func (p *Postgres) MemoryErasureBatch(ctx context.Context, s Scope, id string, proposal bool, after, kind string) ([]Ref, error) {
	if checkScope(s) != nil || !isMemoryErasureKind(kind) || !validRunLabel(id, 256) || (after != "" && !validRunLabel(after, 256)) {
		return nil, ErrInvalid
	}
	cols := memoryErasureTargets(kind, proposal)
	if kind == memoryEvaluationErasureKind || kind == candidateEvaluationErasureKind {
		rows, e := p.selectRecords(ctx, "evaluations", s, `doc->'target'->>'id'=$6 AND doc->'target'->>'collection'=ANY($7)`, []any{id, cols}, "id ASC", sourceMemoryErasureBatch)
		if e != nil {
			return nil, e
		}
		if len(rows) > 0 {
			out := make([]Ref, 0, len(rows))
			for _, r := range rows {
				out = append(out, Ref{Collection: "evaluations", ID: r.ID, Revision: r.Revision})
			}
			return out, nil
		}
	}
	parts := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, col := range cols {
		parts[i] = fmt.Sprintf(`doc->'relations' @> $%d::jsonb`, i+6)
		args[i] = fmt.Sprintf(`[{"collection":%q,"id":%q}]`, col, id)
	}
	w := "(" + strings.Join(parts, " OR ") + ")"
	if kind == memoryEvaluationErasureKind || kind == candidateEvaluationErasureKind {
		for _, col := range []string{colSkillVersions, "sources"} {
			rows, e := p.selectRecords(ctx, col, s, w+" AND kind IN ('lesson_skill_proposal','lesson_skill_source')", args, "id ASC", sourceMemoryErasureBatch)
			if e != nil {
				return nil, e
			}
			if len(rows) > 0 {
				out := make([]Ref, 0, len(rows))
				for _, r := range rows {
					out = append(out, Ref{Collection: col, ID: r.ID, Revision: r.Revision})
				}
				return out, nil
			}
		}
	}
	if after != "" {
		w += fmt.Sprintf(" AND id>$%d", len(args)+6)
		args = append(args, after)
	}
	rows, e := p.selectRecords(ctx, "memory_relations", s, w, args, "id ASC", sourceMemoryErasureBatch)
	if e != nil {
		return nil, e
	}
	out := make([]Ref, 0, len(rows))
	for _, r := range rows {
		out = append(out, Ref{Collection: "memory_relations", ID: r.ID, Revision: r.Revision})
	}
	return out, nil
}

func (p *Postgres) ValidateBlobRestore(ctx context.Context, files map[string]int64) error {
	counts := map[string]int64{}
	for _, col := range []string{colArtifacts, colBlobs} {
		rows, err := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection=$1 ORDER BY id LIMIT $2`, col, maxBackupFiles+1)
		if err != nil {
			return pgErr(err)
		}
		n := 0
		for rows.Next() {
			n++
			if n > maxBackupFiles {
				rows.Close()
				return ErrUnavailable
			}
			var b []byte
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return ErrUnavailable
			}
			r, e := pgDecode(b)
			if e != nil {
				rows.Close()
				return e
			}
			if col == colArtifacts {
				e = countRestoreArtifact(r, files, counts)
			} else {
				e = checkRestoreBlob(r, files, counts)
			}
			if e != nil {
				rows.Close()
				return e
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return pgErr(err)
		}
		rows.Close()
	}
	if len(counts) != 0 {
		return ErrUnavailable
	}
	return nil
}

func (p *Postgres) ListSkillTrialRecords(ctx context.Context, scope Scope, skillID, candidateID string, limit int, after string) ([]Record, error) {
	if !validScope(scope) || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colSkillTrials, scope, `doc->>'skill_id'=$6 AND doc->'skill_trial'->'candidate'->>'id'=$7 AND id>$8`, []any{skillID, candidateID, after}, "id ASC", limit+1)
}

// memoryOutcomeWhere is the one filter for ST-812 reads; selectRecords and the
// aggregate below add the scope columns. Arguments start at $6.
func memoryOutcomeWhere(in MemoryOutcomeQuery) (string, []any) {
	w := `kind='memory_outcome' AND status='active' AND COALESCE((doc->>'content_erased')::boolean,false)=false AND COALESCE((doc->>'privacy_quarantined')::boolean,false)=false AND (doc->'expires_at' IS NULL OR (doc->'expires_at'->>'$date')::timestamptz > now()) AND doc->'target'->>'collection'='memories' AND doc->'target'->>'id'=ANY($6) AND (doc->'tags' ? 'dimension:personal_compliance' OR doc->'tags' ? 'dimension:work_task')`
	args := []any{in.MemoryIDs}
	if in.Dimension != "" {
		w += fmt.Sprintf(` AND doc->'tags' ? $%d`, len(args)+6)
		args = append(args, "dimension:"+in.Dimension)
	}
	if in.From != nil {
		w += fmt.Sprintf(" AND %s >= $%d", pgDate, len(args)+6)
		args = append(args, *in.From)
	}
	if in.To != nil {
		w += fmt.Sprintf(" AND %s < $%d", pgDate, len(args)+6)
		args = append(args, *in.To)
	}
	return w, args
}

func (p *Postgres) MemoryOutcomes(ctx context.Context, in MemoryOutcomeQuery) ([]Record, error) {
	in, after, err := normalizeMemoryOutcomeQuery(in, true)
	if err != nil {
		return nil, err
	}
	w, args := memoryOutcomeWhere(in)
	if after != nil {
		w += fmt.Sprintf(" AND (%s < $%d OR (%s = $%d AND id < $%d))", pgDate, len(args)+6, pgDate, len(args)+6, len(args)+7)
		args = append(args, after.At, after.ID)
	}
	return p.selectRecords(ctx, "evaluations", in.Scope, w, args, pgDate+" DESC,id DESC", in.Limit+1)
}

func (p *Postgres) MemoryOutcomeCounts(ctx context.Context, in MemoryOutcomeQuery) ([]MemoryOutcomeCount, error) {
	in, _, err := normalizeMemoryOutcomeQuery(in, false)
	if err != nil {
		return nil, err
	}
	w, args := memoryOutcomeWhere(in)
	params := append([]any{"evaluations", in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID}, args...)
	rows, err := p.q().Query(ctx, `SELECT doc->'target'->>'id', count(*), count(*) FILTER (WHERE (doc->>'passed')::boolean), max(`+pgDate+`) FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND (`+w+`) GROUP BY 1 ORDER BY 1`, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []MemoryOutcomeCount{}
	for rows.Next() {
		var c MemoryOutcomeCount
		if err = rows.Scan(&c.MemoryID, &c.Total, &c.Passed, &c.Last); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, c)
	}
	return out, pgErr(rows.Err())
}

// MemoryUseCounts counts distinct context_usage receipts that handed out each
// memory id; the scope columns are the only range condition for ST-813 uses.
func (p *Postgres) MemoryUseCounts(ctx context.Context, s Scope, ids []string) ([]MemoryUseCount, error) {
	if checkScope(s) != nil || len(ids) == 0 || len(ids) > memorySignalBatch {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT it->'memory'->>'id', count(DISTINCT id), max(`+pgDate+`) FROM memory_records, jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'usage'->'items')='array' THEN doc->'usage'->'items' ELSE '[]'::jsonb END) it WHERE collection='jobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind='context_usage' AND status='done' AND it->'memory'->>'collection'='memories' AND it->'memory'->>'id'=ANY($5) GROUP BY 1 ORDER BY 1`, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID, ids)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []MemoryUseCount{}
	for rows.Next() {
		var c MemoryUseCount
		if err = rows.Scan(&c.MemoryID, &c.Uses, &c.Last); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, c)
	}
	return out, pgErr(rows.Err())
}
