package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type postgresTokenContextKey struct{}
type postgresTokenBinding struct{ hash string }

// ContextWithPostgresToken binds only a digest to a request. An empty token is
// an explicit unbound override, so a worker credential cannot leak into HTTP.
func ContextWithPostgresToken(ctx context.Context, token string) context.Context {
	hash := ""
	if token != "" {
		sum := sha256.Sum256([]byte(token))
		hash = hex.EncodeToString(sum[:])
	}
	return context.WithValue(ctx, postgresTokenContextKey{}, postgresTokenBinding{hash: hash})
}

func (p *Postgres) effectiveTokenHash(ctx context.Context) (string, bool) {
	if binding, ok := ctx.Value(postgresTokenContextKey{}).(postgresTokenBinding); ok {
		return binding.hash, true
	}
	return p.tokenHash, p.tokenHash != ""
}

// WithToken returns a lightweight repository view. It retains no raw secret
// and does not own the shared connection pool.
func (p *Postgres) WithToken(token string) (*Postgres, error) {
	if p == nil || p.pool == nil || strings.TrimSpace(token) == "" {
		return nil, ErrDenied
	}
	sum := sha256.Sum256([]byte(token))
	return &Postgres{pool: p.pool, erasureOnly: p.erasureOnly, tokenHash: hex.EncodeToString(sum[:]), vectorMaxDistance: p.vectorMaxDistance}, nil
}

type postgresQuery struct{ store *Postgres }

func (q postgresQuery) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	hash, bound := q.store.effectiveTokenHash(ctx)
	if bound {
		if _, err = tx.Exec(ctx, `SELECT set_config('arkism.token_hash',$1,true)`, hash); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
	}
	return tx, nil
}

func (q postgresQuery) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, err := q.begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return tag, err
	}
	return tag, tx.Commit(ctx)
}

func (q postgresQuery) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx, err := q.begin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &postgresRows{Rows: rows, tx: tx, ctx: ctx}, nil
}

func (q postgresQuery) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx, err := q.begin(ctx)
	if err != nil {
		return postgresRow{err: err}
	}
	return postgresRow{row: tx.QueryRow(ctx, sql, args...), tx: tx, ctx: ctx}
}

type postgresRow struct {
	row pgx.Row
	tx  pgx.Tx
	ctx context.Context
	err error
}

func (r postgresRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	err := r.row.Scan(dest...)
	if err != nil {
		_ = r.tx.Rollback(r.ctx)
		return err
	}
	return r.tx.Commit(r.ctx)
}

type postgresRows struct {
	pgx.Rows
	tx     pgx.Tx
	ctx    context.Context
	closed bool
	err    error
}

func (r *postgresRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.Rows.Close()
	if err := r.Rows.Err(); err != nil {
		r.err = err
		_ = r.tx.Rollback(r.ctx)
		return
	}
	r.err = r.tx.Commit(r.ctx)
}

func (r *postgresRows) Next() bool {
	ok := r.Rows.Next()
	if !ok {
		r.Close()
	}
	return ok
}

func (r *postgresRows) Scan(dest ...any) error {
	err := r.Rows.Scan(dest...)
	if err != nil {
		r.Close()
	}
	return err
}

func (r *postgresRows) Values() ([]any, error) {
	values, err := r.Rows.Values()
	if err != nil {
		r.Close()
	}
	return values, err
}

func (r *postgresRows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.Rows.Err()
}

var permissionPattern = regexp.MustCompile(`^(?:\*|[a-z][a-z0-9_.:-]{0,127})$`)

type PrincipalGrant struct {
	PrincipalID    string
	Role           string
	Token          string
	OrganizationID string
	ProjectID      string
	Actions        []string
}

// ProvisionPrincipal replaces one principal's permissions in one project.
// The caller must use the arkism_init connection; runtime roles have no
// privileges on these tables.
func (p *Postgres) ProvisionPrincipal(ctx context.Context, grant PrincipalGrant) error {
	grant.PrincipalID = strings.TrimSpace(grant.PrincipalID)
	grant.Role = strings.ToLower(strings.TrimSpace(grant.Role))
	grant.OrganizationID = strings.TrimSpace(grant.OrganizationID)
	grant.ProjectID = strings.TrimSpace(grant.ProjectID)
	if !validRunLabel(grant.PrincipalID, 256) || !decisionRoles[grant.Role] || strings.TrimSpace(grant.Token) == "" || len(grant.Token) > 4096 || !validScope(Scope{WorkspaceID: grant.OrganizationID, ProjectID: grant.ProjectID}) || len(grant.Actions) == 0 {
		return ErrInvalid
	}
	actions := make([]string, 0, len(grant.Actions))
	seen := map[string]bool{}
	for _, action := range grant.Actions {
		action = strings.ToLower(strings.TrimSpace(action))
		if !permissionPattern.MatchString(action) {
			return ErrInvalid
		}
		if !seen[action] {
			seen[action] = true
			actions = append(actions, action)
		}
	}
	digest := sha256.Sum256([]byte(grant.Token))
	return p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		q := tx.(*Postgres).q()
		if _, err := q.Exec(ctx, `INSERT INTO principals(id,role,token_sha256,active) VALUES($1,$2,$3,true)
ON CONFLICT(id) DO UPDATE SET role=EXCLUDED.role,token_sha256=EXCLUDED.token_sha256,active=true`, grant.PrincipalID, grant.Role, digest[:]); err != nil {
			return pgErr(err)
		}
		if _, err := q.Exec(ctx, `INSERT INTO organization_memberships(organization_id,principal_id,active) VALUES($1,$2,true)
ON CONFLICT(organization_id,principal_id) DO UPDATE SET active=true`, grant.OrganizationID, grant.PrincipalID); err != nil {
			return pgErr(err)
		}
		if _, err := q.Exec(ctx, `INSERT INTO project_memberships(organization_id,project_id,principal_id,active) VALUES($1,$2,$3,true)
ON CONFLICT(organization_id,project_id,principal_id) DO UPDATE SET active=true`, grant.OrganizationID, grant.ProjectID, grant.PrincipalID); err != nil {
			return pgErr(err)
		}
		if _, err := q.Exec(ctx, `DELETE FROM principal_permissions WHERE principal_id=$1 AND organization_id=$2 AND project_id=$3`, grant.PrincipalID, grant.OrganizationID, grant.ProjectID); err != nil {
			return pgErr(err)
		}
		for _, action := range actions {
			if _, err := q.Exec(ctx, `INSERT INTO principal_permissions(principal_id,organization_id,project_id,action,allowed) VALUES($1,$2,$3,$4,true)`, grant.PrincipalID, grant.OrganizationID, grant.ProjectID, action); err != nil {
				return pgErr(err)
			}
		}
		return nil
	})
}

func (p *Postgres) SetPrincipalActive(ctx context.Context, principalID string, active bool) error {
	if !validRunLabel(principalID, 256) {
		return ErrInvalid
	}
	tag, err := p.q().Exec(ctx, `UPDATE principals SET active=$2 WHERE id=$1`, principalID, active)
	if err != nil {
		return pgErr(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) ReserveCandidateQuota(ctx context.Context, scope Scope, accepted time.Time, limit int) error {
	if !validScope(scope) || accepted.IsZero() || limit < 1 || limit > 100000 {
		return ErrInvalid
	}
	var used int
	err := p.q().QueryRow(ctx, `SELECT reserve_candidate_quota($1,$2,$3,$4)`, scope.WorkspaceID, scope.ProjectID, accepted.UTC().Format("2006-01-02"), limit).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCandidateQuota
	}
	if err != nil {
		return pgErr(err)
	}
	if used == 0 {
		return ErrCandidateQuota
	}
	return nil
}

// InitRLS installs the canonical Supabase permission schema and policies. It
// is safe to call again after optional canonical trace tables are created.
func (p *Postgres) InitRLS(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS principals (
id text PRIMARY KEY, role text NOT NULL CHECK (role IN ('human','agent','service','pipeline','classifier')),
token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256)=32), active boolean NOT NULL DEFAULT true)`,
		`ALTER TABLE principals DROP CONSTRAINT IF EXISTS principals_role_check`,
		`ALTER TABLE principals ADD CONSTRAINT principals_role_check CHECK (role IN ('human','agent','service','pipeline','classifier'))`,
		`CREATE TABLE IF NOT EXISTS organization_memberships (
organization_id text NOT NULL, principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
active boolean NOT NULL DEFAULT true, PRIMARY KEY(organization_id,principal_id))`,
		`CREATE TABLE IF NOT EXISTS project_memberships (
organization_id text NOT NULL, project_id text NOT NULL, principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
active boolean NOT NULL DEFAULT true, PRIMARY KEY(organization_id,project_id,principal_id),
FOREIGN KEY(organization_id,principal_id) REFERENCES organization_memberships(organization_id,principal_id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS principal_permissions (
principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE, organization_id text NOT NULL,
project_id text NOT NULL, action text NOT NULL, allowed boolean NOT NULL DEFAULT true,
PRIMARY KEY(principal_id,organization_id,project_id,action),
FOREIGN KEY(organization_id,project_id,principal_id) REFERENCES project_memberships(organization_id,project_id,principal_id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS candidate_daily_usage (
day date PRIMARY KEY, used integer NOT NULL CHECK (used > 0))`,
		`CREATE OR REPLACE FUNCTION current_principal() RETURNS TABLE(id text, role text, active boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT p.id,p.role,p.active FROM arkism.principals p
 WHERE encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true) LIMIT 1
$$`,
		`CREATE OR REPLACE FUNCTION has_permission(org text, project text, requested_action text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT EXISTS (
  SELECT 1 FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.organization_id=org AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=org AND m.project_id=project AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=org AND x.project_id=project AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
    AND (org NOT IN ('101-internal','__memory_blobs__','__unparsed__') OR p.role='service')
 )
$$`,
		`CREATE OR REPLACE FUNCTION has_any_permission(requested_action text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT EXISTS (
  SELECT 1 FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
 )
$$`,
		// Same rule as has_permission, as the set of (organization, project) pairs the token may use:
		// policies test membership in it once per statement instead of calling has_permission per row
		// (a scan of 15k rows cost 626ms per row-call, 5ms as a hashed subplan).
		`CREATE OR REPLACE FUNCTION permitted_scopes(requested_action text) RETURNS TABLE(org text, project text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT DISTINCT m.organization_id, m.project_id FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
    AND (m.organization_id NOT IN ('101-internal','__memory_blobs__','__unparsed__') OR p.role='service')
$$`,
		`CREATE OR REPLACE FUNCTION authorize_principal(org text, project text, requested_action text)
RETURNS TABLE(principal_id text, principal_role text, allowed boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT p.id,p.role,arkism.has_permission(org,project,requested_action)
 FROM arkism.principals p
 WHERE encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true) LIMIT 1
$$`,
		`CREATE OR REPLACE FUNCTION reserve_candidate_quota(org text, project text, quota_day date, quota_limit integer) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
DECLARE next_used integer;
BEGIN
 IF quota_limit < 1 OR quota_limit > 100000 OR
    (NOT (session_user=current_user AND COALESCE(current_setting('role',true),'none') IN ('none','')) AND NOT arkism.has_permission(org,project,'write:memory_candidates')) THEN
  RAISE EXCEPTION 'candidate quota denied' USING ERRCODE='42501';
 END IF;
 INSERT INTO arkism.candidate_daily_usage(day,used) VALUES(quota_day,1)
 ON CONFLICT(day) DO UPDATE SET used=arkism.candidate_daily_usage.used+1
 WHERE arkism.candidate_daily_usage.used < quota_limit RETURNING used INTO next_used;
	 RETURN COALESCE(next_used,0);
END
$$`,
		`REVOKE ALL ON principals,organization_memberships,project_memberships,principal_permissions,candidate_daily_usage FROM PUBLIC`,
		`REVOKE ALL ON FUNCTION current_principal(),has_permission(text,text,text),has_any_permission(text),permitted_scopes(text),authorize_principal(text,text,text),reserve_candidate_quota(text,text,date,integer) FROM PUBLIC`,
		`DO $$ DECLARE role_name text; BEGIN
 FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role','arkism_app'] LOOP
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
   EXECUTE format('REVOKE ALL ON principals,organization_memberships,project_memberships,principal_permissions,candidate_daily_usage FROM %I',role_name);
   EXECUTE format('GRANT EXECUTE ON FUNCTION current_principal(),has_permission(text,text,text),has_any_permission(text),permitted_scopes(text),authorize_principal(text,text,text),reserve_candidate_quota(text,text,date,integer) TO %I',role_name);
  END IF;
 END LOOP;
END $$`,
	}
	for _, statement := range statements {
		if _, err := p.q().Exec(ctx, statement); err != nil {
			return pgErr(err)
		}
	}
	for _, table := range append([]string{"memory_records", "execution_traces", "source_events", "entities", "relations"}, codeGraphTables...) {
		var exists bool
		if err := p.q().QueryRow(ctx, `SELECT to_regclass('arkism.'||$1) IS NOT NULL`, table).Scan(&exists); err != nil {
			return pgErr(err)
		}
		if !exists {
			continue
		}
		name := pgx.Identifier{table}.Sanitize()
		organizationColumn := "organization_id"
		if table == "memory_records" {
			organizationColumn = "workspace_id"
		}
		policy := `ALTER TABLE ` + name + ` ENABLE ROW LEVEL SECURITY; ALTER TABLE ` + name + ` FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON ` + name + `;
CREATE POLICY arkism_init_owner ON ` + name + ` FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON ` + name + `;
CREATE POLICY arkism_read ON ` + name + ` FOR SELECT TO PUBLIC USING (
 (` + organizationColumn + `,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (` + organizationColumn + `='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON ` + name + `;
DROP POLICY IF EXISTS arkism_insert ON ` + name + `;
CREATE POLICY arkism_insert ON ` + name + ` FOR INSERT TO PUBLIC WITH CHECK (
 (` + organizationColumn + `,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (` + organizationColumn + `='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON ` + name + `;
CREATE POLICY arkism_update ON ` + name + ` FOR UPDATE TO PUBLIC USING (
 (` + organizationColumn + `,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (` + organizationColumn + `='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (` + organizationColumn + `,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (` + organizationColumn + `='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON ` + name + `;
CREATE POLICY arkism_delete ON ` + name + ` FOR DELETE TO PUBLIC USING (
 (` + organizationColumn + `,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (` + organizationColumn + `='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))`
		if _, err := p.q().Exec(ctx, policy); err != nil {
			return pgErr(err)
		}
	}
	if err := p.initClassifierGuard(ctx); err != nil {
		return err
	}
	return p.initArtifactPrivacyRLS(ctx)
}

// initClassifierGuard: a classifier principal (ADR-0022) may change only a waiting candidate's classification and
// write its audit row. Enforced here, under the app, so no API path can approve, edit or delete on its behalf.
func (p *Postgres) initClassifierGuard(ctx context.Context) error {
	var exists bool
	if err := p.q().QueryRow(ctx, `SELECT to_regclass('arkism.memory_records') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return pgErr(err)
	}
	_, err := p.q().Exec(ctx, `CREATE OR REPLACE FUNCTION classifier_write_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
BEGIN
 -- payload is what the service decodes and doc its jsonb copy: both must hold the same change.
 IF NOT EXISTS (SELECT 1 FROM arkism.current_principal() c WHERE c.role='classifier') THEN
  RETURN COALESCE(NEW, OLD);
 END IF;
 IF TG_OP='UPDATE' AND OLD.collection='memory_candidates' AND OLD.status IN ('candidate','conflicted')
    AND (NEW.collection,NEW.id,NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,NEW.status,NEW.kind)
      = (OLD.collection,OLD.id,OLD.workspace_id,OLD.project_id,OLD.user_id,OLD.agent_id,OLD.status,OLD.kind)
    AND NEW.doc - 'classification' - 'revision' - 'updated_at' = OLD.doc - 'classification' - 'revision' - 'updated_at'
    AND NEW.doc->'classification'->>'by' = (SELECT c.id FROM arkism.current_principal() c)
    AND convert_from(NEW.payload,'UTF8')::jsonb = NEW.doc
    AND NEW.embedding::text IS NOT DISTINCT FROM OLD.embedding::text THEN
  RETURN NEW;
 END IF;
 -- The audit row names this classifier and a waiting candidate in its scope that it classified.
 IF TG_OP='INSERT' AND NEW.collection='audit_events' AND NEW.kind='classify:memory_candidates'
    AND convert_from(NEW.payload,'UTF8')::jsonb = NEW.doc
    AND NEW.doc->>'created_by' = (SELECT c.id FROM arkism.current_principal() c)
    AND EXISTS (SELECT 1 FROM arkism.memory_records m WHERE m.collection='memory_candidates' AND m.id=NEW.doc->'target'->>'id'
      AND (m.workspace_id,m.project_id,m.user_id,m.agent_id)=(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id)
      AND m.status IN ('candidate','conflicted') AND m.doc->'classification'->>'by' = NEW.doc->>'created_by') THEN
  RETURN NEW;
 END IF;
 RAISE EXCEPTION 'classifier may only classify memory candidates' USING ERRCODE='AK403';
END $$;
DROP TRIGGER IF EXISTS memory_records_classifier_guard ON memory_records;
CREATE TRIGGER memory_records_classifier_guard BEFORE INSERT OR UPDATE OR DELETE ON memory_records FOR EACH ROW EXECUTE FUNCTION classifier_write_guard()`)
	return pgErr(err)
}
