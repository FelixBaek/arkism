//go:build integration

// Shared helpers for the integration suite. Every test runs against a
// disposable Postgres database from isolatedPostgres (POSTGRES_TEST_DSN).
package memory

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

func itContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func itScope(project string) Scope { return Scope{WorkspaceID: "ws-it", ProjectID: project} }

func itRecord(scope Scope, text string) Record {
	now := time.Now().UTC()
	return Record{ID: ID(), Scope: scope, Kind: "memory", Status: statusActive, Revision: 1, Text: text,
		Sources: []SourceRef{{SourceID: "src-1", Locator: "original"}}, CreatedAt: now, UpdatedAt: now}
}

func itJob(t *testing.T, repo *Postgres, ctx context.Context, scope Scope, id string) Record {
	t.Helper()
	rec, err := repo.Get(ctx, colJobs, scope, id)
	if err != nil {
		t.Fatalf("read job %s: %v", id, err)
	}
	return rec
}

// itDSN is the connection string of the isolated test database, for child processes.
func itDSN(repo *Postgres) string {
	c := repo.pool.Config().ConnConfig
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, c.Password), Host: c.Host + ":" + strconv.Itoa(int(c.Port)), Path: "/" + c.Database, RawQuery: "sslmode=disable"}
	return u.String()
}

// memoryFenceRaceRepo pauses the first FenceSource call so a test can race a
// writer against source deletion.
type memoryFenceRaceRepo struct {
	Repository
	once             sync.Once
	reached, release chan struct{}
}
type memoryFenceRaceTx struct {
	Tx
	r *memoryFenceRaceRepo
}

func (r *memoryFenceRaceRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, memoryFenceRaceTx{Tx: tx, r: r}) })
}
func (x memoryFenceRaceTx) FenceSource(ctx context.Context, scope Scope, id string) error {
	x.r.once.Do(func() {
		close(x.r.reached)
		select {
		case <-x.r.release:
		case <-ctx.Done():
		}
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return x.Tx.FenceSource(ctx, scope, id)
}
