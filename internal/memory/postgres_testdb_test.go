package memory

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func isolatedPostgres(t *testing.T) *Postgres {
	t.Helper()
	return isolatedPostgresSchema(t, true)
}

// Restore tests need a truly empty database: Init must not mask missing schema.
func isolatedPostgresSchema(t *testing.T, initialize bool) *Postgres {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set; local database not tested")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "arkism_") || net.ParseIP(cfg.ConnConfig.Host) == nil || !net.ParseIP(cfg.ConnConfig.Host).IsLoopback() {
		t.Fatal("requires an explicitly disposable loopback arkism_ database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	name := "arkism_test_" + ID()
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+quoted)
		if err != nil {
			t.Error("remove test database:", err)
		}
		admin.Close()
	})
	child := cfg.Copy()
	child.ConnConfig.Database = name
	child.ConnConfig.RuntimeParams["search_path"] = "arkism,extensions,pg_catalog"
	pool, err := pgxpool.NewWithConfig(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	p := &Postgres{pool: pool, ownsPool: true}
	t.Cleanup(p.Close)
	if initialize {
		if err = p.Init(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// pgCollection returns every row of one collection, for tests that read back
// what the store committed (audit events, jobs) without a public list API.
func pgCollection(t *testing.T, p *Postgres, col string) []Record {
	t.Helper()
	rows, err := p.pool.Query(context.Background(), "SELECT payload FROM memory_records WHERE collection=$1 ORDER BY id", col)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		r, err := pgDecode(b)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
