//go:build integration

package memory

import "testing"

func TestIntegrationSearchModes(t *testing.T) { exerciseSearchModes(t, isolatedPostgres(t)) }

func TestIntegrationSearchModesSkipUnselectedQueries(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	row := f.activeMemory("native selection", "selection")
	// A plain mongod cannot execute $vectorSearch. Passing a valid vector still
	// succeeds when only exact is selected: no vector pipeline was executed.
	lanes, err := repo.SearchCollection(testCtx, colMemories, SearchInput{Scope: scopeA, Query: "selection", Limit: 8, Modes: []string{"exact"}}, make([]float64, VectorDimension), "test-space")
	if err != nil || len(lanes) != 3 || len(lanes[0]) != 1 || lanes[0][0].ID != row.ID || len(lanes[1]) != 0 || len(lanes[2]) != 0 {
		t.Fatal("unselected native lanes ran", lanes, err)
	}
}
