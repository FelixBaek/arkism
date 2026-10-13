//go:build integration

package memory

import (
	"context"
	"sync"
	"testing"
)

type searchFenceRaceRepo struct {
	Repository
	target           Ref
	once             sync.Once
	reached, release chan struct{}
}
type searchFenceRaceTx struct {
	Tx
	r *searchFenceRaceRepo
}

func (r *searchFenceRaceRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, searchFenceRaceTx{tx, r}) })
}
func (r *searchFenceRaceRepo) wait(ctx context.Context) {
	r.once.Do(func() {
		close(r.reached)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
}
func (t searchFenceRaceTx) FenceSearchRead(ctx context.Context, scope Scope, col, id string, revision int64) error {
	if col == t.r.target.Collection && id == t.r.target.ID {
		t.r.wait(ctx)
	}
	return t.Tx.FenceSearchRead(ctx, scope, col, id, revision)
}
func (t searchFenceRaceTx) FenceSource(ctx context.Context, scope Scope, id string) error {
	if t.r.target.Collection == "sources" && t.r.target.ID == id {
		t.r.wait(ctx)
	}
	return t.Tx.FenceSource(ctx, scope, id)
}

func TestIntegrationSearchUnreturnedCandidatesNotFenced(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	rows := []Record{f.activeMemory("needle first", "needle"), f.activeMemory("needle second", "needle"), f.activeMemory("needle third", "needle")}
	before := map[string]Record{}
	for _, r := range rows {
		current, e := repo.Get(testCtx, colMemories, scopeA, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		before[r.ID] = current
	}
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "needle", Limit: 3, MaxChars: 1})
	if e != nil || len(out.Items) != 1 {
		t.Fatal("bounded search", e, out)
	}
	for _, r := range rows {
		current, e := repo.Get(testCtx, colMemories, scopeA, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		want := before[r.ID].MemoryReferenceFence
		if r.ID == out.Items[0].ID {
			want++
		}
		if current.MemoryReferenceFence != want || current.Revision != before[r.ID].Revision || !current.UpdatedAt.Equal(before[r.ID].UpdatedAt) {
			t.Fatal("unexpected fence or semantic mutation", r.ID, current.MemoryReferenceFence, want)
		}
	}
}
