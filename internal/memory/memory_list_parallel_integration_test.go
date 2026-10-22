//go:build integration

package memory

import (
	"strconv"
	"sync"
	"testing"
)

// seedListMemories copies one approved memory n times over 70 sources, so a 100-row page needs two source reads.
func seedListMemories(t *testing.T, f *fixture, repo Repository, n int) {
	t.Helper()
	base := f.activeMemory("parallel list seed", "seed")
	sources := []Record{}
	for i := 0; i < 70; i++ {
		sources = append(sources, f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "seed source " + strconv.Itoa(i)})))
	}
	for i := 0; i < n; i++ {
		r := base
		r.ID = ID()
		r.Sources = refs(sources[i%len(sources)])
		if e := repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
}

// #173: list reads that only share-lock rows and append an audit row must not exhaust serializable retries.
func TestIntegrationParallelCatalogQueriesDoNotExhaustRetries(t *testing.T) {
	repo := isolatedPostgres(t)
	f, _ := memoryListFixture(t, repo)
	seedListMemories(t, f, repo, 2000)
	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for _, state := range []string{"active", "idle", "active", "idle", "active", "provenance"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := f.svc.QueryMemoryCatalog(testCtx, f.human, CatalogQueryInput{Scope: scopeA, State: state, Limit: 1}, false)
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal("round", round, e)
			}
		}
	}
}
