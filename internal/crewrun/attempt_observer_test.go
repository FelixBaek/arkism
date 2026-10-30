package crewrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func observationInt(v int64) *int64       { return &v }
func observationFloat(v float64) *float64 { return &v }
func observationString(v string) *string  { return &v }
func observerTestContext() context.Context {
	run := ObservationRun{Scope: ObservationScope{"ws", "proj", "usr", "agt"}, RunRef: ObservationRef{"crew_runs", "run", 7}, RootRunID: observationString("run"), RootRunCollection: observationString("crew_runs"),
		Definition: ObservationDefinition{Ref: ObservationRef{"crew", "definition", 3}, SourceType: "workflow", DefinitionSHA256: observationString(strings.Repeat("d", 64)), HashState: "recorded"}, ExecutionPath: "workflow", InvocationMode: "flow"}
	task := ObservationTask{"crew_experiment", "task", "v1", strings.Repeat("a", 64), strings.Repeat("b", 64)}
	return WithAttemptContext(context.Background(), AttemptContext{Run: run, Task: task, NodeID: "node", NodeAttempt: 1})
}
func observerKnownUsage() UsageObservation {
	u := UnknownUsageObservation()
	u.InputTokens = observationInt(10)
	u.OutputTokens = observationInt(20)
	u.InputBasis = "provider_reported"
	u.OutputBasis = "provider_reported"
	u.UsageBasis = "provider_reported"
	u.CacheReadTokens = observationInt(2)
	u.CacheReadBasis = "provider_reported"
	u.CacheWriteTokens = observationInt(1)
	u.CacheWriteBasis = "provider_reported"
	u.ReasoningTokens = observationInt(3)
	u.ReasoningBasis = "provider_reported"
	u.CostUSD = observationFloat(.25)
	u.CostBasis = "provider_reported"
	u.PricingRevision = observationString("prices-v1")
	u.ProviderUsageSchema = observationString("script-v1")
	u.StartedAtNS = observationInt(1000)
	u.FinishedAtNS = observationInt(1100)
	u.DurationNS = observationInt(100)
	u.DurationBasis = "observed_monotonic"
	return u
}
func observerTestRecord(t *testing.T, ctx context.Context, phase, key, status string, revision, retry int64) AttemptObservation {
	t.Helper()
	id, err := AttemptIdentityFromContext(ctx, phase, key, retry, observationString("reservation-"+key))
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := AttemptContextFrom(ctx)
	errorCode := ""
	if status != "succeeded" && status != "reserved" {
		errorCode = status
	}
	r := AttemptObservation{ContractID: CrewObservationContractID, Identity: id, Task: scope.Task, Status: status, DispatchState: "dispatched", Usage: observerKnownUsage(), ErrorCode: errorCode, ObservationRevision: revision, UsageRole: "provider_leaf", LeafCoverage: "not_applicable"}
	if status == "reserved" || status == "cancelled_before_dispatch" {
		r.DispatchState = "not_dispatched"
		r.Usage = UnknownUsageObservation()
	}
	sealed, err := SealAttemptObservation(r)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}
func observerReseal(t *testing.T, r AttemptObservation) AttemptObservation {
	t.Helper()
	r.Identity.AttemptID = ""
	sealed, err := SealAttemptObservation(r)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}
func observerSchedule(t *testing.T) []AttemptObservation {
	ctx := observerTestContext()
	plan := observerTestRecord(t, ctx, "plan", "plan/retry0", "succeeded", 2, 0)
	child, _ := AttemptContextFrom(ctx)
	child.ParentAttemptID = observationString(plan.Identity.AttemptID)
	childCtx := WithAttemptContext(ctx, child)
	retry := observerTestRecord(t, childCtx, "worker", "worker0/retry1", "timeout", 1, 1)
	fallback := child
	fallback.ParentAttemptID = observationString(retry.Identity.AttemptID)
	fallback.FallbackParentCallKey = observationString("worker0/retry1")
	return []AttemptObservation{
		observerTestRecord(t, ctx, "plan", "plan/retry0", "reserved", 1, 0), plan,
		observerTestRecord(t, childCtx, "worker", "worker0/retry0", "failed", 1, 0), retry,
		observerTestRecord(t, WithAttemptContext(ctx, fallback), "worker", "fallback/worker0/retry0", "succeeded", 1, 0),
		observerTestRecord(t, childCtx, "worker", "worker1/retry0", "succeeded", 1, 0),
		observerTestRecord(t, childCtx, "synthesis", "synthesis/retry0", "succeeded", 1, 0),
		observerTestRecord(t, ctx, "moderator", "moderator/retry0", "succeeded", 1, 0),
	}
}
func TestAttemptObserverStartsOff(t *testing.T) {
	var observer AttemptObserver
	if observer.Snapshot().Enabled {
		t.Fatal("zero observer must be off")
	}
	observer.Observe(AttemptObservation{})
	ctx := WithAttemptObserver(observerTestContext(), &observer)
	RecordAttempt(ctx, AttemptObservation{})
	MarkAttemptObservationIncomplete(ctx, "secret")
	if !reflect.DeepEqual(observer.Snapshot().Issues, []string{}) {
		t.Fatal("off observer changed result")
	}
	var absent *AttemptObserver
	absent.Observe(AttemptObservation{})
	if absent.Snapshot().Enabled || AttemptObserverEnabled(context.Background()) {
		t.Fatal("nil observer on")
	}
	enabled := NewAttemptObserver(nil)
	if !enabled.Snapshot().Enabled {
		t.Fatal("explicit nil sink must enable memory mode")
	}
}
func TestAttemptObserverIndependentCanonicalGolden(t *testing.T) {
	plan := observerTestRecord(t, observerTestContext(), "plan", "plan/retry0", "succeeded", 2, 0)
	// Fixed independent Python typed-c14n vector from the frozen v2 16-field recipe.
	if plan.Identity.AttemptID != "crw2-attempt-4af30c062b37993ccc8e39a2b0c015c60ddc966966ba485f0d561e3ef845acea" {
		t.Fatal(plan.Identity.AttemptID)
	}
	if plan.RecordSHA256 != "c8bac07530e530c15d6bdcba3387267ef558a81e00dd5c6274f7cbe1ff8f1aa1" {
		t.Fatal(plan.RecordSHA256)
	}
	reserved := observerTestRecord(t, observerTestContext(), "plan", "plan/retry0", "reserved", 1, 0)
	if reserved.RecordSHA256 != "de6a5cc95d622a1fc7df53fc33268269700a7fa2d3c6f50afc04313b9a91441c" {
		t.Fatal(reserved.RecordSHA256)
	}
}
func TestAttemptObserverScheduleReplayAndFailureCost(t *testing.T) {
	schedule := observerSchedule(t)
	var previous AttemptLedgerSnapshot
	for run := 0; run < 2; run++ {
		delivered := 0
		collector := NewAttemptObserver(func(AttemptObservation) error { delivered++; return nil })
		for _, r := range schedule {
			collector.Observe(r)
		}
		first := collector.Snapshot()
		for _, r := range schedule {
			collector.Observe(r)
		}
		second := collector.Snapshot()
		if !reflect.DeepEqual(first, second) || delivered != 8 {
			t.Fatal("receipt replay charged or delivered twice")
		}
		if first.Complete || !slices.Contains(first.Issues, "parent_evidence_unverified") || len(first.Records) != 8 || len(first.Latest) != 7 || first.Totals.DispatchedAttempts != 7 {
			t.Fatalf("ledger %+v", first)
		}
		totals := first.Totals
		if totals.Input.KnownSum != 70 || totals.Output.KnownSum != 140 || totals.CacheRead.KnownSum != 14 || totals.CacheWrite.KnownSum != 7 || totals.Reasoning.KnownSum != 21 || totals.Cost.KnownSum != 1.75 || totals.CallDuration.KnownSum != 700 {
			t.Fatalf("totals %+v", totals)
		}
		if totals.StatusCounts["failed"] != 1 || totals.StatusCounts["timeout"] != 1 || totals.StatusCounts["succeeded"] != 5 {
			t.Fatal(totals.StatusCounts)
		}
		if run > 0 && !reflect.DeepEqual(previous, first) {
			t.Fatal("same fake schedule changed identities/totals")
		}
		previous = first
	}
}
func TestAttemptObserverStableIDExcludesMutableReceiptVersions(t *testing.T) {
	record := observerSchedule(t)[1]
	id := record.Identity.AttemptID
	record.Identity.Run.RunRef.Revision++
	record.Identity.Run.ResultRevision = observationInt(50)
	record.Identity.CanonicalAttemptID = observationString("different-receipt")
	record.Identity.AttemptRef = &ObservationRef{"jobs", "different-receipt", 99}
	next, err := StableAttemptID(record.Identity)
	if err != nil || next != id {
		t.Fatalf("mutable fields changed call identity %s %v", next, err)
	}
	record.Identity.Run.Definition.Ref.Revision++
	next, err = StableAttemptID(record.Identity)
	if err != nil || next == id {
		t.Fatal("definition version did not change identity")
	}
}
func TestAttemptObserverUnknownPartialIsNotFree(t *testing.T) {
	record := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "failed", 1, 0)
	record.Usage = UnknownUsageObservation()
	record.Usage.InputTokens = observationInt(12)
	record.Usage.InputBasis = "provider_reported"
	record = observerReseal(t, record)
	collector := NewAttemptObserver(nil)
	collector.Observe(record)
	result := collector.Snapshot()
	if result.Complete || result.Totals.Input.KnownSum != 12 || result.Totals.Output.Value != nil || result.Totals.Cost.Value != nil || result.Totals.Output.UnknownCalls != 1 {
		t.Fatalf("missing usage became free %+v", result)
	}
	if !slices.Contains(result.Issues, "unknown_usage") || !slices.Contains(result.Issues, "unknown_cost") {
		t.Fatal(result.Issues)
	}
}
func TestAttemptObserverActualZeroAndNonAdditiveComponents(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "zero/retry0", "succeeded", 1, 0)
	r.Usage.InputTokens = observationInt(0)
	r.Usage.OutputTokens = observationInt(0)
	r.Usage.CostUSD = observationFloat(0)
	r = observerReseal(t, r)
	o := NewAttemptObserver(nil)
	o.Observe(r)
	s := o.Snapshot()
	if !s.Complete || s.Totals.Input.Value == nil || *s.Totals.Input.Value != 0 || s.Totals.Cost.Value == nil || *s.Totals.Cost.Value != 0 {
		t.Fatal("zero was converted to unknown")
	}
	if s.Totals.Input.KnownSum != 0 || s.Totals.CacheRead.KnownSum != 2 || s.Totals.Reasoning.KnownSum != 3 {
		t.Fatal("overlapping components added into input")
	}
}
func TestAttemptObserverReservedAndCancelledBeforeDispatch(t *testing.T) {
	o := NewAttemptObserver(nil)
	o.Observe(observerTestRecord(t, observerTestContext(), "main", "main/retry0", "reserved", 1, 0))
	s := o.Snapshot()
	if s.Complete || s.Totals.ReservedOnly != 1 || s.Totals.DispatchedAttempts != 0 || s.Totals.Cost.KnownSum != 0 {
		t.Fatal("reservation charged")
	}
	o = NewAttemptObserver(nil)
	o.Observe(observerTestRecord(t, observerTestContext(), "main", "main/retry0", "cancelled_before_dispatch", 1, 0))
	s = o.Snapshot()
	if s.Totals.CancelledBeforeDispatch != 1 || s.Totals.DispatchedAttempts != 0 || s.Latest[0].Usage.InputTokens != nil {
		t.Fatal("pre-dispatch fabricated provider usage")
	}
}
func TestAttemptObserverSinkFailureAndPanicDoNotChangeWork(t *testing.T) {
	for _, kind := range []string{"error", "panic"} {
		t.Run(kind, func(t *testing.T) {
			o := NewAttemptObserver(func(AttemptObservation) error {
				if kind == "panic" {
					panic("secret body")
				}
				return errors.New("secret body")
			})
			ctx := WithAttemptObserver(observerTestContext(), o)
			work := func() string {
				RecordAttempt(ctx, observerTestRecord(t, ctx, "main", "main/retry0", "succeeded", 1, 0))
				return "original answer"
			}
			if work() != "original answer" {
				t.Fatal("observer changed work")
			}
			s := o.Snapshot()
			if s.Complete || len(s.Records) != 1 {
				t.Fatal(s.Issues)
			}
			for _, issue := range s.Issues {
				if strings.Contains(issue, "secret") {
					t.Fatal("raw sink error leaked")
				}
			}
		})
	}
}
func TestAttemptObserverPendingSinkIsIncomplete(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	o := NewAttemptObserver(func(AttemptObservation) error { close(entered); <-release; return nil })
	finished := make(chan struct{})
	go func() {
		o.Observe(observerTestRecord(t, observerTestContext(), "main", "main/retry0", "succeeded", 1, 0))
		close(finished)
	}()
	<-entered
	s := o.Snapshot()
	if s.Complete || !slices.Contains(s.Issues, "sink_pending") {
		t.Fatal("in-flight delivery claimed complete")
	}
	close(release)
	<-finished
	if !o.Snapshot().Complete {
		t.Fatal(o.Snapshot().Issues)
	}
}
func TestAttemptObserverConflictingReplayAndUnknownSuccessRewrite(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "unknown", 1, 0)
	for _, kind := range []string{"same_revision", "status_rewrite", "task_change", "canonical_alias"} {
		t.Run(kind, func(t *testing.T) {
			o := NewAttemptObserver(nil)
			o.Observe(r)
			changed := cloneObservation(r)
			switch kind {
			case "same_revision":
				changed.Usage.DurationNS = observationInt(101)
			case "status_rewrite":
				changed.ObservationRevision = 2
				changed.Status = "succeeded"
				changed.ErrorCode = ""
			case "task_change":
				changed.ObservationRevision = 2
				changed.Task.TaskID = "other"
			case "canonical_alias":
				changed.Identity.CallKey = "different"
				changed.Identity.AttemptID = ""
			}
			changed = observerReseal(t, changed)
			o.Observe(changed)
			s := o.Snapshot()
			if s.Complete || len(s.Records) != 1 || s.Totals.DispatchedAttempts != 1 {
				t.Fatal("conflict accepted", s.Issues)
			}
		})
	}
}
func TestAttemptObserverParentTaskAndRootEvidence(t *testing.T) {
	plan := observerSchedule(t)[1]
	ctx := observerTestContext()
	scope, _ := AttemptContextFrom(ctx)
	scope.ParentAttemptID = observationString(plan.Identity.AttemptID)
	child := observerTestRecord(t, WithAttemptContext(ctx, scope), "worker", "worker/retry0", "succeeded", 1, 0)
	o := NewAttemptObserver(nil)
	o.Observe(child)
	if !slices.Contains(o.Snapshot().Issues, "missing_parent") {
		t.Fatal("missing parent not reported")
	}
	for _, kind := range []string{"task", "root", "scope"} {
		t.Run(kind, func(t *testing.T) {
			p := cloneObservation(plan)
			p.ObservationRevision = 1
			switch kind {
			case "task":
				p.Task.TaskID = "other"
			case "root":
				p.Identity.Run.RootRunID = observationString("other-root")
				p.Identity.Run.ParentRunID = observationString("other-root")
				p.Identity.Run.ParentRunCollection = observationString("crew_runs")
			case "scope":
				p.Identity.Run.Scope.UserID = "other"
			}
			p = observerReseal(t, p)
			c := cloneObservation(child)
			c.Identity.ParentAttemptID = observationString(p.Identity.AttemptID)
			c = observerReseal(t, c)
			collector := NewAttemptObserver(nil)
			collector.Observe(p)
			collector.Observe(c)
			if !slices.Contains(collector.Snapshot().Issues, "parent_lineage_mismatch") {
				t.Fatal("foreign parent accepted")
			}
		})
	}
}
func TestAttemptObserverKeepsMoreThanFinalTraceCap(t *testing.T) {
	o := NewAttemptObserver(nil)
	ctx := observerTestContext()
	for i := 0; i < 80; i++ {
		r := observerTestRecord(t, ctx, "worker", strings.Repeat("x", i+1), "succeeded", 1, 0)
		o.Observe(r)
	}
	result := o.Snapshot()
	if len(result.Records) != 80 || result.Totals.DispatchedAttempts != 80 || result.EvidenceKind != "per_attempt_ledger" {
		t.Fatal("ledger collapsed to final-node projection")
	}
	MarkAttemptObservationIncomplete(WithAttemptObserver(ctx, o), "trace_store_failed")
	if o.Snapshot().Complete {
		t.Fatal("trace failure hidden by work success")
	}
}
func TestAttemptObserverConcurrentCollectionAndCloneIsolation(t *testing.T) {
	o := NewAttemptObserver(nil)
	var wg sync.WaitGroup
	for _, record := range observerSchedule(t) {
		wg.Add(1)
		go func(r AttemptObservation) { defer wg.Done(); o.Observe(r) }(record)
	}
	wg.Wait()
	s := o.Snapshot()
	if s.Complete || !slices.Contains(s.Issues, "parent_evidence_unverified") || s.Totals.DispatchedAttempts != 7 {
		t.Fatal(s.Issues)
	}
	*s.Latest[0].Usage.InputTokens = 999
	s.Records[0].Identity.Run.Scope.WorkspaceID = "changed"
	again := o.Snapshot()
	if again.Totals.Input.KnownSum != 70 || again.Records[0].Identity.Run.Scope.WorkspaceID != "ws" {
		t.Fatal("snapshot aliases retained state")
	}
}
func TestAttemptObserverTimingExactPrecisionAndOverflow(t *testing.T) {
	ctx := observerTestContext()
	scope, _ := AttemptContextFrom(ctx)
	o := NewAttemptObserver(nil)
	r := observerTestRecord(t, ctx, "main", "long/retry0", "succeeded", 1, 0)
	r.Usage.DurationNS = observationInt(math.MaxInt64)
	r = observerReseal(t, r)
	o.Observe(r)
	timing, err := o.Timing(scope.Run, observationInt(100), "observed_monotonic")
	if err != nil || *timing.SumCallDurationNS != math.MaxInt64 || *timing.WallDurationNS != 100 {
		t.Fatalf("lost duration precision %+v %v", timing, err)
	}
	other := observerTestRecord(t, ctx, "worker", "overflow/retry0", "succeeded", 1, 0)
	o.Observe(other)
	s := o.Snapshot()
	if s.Complete || s.Totals.CallDuration.Value != nil || !slices.Contains(s.Issues, "accounting_overflow") {
		t.Fatal("duration overflow was wrapped")
	}
}
func TestAttemptObserverValidationBoundaries(t *testing.T) {
	for _, kind := range []string{"unknown_basis_value", "known_basis_nil", "mixed_overall", "negative", "nan_cost", "time_order", "unicode", "hash", "receipt_id"} {
		t.Run(kind, func(t *testing.T) {
			r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "succeeded", 1, 0)
			switch kind {
			case "unknown_basis_value":
				r.Usage.InputBasis = "unknown"
			case "known_basis_nil":
				r.Usage.InputTokens = nil
			case "mixed_overall":
				r.Usage.InputBasis = "estimated"
			case "negative":
				r.Usage.InputTokens = observationInt(-1)
			case "nan_cost":
				r.Usage.CostUSD = observationFloat(math.NaN())
			case "time_order":
				r.Usage.FinishedAtNS = observationInt(999)
			case "unicode":
				r.Task.TaskID = string([]byte{255})
			case "hash":
				r.RecordSHA256 = strings.Repeat("f", 64)
			case "receipt_id":
				r.Identity.AttemptRef = &ObservationRef{"jobs", "other", 1}
			}
			if ValidateAttemptObservation(r) == nil {
				t.Fatal("invalid record accepted")
			}
			o := NewAttemptObserver(nil)
			o.Observe(r)
			if o.Snapshot().Complete || len(o.Snapshot().Records) != 0 {
				t.Fatal("invalid record entered ledger")
			}
		})
	}
}
func TestAttemptObserverFixedReasonsAndContextIsolation(t *testing.T) {
	ctx := observerTestContext()
	scope, _ := AttemptContextFrom(ctx)
	*scope.Run.RootRunID = "changed"
	scope.Task.TaskID = "changed"
	actual, _ := AttemptContextFrom(ctx)
	if *actual.Run.RootRunID != "run" || actual.Task.TaskID != "task" {
		t.Fatal("context aliases caller")
	}
	o := NewAttemptObserver(nil)
	ctx = WithAttemptObserver(ctx, o)
	MarkAttemptObservationIncomplete(ctx, "raw secret body")
	if !reflect.DeepEqual(o.Snapshot().Issues, []string{"invalid_incomplete_reason", "no_observations"}) {
		t.Fatal(o.Snapshot().Issues)
	}
	if _, err := AttemptIdentityFromContext(context.Background(), "main", "main", 0, nil); err == nil {
		t.Fatal("missing context fabricated identity")
	}
}

func TestAttemptObserverFixedErrorCodesRejectBodies(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "failed", 1, 0)
	r.ErrorCode = "secret_payload"
	if _, err := SealAttemptObservation(r); err == nil {
		t.Fatal("arbitrary regex-valid body became a fixed error code")
	}
	if NormalizeAttemptErrorCode("secret_payload") != "unknown_error" || NormalizeAttemptErrorCode("timeout") != "timeout" {
		t.Fatal("normalizer forwarded body or lost a known code")
	}
}
func TestAttemptObserverTimingCannotMixLogicalRuns(t *testing.T) {
	ctx := observerTestContext()
	first := observerTestRecord(t, ctx, "main", "first/retry0", "succeeded", 1, 0)
	second := observerTestRecord(t, ctx, "worker", "second/retry0", "succeeded", 1, 0)
	second.Identity.Run.RunRef.ID = "other-run"
	second.Identity.Run.RootRunID = observationString("other-run")
	second.Usage.DurationNS = observationInt(200)
	second = observerReseal(t, second)
	collector := NewAttemptObserver(nil)
	collector.Observe(first)
	collector.Observe(second)
	timing, err := collector.Timing(first.Identity.Run, observationInt(80), "observed_monotonic")
	if err != nil || timing.SumCallDurationNS == nil || *timing.SumCallDurationNS != 100 {
		t.Fatalf("other run time mixed %+v %v", timing, err)
	}
	missing := first.Identity.Run
	missing.RunRef.ID = "unseen"
	missing.RootRunID = observationString("unseen")
	timing, err = collector.Timing(missing, nil, "unknown")
	if err != nil || timing.SumCallDurationNS != nil || timing.SumCallDurationBasis != "unknown" {
		t.Fatal("absent ledger became observed zero")
	}
}

func TestAttemptObserverCanonicalReferenceDomains(t *testing.T) {
	for _, kind := range []string{"run", "definition", "attempt", "missing_canonical_id"} {
		t.Run(kind, func(t *testing.T) {
			r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "succeeded", 1, 0)
			switch kind {
			case "run":
				r.Identity.Run.RunRef.Collection = "crew_meetings"
			case "definition":
				r.Identity.Run.Definition.Ref.Collection = "crew_runs"
			case "attempt":
				r.Identity.AttemptRef = &ObservationRef{"flow_attempts", *r.Identity.CanonicalAttemptID, 1}
			case "missing_canonical_id":
				r.Identity.AttemptRef = &ObservationRef{"jobs", *r.Identity.CanonicalAttemptID, 1}
				r.Identity.CanonicalAttemptID = nil
			}
			if _, err := SealAttemptObservation(r); err == nil {
				t.Fatal("reference domain or required canonical ID was invented")
			}
		})
	}
}

func TestAttemptObserverEmptyLedgerCannotClaimKnownFreeRun(t *testing.T) {
	snapshot := NewAttemptObserver(nil).Snapshot()
	if snapshot.Complete || !slices.Contains(snapshot.Issues, "no_observations") {
		t.Fatal("no observations promoted to complete")
	}
	if snapshot.Totals.Input.Value != nil || snapshot.Totals.Output.Value != nil || snapshot.Totals.Cost.Value != nil || snapshot.Totals.CallDuration.Value != nil {
		t.Fatal("no evidence became known zero")
	}
}
func TestAttemptObserverSuccessRequiresKnownDispatch(t *testing.T) {
	for _, state := range []string{"not_dispatched", "unknown"} {
		t.Run(state, func(t *testing.T) {
			r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "succeeded", 1, 0)
			r.DispatchState = state
			if _, err := SealAttemptObservation(r); err == nil {
				t.Fatal("success without confirmed dispatch accepted")
			}
		})
	}
}
func TestAttemptObserverConfirmedDispatchCannotDisappear(t *testing.T) {
	for _, state := range []string{"not_dispatched", "unknown"} {
		t.Run(state, func(t *testing.T) {
			r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "failed", 1, 0)
			o := NewAttemptObserver(nil)
			o.Observe(r)
			changed := cloneObservation(r)
			changed.ObservationRevision = 2
			changed.DispatchState = state
			changed.Usage = UnknownUsageObservation()
			changed = observerReseal(t, changed)
			o.Observe(changed)
			s := o.Snapshot()
			if s.Complete || s.Totals.DispatchedAttempts != 1 || s.Totals.Cost.ProviderReportedSum != .25 || len(s.Records) != 1 {
				t.Fatalf("confirmed call/cost disappeared %+v", s)
			}
		})
	}
}
func TestAttemptObserverCanonicalReservationCannotChange(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "failed", 1, 0)
	o := NewAttemptObserver(nil)
	o.Observe(r)
	changed := cloneObservation(r)
	changed.ObservationRevision = 2
	changed.Identity.CanonicalAttemptID = observationString("another-reservation")
	changed = observerReseal(t, changed)
	o.Observe(changed)
	s := o.Snapshot()
	if s.Complete || len(s.Records) != 1 || *s.Latest[0].Identity.CanonicalAttemptID != *r.Identity.CanonicalAttemptID {
		t.Fatal("canonical reservation silently replaced")
	}
}
func TestAttemptObserverReportedEvidenceCannotBecomeUnknown(t *testing.T) {
	for _, part := range []string{"input", "output", "cache_read", "cache_write", "reasoning", "cost", "duration"} {
		t.Run(part, func(t *testing.T) {
			r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "failed", 1, 0)
			o := NewAttemptObserver(nil)
			o.Observe(r)
			changed := cloneObservation(r)
			changed.ObservationRevision = 2
			switch part {
			case "input":
				changed.Usage.InputTokens = nil
				changed.Usage.InputBasis = "unknown"
				changed.Usage.UsageBasis = "unknown"
			case "output":
				changed.Usage.OutputTokens = nil
				changed.Usage.OutputBasis = "unknown"
				changed.Usage.UsageBasis = "unknown"
			case "cache_read":
				changed.Usage.CacheReadTokens = nil
				changed.Usage.CacheReadBasis = "unknown"
			case "cache_write":
				changed.Usage.CacheWriteTokens = nil
				changed.Usage.CacheWriteBasis = "unknown"
			case "reasoning":
				changed.Usage.ReasoningTokens = nil
				changed.Usage.ReasoningBasis = "unknown"
			case "cost":
				changed.Usage.CostUSD = nil
				changed.Usage.CostBasis = "unknown"
			case "duration":
				changed.Usage.DurationNS = nil
				changed.Usage.DurationBasis = "unknown"
			}
			changed = observerReseal(t, changed)
			o.Observe(changed)
			s := o.Snapshot()
			if s.Complete || len(s.Records) != 1 || s.Totals.Cost.ProviderReportedSum != .25 {
				t.Fatal("prior reported lower bound lost", s.Issues)
			}
		})
	}
}
func TestAttemptObserverReportedAmountCannotRewindWithoutProof(t *testing.T) {
	for _, part := range []string{"input", "cost", "duration"} {
		t.Run(part, func(t *testing.T) {
			r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "failed", 1, 0)
			o := NewAttemptObserver(nil)
			o.Observe(r)
			changed := cloneObservation(r)
			changed.ObservationRevision = 2
			switch part {
			case "input":
				changed.Usage.InputTokens = observationInt(0)
			case "cost":
				changed.Usage.CostUSD = observationFloat(0)
			case "duration":
				changed.Usage.DurationNS = observationInt(0)
			}
			changed = observerReseal(t, changed)
			o.Observe(changed)
			s := o.Snapshot()
			if s.Complete || len(s.Records) != 1 || s.Totals.Input.ProviderReportedSum != 10 || s.Totals.Cost.ProviderReportedSum != .25 || s.Totals.CallDuration.KnownSum != 100 {
				t.Fatal("reported lower bound was rewritten")
			}
		})
	}
}
func TestAttemptObserverEstimateCanRefineToSmallerReportedCost(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "failed", 1, 0)
	r.Usage.CostUSD = observationFloat(.50)
	r.Usage.CostBasis = "estimated"
	r = observerReseal(t, r)
	changed := cloneObservation(r)
	changed.ObservationRevision = 2
	changed.Usage.CostUSD = observationFloat(.25)
	changed.Usage.CostBasis = "provider_reported"
	changed = observerReseal(t, changed)
	o := NewAttemptObserver(nil)
	o.Observe(r)
	o.Observe(changed)
	s := o.Snapshot()
	if !s.Complete || len(s.Records) != 2 || s.Totals.Cost.KnownSum != .25 || s.Totals.Cost.ProviderReportedSum != .25 || s.Totals.Cost.EstimatedSum != 0 {
		t.Fatal("valid evidence refinement rejected or double-counted", s.Issues)
	}
}

func TestAttemptObserverPredispatchCannotHideReportedCharge(t *testing.T) {
	for _, status := range []string{"failed", "cancelled_before_dispatch", "reserved"} {
		for _, part := range []string{"input", "output", "cache_read", "cache_write", "reasoning", "cost"} {
			t.Run(status+"/"+part, func(t *testing.T) {
				r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", status, 1, 0)
				r.DispatchState = "not_dispatched"
				r.Usage = UnknownUsageObservation()
				switch part {
				case "input":
					r.Usage.InputTokens = observationInt(1)
					r.Usage.InputBasis = "provider_reported"
				case "output":
					r.Usage.OutputTokens = observationInt(1)
					r.Usage.OutputBasis = "provider_reported"
				case "cache_read":
					r.Usage.CacheReadTokens = observationInt(1)
					r.Usage.CacheReadBasis = "provider_reported"
				case "cache_write":
					r.Usage.CacheWriteTokens = observationInt(1)
					r.Usage.CacheWriteBasis = "provider_reported"
				case "reasoning":
					r.Usage.ReasoningTokens = observationInt(1)
					r.Usage.ReasoningBasis = "provider_reported"
				case "cost":
					r.Usage.CostUSD = observationFloat(.25)
					r.Usage.CostBasis = "provider_reported"
				}
				if _, err := SealAttemptObservation(r); err == nil {
					t.Fatal("positive reported charge hidden by not_dispatched")
				}
			})
		}
	}
}
func TestAttemptObserverPredispatchZeroAndEstimateRemainRepresentable(t *testing.T) {
	for _, basis := range []string{"provider_reported", "estimated"} {
		t.Run(basis, func(t *testing.T) {
			r := observerTestRecord(t, observerTestContext(), "main", "main/retry0", "cancelled_before_dispatch", 1, 0)
			r.Usage = observerKnownUsage()
			amount := int64(0)
			cost := 0.0
			if basis == "estimated" {
				amount = 100
				cost = .5
				r.Status = "reserved"
				r.ErrorCode = ""
			}
			r.Usage.InputTokens = observationInt(amount)
			r.Usage.OutputTokens = observationInt(amount)
			r.Usage.CacheReadTokens = observationInt(amount)
			r.Usage.CacheWriteTokens = observationInt(amount)
			r.Usage.ReasoningTokens = observationInt(amount)
			r.Usage.InputBasis = basis
			r.Usage.OutputBasis = basis
			r.Usage.CacheReadBasis = basis
			r.Usage.CacheWriteBasis = basis
			r.Usage.ReasoningBasis = basis
			r.Usage.UsageBasis = basis
			r.Usage.CostUSD = observationFloat(cost)
			r.Usage.CostBasis = basis
			if _, err := SealAttemptObservation(r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAttemptObserverV2UnknownRootCannotMintIdentity(t *testing.T) {
	scope, _ := AttemptContextFrom(observerTestContext())
	scope.Run.RootRunID = nil
	scope.Run.RootRunCollection = nil
	id := AttemptIdentity{Run: scope.Run, NodeID: "node", NodeAttempt: 1, CallKey: "call", IdentityBasis: "observed_legacy_call", Phase: "main"}
	if _, err := StableAttemptID(id); err == nil {
		t.Fatal("unknown root minted a complete stable identity")
	}
}

func observerV2EvidenceBytes(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}
func observerV2Group(t *testing.T) (*AttemptObserver, AttemptObservation, AttemptObservation, ObservationEvidence) {
	t.Helper()
	ctx := observerTestContext()
	parent := observerTestRecord(t, ctx, "main", "outer/retry0", "succeeded", 1, 0)
	parent.UsageRole = "transport_aggregate"
	parent.LeafCoverage = "complete"
	parent = observerReseal(t, parent)
	scope, _ := AttemptContextFrom(ctx)
	scope.ParentAttemptID = observationString(parent.Identity.AttemptID)
	scope.IdentityBasis = "observed_host_leaf"
	childID, err := AttemptIdentityFromContext(WithAttemptContext(ctx, scope), "worker", "host/retry0", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := SealAttemptObservation(AttemptObservation{ContractID: CrewObservationContractID, Identity: childID, Task: scope.Task, Status: "failed", DispatchState: "dispatched", Usage: observerKnownUsage(), ErrorCode: "failed", ObservationRevision: 1, UsageRole: "provider_leaf", LeafCoverage: "not_applicable"})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := SealObservationParentLink(ObservationParentLinkEvidence{ContractID: CrewObservationContractID, Scope: scope.Run.Scope, Task: scope.Task, RootRunCollection: "crew_runs", RootRunID: "run", ParentAttemptID: parent.Identity.AttemptID, ChildAttemptID: child.Identity.AttemptID, ParentRunRef: parent.Identity.Run.RunRef, ChildRunRef: child.Identity.Run.RunRef, Relationship: "host_leaf", SourceSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := SealObservationSenderJournal(ObservationSenderJournal{ContractID: CrewObservationContractID, Scope: scope.Run.Scope, Task: scope.Task, RootRunCollection: "crew_runs", RootRunID: "run", ParentAttemptID: parent.Identity.AttemptID, SenderBoundaryID: "synthetic-boundary", State: "closed", MemberAttemptIDs: []string{child.Identity.AttemptID}, OpeningEventSHA256: strings.Repeat("b", 64), ClosingEventSHA256: observationString(strings.Repeat("c", 64))})
	if err != nil {
		t.Fatal(err)
	}
	membership, err := ObservationMembershipHash(journal.MemberAttemptIDs)
	if err != nil {
		t.Fatal(err)
	}
	journalRaw := observerV2EvidenceBytes(t, journal)
	closure, err := SealObservationDispatchClosure(ObservationDispatchClosure{ContractID: CrewObservationContractID, Scope: scope.Run.Scope, Task: scope.Task, RootRunCollection: "crew_runs", RootRunID: "run", ParentAttemptID: parent.Identity.AttemptID, SenderBoundaryID: "synthetic-boundary", MemberAttemptIDs: []string{child.Identity.AttemptID}, MembershipSHA256: membership, SenderJournalSHA256: observationBytesSHA(journalRaw)})
	if err != nil {
		t.Fatal(err)
	}
	proofRaw := observerV2EvidenceBytes(t, proof)
	evidence := ObservationEvidence{ParentLinks: map[string][]byte{child.Identity.AttemptID: proofRaw}, ParentSHA256: map[string]string{child.Identity.AttemptID: observationBytesSHA(proofRaw)}, Closures: []ObservationDispatchClosure{closure}, SenderJournals: map[string][]byte{"synthetic-boundary": journalRaw}, SenderSHA256: map[string]string{"synthetic-boundary": observationBytesSHA(journalRaw)}}
	collector := NewAttemptObserver(nil)
	collector.Observe(parent)
	collector.Observe(child)
	return collector, parent, child, evidence
}
func TestAttemptObserverV2EveryCollectionIndependentIDs(t *testing.T) {
	vectors := []struct{ collection, source, path, want string }{
		{"crew_runs", "workflow", "workflow", "crw2-attempt-6d3aad344f91724babf0ba53cb60ffc27e3355a55b94327fbc6d167f6889e62a"},
		{"crew_meetings", "team", "crew", "crw2-attempt-a470d1b5d92c4f41935d61617856e2fd70577636cebe906eb61f448995b22b07"},
		{"crew_assists", "agent", "single", "crw2-attempt-2f0100de126f39702774e83fcdb316fcafde2da36183e9ad8af748f5aa07d76f"},
	}
	for _, vector := range vectors {
		t.Run(vector.collection, func(t *testing.T) {
			scope, _ := AttemptContextFrom(observerTestContext())
			run := scope.Run
			run.RunRef = ObservationRef{vector.collection, "same-execution-id", 9}
			run.RootRunID = observationString("same-execution-id")
			run.RootRunCollection = observationString(vector.collection)
			run.Definition.Ref = ObservationRef{"crew", "same-definition", 3}
			run.Definition.SourceType = vector.source
			run.Definition.HashState = "not_recorded"
			run.Definition.DefinitionSHA256 = nil
			run.InvocationMode = "legacy"
			run.ExecutionPath = vector.path
			id, err := StableAttemptID(AttemptIdentity{Run: run, NodeID: "same-node", NodeAttempt: 1, CallKey: "same-call", Phase: "main", IdentityBasis: "observed_legacy_call"})
			if err != nil || id != vector.want {
				t.Fatalf("%s %v", id, err)
			}
		})
	}
}
func TestAttemptObserverV2DistinctSpendViewsAndQualification(t *testing.T) {
	collector, _, _, evidence := observerV2Group(t)
	s := collector.Snapshot()
	if s.Complete || s.RuntimeAuthenticated || s.RunCoverage != "unknown" || s.Totals.Cost.KnownSum != .25 || s.AggregateTotals.Cost.KnownSum != .25 {
		t.Fatalf("overlapping views %+v", s)
	}
	q, err := QualifyObservationLedger(s, evidence)
	if err != nil || !q.IntegrityComplete || !q.AccountingComplete || q.RuntimeAuthenticated || q.RunCoverage != "unknown" || len(q.Groups) != 1 || q.Groups[0].LeafCost.KnownSum != .25 {
		t.Fatalf("qualification %+v %v", q, err)
	}
	evidence.Closures = nil
	if _, err = QualifyObservationLedger(s, evidence); err == nil {
		t.Fatal("bare complete enum qualified")
	}
}
func TestAttemptObserverV2AggregateOnlyNotFree(t *testing.T) {
	_, parent, _, _ := observerV2Group(t)
	collector := NewAttemptObserver(nil)
	collector.Observe(parent)
	s := collector.Snapshot()
	if s.Complete || s.Totals.Cost.Value != nil || s.AggregateTotals.Cost.KnownSum != .25 {
		t.Fatal("aggregate became free leaf cost")
	}
	parent.UsageRole = "opaque_dispatch"
	parent.LeafCoverage = "unknown"
	parent = observerReseal(t, parent)
	collector = NewAttemptObserver(nil)
	collector.Observe(parent)
	if collector.Snapshot().Totals.Cost.Value != nil {
		t.Fatal("opaque became free")
	}
}
func TestAttemptObserverV2ReservationEstimatesSeparate(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "reserve", "reserved", 1, 0)
	r.Usage.CostUSD = observationFloat(2)
	r.Usage.CostBasis = "estimated"
	r = observerReseal(t, r)
	o := NewAttemptObserver(nil)
	o.Observe(r)
	s := o.Snapshot()
	if s.ReservationEstimates.KnownSum != 2 || s.Totals.Cost.KnownSum != 0 || s.Totals.Cost.Value != nil {
		t.Fatal("reservation estimated as spent")
	}
}
func TestAttemptObserverV2ClosedJSONBoundary(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "decode", "succeeded", 1, 0)
	raw := observerV2EvidenceBytes(t, r)
	if _, err := DecodeAttemptObservation(raw); err != nil {
		t.Fatal(err)
	}
	mutations := map[string][]byte{
		"duplicate":     bytes.Replace(raw, []byte(`"observation_revision":1`), []byte(`"observation_revision":1,"observation_revision":1`), 1),
		"unknown":       append([]byte(`{"secret_unknown":1,`), raw[1:]...),
		"missing":       bytes.Replace(raw, []byte(`,"parent_run_collection":null`), nil, 1),
		"nullbool":      bytes.Replace(raw, []byte(`"token_ceiling_enforced":false`), []byte(`"token_ceiling_enforced":null`), 1),
		"fractionint":   bytes.Replace(raw, []byte(`"observation_revision":1`), []byte(`"observation_revision":1.0`), 1),
		"exponentint":   bytes.Replace(raw, []byte(`"observation_revision":1`), []byte(`"observation_revision":1e0`), 1),
		"boolint":       bytes.Replace(raw, []byte(`"observation_revision":1`), []byte(`"observation_revision":true`), 1),
		"surrogate":     bytes.Replace(raw, []byte(`"task_id":"task"`), []byte(`"task_id":"\ud800"`), 1),
		"low_surrogate": bytes.Replace(raw, []byte(`"task_id":"task"`), []byte(`"task_id":"\udc00"`), 1),
		"invalidutf8":   append(append([]byte{}, raw...), 0xff),
		"floatinf":      bytes.Replace(raw, []byte(`"cost_usd":0.25`), []byte(`"cost_usd":1e400`), 1),
		"depth":         []byte(`{"x":` + strings.Repeat("[", 129) + `0` + strings.Repeat("]", 129) + `}`),
	}
	for name, bad := range mutations {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(bad, raw) {
				t.Fatal("mutation did not occur")
			}
			if _, err := DecodeAttemptObservation(bad); err == nil {
				t.Fatal("invalid transport accepted")
			}
		})
	}
	r.Task.TaskID = "😀"
	r = observerReseal(t, r)
	raw = observerV2EvidenceBytes(t, r)
	raw = bytes.Replace(raw, []byte("😀"), []byte(`\ud83d\ude00`), 1)
	if _, err := DecodeAttemptObservation(raw); err != nil {
		t.Fatal("valid pair rejected", err)
	}
}
func TestAttemptObserverV2ProofAndClosureNegative(t *testing.T) {
	for _, kind := range []string{"pin", "open", "missingmember", "omittedpaidleaf", "foreignscope", "orphanproof"} {
		t.Run(kind, func(t *testing.T) {
			o, parent, child, e := observerV2Group(t)
			s := o.Snapshot()
			switch kind {
			case "pin":
				e.SenderSHA256["synthetic-boundary"] = strings.Repeat("0", 64)
			case "open":
				journal, _ := DecodeObservationSenderJournal(e.SenderJournals["synthetic-boundary"])
				journal.State = "open"
				journal.ClosingEventSHA256 = nil
				journal, err := SealObservationSenderJournal(journal)
				if err != nil {
					t.Fatal(err)
				}
				raw := observerV2EvidenceBytes(t, journal)
				e.SenderJournals["synthetic-boundary"] = raw
				e.SenderSHA256["synthetic-boundary"] = observationBytesSHA(raw)
				e.Closures[0].SenderJournalSHA256 = observationBytesSHA(raw)
				e.Closures[0], _ = SealObservationDispatchClosure(e.Closures[0])
			case "missingmember":
				s.Records = []AttemptObservation{parent}
				e.ParentLinks = nil
				e.ParentSHA256 = nil
			case "omittedpaidleaf":
				other := child
				other.Identity.CallKey = "other-host"
				other = observerReseal(t, other)
				s.Records = append(s.Records, other)
				proof, _ := DecodeObservationParentLink(e.ParentLinks[child.Identity.AttemptID])
				proof.ChildAttemptID = other.Identity.AttemptID
				proof, _ = SealObservationParentLink(proof)
				raw := observerV2EvidenceBytes(t, proof)
				e.ParentLinks[other.Identity.AttemptID] = raw
				e.ParentSHA256[other.Identity.AttemptID] = observationBytesSHA(raw)
			case "foreignscope":
				proof, _ := DecodeObservationParentLink(e.ParentLinks[child.Identity.AttemptID])
				proof.Scope.ProjectID = "foreign"
				proof, _ = SealObservationParentLink(proof)
				raw := observerV2EvidenceBytes(t, proof)
				e.ParentLinks[child.Identity.AttemptID] = raw
				e.ParentSHA256[child.Identity.AttemptID] = observationBytesSHA(raw)
			case "orphanproof":
				e.ParentLinks[parent.Identity.AttemptID] = e.ParentLinks[child.Identity.AttemptID]
			}
			if _, err := QualifyObservationLedger(s, e); err == nil {
				t.Fatal("invalid evidence qualified")
			}
		})
	}
}
func TestAttemptObserverV2EvidenceInteroperability(t *testing.T) {
	_, parent, child, e := observerV2Group(t)
	for _, entry := range []struct {
		name  string
		value any
	}{{"AttemptObservation", parent}, {"AttemptObservation", child}, {"DispatchClosure", e.Closures[0]}} {
		t.Log("V2_INTEROP", entry.name, string(observerV2EvidenceBytes(t, entry.value)))
	}
	for _, entry := range []struct {
		name string
		raw  []byte
	}{{"ParentLinkEvidence", e.ParentLinks[child.Identity.AttemptID]}, {"SenderJournal", e.SenderJournals["synthetic-boundary"]}} {
		t.Log("V2_INTEROP", entry.name, string(entry.raw))
	}
}

func TestAttemptObserverV2UnknownGroupCostStaysUnknown(t *testing.T) {
	_, parent, child, e := observerV2Group(t)
	child.Usage = UnknownUsageObservation()
	child = observerReseal(t, child)
	o := NewAttemptObserver(nil)
	o.Observe(parent)
	o.Observe(child)
	s := o.Snapshot()
	if s.Totals.Cost.Value != nil || s.AggregateTotals.Cost.ProviderReportedSum != .25 {
		t.Fatal("unknown hidden as zero")
	}
	q, err := QualifyObservationLedger(s, e)
	if err != nil || !q.IntegrityComplete || q.AccountingComplete || q.Groups[0].LeafCost.Value != nil || q.Groups[0].LeafCost.UnknownCalls != 1 {
		t.Fatalf("unknown group %+v %v", q, err)
	}
}
func TestAttemptObserverV2RoleAndLegacyDiscriminatorCannotMove(t *testing.T) {
	initial := observerTestRecord(t, observerTestContext(), "main", "role", "failed", 1, 0)
	later := initial
	later.ObservationRevision = 2
	later.UsageRole = "transport_aggregate"
	later.LeafCoverage = "unknown"
	later = observerReseal(t, later)
	o := NewAttemptObserver(nil)
	o.Observe(initial)
	o.Observe(later)
	if len(o.Snapshot().Records) != 1 || !slices.Contains(o.Snapshot().Issues, "observation_transition") {
		t.Fatal("role mutation hid paid leaf")
	}
	initial.ContractID = "arkism-crew-research-v1"
	if _, err := SealAttemptObservation(initial); err == nil {
		t.Fatal("new fields accepted as legacy v1")
	}
}
func TestAttemptObserverV2StrictFiniteFloatLexeme(t *testing.T) {
	r := observerTestRecord(t, observerTestContext(), "main", "finite", "failed", 1, 0)
	r.Usage.CostUSD = observationFloat(1e308)
	r = observerReseal(t, r)
	raw := observerV2EvidenceBytes(t, r)
	raw = bytes.Replace(raw, []byte(`1e+308`), []byte("1"+strings.Repeat("0", 308)), 1)
	if _, err := DecodeAttemptObservation(raw); err != nil {
		t.Fatal("finite309 digits rejected", err)
	}
}
func TestAttemptObserverV2ParentRootScopeAndCycle(t *testing.T) {
	for _, kind := range []string{"task", "root", "collection", "reference"} {
		t.Run(kind, func(t *testing.T) {
			o, _, child, e := observerV2Group(t)
			proof, _ := DecodeObservationParentLink(e.ParentLinks[child.Identity.AttemptID])
			switch kind {
			case "task":
				proof.Task.TaskID = "other"
			case "root":
				proof.RootRunID = "other"
			case "collection":
				proof.RootRunCollection = "crew_assists"
			case "reference":
				proof.ParentRunRef.Revision = 999
			}
			proof, err := SealObservationParentLink(proof)
			if err != nil {
				t.Fatal(err)
			}
			raw := observerV2EvidenceBytes(t, proof)
			e.ParentLinks[child.Identity.AttemptID] = raw
			e.ParentSHA256[child.Identity.AttemptID] = observationBytesSHA(raw)
			if _, err = QualifyObservationLedger(o.Snapshot(), e); err == nil {
				t.Fatal("foreign evidence accepted")
			}
		})
	}
	r := observerTestRecord(t, observerTestContext(), "main", "self", "succeeded", 1, 0)
	r.Identity.ParentAttemptID = observationString(r.Identity.AttemptID)
	if _, err := SealAttemptObservation(r); err == nil {
		t.Fatal("self parent identity accepted")
	}
	run := r.Identity.Run
	run.ParentRunCollection = observationString("crew_runs")
	run.ParentRunID = observationString("run")
	if err := validateObservationRun(run); err == nil {
		t.Fatal("self run parent accepted")
	}
}

func TestAttemptObserverV2QualificationPreservesCollectorFailures(t *testing.T) {
	record := observerTestRecord(t, observerTestContext(), "main", "failed-sink", "succeeded", 1, 0)
	for _, kind := range []string{"sink", "mark", "pending", "unknown-issue"} {
		t.Run(kind, func(t *testing.T) {
			o := NewAttemptObserver(nil)
			switch kind {
			case "sink":
				o = NewAttemptObserver(func(AttemptObservation) error { return errors.New("private body") })
			case "mark":
				o.note("trace_missing")
			}
			o.Observe(record)
			s := o.Snapshot()
			if kind == "pending" {
				s.Issues = append(s.Issues, "sink_pending")
			}
			if kind == "unknown-issue" {
				s.Issues = append(s.Issues, "private source body")
			}
			q, err := QualifyObservationLedger(s, ObservationEvidence{})
			if err == nil || q.IntegrityComplete || q.AccountingComplete {
				t.Fatalf("source collector failure erased %+v %v", q, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("raw issue exposed")
			}
		})
	}
}

func TestAttemptObserverV2PartialAggregateQualificationIncomplete(t *testing.T) {
	for _, coverage := range []string{"partial", "unknown"} {
		t.Run(coverage, func(t *testing.T) {
			_, parent, _, _ := observerV2Group(t)
			parent.LeafCoverage = coverage
			parent = observerReseal(t, parent)
			o := NewAttemptObserver(nil)
			o.Observe(parent)
			s := o.Snapshot()
			q, err := QualifyObservationLedger(s, ObservationEvidence{})
			if err == nil || q.IntegrityComplete || q.AccountingComplete || s.AggregateTotals.Cost.ProviderReportedSum != .25 || s.Totals.Cost.Value != nil {
				t.Fatalf("partial aggregate promoted %+v %v", q, err)
			}
		})
	}
}
