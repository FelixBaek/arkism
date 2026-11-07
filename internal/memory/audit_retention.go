package memory

import (
	"context"
	"time"
)

// ExpireAuditEvents counts the audit events written before cutoff (they are never
// updated, so updated_at is their creation time) and, with
// execute, deletes them. Only a globally visible maintenance role may run it: under row-level
// security any other role would delete just the scopes its token reaches and
// report that as the whole table. cutoff must be at least a day in the past.
func (p *Postgres) ExpireAuditEvents(ctx context.Context, cutoff time.Time, execute bool) (int64, error) {
	return p.expireCollection(ctx, "audit_events", cutoff, execute)
}

// ExpireLLMRequests is ExpireAuditEvents for model call traces (llm_requests rows are never updated either).
func (p *Postgres) ExpireLLMRequests(ctx context.Context, cutoff time.Time, execute bool) (int64, error) {
	return p.expireCollection(ctx, colLLMRequests, cutoff, execute)
}

func (p *Postgres) expireCollection(ctx context.Context, collection string, cutoff time.Time, execute bool) (int64, error) {
	if p == nil || p.pool == nil || p.tx != nil || cutoff.IsZero() || !cutoff.Before(time.Now().Add(-24*time.Hour)) {
		return 0, ErrInvalid
	}
	if _, bound := p.effectiveTokenHash(ctx); bound {
		return 0, ErrDenied
	}
	// Role metadata is safe to inspect before the application gate. A caller
	// without schema USAGE must still receive the explicit owner denial. Every
	// subsequent record read/delete remains behind p.q()'s maintenance gate.
	owner, err := retentionOwner(ctx, p.pool)
	if err != nil {
		return 0, ErrUnavailable
	}
	if !owner {
		return 0, ErrDenied
	}
	if !execute {
		var count int64
		if err := p.q().QueryRow(ctx, `SELECT count(*) FROM memory_records WHERE collection=$1 AND updated_at < $2`, collection, cutoff).Scan(&count); err != nil {
			return 0, ErrUnavailable
		}
		return count, nil
	}
	// Each statement commits independently. The unchanged cutoff and the rows
	// still present are the resume cursor; interruption cannot skip old rows.
	var total int64
	for {
		n, err := p.expireCollectionBatch(ctx, collection, cutoff, auditRetentionBatchSize)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}

const auditRetentionBatchSize = 1000

// ExpireAuditEventsBatch commits at most limit rows. Reuse the same cutoff after
// interruption and call until zero rows are deleted. No offset or mutable cursor
// is required: deleted rows are the durable progress marker.
func (p *Postgres) ExpireAuditEventsBatch(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return p.retentionBatch(ctx, "audit_events", cutoff, limit)
}

func (p *Postgres) ExpireLLMRequestsBatch(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return p.retentionBatch(ctx, colLLMRequests, cutoff, limit)
}

func (p *Postgres) retentionBatch(ctx context.Context, collection string, cutoff time.Time, limit int) (int64, error) {
	if p == nil || p.pool == nil || p.tx != nil || limit < 1 || limit > auditRetentionBatchSize || cutoff.IsZero() || !cutoff.Before(time.Now().Add(-24*time.Hour)) {
		return 0, ErrInvalid
	}
	if _, bound := p.effectiveTokenHash(ctx); bound {
		return 0, ErrDenied
	}
	owner, err := retentionOwner(ctx, p.pool)
	if err != nil {
		return 0, ErrUnavailable
	}
	if !owner {
		return 0, ErrDenied
	}
	return p.expireCollectionBatch(ctx, collection, cutoff, limit)
}

func (p *Postgres) expireCollectionBatch(ctx context.Context, collection string, cutoff time.Time, limit int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Lock selected rows without SKIP LOCKED: zero must never mean that a
	// concurrent cleanup merely holds the remaining eligible rows.
	tag, err := p.q().Exec(ctx, `WITH batch AS (
 SELECT ctid FROM memory_records WHERE collection=$1 AND updated_at < $2
 ORDER BY updated_at,workspace_id,project_id,user_id,agent_id,id LIMIT $3 FOR UPDATE
) DELETE FROM memory_records r USING batch b WHERE r.ctid=b.ctid`, collection, cutoff, limit)
	if err != nil {
		return 0, ErrUnavailable
	}
	return tag.RowsAffected(), nil
}

// Retention needs global row visibility. Schema/database ownership alone does
// not bypass FORCE RLS and can falsely report a filtered zero as completion.
func retentionOwner(ctx context.Context, q pgQuery) (bool, error) {
	var allowed bool
	err := q.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls OR rolname='arkism_init' FROM pg_roles WHERE rolname=current_user`).Scan(&allowed)
	return allowed, err
}

// schemaOwner reports whether the connected role may change the schema: a superuser, an RLS-bypassing role, arkism_init, or the owner of the
// arkism schema or of the database.
func schemaOwner(ctx context.Context, q pgQuery) (bool, error) {
	var allowed bool
	err := q.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls OR rolname='arkism_init'
OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='arkism' AND nspowner=r.oid)
OR EXISTS(SELECT 1 FROM pg_database WHERE datname=current_database() AND datdba=r.oid)
FROM pg_roles r WHERE rolname=current_user`).Scan(&allowed)
	return allowed, err
}
