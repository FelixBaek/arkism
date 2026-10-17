package memory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSchemaMigrationChecksumAcrossPlatforms(t *testing.T) {
	// Persisted compatibility contract: add a migration instead of changing this asset.
	const baselineChecksum = "b675ded6c408ba842b9568a2e675be6d66fdedcf089239642f5ff6c4c0547ab8"
	if schemaMigrations()[0].checksum() != baselineChecksum {
		t.Fatal("applied baseline changed; append a new migration")
	}
	lf := schemaMigration{1, "baseline", "SELECT 1;\nSELECT 2;\n"}
	crlf := schemaMigration{1, "baseline", "SELECT 1;\r\nSELECT 2;\r\n"}
	changed := schemaMigration{1, "baseline", "SELECT 1;\nSELECT 3;\n"}
	if lf.checksum() != crlf.checksum() || lf.checksum() == changed.checksum() {
		t.Fatal("checksum ignores content changes or depends on checkout line endings")
	}
}

func schemaExec(t *testing.T, p *Postgres, sql string, args ...any) {
	t.Helper()
	if _, err := p.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func schemaCount(t *testing.T, p *Postgres) int {
	t.Helper()
	var count int
	if err := p.pool.QueryRow(context.Background(), `SELECT count(*) FROM arkism.schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func schemaSnapshot(t *testing.T, p *Postgres) []string {
	t.Helper()
	rows, err := p.pool.Query(context.Background(), `SELECT version::text||':'||name||':'||checksum||':'||applied_at::text FROM arkism.schema_migrations
UNION ALL SELECT oid::text||':'||xmin::text FROM pg_proc WHERE pronamespace='arkism'::regnamespace ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		out = append(out, value)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	return out
}

func TestSchemaMigrationsFreshAndNoReplay(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	if p.SearchReady(ctx) == nil {
		t.Fatal("empty database accepted")
	}
	if err := p.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.SearchReady(ctx); err != nil {
		t.Fatal(err)
	}
	before := schemaSnapshot(t, p)
	gate, err := p.recoveryWriteGate(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Init(ctx); err != nil {
		t.Fatal(err)
	}
	afterGate, err := p.recoveryWriteGate(ctx, "")
	if err != nil || !reflect.DeepEqual(gate, afterGate) {
		t.Fatal("identity/gate changed", err)
	}
	if schemaCount(t, p) != 1 || !reflect.DeepEqual(before, schemaSnapshot(t, p)) {
		t.Fatal("applied baseline replayed")
	}
}

func TestSchemaMigrationsAdoptLegacyPreservesDataAndPausedGate(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	legacy, err := os.ReadFile("testdata/schema_before_migrations.sql")
	if err != nil {
		t.Fatal(err)
	}
	identity := bytes.Repeat([]byte{23}, 32)
	gate := []byte(`{"version":1,"state":"open","generation":"` + strings.Repeat("a", 64) + `","changed_at":"2026-10-18T00:00:00Z"}`)
	schemaExec(t, p, string(legacy), pgx.QueryExecModeSimpleProtocol, identity, gate)
	row := Record{ID: "before-upgrade", Scope: scopeA, Status: statusActive, Kind: "decision", Revision: 7, Text: "기존 승인된 자료는 그대로 보존한다", UpdatedAt: time.Now().UTC()}
	if err := p.Insert(ctx, "memories", row); err != nil {
		t.Fatal(err)
	}
	schemaExec(t, p, `UPDATE memory_storage_metadata SET value=convert_to(replace(convert_from(value,'UTF8'),'"open"','"paused"'),'UTF8') WHERE key='recovery_write_gate'`)
	before, err := p.Get(ctx, "memories", scopeA, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeGate, err := p.recoveryWriteGate(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.SearchReady(ctx) == nil {
		t.Fatal("unversioned schema ready before upgrade")
	}
	if err := p.Init(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := p.Get(ctx, "memories", scopeA, row.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("legacy record changed", err)
	}
	afterGate, err := p.recoveryWriteGate(ctx, "")
	if err != nil || !reflect.DeepEqual(beforeGate, afterGate) {
		t.Fatal("paused gate/identity changed", err)
	}
	if err := p.SearchReady(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Insert(ctx, "memories", Record{ID: "must-stay-paused", Scope: scopeA, Revision: 1}); err == nil {
		t.Fatal("upgrade reopened writes")
	}
}

func TestSchemaMigrationsRollbackAndRetry(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	before := schemaSnapshot(t, p)
	failed := append(schemaMigrations(), schemaMigration{2, "atomic-change", `CREATE TABLE arkism.migration_probe(id integer); INSERT INTO arkism.migration_probe VALUES(1); SELECT 1/0;`})
	if p.migrateSchema(ctx, failed) == nil {
		t.Fatal("failed migration accepted")
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('arkism.migration_probe') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("partial DDL survived", err)
	}
	if !reflect.DeepEqual(before, schemaSnapshot(t, p)) {
		t.Fatal("failed migration changed history")
	}
	fixed := append(schemaMigrations(), schemaMigration{2, "atomic-change", `CREATE TABLE arkism.migration_probe(id integer); INSERT INTO arkism.migration_probe VALUES(1);`})
	if err := p.migrateSchema(ctx, fixed); err != nil {
		t.Fatal("retry", err)
	}
	if schemaCount(t, p) != 2 {
		t.Fatal("retry not recorded exactly once")
	}
	if err := p.migrateSchema(ctx, fixed); err != nil {
		t.Fatal("replay", err)
	}
	if p.Init(ctx) == nil || p.SearchReady(ctx) == nil {
		t.Fatal("older binary accepted future version")
	}
}

func TestSchemaMigrationsBootstrapFailureLeavesNoHistory(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	schemaExec(t, p, `CREATE SCHEMA arkism; CREATE TABLE arkism.memory_records(id integer PRIMARY KEY); INSERT INTO arkism.memory_records VALUES(17)`)
	if p.Init(ctx) == nil {
		t.Fatal("incompatible legacy table accepted")
	}
	var history bool
	var id int
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('arkism.schema_migrations') IS NOT NULL`).Scan(&history); err != nil || history {
		t.Fatal("partial bootstrap history survived", err)
	}
	if err := p.pool.QueryRow(ctx, `SELECT id FROM arkism.memory_records`).Scan(&id); err != nil || id != 17 {
		t.Fatal("failed bootstrap changed existing data", err)
	}
	// This malformed table belongs only to this disposable test database.
	schemaExec(t, p, `DROP TABLE arkism.memory_records`)
	if err := p.Init(ctx); err != nil {
		t.Fatal("bootstrap cannot retry", err)
	}
}

func TestSchemaMigrationsRejectRequestTokenContext(t *testing.T) {
	p := isolatedPostgres(t)
	before := schemaSnapshot(t, p)
	ctx := ContextWithPostgresToken(context.Background(), "synthetic-request-credential")
	if err := p.Init(ctx); !errors.Is(err, ErrDenied) {
		t.Fatal("request token may initialize schema", err)
	}
	if !reflect.DeepEqual(before, schemaSnapshot(t, p)) {
		t.Fatal("request changed schema")
	}
}

func TestSchemaMigrationsRejectIncompatibleHistory(t *testing.T) {
	for name, sql := range map[string]string{
		"checksum": `UPDATE arkism.schema_migrations SET checksum=repeat('0',64)`,
		"name":     `UPDATE arkism.schema_migrations SET name='modified'`,
		"future":   `INSERT INTO arkism.schema_migrations VALUES(2,'unknown',repeat('a',64),now())`,
		"gap":      `UPDATE arkism.schema_migrations SET version=3`,
		"missing":  `DELETE FROM arkism.schema_migrations`,
	} {
		t.Run(name, func(t *testing.T) {
			p := isolatedPostgres(t)
			schemaExec(t, p, sql)
			before := schemaSnapshot(t, p)
			if p.Init(context.Background()) == nil || p.SearchReady(context.Background()) == nil {
				t.Fatal("incompatible history accepted")
			}
			if !reflect.DeepEqual(before, schemaSnapshot(t, p)) {
				t.Fatal("failed validation rewrote history")
			}
		})
	}
}

func TestSchemaMigrationsConcurrentInitialization(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { errs <- p.Init(context.Background()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if schemaCount(t, p) != 1 {
		t.Fatal("concurrent migration duplicated")
	}
	if err := p.SearchReady(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaMigrationsCancelledLockCanRetry(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaMigrationLock); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if p.Init(bounded) == nil {
		t.Fatal("lock ignored cancellation")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Init(ctx); err != nil {
		t.Fatal("lock leaked after cancellation", err)
	}
}

func TestSchemaMigrationsApplicationCannotChangeHistory(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	schemaExec(t, p, `DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='arkism_app') THEN CREATE ROLE arkism_app NOLOGIN NOSUPERUSER NOBYPASSRLS; END IF; END $$;
	DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='arkism_init') THEN CREATE ROLE arkism_init NOLOGIN NOSUPERUSER NOBYPASSRLS; END IF; END $$;
	CREATE SCHEMA extensions; CREATE EXTENSION vector WITH SCHEMA extensions;
	CREATE SCHEMA arkism AUTHORIZATION arkism_init;
	GRANT USAGE ON SCHEMA extensions TO arkism_init;
	GRANT USAGE ON SCHEMA arkism,extensions TO arkism_app;
	ALTER DEFAULT PRIVILEGES FOR ROLE arkism_init IN SCHEMA arkism GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO arkism_app;`)
	schemaExec(t, p, "GRANT CREATE ON DATABASE "+pgx.Identifier{p.pool.Config().ConnConfig.Database}.Sanitize()+" TO arkism_init")
	initializer := schemaRolePostgres(t, p, "arkism_init")
	if err := initializer.Init(ctx); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := p.pool.QueryRow(ctx, `SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='arkism.schema_migrations'::regclass`).Scan(&owner); err != nil || owner != "arkism_init" {
		t.Fatal("migration owner differs from initializer", err)
	}
	app := schemaRolePostgres(t, p, "arkism_app")
	if err := app.SearchReady(ctx); err != nil {
		t.Fatal("app cannot check readiness", err)
	}
	if schemaCount(t, app) != 1 {
		t.Fatal("history not readable")
	}
	for _, sql := range []string{`DELETE FROM arkism.schema_migrations`, `UPDATE arkism.schema_migrations SET checksum=repeat('b',64)`, `INSERT INTO arkism.schema_migrations VALUES(2,'bad',repeat('c',64),now())`, `TRUNCATE arkism.schema_migrations`} {
		if _, err := app.pool.Exec(ctx, sql); err == nil {
			t.Fatal("app can change migration history")
		}
	}
	if err := app.Init(ctx); !errors.Is(err, ErrDenied) {
		t.Fatal("app may migrate", err)
	}
}

func schemaRolePostgres(t *testing.T, p *Postgres, role string) *Postgres {
	t.Helper()
	cfg := p.pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &Postgres{pool: pool}
}
