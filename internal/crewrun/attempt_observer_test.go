package crewrun

import (
	"context"
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
	run := ObservationRun{Scope: ObservationScope{"ws", "proj", "usr", "agt"}, RunRef: ObservationRef{"crew_runs", "run", 7}, RootRunID: observationString("root"),
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
	r := AttemptObservation{ContractID: CrewObservationContractID, Identity: id, Task: scope.Task, Status: status, DispatchState: "dispatched", Usage: observerKnownUsage(), ErrorCode: errorCode, ObservationRevision: revision}
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
	// Fixed independent Python typed-c14n vector from the frozen 13-field recipe.
	if plan.Identity.AttemptID != "crw-attempt-2158e9946a4799ad77c4dff8d2a92189df25d4e09a7cb5485bedb2056a29e89c" {
		t.Fatal(plan.Identity.AttemptID)
	}
	if plan.RecordSHA256 != "d54fc0e63cb1a8fe0719a984a7e4c4d21a6b5240ddf0aa0b157c6c1b4b65f5dc" {
		t.Fatal(plan.RecordSHA256)
	}
	reserved := observerTestRecord(t, observerTestContext(), "plan", "plan/retry0", "reserved", 1, 0)
	if reserved.RecordSHA256 != "9d7797e15bcdbadec36633530494cb245146a2ab752ec7b607461f630bd4a5db" {
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
		if !first.Complete || len(first.Records) != 8 || len(first.Latest) != 7 || first.Totals.DispatchedAttempts != 7 {
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
	if !s.Complete || s.Totals.DispatchedAttempts != 7 {
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
	if *actual.Run.RootRunID != "root" || actual.Task.TaskID != "task" {
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
	second.Identity.Run.RootRunID = observationString("other-root")
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
