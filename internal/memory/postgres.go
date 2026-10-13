package memory

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgQuery interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Postgres struct {
	memoryObservedAt time.Time
	pool             *pgxpool.Pool
	tx               pgx.Tx
	erasureOnly      bool
	tokenHash        string
	ownsPool         bool
	// vectorMaxDistance > 0 keeps weak nearest neighbours out of the vector lane; see pgVectorCut.
	vectorMaxDistance float64
}

var _ Tx = (*Postgres)(nil)

func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrInvalid
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, ErrUnavailable
	}
	// Private application schema; Supabase installs vector in extensions.
	cfg.ConnConfig.RuntimeParams["search_path"] = "arkism,extensions,pg_catalog"
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, ErrUnavailable
	}
	return &Postgres{pool: p, ownsPool: true}, nil
}
func (p *Postgres) SetRecoveryMode(enabled bool) { p.erasureOnly = enabled }

// SetVectorMaxDistance sets the cosine distance above which a vector-only match is not
// returned. The value belongs to one embedding space (measured per model); 0 disables it.
func (p *Postgres) SetVectorMaxDistance(d float64) { p.vectorMaxDistance = d }
func (p *Postgres) Close() {
	if p != nil && p.pool != nil && p.ownsPool {
		p.pool.Close()
	}
}
func (p *Postgres) q() pgQuery {
	if p.tx != nil {
		return p.tx
	}
	return postgresQuery{store: p}
}

func (p *Postgres) Init(ctx context.Context) error {
	_, err := p.q().Exec(ctx, `CREATE SCHEMA IF NOT EXISTS arkism; REVOKE ALL ON SCHEMA arkism FROM PUBLIC; CREATE SCHEMA IF NOT EXISTS extensions; CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA extensions`)
	if err != nil {
		return ErrUnavailable
	}
	_, err = p.q().Exec(ctx, `CREATE TABLE IF NOT EXISTS memory_records (
collection text NOT NULL, id text NOT NULL, workspace_id text NOT NULL, project_id text NOT NULL,
user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '', revision bigint NOT NULL,
status text NOT NULL DEFAULT '', kind text NOT NULL DEFAULT '', updated_at timestamptz,
run_at timestamptz, lease_until timestamptz, lease_token text NOT NULL DEFAULT '', attempts integer NOT NULL DEFAULT 0,
	payload bytea NOT NULL, doc jsonb NOT NULL, embedding vector(1024), embedding_space text, embedding_dimension integer,
PRIMARY KEY (collection,id))`)
	if err != nil {
		return ErrUnavailable
	}
	_, err = p.q().Exec(ctx, `CREATE INDEX IF NOT EXISTS memory_records_scope ON memory_records (collection,workspace_id,project_id,user_id,agent_id)`)
	if err != nil {
		return ErrUnavailable
	}
	_, err = p.q().Exec(ctx, `ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding vector(1024); ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_space text; ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_dimension integer`)
	if err != nil {
		return ErrUnavailable
	}
	_, err = p.q().Exec(ctx, `CREATE INDEX IF NOT EXISTS memory_records_jobs ON memory_records (collection,status,run_at,lease_until)`)
	if err != nil {
		return ErrUnavailable
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS memory_records_scope_id ON memory_records(collection,workspace_id,project_id,user_id,agent_id,id)`,
		`CREATE INDEX IF NOT EXISTS memory_records_scope_state ON memory_records(collection,workspace_id,project_id,user_id,agent_id,status,updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS memory_records_keywords ON memory_records USING gin ((doc->'keywords_lower'))`,
		`CREATE INDEX IF NOT EXISTS memory_records_document ON memory_records USING gin (doc jsonb_path_ops)`,
		`CREATE INDEX IF NOT EXISTS memory_records_fts ON memory_records USING gin (` + pgTextSearchExpression + `)`,
	} {
		if _, err = p.q().Exec(ctx, stmt); err != nil {
			return ErrUnavailable
		}
	}
	if err = p.initSearchTokens(ctx); err != nil {
		return err
	}
	if _, err = p.q().Exec(ctx, `CREATE TABLE IF NOT EXISTS memory_storage_metadata (key text PRIMARY KEY, value bytea NOT NULL)`); err != nil {
		return ErrUnavailable
	}
	// Defense in depth for Supabase installations with broad default grants.
	if _, err = p.q().Exec(ctx, `REVOKE ALL ON ALL TABLES IN SCHEMA arkism FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
  FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      EXECUTE format('REVOKE ALL ON SCHEMA arkism FROM %I', role_name);
      EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA arkism FROM %I', role_name);
    END IF;
  END LOOP;
END $$;`); err != nil {
		return ErrUnavailable
	}
	var identity [32]byte
	if _, err = rand.Read(identity[:]); err != nil {
		return ErrUnavailable
	}
	if _, err = p.q().Exec(ctx, `INSERT INTO memory_storage_metadata(key,value) VALUES('storage_identity',$1) ON CONFLICT (key) DO NOTHING`, identity[:]); err != nil {
		return ErrUnavailable
	}
	if err = p.initTraceSchema(ctx); err != nil {
		return err
	}
	if err = p.initPrivacySchema(ctx); err != nil {
		return err
	}
	if err = p.initCodeGraphSchema(ctx); err != nil {
		return err
	}
	if err = p.InitRLS(ctx); err != nil {
		return err
	}
	return p.initRecoveryWriteGate(ctx)
}
func (p *Postgres) SearchReady(ctx context.Context) error {
	var ok bool
	if err := p.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname='vector') AND to_regclass('arkism.memory_records') IS NOT NULL AND to_regclass('arkism.memory_storage_metadata') IS NOT NULL AND to_regclass('arkism.memory_records_fts') IS NOT NULL AND to_regclass('arkism.memory_records_keywords') IS NOT NULL AND to_regclass('arkism.memory_records_search_tokens') IS NOT NULL
AND (SELECT count(*)=5 AND bool_and(relrowsecurity AND relforcerowsecurity) FROM pg_class WHERE oid=ANY(ARRAY[to_regclass('arkism.memory_records'),to_regclass('arkism.execution_traces'),to_regclass('arkism.source_events'),to_regclass('arkism.entities'),to_regclass('arkism.relations')]))
AND to_regclass('arkism.principals') IS NOT NULL AND to_regclass('arkism.principal_permissions') IS NOT NULL`).Scan(&ok); err != nil || !ok {
		return ErrUnavailable
	}
	return p.checkRecoveryWriteGateSchema(ctx)
}

// payload and doc hold the same storage document (see storecodec.go):
// payload is the exact bytes the service decodes, doc the queryable jsonb.
func encodeRecord(r Record) ([]byte, json.RawMessage, error) {
	b, err := storeMarshal(r)
	if err != nil {
		return nil, nil, err
	}
	return b, json.RawMessage(b), nil
}
func pgDecode(b []byte) (Record, error) {
	var r Record
	if err := storeUnmarshal(b, &r); err != nil {
		return r, ErrUnavailable
	}
	return r, nil
}
func pgVector(e *Embedding) (any, any, any, error) {
	if e == nil || len(e.Vector) == 0 {
		return nil, nil, nil, nil
	}
	if len(e.Vector) != VectorDimension {
		return nil, nil, nil, ErrInvalid
	}
	dim := e.Dimension
	if dim == 0 {
		dim = VectorDimension
	}
	if dim != VectorDimension {
		return nil, nil, nil, ErrInvalid
	}
	parts := make([]string, len(e.Vector))
	for i, v := range e.Vector {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, nil, nil, ErrInvalid
		}
		parts[i] = fmt.Sprintf("%g", v)
	}
	return "[" + strings.Join(parts, ",") + "]", e.Space, dim, nil
}
func pgErr(err error) error {
	if err == nil {
		return nil
	}
	var e *pgconn.PgError
	if errors.As(err, &e) {
		if e.Code == "23505" {
			return ErrConflict
		}
		if e.Code == "40001" || e.Code == "40P01" {
			return transientTransactionError{}
		}
	}
	return ErrUnavailable
}
func (p *Postgres) row(ctx context.Context, collection string, s Scope, id string, forUpdate bool) (Record, error) {
	if checkScope(s) != nil || id == "" {
		return Record{}, ErrInvalid
	}
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	var b []byte
	err := p.q().QueryRow(ctx, `SELECT payload FROM memory_records WHERE collection=$1 AND id=$2 AND workspace_id=$3 AND project_id=$4 AND user_id=$5 AND agent_id=$6`+lock, collection, id, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, pgErr(err)
	}
	r, err := pgDecode(b)
	if err != nil {
		return Record{}, err
	}
	if !sameScope(r.Scope, s) {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (p *Postgres) Get(ctx context.Context, c string, s Scope, id string) (Record, error) {
	return p.row(ctx, c, s, id, false)
}

// All memory writes in one transaction share one observation boundary.
func (p *Postgres) memoryObservation(col string) time.Time {
	if p.tx == nil || col != colMemories {
		return time.Now()
	}
	if p.memoryObservedAt.IsZero() {
		p.memoryObservedAt = time.Now()
	}
	return p.memoryObservedAt
}

func (p *Postgres) Insert(ctx context.Context, c string, r Record) error {
	if checkScope(r.Scope) != nil || r.ID == "" {
		return ErrInvalid
	}
	now := time.Now().UTC()
	if r.Revision <= 0 {
		r.Revision = 1
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	if err := recordMemoryState(c, nil, &r, p.memoryObservation(c)); err != nil {
		return err
	}
	r.Keywords = DocumentKeywords(r.Text, r.Summary, r.Keywords)
	r.KeywordsLower = make([]string, len(r.Keywords))
	for i, k := range r.Keywords {
		r.KeywordsLower[i] = strings.ToLower(k)
	}
	b, j, err := encodeRecord(r)
	if err != nil {
		return ErrInvalid
	}
	vec, space, dim, err := pgVector(r.Embedding)
	if err != nil {
		return err
	}
	_, err = p.q().Exec(ctx, `INSERT INTO memory_records(collection,id,workspace_id,project_id,user_id,agent_id,revision,status,kind,updated_at,run_at,lease_until,lease_token,attempts,payload,doc,embedding,embedding_space,embedding_dimension,search_tokens) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::vector,$18,$19,$20::tsvector)`, c, r.ID, r.Scope.WorkspaceID, r.Scope.ProjectID, r.Scope.UserID, r.Scope.AgentID, r.Revision, r.Status, r.Kind, r.UpdatedAt, r.RunAt, r.LeaseUntil, r.LeaseToken, r.Attempts, b, j, vec, space, dim, pgSearchTokens(c, r))
	return pgErr(err)
}
func (p *Postgres) Replace(ctx context.Context, c string, r Record, revision int64) error {
	if p.tx == nil {
		return p.Transaction(ctx, func(ctx context.Context, tx Tx) error { return tx.Replace(ctx, c, r, revision) })
	}
	if checkScope(r.Scope) != nil || r.ID == "" || revision <= 0 {
		return ErrInvalid
	}
	if err := checkReplaceRevision(r.Revision, revision); err != nil {
		return err
	}
	current, err := p.row(ctx, c, r.Scope, r.ID, true)
	if err != nil {
		return err
	}
	if !sameProvenance(current, r) || c == "sources" && current.TrustLevel != r.TrustLevel {
		return ErrConflict
	}
	if err := recordMemoryState(c, &current, &r, p.memoryObservation(c)); err != nil {
		return err
	}
	r.Keywords = DocumentKeywords(r.Text, r.Summary, r.Keywords)
	r.KeywordsLower = make([]string, len(r.Keywords))
	for i, k := range r.Keywords {
		r.KeywordsLower[i] = strings.ToLower(k)
	}
	b, j, err := encodeRecord(r)
	if err != nil {
		return ErrInvalid
	}
	vec, space, dim, err := pgVector(r.Embedding)
	if err != nil {
		return err
	}
	res, err := p.q().Exec(ctx, `UPDATE memory_records SET revision=$7,status=$8,kind=$9,updated_at=$10,run_at=$11,lease_until=$12,lease_token=$13,attempts=$14,payload=$15,doc=$16,embedding=$17::vector,embedding_space=$18,embedding_dimension=$19,search_tokens=$21::tsvector WHERE collection=$1 AND id=$2 AND workspace_id=$3 AND project_id=$4 AND user_id=$5 AND agent_id=$6 AND revision=$20`, c, r.ID, r.Scope.WorkspaceID, r.Scope.ProjectID, r.Scope.UserID, r.Scope.AgentID, r.Revision, r.Status, r.Kind, r.UpdatedAt, r.RunAt, r.LeaseUntil, r.LeaseToken, r.Attempts, b, j, vec, space, dim, revision, pgSearchTokens(c, r))
	if err != nil {
		return pgErr(err)
	}
	if res.RowsAffected() == 0 {
		return p.casMiss(ctx, c, r.Scope, r.ID)
	}
	return nil
}
func sameProvenance(a, b Record) bool {
	if !reflect.DeepEqual(a.SkillBasis, b.SkillBasis) || a.SkillBasis != nil && a.SkillID != b.SkillID {
		return false
	}
	return ((a.Kind != proposedSkillKind && a.Kind != proposedSkillSourceKind) || a.Kind == b.Kind && reflect.DeepEqual(a.Relations, b.Relations)) && reflect.DeepEqual(a.ComparisonPlan, b.ComparisonPlan) && reflect.DeepEqual(a.ArchiveMessages, b.ArchiveMessages) && a.ErasureParentID == b.ErasureParentID && reflect.DeepEqual(a.Proposal, b.Proposal) && a.CreatedBy == b.CreatedBy && a.CreatedByType == b.CreatedByType && a.CreatedAt.Equal(b.CreatedAt) && reflect.DeepEqual(a.Sources, b.Sources) && reflect.DeepEqual(a.Generator, b.Generator) && a.OriginProduct == b.OriginProduct && a.SourceRef == b.SourceRef && effectiveSourceEventID(a) == effectiveSourceEventID(b) && a.TraceID == b.TraceID && reflect.DeepEqual(a.CollectedAt, b.CollectedAt)
}
func (p *Postgres) casMiss(ctx context.Context, c string, s Scope, id string) error {
	_, e := p.Get(ctx, c, s, id)
	if e == nil {
		return ErrConflict
	}
	return e
}
func (p *Postgres) Delete(ctx context.Context, c string, s Scope, id string, revision int64) error {
	if checkScope(s) != nil || id == "" || revision <= 0 {
		return ErrInvalid
	}
	res, e := p.q().Exec(ctx, `DELETE FROM memory_records WHERE collection=$1 AND id=$2 AND workspace_id=$3 AND project_id=$4 AND user_id=$5 AND agent_id=$6 AND revision=$7`, c, id, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID, revision)
	if e != nil {
		return pgErr(e)
	}
	if res.RowsAffected() == 0 {
		return p.casMiss(ctx, c, s, id)
	}
	return nil
}

// Serialization failures retry after a growing, jittered pause: retrying at once only meets the same
// concurrent transactions again (several quick approvals in the desktop failed all five attempts).
const txAttempts = 8

func txBackoff(ctx context.Context, attempt int) bool {
	base := time.Duration(10<<attempt) * time.Millisecond // 20ms … 1.28s
	var b [2]byte
	_, _ = rand.Read(b[:])
	t := time.NewTimer(base/2 + base*time.Duration(binary.LittleEndian.Uint16(b[:]))/65536)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (p *Postgres) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return p.transactionWithIsolation(ctx, pgx.Serializable, fn)
}

// Archive mutations explicitly fence chunk and header revisions. ReadCommitted
// avoids retrying hundreds of megabytes when unrelated audit rows are inserted.
func (p *Postgres) SessionArchiveTransaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return p.transactionWithIsolation(ctx, pgx.ReadCommitted, fn)
}

func (p *Postgres) transactionWithIsolation(ctx context.Context, isolation pgx.TxIsoLevel, fn func(context.Context, Tx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	if p.tx != nil {
		return fn(ctx, p)
	}
	for i := 0; i < txAttempts; i++ {
		if i > 0 && !txBackoff(ctx, i) {
			return ErrUnavailable
		}
		tx, e := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
		if e != nil {
			return ErrUnavailable
		}
		tokenHash, bound := p.effectiveTokenHash(ctx)
		if bound {
			if _, e = tx.Exec(ctx, `SELECT set_config('arkism.token_hash',$1,true)`, tokenHash); e != nil {
				_ = tx.Rollback(ctx)
				return pgErr(e)
			}
		}
		child := &Postgres{pool: p.pool, tx: tx, erasureOnly: p.erasureOnly, tokenHash: tokenHash, vectorMaxDistance: p.vectorMaxDistance}
		e = fn(ctx, child)
		if e != nil {
			_ = tx.Rollback(ctx)
			var transient transientTransactionError
			if errors.As(e, &transient) {
				continue
			}
			return e
		}
		e = tx.Commit(ctx)
		if e == nil {
			return nil
		}
		mapped := pgErr(e)
		var transient transientTransactionError
		if !errors.As(mapped, &transient) {
			return mapped
		}
	}
	return ErrUnavailable
}

func (p *Postgres) SourceMetadata(ctx context.Context, s Scope, ids []string, includeText bool) ([]Record, error) {
	if checkScope(s) != nil || len(ids) == 0 || len(ids) > 64 {
		return nil, ErrInvalid
	}
	rows, e := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection='sources' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=ANY($5)`, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID, ids)
	if e != nil {
		return nil, pgErr(e)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, ErrUnavailable
		}
		r, e := pgDecode(b)
		if e != nil {
			return nil, e
		}
		if !includeText {
			r.Text = ""
			r.SkillBundle = nil
		}
		out = append(out, r)
	}
	if e = rows.Err(); e != nil {
		return nil, pgErr(e)
	}
	return out, nil
}

func (p *Postgres) fence(ctx context.Context, s Scope, c, id string, rev int64, mode string) error {
	if p.tx == nil {
		return p.Transaction(ctx, func(ctx context.Context, tx Tx) error { return tx.(*Postgres).fence(ctx, s, c, id, rev, mode) })
	}
	if checkScope(s) != nil || id == "" || rev < 1 {
		return ErrInvalid
	}
	r, e := p.row(ctx, c, s, id, true)
	if e != nil {
		return mapFence(e)
	}
	if mode == "search" && (r.Status != "active" || !live(r)) {
		return ErrConflict
	}
	if mode == "source" && (r.PrivacyQuarantined || r.Status != "active" || (r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now()))) {
		return ErrConflict
	}
	if mode == "revision" && r.Revision != rev {
		return ErrConflict
	}
	if mode == "search" && r.Revision != rev {
		return ErrConflict
	}
	if mode == "run" && r.Revision != rev {
		return ErrConflict
	}
	if mode == "relation" && c != "memories" && c != "memory_candidates" {
		return ErrInvalid
	}
	r.MemoryReferenceFence++
	if mode == "run" {
		r.RunCallFence++
	}
	return p.writePayload(ctx, c, r)
}
func mapFence(e error) error {
	if errors.Is(e, ErrNotFound) {
		return ErrConflict
	}
	return e
}
func (p *Postgres) writePayload(ctx context.Context, c string, r Record) error {
	// Fence/lease updates must retain fields not modeled by this binary.
	var original []byte
	e := p.q().QueryRow(ctx, `SELECT payload FROM memory_records WHERE collection=$1 AND id=$2 AND workspace_id=$3 AND project_id=$4 AND user_id=$5 AND agent_id=$6 FOR UPDATE`, c, r.ID, r.Scope.WorkspaceID, r.Scope.ProjectID, r.Scope.UserID, r.Scope.AgentID).Scan(&original)
	if e != nil {
		return pgErr(e)
	}
	var fields map[string]json.RawMessage
	if e = json.Unmarshal(original, &fields); e != nil {
		return ErrUnavailable
	}
	updated, _, e := encodeRecord(r)
	if e != nil {
		return ErrInvalid
	}
	var values map[string]json.RawMessage
	if e = json.Unmarshal(updated, &values); e != nil {
		return ErrInvalid
	}
	for _, key := range []string{"revision", "status", "kind", "updated_at", "lease_until", "lease_token", "attempts", "run_at", "memory_reference_fence", "run_call_fence"} {
		if value, ok := values[key]; ok {
			fields[key] = value
		} else {
			delete(fields, key)
		}
	}
	b, e := json.Marshal(fields)
	if e != nil {
		return ErrInvalid
	}
	j := json.RawMessage(b)
	res, e := p.q().Exec(ctx, `UPDATE memory_records SET revision=$9,status=$11,kind=$12,updated_at=$13,lease_until=$14,lease_token=$15,attempts=$16,run_at=$17,payload=$1,doc=$2 WHERE collection=$3 AND id=$4 AND workspace_id=$5 AND project_id=$6 AND user_id=$7 AND agent_id=$8 AND revision IN ($9,$10)`, b, j, c, r.ID, r.Scope.WorkspaceID, r.Scope.ProjectID, r.Scope.UserID, r.Scope.AgentID, r.Revision, r.Revision-1, r.Status, r.Kind, r.UpdatedAt, r.LeaseUntil, r.LeaseToken, r.Attempts, r.RunAt)
	if e != nil {
		return pgErr(e)
	}
	if res.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// LockForRead share-locks rows a read is about to return, in one statement. Inside the read's
// serializable transaction it blocks concurrent writers until commit and fails with a conflict when
// a row changed after the snapshot, which is what per-row fence writes gave list reads, without
// rewriting each row and its search indexes.
func (p *Postgres) LockForRead(ctx context.Context, s Scope, collection string, ids []string) (map[string]Record, error) {
	if p.tx == nil || checkScope(s) != nil || !knownCollection(collection) || len(ids) == 0 || len(ids) > 500 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND id=ANY($6) ORDER BY id FOR SHARE`, collection, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID, ids)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := map[string]Record{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, pgErr(err)
		}
		r, e := pgDecode(b)
		if e != nil {
			return nil, e
		}
		if !sameScope(r.Scope, s) {
			return nil, ErrConflict
		}
		out[r.ID] = r
	}
	if err = rows.Err(); err != nil {
		return nil, pgErr(err)
	}
	return out, nil
}

func (p *Postgres) FenceSource(ctx context.Context, s Scope, id string) error {
	return p.fence(ctx, s, "sources", id, 1, "source")
}
func (p *Postgres) FenceRelationMember(ctx context.Context, s Scope, c, id string) error {
	return p.fence(ctx, s, c, id, 1, "relation")
}
func (p *Postgres) FenceSearchRead(ctx context.Context, s Scope, c, id string, rev int64) error {
	if !knownCollection(c) || c == "jobs" || c == "audit_events" || !validRunLabel(id, 256) {
		return ErrInvalid
	}
	return p.fence(ctx, s, c, id, rev, "search")
}

// FenceRunCall: a real parent write serializes call insertion against run
// completion even when the caller's logical revision is unchanged. Use only
// inside a transaction.
// shortcut: one run is a write hotspot; measure retries before adding coordination.
func (p *Postgres) FenceRunCall(ctx context.Context, s Scope, id string, rev int64) error {
	return p.fence(ctx, s, "agent_runs", id, rev, "run")
}
func (p *Postgres) CheckRunToolTimes(ctx context.Context, s Scope, id string, finished time.Time) error {
	if checkScope(s) != nil || id == "" || finished.IsZero() {
		return ErrInvalid
	}
	rows, e := p.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection='tool_calls' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND doc->'target'->>'collection'='agent_runs' AND doc->'target'->>'id'=$5`, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID, id)
	if e != nil {
		return pgErr(e)
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return ErrUnavailable
		}
		r, e := pgDecode(b)
		if e != nil {
			return e
		}
		if r.Target != nil && r.Target.Collection == "agent_runs" && r.Target.ID == id && (r.ToolCall == nil || r.ToolCall.FinishedAt.IsZero() || r.ToolCall.FinishedAt.After(finished)) {
			return ErrConflict
		}
	}
	return rows.Err()
}

func (p *Postgres) ClaimJob(ctx context.Context, now time.Time, lease time.Duration) (Record, error) {
	if lease <= 0 {
		return Record{}, ErrInvalid
	}
	var out Record
	e := p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		pp := tx.(*Postgres)
		kinds := append([]string{"embedding"}, erasureJobKinds...)
		if pp.erasureOnly {
			kinds = erasureJobKinds
		}
		rows, e := pp.q().Query(ctx, `SELECT payload FROM memory_records WHERE collection='jobs' AND kind=ANY($1) AND ((status=ANY($2) AND (run_at IS NULL OR run_at<=$3)) OR (status='running' AND lease_until<=$3)) ORDER BY run_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, kinds, []string{JobStatusPending, JobStatusRetry}, now.UTC())
		if e != nil {
			return pgErr(e)
		}
		if !rows.Next() {
			// pgx reports server errors (serialization, timeout) only here.
			rows.Close()
			if e = rows.Err(); e != nil {
				return pgErr(e)
			}
			return ErrNotFound
		}
		var b []byte
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return ErrUnavailable
		}
		rows.Close()
		out, e = pgDecode(b)
		if e != nil {
			return e
		}
		tok, e := newLeaseToken()
		if e != nil {
			return ErrUnavailable
		}
		out.Status = JobStatusRunning
		out.LeaseUntil = now.UTC().Add(lease)
		out.LeaseToken = tok
		out.UpdatedAt = now.UTC()
		out.Attempts++
		out.Revision++
		return pp.writePayload(ctx, "jobs", out)
	})
	return out, e
}
func (p *Postgres) FinishJob(ctx context.Context, r Record, success bool) error {
	if checkScope(r.Scope) != nil || r.ID == "" || r.LeaseToken == "" || r.Revision <= 0 {
		return ErrInvalid
	}
	return p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		pp := tx.(*Postgres)
		cur, e := pp.row(ctx, "jobs", r.Scope, r.ID, true)
		if e != nil {
			return e
		}
		if cur.Revision != r.Revision || cur.LeaseToken != r.LeaseToken {
			return ErrConflict
		}
		now := time.Now().UTC()
		cur.LeaseToken = ""
		cur.LeaseUntil = time.Time{}
		cur.UpdatedAt = now
		cur.Revision++
		if success {
			cur.Status = JobStatusDone
		} else if r.Attempts >= maxJobAttempts {
			cur.Status = JobStatusFailed
		} else {
			cur.Status = JobStatusRetry
			x := now.Add(retryDelay(r.Attempts))
			cur.RunAt = &x
		}
		if e = pp.writePayload(ctx, "jobs", cur); e != nil {
			return e
		}

		return nil
	})
}
