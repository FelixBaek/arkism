package crewrun

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func runtimeTestContext() (context.Context, *AttemptObserver) {
	o := NewAttemptObserver(nil)
	return WithAttemptObserver(observerTestContext(), o), o
}

func TestObservationRuntimeOffPreservesDispatch(t *testing.T) {
	e := &Engine{}
	ctx := context.Background()
	expected := aiAnswer{OK: true, Output: "same", Usage: &aiUsage{InputTokens: 11}}
	sentinel := errors.New("same failure")
	calls := 0
	answer, err := e.observationDispatch(ctx, aiSpec{}, 0, nil, func(actual context.Context) (aiAnswer, error) {
		calls++
		if actual != ctx || actual.Value(observationRequestKey{}) != nil {
			t.Fatal("off context changed")
		}
		return expected, sentinel
	})
	if calls != 1 || !reflect.DeepEqual(answer, expected) || err != sentinel {
		t.Fatal("off behavior changed")
	}
	if ObservationEvidenceFromContext(ctx).ParentLinks != nil || UnallocatedUsageFromContext(ctx) != nil {
		t.Fatal("off allocated runtime")
	}
}

func TestObservationRuntimeDirectFailureAndActualRetryParent(t *testing.T) {
	ctx, o := runtimeTestContext()
	ctx = withDirectObservation(withCallLabel(ctx, "worker0"))
	e := &Engine{}
	usage := observerKnownUsage()
	first, err := e.observationDispatch(ctx, aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) {
		return aiAnswer{Output: "partial", observationUsage: &usage}, context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) || first.Output != "partial" {
		t.Fatal("business answer changed")
	}
	nextCtx := observationAnswerParent(ctx, first, false)
	second, err := e.observationDispatch(nextCtx, aiSpec{}, 1, nil, func(context.Context) (aiAnswer, error) { return aiAnswer{OK: true, observationUsage: &usage}, nil })
	if err != nil {
		t.Fatal(err)
	}
	snapshot := o.Snapshot()
	if len(snapshot.Latest) != 2 || len(snapshot.Records) != 4 {
		t.Fatalf("attempts lost: %+v", snapshot.Issues)
	}
	firstRecord, ok := observationLatest(ctx, first.observationID)
	if !ok || firstRecord.Status != "timeout" || firstRecord.Usage.DurationBasis != "observed_monotonic" {
		t.Fatal("timeout evidence missing")
	}
	secondRecord, ok := observationLatest(ctx, second.observationID)
	if !ok || secondRecord.Identity.ParentAttemptID == nil || *secondRecord.Identity.ParentAttemptID != first.observationID || secondRecord.Identity.RetryIndex != 1 || secondRecord.Identity.Phase != "worker" {
		t.Fatal("retry parent missing")
	}
	if _, err := QualifyObservationLedger(snapshot, ObservationEvidenceFromContext(ctx)); err != nil {
		t.Fatal(err)
	}
	if snapshot.ProviderLeafTotals.Cost.Value == nil || *snapshot.ProviderLeafTotals.Cost.Value != .5 {
		t.Fatal("paid failed call lost")
	}
}

func TestObservationRuntimeFallbackUsesFailedCallKey(t *testing.T) {
	ctx, _ := runtimeTestContext()
	e := &Engine{}
	first, _ := e.observationDispatch(withDirectObservation(withCallLabel(ctx, "plan")), aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) { return aiAnswer{}, errors.New("failed") })
	fallback := withCallLabel(observationAnswerParent(ctx, first, true), "fallback")
	second, _ := e.observationDispatch(withDirectObservation(fallback), aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) { return aiAnswer{}, nil })
	record, ok := observationLatest(ctx, second.observationID)
	if !ok || record.Identity.FallbackParentCallKey == nil || *record.Identity.FallbackParentCallKey != "plan/retry0" || record.Identity.ParentAttemptID == nil || *record.Identity.ParentAttemptID != first.observationID {
		t.Fatal("fallback causality absent")
	}
}

func TestObservationRuntimeTransportKeepsFailedLeafAndCleanup(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "closed"}[closed], func(t *testing.T) {
			ctx, o := runtimeTestContext()
			e := &Engine{}
			path := ""
			answer, err := e.observationDispatch(ctx, aiSpec{}, 0, observationString("outer-reservation"), func(callCtx context.Context) (aiAnswer, error) {
				req, ok := callCtx.Value(observationRequestKey{}).(ObservationTransportRequest)
				if !ok {
					t.Fatal("transport not provided")
				}
				path = req.JournalDir
				writer, openErr := OpenObservationJournalWriter(req)
				if openErr != nil {
					t.Fatal(openErr)
				}
				leaf, proof := transportTestLeaf(t, req.Parent, 0, .75, "provider_reported")
				if err := writer.Begin(leaf.Identity.AttemptID); err != nil {
					t.Fatal(err)
				}
				if err := writer.Record(leaf, proof); err != nil {
					t.Fatal(err)
				}
				if closed {
					if err := writer.CloseBoundary(); err != nil {
						t.Fatal(err)
					}
				}
				return aiAnswer{Output: "business partial"}, errors.New("killed")
			})
			if err == nil || answer.Output != "business partial" {
				t.Fatal("callback error changed")
			}
			snap := o.Snapshot()
			if len(snap.Latest) != 2 || snap.ProviderLeafTotals.Cost.Value == nil || *snap.ProviderLeafTotals.Cost.Value != .75 {
				t.Fatalf("failed paid leaf lost: %+v", snap)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("own journal not removed", err)
			}
			parent, ok := observationLatest(ctx, answer.observationID)
			if !ok || parent.Status != "failed" || parent.Identity.CanonicalAttemptID == nil || *parent.Identity.CanonicalAttemptID != "outer-reservation" {
				t.Fatal("outer reservation lost")
			}
			expected := "partial"
			if closed {
				expected = "complete"
			}
			if parent.LeafCoverage != expected {
				t.Fatalf("coverage %s", parent.LeafCoverage)
			}
			if closed {
				if _, err := QualifyObservationLedger(snap, ObservationEvidenceFromContext(ctx)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestObservationRuntimeLegacyProvenanceRemainsUnallocated(t *testing.T) {
	ctx, o := runtimeTestContext()
	cost := .125
	_, err := (&Engine{}).observationDispatch(withDirectObservation(ctx), aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) {
		return aiAnswer{Usage: &aiUsage{InputTokens: 3, OutputTokens: 4, CostUSD: &cost}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	receipts := UnallocatedUsageFromContext(ctx)
	if len(receipts) != 1 || receipts[0].CostUSD == nil || *receipts[0].CostUSD != cost || receipts[0].InputTokens != 3 {
		t.Fatal("numeric evidence lost")
	}
	*receipts[0].CostUSD = 999
	if *UnallocatedUsageFromContext(ctx)[0].CostUSD != cost {
		t.Fatal("diagnostic view aliases state")
	}
	if o.Snapshot().ProviderLeafTotals.Cost.Value != nil {
		t.Fatal("unknown provenance promoted")
	}
}

func TestObservationRuntimeEstimatedZeroAndAbsentTokens(t *testing.T) {
	ctx, _ := runtimeTestContext()
	zero := 0.0
	usage := observationAnswerUsage(ctx, aiAnswer{Usage: &aiUsage{Estimated: true, CostUSD: &zero}})
	if usage.CostUSD == nil || *usage.CostUSD != 0 || usage.CostBasis != "estimated" || usage.InputTokens != nil || usage.InputBasis != "unknown" {
		t.Fatal("zero and absent confused")
	}
}

func TestObservationRuntimePendingDeliveryIsIdempotent(t *testing.T) {
	ctx, o := runtimeTestContext()
	done := observationDeliveryPending(ctx)
	if !slices.Contains(o.Snapshot().Issues, "sink_pending") {
		t.Fatal("pending delivery hidden")
	}
	done()
	done()
	if slices.Contains(o.Snapshot().Issues, "sink_pending") {
		t.Fatal("completed delivery remained pending")
	}
}

func TestObservationRuntimeInvalidIdentityStillDispatches(t *testing.T) {
	o := NewAttemptObserver(nil)
	ctx := WithAttemptObserver(context.Background(), o)
	calls := 0
	_, err := (&Engine{}).observationDispatch(ctx, aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) { calls++; return aiAnswer{}, nil })
	if err != nil || calls != 1 || len(o.Snapshot().Records) != 0 || !slices.Contains(o.Snapshot().Issues, "identity_unavailable") {
		t.Fatal("missing facts changed business")
	}
}

func TestObservationRuntimeRefusalAndReplayDoNotInventFreeHistory(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "replay"}[replay], func(t *testing.T) {
			ctx, o := runtimeTestContext()
			(&Engine{}).observationRefused(ctx, aiSpec{}, 0, observationString("reservation"), replay, nil)
			rows := o.Snapshot().Latest
			if len(rows) != 1 || rows[0].Usage.CostUSD != nil {
				t.Fatal("refusal cost invented")
			}
			if replay && (rows[0].Status != "unknown" || rows[0].DispatchState != "unknown") {
				t.Fatal("replay classified free")
			}
			if !replay && rows[0].Status != "cancelled_before_dispatch" {
				t.Fatal("refusal classification")
			}
		})
	}
}

func TestObservationRuntimeConcurrentIndependentCalls(t *testing.T) {
	ctx, o := runtimeTestContext()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := string(rune('a' + i))
			usage := observerKnownUsage()
			_, _ = (&Engine{}).observationDispatch(withDirectObservation(withCallLabel(ctx, label)), aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) { return aiAnswer{observationUsage: &usage}, nil })
		}(i)
	}
	wg.Wait()
	snap := o.Snapshot()
	if len(snap.Latest) != 12 || snap.ProviderLeafTotals.Cost.Value == nil || *snap.ProviderLeafTotals.Cost.Value != 3 {
		t.Fatalf("concurrent capture lost calls: %d %+v", len(snap.Latest), snap.Issues)
	}
}

func TestObservationFlowSettlementFailureKeepsPaidAttemptAndBlocksReplay(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	ac, _ := AttemptContextFrom(observerTestContext())
	o := NewAttemptObserver(nil)
	ctx = withDirectObservation(WithAttemptObserver(WithAttemptContext(ctx, ac), o))
	fs.settleErr = errors.New("synthetic lost receipt")
	calls := 0
	usage := observerKnownUsage()
	answer, err := e.budgetedAIContext(ctx, aiSpec{}, 0, func(context.Context) (aiAnswer, error) {
		calls++
		return aiAnswer{OK: true, Output: "result", observationUsage: &usage}, nil
	})
	if !errors.Is(err, errFlowAttemptUnavailable) || answer.Output != "" || answer.observationID == "" || calls != 1 {
		t.Fatal("settlement control changed", err)
	}
	rows := o.Snapshot().Latest
	if len(rows) != 1 || rows[0].Identity.CanonicalAttemptID == nil || rows[0].Usage.CostUSD == nil || *rows[0].Usage.CostUSD != .25 {
		t.Fatal("paid attempt lost with settlement response")
	}
	_, err = e.budgetedAIContext(ctx, aiSpec{}, 0, func(context.Context) (aiAnswer, error) { calls++; return aiAnswer{}, nil })
	if err == nil || calls != 1 || len(o.Snapshot().Latest) != 1 {
		t.Fatal("ambiguous settlement redispatched or overwritten")
	}
}

func TestObservationFlowRefusalRetainsNoDispatchEvidence(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	ac, _ := AttemptContextFrom(observerTestContext())
	o := NewAttemptObserver(nil)
	ctx = WithAttemptObserver(WithAttemptContext(ctx, ac), o)
	fs.reserveErr = &APIError{409, "flow_budget_limit"}
	_, err := e.budgetedAIContext(ctx, aiSpec{}, 0, func(context.Context) (aiAnswer, error) { t.Fatal("refusal dispatched"); return aiAnswer{}, nil })
	rows := o.Snapshot().Latest
	if !flowCallControl(err) || len(rows) != 1 || rows[0].Status != "cancelled_before_dispatch" || rows[0].DispatchState != "not_dispatched" || rows[0].Usage.CostUSD != nil {
		t.Fatal("budget refusal evidence changed")
	}
}

func TestObservationReportDirectKnownAndUnknownRemainDistinct(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "known"}[known], func(t *testing.T) {
			ctx, o := runtimeTestContext()
			_, err := (&Engine{}).observationDispatch(withDirectObservation(ctx), aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) {
				answer := aiAnswer{OK: true}
				if known {
					usage := observerKnownUsage()
					answer.observationUsage = &usage
				}
				return answer, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			report, err := FinalizeAttemptObservationReport(ctx)
			if err != nil || !report.Qualification.IntegrityComplete || report.Qualification.AccountingComplete != known || report.Qualification.RunCoverage != "unknown" || report.Qualification.RuntimeAuthenticated {
				t.Fatalf("report %+v err=%v", report.Qualification, err)
			}
			digest, err := observationDigest(report.Snapshot.Records)
			if err != nil || digest != report.RecordSetSHA256 {
				t.Fatal("report record digest missing")
			}
			if known {
				*report.Snapshot.Latest[0].Usage.CostUSD = 99
				if *o.Snapshot().Latest[0].Usage.CostUSD != .25 {
					t.Fatal("report aliases collector")
				}
			}
		})
	}
}

func TestObservationReportRejectsPartialOriginalHostCapture(t *testing.T) {
	ctx, _ := runtimeTestContext()
	_, _ = (&Engine{}).observationDispatch(ctx, aiSpec{}, 0, nil, func(context.Context) (aiAnswer, error) { return aiAnswer{}, nil })
	if _, err := FinalizeAttemptObservationReport(ctx); err == nil {
		t.Fatal("empty open host boundary laundered")
	}
}

type runtimeBlockingSettlementStore struct {
	*childStoreFake
	entered chan struct{}
	release chan struct{}
}

func (s *runtimeBlockingSettlementStore) SettleFlowAttempt(ctx context.Context, run Run, id string, in FlowAttemptSettlement) error {
	close(s.entered)
	<-s.release
	return s.childStoreFake.SettleFlowAttempt(ctx, run, id, in)
}

func TestObservationReportCannotCloseDuringFlowSettlement(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	store := &runtimeBlockingSettlementStore{childStoreFake: fs, entered: make(chan struct{}), release: make(chan struct{})}
	e.Store = store
	ac, _ := AttemptContextFrom(observerTestContext())
	o := NewAttemptObserver(nil)
	ctx = withDirectObservation(WithAttemptObserver(WithAttemptContext(ctx, ac), o))
	completed := make(chan error, 1)
	go func() {
		usage := observerKnownUsage()
		_, err := e.budgetedAIContext(ctx, aiSpec{}, 0, func(context.Context) (aiAnswer, error) { return aiAnswer{OK: true, observationUsage: &usage}, nil })
		completed <- err
	}()
	<-store.entered
	if len(o.Snapshot().Latest) != 1 || o.Snapshot().Latest[0].Status != "succeeded" {
		close(store.release)
		t.Fatal("test did not reach post-dispatch settlement")
	}
	report, err := FinalizeAttemptObservationReport(ctx)
	close(store.release)
	if err == nil || report.Qualification.AccountingComplete {
		t.Fatal("unfinished settlement marked complete")
	}
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if report, err := FinalizeAttemptObservationReport(ctx); err != nil || !report.Qualification.AccountingComplete {
		t.Fatal("completed settlement not reportable", err)
	}
}
