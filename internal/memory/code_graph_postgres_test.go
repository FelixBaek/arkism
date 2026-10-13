package memory

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/codegraph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// codeGraphPostgres is restrictedPostgres with the code graph tables granted:
// a NOBYPASSRLS runtime role, so every query below goes through RLS.
func codeGraphPostgres(t *testing.T, admin *Postgres) *Postgres {
	t.Helper()
	ctx := context.Background()
	role := "arkism_cg_test_" + ID()
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err := admin.q().Exec(ctx, "CREATE ROLE "+quoted+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.q().Exec(context.Background(), "DROP OWNED BY "+quoted+"; DROP ROLE "+quoted)
	})
	if _, err := admin.q().Exec(ctx, "GRANT USAGE ON SCHEMA arkism,extensions TO "+quoted+`; GRANT SELECT ON memory_storage_metadata TO `+quoted+`;
GRANT SELECT,INSERT,UPDATE,DELETE ON memory_records,code_repos,code_symbols,code_edges TO `+quoted+`;
GRANT EXECUTE ON FUNCTION current_principal(),has_permission(text,text,text),has_any_permission(text),permitted_scopes(text),authorize_principal(text,text,text) TO `+quoted); err != nil {
		t.Fatal(err)
	}
	cfg := admin.pool.Config().Copy()
	cfg.MaxConns = 4
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

func postgresCodeGraphEnv(t *testing.T) (codeGraphEnv, *Postgres, *Postgres) {
	admin := isolatedPostgres(t)
	ctx := context.Background()
	grants := []PrincipalGrant{
		{PrincipalID: "cg-human-a", Role: roleHuman, Token: "cg-token-human-a-never-stored", OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", codeGraphRead, codeGraphWrite}},
		{PrincipalID: "cg-agent-a", Role: roleAgent, Token: "cg-token-agent-a-never-stored", OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", codeGraphRead, codeGraphWrite}},
		{PrincipalID: "cg-human-b", Role: roleHuman, Token: "cg-token-human-b-never-stored", OrganizationID: scopeB.WorkspaceID, ProjectID: scopeB.ProjectID, Actions: []string{"db:read", "db:write", codeGraphRead, codeGraphWrite}},
	}
	for _, g := range grants {
		if err := admin.ProvisionPrincipal(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	app := codeGraphPostgres(t, admin)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: app, Auth: auth}
	return codeGraphEnv{svc: svc, human: grants[0].Token, agent: grants[1].Token, other: grants[2].Token,
		ctx: func(token string) context.Context { return ContextWithPostgresToken(ctx, token) }}, admin, app
}

func TestPostgresCodeGraphScenario(t *testing.T) {
	env, _, _ := postgresCodeGraphEnv(t)
	runCodeGraphScenario(t, env)
}

func TestPostgresCodeGraphRLS(t *testing.T) {
	env, admin, app := postgresCodeGraphEnv(t)
	upload(t, env, scopeA, "fixture", indexFixture(t), 0)
	ctxB := env.ctx(env.other)
	// the store itself, below the service's authorization: RLS hides scope A from token B
	if hits, err := app.FindCodeSymbols(ctxB, scopeA, "", "Total", false, 10); err != nil || len(hits) != 0 {
		t.Fatalf("token B read scope A rows: %v %v", hits, err)
	}
	if repos, err := app.CodeRepos(ctxB, scopeA); err != nil || len(repos) != 0 {
		t.Fatalf("token B listed scope A repos: %v %v", repos, err)
	}
	b := batches(scopeA, "intruder", indexFixture(t), 100)[0]
	if _, err := app.ApplyCodeBatch(ctxB, scopeA, b, time.Now()); err == nil {
		t.Fatal("token B wrote into scope A")
	}
	if _, err := app.FindCodeSymbols(context.Background(), scopeA, "", "Total", false, 10); err == nil {
		// an unbound connection sees nothing either
		if hits, _ := app.FindCodeSymbols(context.Background(), scopeA, "", "Total", false, 10); len(hits) != 0 {
			t.Fatal("unbound read returned rows")
		}
	}
	var forced int
	if err := admin.q().QueryRow(context.Background(), `SELECT count(*) FROM pg_class WHERE relname = ANY($1) AND relrowsecurity AND relforcerowsecurity`, codeGraphTables).Scan(&forced); err != nil || forced != 3 {
		t.Fatalf("FORCE RLS on code graph tables: %d %v", forced, err)
	}
}

// TestPostgresCodeGraphArkism uploads this repository's own graph into a fresh
// database and asks questions right away (before autovacuum has statistics).
func TestPostgresCodeGraphArkism(t *testing.T) {
	if testing.Short() || os.Getenv("POSTGRES_TEST_DSN") == "" {
		t.Skip("needs POSTGRES_TEST_DSN and the full repository")
	}
	env, _, _ := postgresCodeGraphEnv(t)
	started := time.Now()
	res, err := codegraph.Index(context.Background(), codegraph.Options{Root: "../.."})
	if err != nil {
		t.Fatal(err)
	}
	indexed := time.Since(started)
	started = time.Now()
	upload(t, env, scopeA, "arkism", res, 0)
	uploaded := time.Since(started)
	ctx := env.ctx(env.agent)
	started = time.Now()
	out, err := env.svc.CodeImpact(ctx, env.agent, CodeGraphQuery{Scope: scopeA, Symbol: "memory.Postgres.Get", Depth: 3, Limit: 200, MaxTokens: codeTokenMax})
	asked := time.Since(started)
	if err != nil || len(out.Items) == 0 {
		t.Fatalf("impact %+v %v", out, err)
	}
	started = time.Now()
	callers, err := env.svc.CodeCallers(ctx, env.agent, CodeGraphQuery{Scope: scopeA, Symbol: "memory.Service.authorize"})
	again := time.Since(started)
	if err != nil || len(callers.Items) == 0 {
		t.Fatalf("callers %+v %v", callers, err)
	}
	t.Logf("arkism: %d files %d symbols %d edges; index %v, upload %v, first impact %v (%d items, %s), callers %v", len(res.Files), res.Stats.Symbols, res.Stats.Edges, indexed, uploaded, asked, len(out.Items), out.Reason, again)
	if asked > 5*time.Second {
		t.Fatalf("impact took %v", asked)
	}
}
