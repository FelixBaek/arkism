package memory

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestFlowAttemptPostgresConcurrentDispatchAndAtomicRootAccounting(t *testing.T) {
	f, p, start := postgresFlowFixture(t)
	f, run, in := startFlowAttemptFixture(t, f, start)
	setAttemptPolicy(t, f, run, FlowPolicy{MaxDepth: 3, MaxChildren: 32, MaxConcurrent: 1})
	var wg sync.WaitGroup
	type response struct {
		r FlowAttemptReservation
		e error
	}
	results := make(chan response, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in)
			results <- response{r, e}
		}()
	}
	wg.Wait()
	close(results)
	dispatches := 0
	var id string
	for got := range results {
		if got.e != nil {
			t.Fatal(got.e)
		}
		if got.r.DispatchAllowed {
			dispatches++
		}
		id = got.r.ID
	}
	if dispatches != 1 {
		t.Fatal("dispatch permits", dispatches)
	}
	root, e := p.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if e != nil || root.FlowBudget == nil || root.FlowBudget.Attempts != 1 || len(root.FlowBudget.Pending) != 1 {
		t.Fatal(root.FlowBudget, e)
	}
	settle := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "succeeded", UsageKnown: true, InputTokens: 2, OutputTokens: 3}}
	if _, e = f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, id, settle); e != nil {
		t.Fatal(e)
	}
	results = make(chan response, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := in
			req.CallKey = fmt.Sprintf("worker%d/retry0", i)
			r, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, req)
			results <- response{r, e}
		}(i)
	}
	wg.Wait()
	close(results)
	dispatches = 0
	for got := range results {
		if got.e == nil && got.r.DispatchAllowed {
			dispatches++
		} else if !errors.Is(got.e, errFlowConcurrencyLimit) {
			t.Fatal(got.e)
		}
	}
	if dispatches != 1 {
		t.Fatal("capacity oversubscribed", dispatches)
	}
	root, e = p.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if e != nil || root.FlowBudget.Attempts != 2 || root.FlowBudget.CommittedTokens != 5 {
		t.Fatal(root.FlowBudget, e)
	}
}
