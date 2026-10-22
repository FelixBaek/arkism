package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresRecordEncodingPreservesHiddenFields(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	want := Record{ID: "m1", Scope: Scope{WorkspaceID: "w", ProjectID: "p"}, Revision: 3, Status: "active", Text: "hello", CreatedAt: now, UpdatedAt: now, LeaseToken: "secret", Attempts: 2, MemoryReferenceFence: 4}
	b, _, err := encodeRecord(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgDecode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Scope != want.Scope || got.Revision != want.Revision || got.LeaseToken != want.LeaseToken || got.Attempts != want.Attempts || got.MemoryReferenceFence != want.MemoryReferenceFence {
		t.Fatalf("decoded record lost fields: got=%+v want=%+v", got, want)
	}
}

func TestPostgresMapsDuplicateAndSerializationErrors(t *testing.T) {
	if !errors.Is(pgErr(&pgconn.PgError{Code: "23505"}), ErrConflict) {
		t.Fatal("duplicate must map to conflict")
	}
	if !errors.Is(pgErr(&pgconn.PgError{Code: "40001"}), ErrUnavailable) {
		t.Fatal("serialization failure must be retryable unavailable")
	}
	var refused rlsRefusal
	if rls := pgErr(&pgconn.PgError{Code: "42501", Message: `new row violates row-level security policy for table "memory_records"`}); !errors.As(rls, &refused) || !errors.Is(rls, ErrUnavailable) {
		t.Fatal("RLS refusal must stay unavailable and be marked")
	}
	if (rlsRefusal{}).Error() != ErrUnavailable.Error() {
		t.Fatal("RLS refusal must read as unavailable")
	}
	if grant := pgErr(&pgconn.PgError{Code: "42501", Message: "permission denied for table memory_records"}); !errors.Is(grant, ErrUnavailable) || errors.As(grant, &refused) {
		t.Fatal("missing grant must stay unavailable")
	}
}

func TestPostgresCoreIntegration(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	var err error
	s := Scope{WorkspaceID: "pg-core-test", ProjectID: t.Name()}
	_, err = p.q().Exec(ctx, `DELETE FROM memory_records WHERE workspace_id=$1 AND project_id=$2`, s.WorkspaceID, s.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	r := Record{ID: "m1", Scope: s, Kind: "memory", Status: "active", Revision: 1, Text: "local", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err = p.Insert(ctx, "memories", r); err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(ctx, "memories", s, r.ID)
	if err != nil || got.Text != r.Text {
		t.Fatalf("get: %+v %v", got, err)
	}
	got.Text = "changed"
	got.Revision = 2
	bad := got
	bad.CreatedBy = "different"
	if err = p.Replace(ctx, "memories", bad, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("provenance replace = %v", err)
	}
	if err = p.Replace(ctx, "memories", got, 1); err != nil {
		t.Fatal(err)
	}
	if err = p.Replace(ctx, "memories", got, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replace = %v", err)
	}
	if err = p.FenceSearchRead(ctx, s, "memories", r.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err = p.Delete(ctx, "memories", s, r.ID, 2); err != nil {
		t.Fatal(err)
	}
	job := Record{ID: "j1", Scope: s, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err = p.Insert(ctx, "jobs", job); err != nil {
		t.Fatal(err)
	}
	claimed, err := p.ClaimJob(ctx, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.LeaseToken == "" || claimed.Status != JobStatusRunning {
		t.Fatalf("claim = %+v", claimed)
	}
	if err = p.FinishJob(ctx, claimed, true); err != nil {
		t.Fatal(err)
	}
	job2 := Record{ID: "j2", Scope: s, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err = p.Insert(ctx, "jobs", job2); err != nil {
		t.Fatal(err)
	}
	claimed, err = p.ClaimJob(ctx, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.FinishJob(ctx, claimed, false); err != nil {
		t.Fatal(err)
	}
	if _, err = p.ClaimJob(ctx, time.Now().UTC(), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry claimed before run_at: %v", err)
	}
}

func TestPostgresPrivateSchemaAndLeastPrivilege(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	if err := p.SearchReady(ctx); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := p.q().QueryRow(ctx, "SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector'").Scan(&schema); err != nil || schema != "extensions" {
		t.Fatalf("extension schema %q: %v", schema, err)
	}
	rows, err := p.q().Query(ctx, "SELECT rolname FROM pg_roles WHERE rolname IN ('anon','authenticated','service_role')")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	for _, name := range names {
		var allowed bool
		err := p.q().QueryRow(ctx, "SELECT has_schema_privilege($1,'arkism','USAGE') OR has_table_privilege($1,'arkism.memory_records','SELECT,INSERT,UPDATE,DELETE')", name).Scan(&allowed)
		if err != nil || allowed {
			t.Fatalf("public API role %s accesses memory: %v", name, err)
		}
	}
	name := "arkism_test_role_" + ID()
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = p.q().Exec(ctx, "CREATE ROLE "+quoted+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := p.q().Exec(ctx, "DROP OWNED BY "+quoted+"; DROP ROLE "+quoted); err != nil {
			t.Error(err)
		}
	}()
	var allowed bool
	if err := p.q().QueryRow(ctx, "SELECT has_schema_privilege($1,'arkism','USAGE')", name).Scan(&allowed); err != nil || allowed {
		t.Fatalf("unprivileged access: %v", err)
	}
	if _, err = p.q().Exec(ctx, "GRANT USAGE ON SCHEMA arkism,extensions TO "+quoted+"; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA arkism TO "+quoted+"; GRANT EXECUTE ON FUNCTION has_permission(text,text,text),has_any_permission(text),permitted_scopes(text) TO "+quoted); err != nil {
		t.Fatal(err)
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SET ROLE "+quoted); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "RESET ROLE")
	var count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM memory_records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unbound RLS role read %d rows", count)
	}
}
