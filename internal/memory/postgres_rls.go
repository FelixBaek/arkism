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
	TokenTTL       time.Duration
}

// ProvisionPrincipal replaces one principal's permissions in one project.
// The caller must use the arkism_init connection; runtime roles have no
// privileges on these tables.
func (p *Postgres) ProvisionPrincipal(ctx context.Context, grant PrincipalGrant) error {
	grant.PrincipalID = strings.TrimSpace(grant.PrincipalID)
	grant.Role = strings.ToLower(strings.TrimSpace(grant.Role))
	grant.OrganizationID = strings.TrimSpace(grant.OrganizationID)
	grant.ProjectID = strings.TrimSpace(grant.ProjectID)
	ttl, err := principalTokenTTL(grant.TokenTTL)
	if err != nil || !validRunLabel(grant.PrincipalID, 256) || !decisionRoles[grant.Role] || !ValidPrincipalToken(grant.Token) || !validScope(Scope{WorkspaceID: grant.OrganizationID, ProjectID: grant.ProjectID}) || len(grant.Actions) == 0 {
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
		if err := provisionPrincipalToken(ctx, q, grant, digest[:], ttl); err != nil {
			return err
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
