package memory

import (
	"context"
	"time"
)

// ExpireAuditEvents counts the audit events written before cutoff (they are never
// updated, so updated_at is their creation time) and, with
// execute, deletes them. Only the schema owner may run it: under row-level
// security any other role would delete just the scopes its token reaches and
// report that as the whole table. cutoff must be at least a day in the past.
func (p *Postgres) ExpireAuditEvents(ctx context.Context, cutoff time.Time, execute bool) (int64, error) {
	if p == nil || p.pool == nil || cutoff.IsZero() || !cutoff.Before(time.Now().Add(-24*time.Hour)) {
		return 0, ErrInvalid
	}
	if _, bound := p.effectiveTokenHash(ctx); bound {
		return 0, ErrDenied
	}
	owner, err := schemaOwner(ctx, p.q())
	if err != nil {
		return 0, ErrUnavailable
	}
	if !owner {
		return 0, ErrDenied
	}
	var count int64
	if err := p.q().QueryRow(ctx, `SELECT count(*) FROM memory_records WHERE collection='audit_events' AND updated_at < $1`, cutoff).Scan(&count); err != nil {
		return 0, ErrUnavailable
	}
	if !execute || count == 0 {
		return count, nil
	}
	tag, err := p.q().Exec(ctx, `DELETE FROM memory_records WHERE collection='audit_events' AND updated_at < $1`, cutoff)
	if err != nil {
		return 0, ErrUnavailable
	}
	return tag.RowsAffected(), nil
}

// schemaOwner reports whether the connected role may change the schema and every
// row in it: a superuser, an RLS-bypassing role, arkism_init, or the owner of the
// arkism schema or of the database.
func schemaOwner(ctx context.Context, q pgQuery) (bool, error) {
	var allowed bool
	err := q.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls OR rolname='arkism_init'
OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='arkism' AND nspowner=r.oid)
OR EXISTS(SELECT 1 FROM pg_database WHERE datname=current_database() AND datdba=r.oid)
FROM pg_roles r WHERE rolname=current_user`).Scan(&allowed)
	return allowed, err
}
