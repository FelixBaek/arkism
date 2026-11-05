package memory

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// Racing capped reservations cannot pass the day's cap on Postgres: the scope's ledger
// lock makes counting and inserting one step even across processes.
func TestPostgresProviderCallReservationsHoldTheCap(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	c := callCap{prefix: "wiki_", limit: 5}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = f.svc.reserveProviderCalls(testCtx, scopeA, "wiki_write", wikiProvider, "m", 2, c)
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrModelBudget) {
			t.Fatal(err)
		}
	}
	calls, err := f.svc.dailyModelCalls(testCtx, scopeA, "wiki_", time.Now())
	if err != nil || ok != 2 || calls != 4 {
		t.Fatalf("reserved %d batches, %d calls, %v", ok, calls, err)
	}
}

func TestPostgresWaitingExtractBatchesListsOnlyWaitingBatches(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	job := func(id, status string, batch *ExtractBatch) Record {
		return Record{ID: id, Scope: scopeA, Kind: fileExtractJobKind, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now,
			FileExtract: &FileExtractJob{Label: "doc", Batch: batch}}
	}
	b := &ExtractBatch{ID: "batch_1", SubmittedAt: now, Reserved: map[string]string{"c0": "row"}}
	insertPGRecords(t, p, colJobs, job("waiting", JobStatusRetry, b), job("plain", JobStatusRetry, nil), job("done", JobStatusDone, b))
	got, err := p.WaitingExtractBatches(testCtx, 10)
	if err != nil || len(got) != 1 || got[0].ID != "waiting" || got[0].FileExtract.Batch.Reserved["c0"] != "row" {
		t.Fatalf("%+v %v", got, err)
	}
}
