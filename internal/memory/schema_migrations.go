package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed" // Required for the embedded, immutable SQL migration assets.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Transaction-scoped and database-local: no lock survives a rollback or lost connection.
const schemaMigrationLock int64 = 0x41524b49534d0001

//go:embed migrations/0001_baseline.sql
var schemaBaselineSQL string

//go:embed migrations/0002_memory_context_search.sql
var schemaMemoryContextSearchSQL string

//go:embed migrations/0003_principal_token_lifecycle.sql
var schemaPrincipalTokenLifecycleSQL string

//go:embed migrations/0004_memory_graph_vector_index.sql
var schemaMemoryGraphVectorIndexSQL string

//go:embed migrations/0005_decision_daily_counts.sql
var schemaDecisionDailyCountsSQL string

type schemaMigration struct {
	version int
	name    string
	sql     string
}

func schemaMigrations() []schemaMigration {
	return []schemaMigration{{1, "baseline", schemaBaselineSQL}, {2, "memory_context_search", schemaMemoryContextSearchSQL}, {3, "principal_token_lifecycle", schemaPrincipalTokenLifecycleSQL}, {4, "memory_graph_vector_index", schemaMemoryGraphVectorIndexSQL}, {5, "decision_daily_counts", schemaDecisionDailyCountsSQL}}
}

func (m schemaMigration) checksum() string {
	// Git may check text out as CRLF on Windows. A database must keep the same
	// migration identity when an installer or restore moves between operating systems.
	sum := sha256.Sum256([]byte(strings.ReplaceAll(m.sql, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}

func schemaFailure(message string) error {
	// Never return SQL, a DSN, data, credentials or raw database diagnostics.
	return fmt.Errorf("database schema %s: %w", message, ErrUnavailable)
}

func schemaDatabaseFailure(message string, err error) error {
	code := "unknown"
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr != nil && len(pgErr.Code) == 5 {
		valid := true
		for _, c := range pgErr.Code {
			if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
				valid = false
				break
			}
		}
		if valid {
			code = pgErr.Code
		}
	}
	return schemaFailure(fmt.Sprintf("%s (SQLSTATE %s)", message, code))
}

// Init applies only unapplied, immutable migrations. Search backfill is a separate,
// bounded and resumable data operation; it cannot leave half-installed schema.
func (p *Postgres) Init(ctx context.Context) error {
	if err := p.migrateSchema(ctx, schemaMigrations()); err != nil {
		return err
	}
	gate, err := p.recoveryWriteGate(ctx, "")
	if err != nil {
		return err
	}
	if gate.Gate.State == "paused" {
		return nil
	}
	budget := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/2)
	}
	_, _, err = p.BackfillSearchTokens(ctx, budget)
	return err
}

func (p *Postgres) migrateSchema(ctx context.Context, migrations []schemaMigration) error {
	if p == nil || p.pool == nil || p.tx != nil || len(migrations) == 0 {
		return ErrInvalid
	}
	if _, bound := p.effectiveTokenHash(ctx); bound {
		return ErrDenied
	}
	for i, migration := range migrations {
		if migration.version != i+1 || migration.name == "" || migration.sql == "" {
			return ErrInvalid
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return schemaFailure("connection unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if err = lockSchemaMigration(ctx, tx); err != nil {
		return err
	}
	applied, err := prepareSchemaHistory(ctx, tx, migrations)
	if err != nil {
		return err
	}
	if err = applyPendingSchemaMigrations(ctx, tx, migrations[applied:]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schemaHistoryPrivileges); err != nil {
		return schemaFailure("history privileges unavailable")
	}
	view := &Postgres{pool: p.pool, tx: tx}
	if err = view.checkSearchSchema(ctx); err != nil {
		return schemaFailure("readiness check failed")
	}
	if err = tx.Commit(ctx); err != nil {
		return schemaFailure("commit failed")
	}
	return nil
}

func lockSchemaMigration(ctx context.Context, tx pgx.Tx) error {
	allowed, err := schemaOwner(ctx, tx)
	if err != nil || !allowed {
		return ErrDenied
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'; SET LOCAL search_path=arkism,extensions,pg_catalog`); err != nil {
		return schemaFailure("configuration unavailable")
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaMigrationLock); err != nil {
		return schemaDatabaseFailure("migration lock unavailable", err)
	}
	return nil
}

func prepareSchemaHistory(ctx context.Context, tx pgx.Tx, migrations []schemaMigration) (int, error) {
	var hadHistory bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('arkism.schema_migrations') IS NOT NULL`).Scan(&hadHistory); err != nil {
		return 0, schemaFailure("history unavailable")
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS arkism;
CREATE TABLE IF NOT EXISTS arkism.schema_migrations (
version integer PRIMARY KEY CHECK(version>0), name text NOT NULL,
checksum text NOT NULL CHECK(checksum ~ '^[0-9a-f]{64}$'),
applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return 0, schemaFailure("history unavailable")
	}
	if err := checkSchemaHistory(ctx, tx, migrations, false); err != nil {
		return 0, err
	}
	var applied int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM arkism.schema_migrations`).Scan(&applied); err != nil {
		return 0, schemaFailure("history unavailable")
	}
	if hadHistory && applied == 0 {
		return 0, schemaFailure("history incomplete")
	}
	return applied, nil
}

func applyPendingSchemaMigrations(ctx context.Context, tx pgx.Tx, migrations []schemaMigration) error {
	for _, migration := range migrations {
		if err := applySchemaMigration(ctx, tx, migration); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO arkism.schema_migrations(version,name,checksum) VALUES($1,$2,$3)`, migration.version, migration.name, migration.checksum()); err != nil {
			return schemaFailure("history write failed")
		}
	}
	return nil
}

// Version one binds its seed values using pgx's SQL-aware simple-protocol
// encoding. The entire frozen SQL batch remains inside the outer transaction.
func applySchemaMigration(ctx context.Context, tx pgx.Tx, migration schemaMigration) error {
	var args []any
	if migration.version == 1 {
		var identity, generation [32]byte
		if _, err := rand.Read(identity[:]); err != nil {
			return schemaFailure("identity generation failed")
		}
		if _, err := rand.Read(generation[:]); err != nil {
			return schemaFailure("gate generation failed")
		}
		// Keep this persisted version-one representation independent of future types.
		gate := struct {
			Version    int       `json:"version"`
			State      string    `json:"state"`
			Generation string    `json:"generation"`
			ChangedAt  time.Time `json:"changed_at"`
		}{1, "open", hex.EncodeToString(generation[:]), time.Now().UTC()}
		data, err := json.Marshal(gate)
		if err != nil {
			return schemaFailure("gate encoding failed")
		}
		args = []any{pgx.QueryExecModeSimpleProtocol, identity[:], data}
	}
	if _, err := tx.Exec(ctx, migration.sql, args...); err != nil {
		return schemaDatabaseFailure(fmt.Sprintf("migration %d failed", migration.version), err)
	}
	return nil
}

func checkSchemaHistory(ctx context.Context, q pgQuery, migrations []schemaMigration, requireCurrent bool) error {
	rows, err := q.Query(ctx, `SELECT version,name,checksum FROM arkism.schema_migrations ORDER BY version LIMIT $1`, len(migrations)+1)
	if err != nil {
		return schemaFailure("history unavailable; run --init-db with the migration owner")
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if rows.Scan(&version, &name, &checksum) != nil || i >= len(migrations) {
			return schemaFailure("history incompatible")
		}
		m := migrations[i]
		if version != m.version || name != m.name || checksum != m.checksum() {
			return schemaFailure("history incompatible")
		}
		i++
	}
	if rows.Err() != nil || (requireCurrent && i != len(migrations)) {
		return schemaFailure("history incomplete; run --init-db with the migration owner")
	}
	return nil
}

const schemaHistoryPrivileges = `REVOKE ALL ON arkism.schema_migrations FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
 FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role','arkism_app'] LOOP
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
   EXECUTE format('REVOKE ALL ON arkism.schema_migrations FROM %I',role_name);
  END IF;
 END LOOP;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='arkism_app') THEN
  GRANT SELECT ON arkism.schema_migrations TO arkism_app;
 END IF;
END $$;`
