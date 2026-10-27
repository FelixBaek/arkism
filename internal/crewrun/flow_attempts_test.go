package crewrun

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func (f *childStoreFake) ReserveFlowAttempt(_ context.Context, run Run, in FlowAttemptRequest) (FlowAttemptReservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, in)
	if f.reserveErr != nil {
		return FlowAttemptReservation{}, f.reserveErr
	}
	key := fmt.Sprintf("%s/%s/%d/%s", run.ID, in.NodeID, in.Attempt, in.CallKey)
	if old, ok := f.attempts[key]; ok {
		old.DispatchAllowed = false
		return old, nil
	}
	if f.attempts == nil {
		f.attempts, f.settlements = map[string]FlowAttemptReservation{}, map[string]FlowAttemptSettlement{}
	}
	out := FlowAttemptReservation{ID: key, Status: "reserved", DispatchAllowed: true}
	f.attempts[key] = out
	return out, nil
}

func (f *childStoreFake) SettleFlowAttempt(_ context.Context, _ Run, id string, in FlowAttemptSettlement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settleErr != nil {
		return f.settleErr
	}
	f.settlements[id] = in
	return nil
}

func attemptFixture(t *testing.T) (*Engine, *childStoreFake, context.Context) {
	t.Helper()
	fs := &childStoreFake{fakeStore: newFakeStore()}
	e := testEngine(t, fs.fakeStore)
	e.Store = fs
	ctx := context.WithValue(context.Background(), flowCallScopeKey{}, flowCallScope{
		run: Run{ID: "run", LeaseToken: "owned"}, nodeID: "node", attempt: 1,
	})
	return e, fs, ctx
}

func TestFlowAttemptReservesBeforeDispatchAndBlocksDuplicate(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	calls := 0
	dispatch := func() (aiAnswer, error) {
		calls++
		if len(fs.attempts) != 1 {
			t.Fatal("dispatch preceded reservation")
		}
		cost := .25
		return aiAnswer{OK: true, Output: "answer", Usage: &aiUsage{InputTokens: 10, OutputTokens: 20, CostUSD: &cost, Estimated: true}}, nil
	}
	if _, err := e.budgetedAI(ctx, aiSpec{Model: "test-model"}, 0, dispatch); err != nil {
		t.Fatal(err)
	}
	if _, err := e.budgetedAI(ctx, aiSpec{Model: "test-model"}, 0, dispatch); err == nil || calls != 1 {
		t.Fatal("duplicate dispatch", calls, err)
	}
	for _, settlement := range fs.settlements {
		if settlement.Status != "succeeded" || !settlement.UsageKnown || !settlement.Estimated || settlement.InputTokens != 10 || settlement.OutputTokens != 20 || *settlement.CostUSD != .25 {
			t.Fatal("usage not preserved", settlement)
		}
	}
	if req := fs.requests[0]; req.NodeID != "node" || req.Attempt != 1 || req.CallKey != "main/retry0" || req.MaxInputTokens != 0 || req.MaxOutputTokens != 0 {
		t.Fatal("identity or unsupported hard bound misrepresented", req)
	}
}

func TestFlowAttemptMissingUsageAndUncertainDispatchStayUnknown(t *testing.T) {
	for _, failure := range []bool{false, true} {
		e, fs, ctx := attemptFixture(t)
		_, err := e.budgetedAI(ctx, aiSpec{Model: "test-model"}, 0, func() (aiAnswer, error) {
			if failure {
				return aiAnswer{}, context.Canceled
			}
			return aiAnswer{OK: true, Output: "answer"}, nil
		})
		if failure != (err != nil) {
			t.Fatal(err)
		}
		for _, settled := range fs.settlements {
			want := "succeeded"
			if failure {
				want = "unknown"
			}
			if settled.Status != want || settled.UsageKnown || settled.CostUSD != nil {
				t.Fatal("missing usage was invented as known zero", settled)
			}
		}
	}
}

func TestFlowAttemptBudgetRefusalAndLostReceiptCannotRetry(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	fs.reserveErr = &APIError{409, "flow_budget_limit"}
	called := false
	dispatch := func() (aiAnswer, error) { called = true; return aiAnswer{OK: true}, nil }
	if _, err := e.budgetedAI(ctx, aiSpec{}, 0, dispatch); err == nil || called || !flowCallControl(err) {
		t.Fatal("budget refusal dispatched work", err)
	}
	fs.reserveErr, fs.settleErr = nil, errors.New("receipt response lost")
	if _, err := e.budgetedAI(ctx, aiSpec{}, 0, dispatch); !errors.Is(err, errFlowAttemptUnavailable) {
		t.Fatal("lost receipt treated as completed", err)
	}
	called = false
	if _, err := e.budgetedAI(ctx, aiSpec{}, 0, dispatch); err == nil || called {
		t.Fatal("lost receipt dispatched again", err)
	}
}

func TestFlowAttemptLabelsSeparatePlanWorkersRetriesAndFallback(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	for _, label := range []string{"plan", "worker0", "worker1", "synthesis", "turn1-2", "turn1-2/fallback"} {
		for retry := 0; retry < 2; retry++ {
			if _, err := e.budgetedAI(withCallLabel(ctx, label), aiSpec{}, retry, func() (aiAnswer, error) { return aiAnswer{OK: true}, nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(fs.attempts) != 12 || len(fs.settlements) != 12 {
		t.Fatal("distinct calls collided", len(fs.attempts), len(fs.settlements))
	}
}

func TestFlowAttemptWrapsActualRunnerRetriesAndCollaboration(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	log := fakeAI(t, e)
	if _, err := e.callAI(ctx, aiSpec{Task: "FAIL", Model: "test-model"}, 5*time.Second, 1); err == nil {
		t.Fatal("expected local fake runner failure")
	}
	if len(fs.attempts) != 2 || len(specs(t, log)) != 2 {
		t.Fatal("retry calls escaped accounting", len(fs.attempts), len(specs(t, log)))
	}
	ctx = withCallLabel(ctx, "collaboration")
	c := map[string]string{"workers": `[{"name":"조사원"},{"name":"작성자"}]`, "concurrency": "2"}
	if _, err := e.runCollab(ctx, c, aiSpec{Task: "research", Model: "test-model"}, 5*time.Second, 0, nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(fs.attempts) != 7 || len(fs.settlements) != 7 || len(specs(t, log)) != 7 {
		t.Fatal("plan, workers, or synthesis escaped accounting", len(fs.attempts), len(fs.settlements), len(specs(t, log)))
	}
}
