package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExpireAuditEventsPreviewsCountsAndDeletesOnlyOldRows(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	scope := Scope{WorkspaceID: "w", ProjectID: "p"}
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	for _, r := range []Record{
		{ID: "old-1", Scope: scope, Kind: "allow:read", Status: "active", CreatedAt: old, UpdatedAt: old},
		{ID: "old-2", Scope: scope, Kind: "policy:deny:read", Status: "active", CreatedAt: old, UpdatedAt: old},
		{ID: "new-1", Scope: scope, Kind: "allow:read", Status: "active"},
	} {
		if err := p.Insert(ctx, "audit_events", r); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if n, err := p.ExpireAuditEvents(ctx, cutoff, false); err != nil || n != 2 {
		t.Fatal("preview", n, err)
	}
	if rows := pgCollection(t, p, "audit_events"); len(rows) != 3 {
		t.Fatal("preview deleted rows", len(rows))
	}
	if n, err := p.ExpireAuditEvents(ctx, cutoff, true); err != nil || n != 2 {
		t.Fatal("execute", n, err)
	}
	rows := pgCollection(t, p, "audit_events")
	if len(rows) != 1 || rows[0].ID != "new-1" {
		t.Fatal("wrong rows left", rows)
	}
	if n, err := p.ExpireAuditEvents(ctx, cutoff, true); err != nil || n != 0 {
		t.Fatal("second execute", n, err)
	}
}

func TestExpireAuditEventsRefusesRecentCutoffsAndTokenBoundCallers(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	for _, cutoff := range []time.Time{{}, time.Now(), time.Now().Add(-time.Hour), time.Now().Add(time.Hour)} {
		if _, err := p.ExpireAuditEvents(ctx, cutoff, true); !errors.Is(err, ErrInvalid) {
			t.Fatal(cutoff, err)
		}
	}
	bound := ContextWithPostgresToken(ctx, "worker-token")
	if _, err := p.ExpireAuditEvents(bound, time.Now().Add(-48*time.Hour), false); !errors.Is(err, ErrDenied) {
		t.Fatal("a token-bound caller sees only its scopes and must not run retention", err)
	}
}

func TestExpireAuditEventsNeedsTheSchemaOwnerAndAWorkingConnection(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	cutoff := time.Now().Add(-48 * time.Hour)
	role := "arkism_reader_" + ID()
	schemaExec(t, p, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN NOSUPERUSER NOBYPASSRLS")
	t.Cleanup(func() { schemaExec(t, p, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()) })
	cfg := p.pool.Config().Copy()
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = ""
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Postgres{pool: pool}
	if _, err := reader.ExpireAuditEvents(ctx, cutoff, false); !errors.Is(err, ErrDenied) {
		t.Fatal("a role that is not the schema owner must be refused", err)
	}
	pool.Close()
	if _, err := reader.ExpireAuditEvents(ctx, cutoff, false); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a closed connection must read as unavailable", err)
	}
	if _, err := (*Postgres)(nil).ExpireAuditEvents(ctx, cutoff, false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

// A schema owner is not necessarily exempt from FORCE RLS. Never report its
// filtered zero rows as a globally completed cleanup.
func TestRetentionRejectsOwnerWithoutGlobalRLSVisibility(t *testing.T) {
	p := isolatedPostgres(t)
	role := "arkism_retention_" + ID()
	quoted := pgx.Identifier{role}.Sanitize()
	owner := pgx.Identifier{p.pool.Config().ConnConfig.User}.Sanitize()
	schemaExec(t, p, "CREATE ROLE "+quoted+" LOGIN NOSUPERUSER NOBYPASSRLS")
	schemaExec(t, p, "GRANT SELECT,DELETE ON arkism.memory_records TO "+quoted)
	schemaExec(t, p, "GRANT EXECUTE ON FUNCTION arkism.permitted_scopes(text),arkism.has_any_permission(text) TO "+quoted)
	schemaExec(t, p, "ALTER SCHEMA arkism OWNER TO "+quoted)
	t.Cleanup(func() {
		schemaExec(t, p, "ALTER SCHEMA arkism OWNER TO "+owner)
		schemaExec(t, p, "DROP OWNED BY "+quoted)
		schemaExec(t, p, "DROP ROLE "+quoted)
	})
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := p.Insert(testCtx, "audit_events", Record{ID: "old-audit", Scope: scopeA, Kind: "allow:read", Status: statusActive, CreatedAt: old, UpdatedAt: old}); err != nil {
		t.Fatal(err)
	}
	cfg := p.pool.Config().Copy()
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = ""
	pool, err := pgxpool.NewWithConfig(testCtx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	restricted := &Postgres{pool: pool}
	if yes, e := schemaOwner(testCtx, restricted.q()); e != nil || !yes {
		t.Fatal("fixture is not schema owner", yes, e)
	}
	cutoff := time.Now().UTC().Add(-36 * time.Hour)
	for _, execute := range []bool{false, true} {
		if _, e := restricted.ExpireAuditEvents(testCtx, cutoff, execute); !errors.Is(e, ErrDenied) {
			t.Fatal("RLS-filtered owner appeared globally complete", execute, e)
		}
	}
	if _, e := restricted.ExpireAuditEventsBatch(testCtx, cutoff, 10); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if n, e := p.ExpireAuditEvents(testCtx, cutoff, false); e != nil || n != 1 {
		t.Fatal("audit changed", n, e)
	}
}
