package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func restrictedPostgres(t *testing.T, admin *Postgres) *Postgres {
	t.Helper()
	ctx := context.Background()
	role := "arkism_rls_test_" + ID()
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err := admin.q().Exec(ctx, "CREATE ROLE "+quoted+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.q().Exec(context.Background(), "DROP OWNED BY "+quoted+"; DROP ROLE "+quoted); err != nil {
			t.Error(err)
		}
	})
	if _, err := admin.q().Exec(ctx, "GRANT USAGE ON SCHEMA arkism,extensions TO "+quoted+`; GRANT SELECT ON memory_storage_metadata TO `+quoted+`; GRANT SELECT,INSERT,UPDATE,DELETE ON memory_records TO `+quoted+`; GRANT EXECUTE ON FUNCTION current_principal(),has_permission(text,text,text),has_any_permission(text),permitted_scopes(text),authorize_principal(text,text,text),reserve_candidate_quota(text,text,date,integer),artifact_gc_held(text),artifact_cleanup_pending(text,text,text,text,text,text) TO `+quoted); err != nil {
		t.Fatal(err)
	}
	cfg := admin.pool.Config().Copy()
	cfg.MaxConns = 1
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+quoted)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := &Postgres{pool: pool, ownsPool: true}
	t.Cleanup(app.Close)
	return app
}

func TestPostgresRLSScopesRevocationAndPoolIsolation(t *testing.T) {
	admin := isolatedPostgres(t)
	ctx := context.Background()
	scopeA := Scope{WorkspaceID: "org-a", ProjectID: "project-a"}
	scopeB := Scope{WorkspaceID: "org-b", ProjectID: "project-b"}
	now := time.Now().UTC()
	rowA := Record{ID: "row-a", Scope: scopeA, Revision: 1, Status: "active", Kind: "fact", Text: "A", CreatedAt: now, UpdatedAt: now}
	rowB := Record{ID: "row-b", Scope: scopeB, Revision: 1, Status: "active", Kind: "fact", Text: "B", CreatedAt: now, UpdatedAt: now}
	if err := admin.Insert(ctx, "memories", rowA); err != nil {
		t.Fatal(err)
	}
	if err := admin.Insert(ctx, "memories", rowB); err != nil {
		t.Fatal(err)
	}
	token := testPrincipalToken("rls-token-that-is-never-stored-raw")
	grant := PrincipalGrant{PrincipalID: "agent-a", Role: roleAgent, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "read:memories"}}
	if err := admin.ProvisionPrincipal(ctx, grant); err != nil {
		t.Fatal(err)
	}

	app := restrictedPostgres(t, admin)
	if _, err := app.Get(ctx, "memories", scopeA, rowA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbound runtime read: %v", err)
	}
	bound := ContextWithPostgresToken(ctx, token)
	if got, err := app.Get(bound, "memories", scopeA, rowA.ID); err != nil || got.ID != rowA.ID {
		t.Fatalf("bound read: %+v %v", got, err)
	}
	if _, err := app.Get(bound, "memories", scopeB, rowB.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross organization read: %v", err)
	}
	foreign := rowB
	foreign.ID = "cross-scope-write"
	if err := app.Insert(bound, "memories", foreign); err == nil {
		t.Fatal("cross organization insert passed RLS")
	}
	rows, err := app.q().Query(bound, `SELECT id FROM memory_records`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(ids) != 1 || ids[0] != rowA.ID {
		t.Fatalf("unscoped SQL escaped RLS: %v %v", ids, err)
	}

	// The same one-connection pool must not retain the preceding token.
	var count int
	if err = app.q().QueryRow(ContextWithPostgresToken(ctx, ""), `SELECT count(*) FROM memory_records`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("request token leaked through pool: %d %v", count, err)
	}
	worker, err := app.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.Get(ctx, "memories", scopeA, rowA.ID); err != nil {
		t.Fatalf("worker token read: %v", err)
	}
	if _, err = worker.Get(ContextWithPostgresToken(ctx, ""), "memories", scopeA, rowA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blank request did not override worker token: %v", err)
	}

	authorizer, err := NewPostgresAuthorizer(admin)
	if err != nil {
		t.Fatal(err)
	}
	request := Authorization{Action: "read:memories", Scope: scopeA, Context: NewContextScope(scopeA, "read:memories", "external:embedding")}
	decision, err := authorizer.Authorize(ctx, token, request)
	if err != nil || !decision.Allowed || decision.ExternalTransfer {
		t.Fatalf("explicit external permission not enforced: %+v %v", decision, err)
	}
	if err = app.ReserveCandidateQuota(bound, scopeA, now, 1); err == nil {
		t.Fatal("quota accepted token without candidate permission")
	}
	grant.Actions = append(grant.Actions, "external:transfer", "write:memory_candidates")
	if err = admin.ProvisionPrincipal(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err = app.ReserveCandidateQuota(bound, scopeA, now, 1); err != nil {
		t.Fatalf("candidate quota reservation: %v", err)
	}
	if err = app.ReserveCandidateQuota(bound, scopeA, now, 1); !errors.Is(err, ErrCandidateQuota) {
		t.Fatalf("candidate quota ceiling: %v", err)
	}
	decision, err = authorizer.Authorize(ctx, token, request)
	if err != nil || !decision.Allowed || !decision.ExternalTransfer {
		t.Fatalf("external permission not applied: %+v %v", decision, err)
	}
	if err = admin.SetPrincipalActive(ctx, grant.PrincipalID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = app.Get(bound, "memories", scopeA, rowA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token read: %v", err)
	}
}

func TestPostgresCanonicalTablesUseOrganizationRLS(t *testing.T) {
	admin := isolatedPostgres(t)
	ctx := context.Background()
	scope := Scope{WorkspaceID: "canonical-org", ProjectID: "canonical-project"}
	other := Scope{WorkspaceID: "other-org", ProjectID: "other-project"}
	token := testPrincipalToken("canonical-table-token")
	if err := admin.ProvisionPrincipal(ctx, PrincipalGrant{PrincipalID: "canonical-agent", Role: roleAgent, Token: token, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"db:read"}}); err != nil {
		t.Fatal(err)
	}
	for i, candidate := range []Scope{scope, other} {
		if _, err := admin.q().Exec(ctx, `INSERT INTO execution_traces(id,organization_id,project_id,thread_id,agent_run_id,status,started_at,finished_at,payload_raw,sensitivity,inspection,request_hash,created_by,created_at)
VALUES($1,$2,$3,$4,$5,'completed',now(),now(),$6,'internal','{}',$7,'test',now())`, "trace-"+candidate.WorkspaceID, candidate.WorkspaceID, candidate.ProjectID, "thread-"+candidate.WorkspaceID, "run-"+candidate.WorkspaceID, []byte{byte(i)}, "hash-"+candidate.WorkspaceID); err != nil {
			t.Fatal(err)
		}
	}
	app := restrictedPostgres(t, admin)
	// restrictedPostgres intentionally grants only memory_records; canonical
	// tables need the same DML grant but remain row-filtered.
	role := ""
	if err := app.pool.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err := admin.q().Exec(ctx, `GRANT SELECT ON execution_traces TO `+quoted); err != nil {
		t.Fatal(err)
	}
	var organizations []string
	rows, err := app.q().Query(ContextWithPostgresToken(ctx, token), `SELECT organization_id FROM execution_traces ORDER BY organization_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var organization string
		if err = rows.Scan(&organization); err != nil {
			t.Fatal(err)
		}
		organizations = append(organizations, organization)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(organizations) != 1 || organizations[0] != scope.WorkspaceID {
		t.Fatalf("canonical scope leak: %v %v", organizations, err)
	}
}
