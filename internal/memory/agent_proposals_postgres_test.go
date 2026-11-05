package memory

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// Racing agent proposals cannot pass the open-proposal cap: the count runs inside the
// insert's serializable transaction.
func TestPostgresAgentProposalCapHoldsUnderRace(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < mmProposedMax-1; i++ {
		insertPGRecords(t, p, colMentalModels, Record{ID: "mm_seed" + ID(), Scope: scopeA, Kind: mentalModelKind, Status: obsProposed, Revision: 1,
			CreatedAt: now, UpdatedAt: now, MentalModel: &MentalModelData{Title: "seed", Query: "seed", Refresh: MentalModelRefresh{Mode: mmRefreshManual}}})
	}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.svc.CreateMentalModel(testCtx, f.agent, MentalModelInput{Scope: scopeA, Title: "race", Query: "race", Refresh: MentalModelRefresh{Mode: mmRefreshManual}})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	open, err := p.ListRecordsByStatus(testCtx, scopeA, colMentalModels, obsProposed, 100)
	if err != nil || ok != 1 || len(open) != mmProposedMax {
		t.Fatalf("created %d, open %d, %v", ok, len(open), err)
	}
}
