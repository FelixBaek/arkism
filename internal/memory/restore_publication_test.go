package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresRestorePublicationPersistsAndFailsClosed(t *testing.T) {
	p := isolatedPostgres(t)
	if err := p.CheckRestorePublication(testCtx); err != nil {
		t.Fatal(err)
	}
	digest := ContentHash("verified manifest")
	if err := p.RequireRestoreReview(testCtx, digest); err != nil {
		t.Fatal(err)
	}
	if err := p.RequireRestoreReview(testCtx, digest); err != nil {
		t.Fatal("retry", err)
	}
	if err := p.RequireRestoreReview(testCtx, ContentHash("other manifest")); !errors.Is(err, ErrConflict) {
		t.Fatal("overwrote pending review", err)
	}
	restricted := restrictedPostgres(t, p)
	if err := restricted.CheckRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("restricted role missed pending gate", err)
	}
	if err := p.Init(testCtx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), p.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	reopened := &Postgres{pool: pool, ownsPool: true}
	defer reopened.Close()
	f := newFixture(t)
	f.svc.Repo = reopened // No file directory supplied.
	if err = f.svc.CheckRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("database reconnect bypassed gate", err)
	}
	normal := httptest.NewServer(Handler(f.svc))
	defer normal.Close()
	recoveryHTTP(t, normal.URL, f.human, "GET", "/healthz", nil, nil, 200)
	for _, path := range []string{"/readyz", "/v1/work-checkpoints/get", "/v1/search"} {
		method := "POST"
		if path == "/readyz" {
			method = "GET"
		}
		recoveryHTTP(t, normal.URL, f.human, method, path, map[string]any{"scope": scopeA}, nil, 503)
	}
	if err = f.svc.WorkOnce(testCtx, f.work); !errors.Is(err, ErrUnavailable) {
		t.Fatal("normal worker claimed work during restore review", err)
	}
	reopened.SetRecoveryMode(true)
	if err = f.svc.WorkOnce(testCtx, f.work); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty recovery worker refused gate", err)
	}
	isolated := httptest.NewServer(RecoveryHandler(f.svc))
	defer isolated.Close()
	recoveryHTTP(t, isolated.URL, f.human, "GET", "/readyz", nil, nil, 503)
	recoveryHTTP(t, isolated.URL, f.agent, "POST", "/v1/session-archives/expire", SessionArchiveExpireInput{Scope: scopeA}, nil, 403)
	empty := isolatedPostgresSchema(t, false)
	if err = empty.CheckRestorePublication(testCtx); err == nil {
		t.Fatal("missing metadata accepted")
	}
}
