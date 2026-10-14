package memory

import (
	"fmt"
	"sync"
	"testing"
)

func TestPostgresModelBudgetConcurrentReservations(t *testing.T) {
	for _, limit := range []string{"monthly", "general"} {
		t.Run(limit, func(t *testing.T) { checkConcurrentBudgetSoftTarget(t, limit) })
	}
}
func checkConcurrentBudgetSoftTarget(t *testing.T, limit string) {
	p := isolatedPostgres(t)
	f, in := modelBudgetFixture(t)
	f.svc.Repo = p
	if limit == "monthly" {
		f.svc.ModelBudgetPolicy.MonthlyPicoUSD = 1_400_000_000
	} else {
		f.svc.ModelBudgetPolicy.GeneralPicoUSD = 1_400_000_000
	}
	f.svc.ModelBudgetPolicy.DailyPicoUSD = 0
	f.svc.ModelBudgetPolicy.DailySoftTargetPicoUSD = 1
	var wg sync.WaitGroup
	var mu sync.Mutex
	dispatches := 0
	// Different operations compete for a shared allowance; no per-agent ledger.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request := in
			request.OperationID = fmt.Sprintf("op-%d", i)
			r, e := f.svc.ReserveModelBudget(testCtx, "budget", request)
			if e != nil {
				if e != ErrModelBudget {
					t.Error(e)
				}
				return
			}
			if r.DispatchAllowed {
				mu.Lock()
				dispatches++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if dispatches != 2 {
		t.Fatal("allowance overflow or lost reservations", dispatches)
	}
	status, e := f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	if e != nil || status.MonthlyCommitted != 1_400_000_000 || status.Outstanding != 1_400_000_000 || !status.DailySoftTargetExceeded {
		t.Fatal(status, e)
	}
}

func TestPostgresModelBudgetSameOperationAndSettlement(t *testing.T) {
	p := isolatedPostgres(t)
	f, in := modelBudgetFixture(t)
	f.svc.Repo = p
	var wg sync.WaitGroup
	var mu sync.Mutex
	dispatches := 0
	var reservation ModelBudgetReservation
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.ReserveModelBudget(testCtx, "budget", in)
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			reservation = r
			if r.DispatchAllowed {
				dispatches++
			}
		}()
	}
	wg.Wait()
	if dispatches != 1 {
		t.Fatal("duplicate dispatch", dispatches)
	}
	inSettle := ModelBudgetSettleInput{Scope: scopeA, ReservationID: reservation.ReservationID, Status: "completed", InputTokens: 200, OutputTokens: 100}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := f.svc.SettleModelBudget(testCtx, "budget", inSettle); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	rows := pgCollection(t, p, colProviderUsage)
	if len(rows) != 1 || rows[0].Cost.EstimatedCostPicoUSD != 70_000_000 {
		t.Fatal("duplicate/incorrect billing")
	}
	status, e := f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	if e != nil || status.Outstanding != 0 || status.MonthlyCommitted != 70_000_000 {
		t.Fatal(status, e)
	}
}
