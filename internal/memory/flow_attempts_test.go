package memory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

func flowAttemptFixture(t *testing.T) (*fixture, Record, FlowAttemptInput) {
	t.Helper()
	f, _, start := flowFixture(t)
	return startFlowAttemptFixture(t, f, start)
}

func startFlowAttemptFixture(t *testing.T, f *fixture, start CrewRunInput) (*fixture, Record, FlowAttemptInput) {
	t.Helper()
	r, e := f.svc.CreateCrewRun(testCtx, tokEngine, start)
	if e != nil {
		t.Fatal(e)
	}
	lease := leaseFlow(t, f, r.ID)
	st := *r.CrewRun
	st.Nodes["research"] = CrewRunNode{Status: "running", Runs: 1}
	r, e = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, LeaseToken: lease, Run: st})
	if e != nil {
		t.Fatal(e)
	}
	return f, r, FlowAttemptInput{Scope: scopeA, LeaseToken: lease, NodeID: "research", Attempt: 1, CallKey: "main/retry0", Model: "gpt-6"}
}

func setAttemptPolicy(t *testing.T, f *fixture, run Record, policy FlowPolicy) {
	t.Helper()
	current, e := f.svc.Repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if e != nil {
		t.Fatal(e)
	}
	st := *current.CrewRun
	st.Policy = &policy
	current.CrewRun = &st
	if e = f.svc.Repo.Replace(testCtx, colCrewRuns, current, current.Revision); e != nil {
		t.Fatal(e)
	}
}

func attemptReserve(t *testing.T, f *fixture, id string, in FlowAttemptInput) FlowAttemptReservation {
	t.Helper()
	r, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, id, in)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestFlowAttemptReserveIsAtomicAndNeverRedispatches(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	first := attemptReserve(t, f, run.ID, in)
	second := attemptReserve(t, f, run.ID, in)
	if !first.DispatchAllowed || second.DispatchAllowed || first.ID != second.ID {
		t.Fatal(first, second)
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if root.FlowBudget.Attempts != 1 || len(root.FlowBudget.Pending) != 1 {
		t.Fatal(root.FlowBudget)
	}
	changed := in
	changed.Model = "different-model"
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, changed); !errors.Is(e, ErrConflict) {
		t.Fatal("changed request reused identity", e)
	}
	changed = in
	changed.Attempt = 2
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, changed); !errors.Is(e, ErrConflict) {
		t.Fatal("wrong node attempt accepted", e)
	}
}

func TestFlowAttemptSettlementKeepsActualEstimateAndUnknownSeparate(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	in.MaxInputTokens, in.MaxOutputTokens = 100, 100
	a := attemptReserve(t, f, run.ID, in)
	cost := .02
	u := FlowAttemptUsage{Status: "succeeded", UsageKnown: true, InputTokens: 10, OutputTokens: 20, CostUSD: &cost}
	settle := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: u}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, settle); e != nil {
		t.Fatal(e)
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if root.FlowBudget.CommittedTokens != 30 || root.FlowBudget.ReservedTokens != 0 || root.FlowBudget.CommittedCostUSD != cost || len(root.FlowBudget.Pending) != 0 {
		t.Fatal(root.FlowBudget)
	}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, settle); e != nil {
		t.Fatal(e)
	}
	unchanged, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if unchanged.Revision != root.Revision {
		t.Fatal("duplicate settlement counted twice")
	}
	settle.OutputTokens++
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, settle); !errors.Is(e, ErrConflict) {
		t.Fatal("changed receipt accepted", e)
	}
	in.CallKey = "worker1/retry0"
	a = attemptReserve(t, f, run.ID, in)
	settle.FlowAttemptUsage = FlowAttemptUsage{Status: "succeeded", UsageKnown: true, Estimated: true, InputTokens: 5, OutputTokens: 6}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, settle); e != nil {
		t.Fatal(e)
	}
	root, _ = f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if root.FlowBudget.CommittedTokens != 30 || root.FlowBudget.EstimatedTokens != 11 || root.FlowBudget.ReservedTokens != 200 || root.FlowBudget.UnknownCost != 1 || root.FlowBudget.EstimatedCostUSD <= 0 {
		t.Fatal(root.FlowBudget)
	}
}

func TestFlowAttemptUnknownBlocksRetryAndFreshAttempt(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	a := attemptReserve(t, f, run.ID, in)
	settle := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "unknown"}}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, settle); e != nil {
		t.Fatal(e)
	}
	if retry := attemptReserve(t, f, run.ID, in); retry.DispatchAllowed || retry.Status != "unknown" {
		t.Fatal(retry)
	}
	in.CallKey = "main/retry1"
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowAttemptReplay) {
		t.Fatal("unknown retry dispatched", e)
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	st := *root.CrewRun
	st.Nodes["research"] = CrewRunNode{Status: "running", Runs: 2}
	root.CrewRun = &st
	if e := f.repo.Replace(testCtx, colCrewRuns, root, root.Revision); e != nil {
		t.Fatal(e)
	}
	in.Attempt = 2
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowAttemptReplay) {
		t.Fatal("new attempt bypassed unresolved call", e)
	}
}

func TestFlowAttemptStrictBudgetsRefuseUnsupportedBoundsAndUnpricedModels(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	policy := FlowPolicy{MaxDepth: 3, MaxChildren: 32, MaxConcurrent: 4, MaxTokens: 100}
	setAttemptPolicy(t, f, run, policy)
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowBudgetUnsupported) {
		t.Fatal("unbounded runtime accepted", e)
	}
	in.MaxInputTokens, in.MaxOutputTokens = 60, 50
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowBudgetLimit) {
		t.Fatal("token reservation exceeded", e)
	}
	policy.MaxCostUSD = .1
	setAttemptPolicy(t, f, run, policy)
	in.MaxInputTokens, in.MaxOutputTokens, in.Model = 10, 10, "unknown-model"
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowBudgetUnsupported) {
		t.Fatal("unpriced model accepted", e)
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if root.FlowBudget != nil {
		t.Fatal("refusal consumed a reservation")
	}
}

func TestFlowAttemptLeaseScopeAndPrivacyBoundaries(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	wrong := in
	wrong.LeaseToken = "other-executor"
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, wrong); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokPipeline, run.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	a := attemptReserve(t, f, run.ID, in)
	settle := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: wrong.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "unknown"}}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, settle); !errors.Is(e, ErrConflict) {
		t.Fatal("stale writer settled", e)
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	root.PrivacyQuarantined = true
	if e := f.repo.Replace(testCtx, colCrewRuns, root, root.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, ErrNotFound) {
		t.Fatal("private replay leaked", e)
	}
}

func TestFlowAttemptCancellationStillAcceptsAccountingButNotDispatch(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	a := attemptReserve(t, f, run.ID, in)
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	st := *root.CrewRun
	st.CancelRequested = true
	root.CrewRun = &st
	if e := f.repo.Replace(testCtx, colCrewRuns, root, root.Revision); e != nil {
		t.Fatal(e)
	}
	in.CallKey = "new/retry0"
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	u := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "failed", UsageKnown: true, InputTokens: 4}}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, u); e != nil {
		t.Fatal("lost usage after cancel", e)
	}
}

func TestFlowAttemptExpiredDeadlineStopsDispatchButRetainsSettlement(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	a := attemptReserve(t, f, run.ID, in)
	if a.CostKnown {
		t.Fatal("zero bounds claimed a known reservation")
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	st := *root.CrewRun
	expired := time.Now().Add(-time.Second)
	st.Deadline = &expired
	root.CrewRun = &st
	if e := f.repo.Replace(testCtx, colCrewRuns, root, root.Revision); e != nil {
		t.Fatal(e)
	}
	in.CallKey = "worker/retry0"
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowTimeLimit) {
		t.Fatal("dispatch past root deadline", e)
	}
	u := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "unknown"}}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, u); e != nil {
		t.Fatal(e)
	}
}

func TestFlowAttemptHTTPClientContractAndNoBodies(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(tokenFile, []byte(tokEngine), 0600); e != nil {
		t.Fatal(e)
	}
	c := &crewrun.Client{BaseURL: "http://local.test", TokenFile: tokenFile, Scope: crewrun.Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID}, HTTP: &http.Client{Transport: flowHandlerTransport{Handler(f.svc)}}}
	leaseRun := crewrun.Run{ID: run.ID, LeaseToken: in.LeaseToken}
	a, e := c.ReserveFlowAttempt(testCtx, leaseRun, crewrun.FlowAttemptRequest{NodeID: in.NodeID, Attempt: in.Attempt, CallKey: in.CallKey, Model: in.Model})
	if e != nil || !a.DispatchAllowed {
		t.Fatal(a, e)
	}
	if e = c.SettleFlowAttempt(testCtx, leaseRun, a.ID, crewrun.FlowAttemptSettlement{Status: "succeeded", UsageKnown: true, Estimated: true, InputTokens: 2, OutputTokens: 3}); e != nil {
		t.Fatal(e)
	}
	record, _ := f.repo.Get(testCtx, colJobs, scopeA, a.ID)
	if record.FlowAttempt.Settlement == nil || !record.FlowAttempt.Settlement.Estimated || record.Text != "" {
		t.Fatal(record.FlowAttempt)
	}
	h := flowHTTPHarness{t, Handler(f.svc)}
	raw := h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, tokEngine, nil, 200, nil)
	if strings.Contains(string(raw), in.LeaseToken) {
		t.Fatal("lease token leaked")
	}
}

type failingAttemptRepo struct{ Repository }
type failingAttemptTx struct{ Tx }

func (r failingAttemptRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, failingAttemptTx{tx}) })
}
func (tx failingAttemptTx) Replace(ctx context.Context, col string, r Record, revision int64) error {
	if col == colCrewRuns && r.FlowBudget != nil {
		return ErrUnavailable
	}
	return tx.Tx.Replace(ctx, col, r, revision)
}

func TestFlowAttemptJobRollsBackWhenRootAccountingFails(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	f.svc.Repo = failingAttemptRepo{f.repo}
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	id, _ := flowAttemptIdentity(run.ID, in)
	if _, e := f.repo.Get(testCtx, colJobs, scopeA, id); !errors.Is(e, ErrNotFound) {
		t.Fatal("orphan dispatch permit", e)
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if root.FlowBudget != nil {
		t.Fatal("partial accounting")
	}
}

func TestFlowAttemptConcurrentReservationHasOneDispatchPermit(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	var wg sync.WaitGroup
	results := make(chan FlowAttemptReservation, 4)
	errorsCh := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in)
			results <- r
			errorsCh <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for e := range errorsCh {
		if e != nil {
			t.Fatal(e)
		}
	}
	dispatched := 0
	for r := range results {
		if r.DispatchAllowed {
			dispatched++
		}
	}
	if dispatched != 1 {
		t.Fatal("permits", dispatched)
	}
	root, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if root.FlowBudget.Attempts != 1 {
		t.Fatal(root.FlowBudget)
	}
}

func TestFlowAttemptConcurrentDistinctCallsRespectRootCapacity(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	setAttemptPolicy(t, f, run, FlowPolicy{MaxDepth: 3, MaxChildren: 32, MaxConcurrent: 1})
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := in
			req.CallKey = fmt.Sprintf("worker%d/retry0", i)
			_, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, req)
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	allowed := 0
	for e := range errs {
		if e == nil {
			allowed++
		} else if !errors.Is(e, errFlowConcurrencyLimit) {
			t.Fatal(e)
		}
	}
	if allowed != 1 {
		t.Fatal("capacity permits", allowed)
	}
}

func TestFlowAttemptLateSettlementPreservesTerminalDeliveryRevision(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	a := attemptReserve(t, f, run.ID, in)
	current, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	state := *current.CrewRun
	state.Status, state.Summary = "failed", "model outcome could not be reused"
	finished, e := f.svc.UpdateCrewRun(testCtx, tokEngine, run.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: current.Revision, LeaseToken: in.LeaseToken, Run: state})
	if e != nil {
		t.Fatal(e)
	}
	u := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "succeeded", UsageKnown: true, InputTokens: 10, OutputTokens: 20}}
	if _, e = f.svc.SettleFlowAttempt(testCtx, tokEngine, run.ID, a.ID, u); e != nil {
		t.Fatal(e)
	}
	after, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	if after.Revision <= finished.Revision || after.CrewRun.Summary != finished.CrewRun.Summary {
		t.Fatal("late accounting rewrote the result")
	}
	outbox, _ := f.repo.Get(testCtx, colJobs, scopeA, flowDeliveryID(run.ID))
	if outbox.FlowDelivery.ResultRevision != finished.Revision {
		t.Fatal("delivery identity changed")
	}
	claimed, e := f.svc.ChangeFlowDelivery(testCtx, tokEngine, outbox.ID, FlowDeliveryChange{Scope: scopeA, ExpectedRevision: outbox.Revision, Action: "claim", LeaseToken: "late-accounting-delivery-token", ResultHash: strings.Repeat("a", 64)})
	if e != nil || claimed.Delivery.Status != "sending" {
		t.Fatal("accounting update stranded the delivery", e)
	}
}

func TestFlowAttemptUnknownOutcomeDoesNotHoldConcurrency(t *testing.T) {
	p := &FlowPolicy{MaxConcurrent: 1}
	a := FlowAttempt{RunID: "run", NodeID: "next", Attempt: 1}
	b := &FlowBudget{Pending: map[string]FlowAttemptPending{"old": {RunID: "run", NodeID: "lost", Attempt: 1, Unknown: true}}}
	if e := checkFlowAttemptBudget(b, p, a); e != nil {
		t.Fatal("an unknown outcome kept a concurrency slot", e)
	}
	b.Pending["old"] = FlowAttemptPending{RunID: "run", NodeID: "lost", Attempt: 1}
	if e := checkFlowAttemptBudget(b, p, a); !errors.Is(e, errFlowConcurrencyLimit) {
		t.Fatal("an in-flight call did not hold its slot", e)
	}
}

func TestFlowAttemptLegacyRunFailsClosed(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	stored, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	st := *stored.CrewRun
	st.RootID, st.Policy, st.Definitions = "", nil, nil // saved before root accounting existed
	stored.CrewRun = &st
	if e := f.repo.Replace(testCtx, colCrewRuns, stored, stored.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowLegacyRun) {
		t.Fatal("legacy run was not refused with a terminal code", e)
	}
}
