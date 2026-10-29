package dynamicsv2

import (
	"bytes"
	"math"
	"os"
	"reflect"
	"sort"
	"testing"
)

func TestTransitionScalarPendingActivation(t *testing.T) {
	state, index, event, cfg := scalarFixturePending(t)
	result, err := ApplyEvent(state, index, event, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != "active" || result.State.StateRevision != 1 || result.State.Value != .5 || result.Cursor.AcceptedSeq != 1 {
		t.Fatal("activation fields differ")
	}
}

func scalarFixturePending(t *testing.T) (StateV2, ReceiptIndexV2, EventV2, DynamicsConfigV2) {
	return scalarFixturePendingConfig(t, fixtureConfig(t, "B2"))
}
func scalarFixturePendingConfig(t *testing.T, cfg DynamicsConfigV2) (StateV2, ReceiptIndexV2, EventV2, DynamicsConfigV2) {
	t.Helper()
	key := fixtureKey()
	key.ConfigSHA256 = cfg.ConfigSHA256
	state, e := scalarSeal[StateV2]("StateV2", StateV2HashProjection{Key: key, Mode: "scalar", Value: cfg.Initial, StateRevision: 0, Status: "pending_activation", Reason: "none"})
	if e != nil {
		t.Fatal(e)
	}
	prefix, e := CanonicalSHA256("StreamInitialPrefixProjectionV2", StreamInitialPrefixProjectionV2{StreamID: "s", ClockDomain: "c", Scope: key.Scope, Dimension: key.Dimension, ConfigSHA256: key.ConfigSHA256})
	if e != nil {
		t.Fatal(e)
	}
	fence, e := scalarSeal[LifecycleFence]("LifecycleFence", LifecycleFenceHashProjection{Namespace: "synthetic", Epoch: 1, MinimumAcceptedSeq: 0, Entries: []FenceEntry{{Scope: key.Scope, Target: key.Target, Status: "current", SourceRefs: []EvidenceRef{}, Reason: "none"}}})
	if e != nil {
		t.Fatal(e)
	}
	cursor := StreamCursorV2{StreamID: "s", ClockDomain: "c", AcceptedAtNS: 0, AcceptedSeq: 0, PrefixSHA256: prefix}
	index, e := scalarSeal[ReceiptIndexV2]("ReceiptIndexV2", ReceiptIndexV2HashProjection{StreamID: "s", ClockDomain: "c", Cursor: cursor, Receipts: []ReceiptV2{}, EvidenceConsumptions: []EvidenceConsumption{}, LifecycleFence: fence})
	if e != nil {
		t.Fatal(e)
	}
	event := sealFixture(t, "EventV2", EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "activation", TransportAttemptID: "first", Key: key, Kind: "activate", ClockDomain: "c", AcceptedAtNS: 10, AcceptedSeq: 1, Outcome: "not_applicable", Attribution: "not_applicable", Reason: "activation"})
	return state, index, event, cfg
}

func scalarFixtureActive(t *testing.T, cfg DynamicsConfigV2) (StateV2, ReceiptIndexV2) {
	t.Helper()
	s, i, e, _ := scalarFixturePendingConfig(t, cfg)
	r, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.State.Value != cfg.Initial || r.State.StateRevision != 1 {
		t.Fatal("activation setup violates literal initial state")
	}
	return r.State, r.Receipts
}
func scalarOutcomeEvent(t *testing.T, s StateV2, i ReceiptIndexV2, at int64, outcome Outcome, attribution Attribution) EventV2 {
	t.Helper()
	e := fixtureOutcome(t)
	e.Key = s.Key
	e.EventID = "result"
	e.AcceptedSeq = i.Cursor.AcceptedSeq + 1
	e.AcceptedAtNS = at
	e.ArrivedAtNS = nil
	e.ObservedAtNS = scalarPointer(int64(10))
	e.Outcome = outcome
	e.Attribution = attribution
	e.Evidence.Target = s.Key.Target
	*e.Evidence = sealFixture(t, "EvidenceBinding", *e.Evidence)
	return sealFixture(t, "EventV2", e)
}
func scalarAssertNear(t *testing.T, got, want float64, cfg DynamicsConfigV2) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > cfg.Atol+cfg.Rtol*math.Abs(want) {
		t.Fatalf("got %.18g want %.18g under atol=%g rtol=%g", got, want, cfg.Atol, cfg.Rtol)
	}
}
func TestTransitionScalarIndependentHandOracles(t *testing.T) {
	cases := []struct {
		name        string
		dt          int64
		outcome     Outcome
		attribution Attribution
		want        float64
		consume     bool
	}{
		{"same_time_passed", 0, "passed", "memory", .625, true}, {"same_time_failed_memory", 0, "failed", "memory", .25, true}, {"same_time_unknown", 0, "unknown", "unattributable", .5, false}, {"same_time_failed_tool", 0, "failed", "tool", .5, false},
		{"tau_passed", 1000000000, "passed", "memory", .3879547904392908706, true}, {"tau_failed_memory", 1000000000, "failed", "memory", .0919698602928605804, true}, {"tau_unknown", 1000000000, "unknown", "unattributable", .1839397205857211608, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			s, i := scalarFixtureActive(t, cfg)
			e := scalarOutcomeEvent(t, s, i, 10+tc.dt, tc.outcome, tc.attribution)
			r, err := ApplyEvent(s, i, e, cfg)
			if err != nil {
				t.Fatal(err)
			}
			scalarAssertNear(t, r.State.Value, tc.want, cfg)
			if r.State.StateRevision != 2 || *r.State.LastAppliedAtNS != e.AcceptedAtNS || *r.State.LastAppliedSeq != 2 {
				t.Fatal("zero numeric jump must still advance applied revision/last")
			}
			if (len(r.Receipts.EvidenceConsumptions) == 1) != tc.consume {
				t.Fatal("evidence consumption predicate differs")
			}
			if err = ValidateTyped("TransitionV2", r); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestTransitionExplicitStrengthProduct(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome Outcome
		want    float64
	}{{"passed", "passed", .515625}, {"failed", "failed", .46875}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			cfg.UnitStrength = .25
			cfg = sealFixture(t, "DynamicsConfigV2", cfg)
			s, i := scalarFixtureActive(t, cfg)
			e := scalarOutcomeEvent(t, s, i, 10, tc.outcome, "memory")
			e.Strength = scalarPointer(.5)
			e = sealFixture(t, "EventV2", e)
			r, err := ApplyEvent(s, i, e, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if r.State.Value != tc.want {
				t.Fatalf("wanted exact product-u literal %g, got %g", tc.want, r.State.Value)
			}
		})
	}
}
func TestTransitionNumericZeroStillConsumesQualifiedEvidence(t *testing.T) {
	for _, name := range []string{"eta_zero", "noout", "unit_zero"} {
		t.Run(name, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			switch name {
			case "eta_zero":
				cfg.EtaPositive = 0
			case "noout":
				cfg.OutcomeJumpEnabled = false
			case "unit_zero":
				cfg.UnitStrength = 0
			}
			cfg = sealFixture(t, "DynamicsConfigV2", cfg)
			s, i := scalarFixtureActive(t, cfg)
			e := scalarOutcomeEvent(t, s, i, 10, "passed", "memory")
			r, err := ApplyEvent(s, i, e, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if r.State.Value != .5 || r.State.StateRevision != 2 || len(r.Receipts.EvidenceConsumptions) != 1 || r.Receipt.EvidenceConsumptionSHA256 == nil {
				t.Fatal("numeric-zero jump changed consumption qualification")
			}
		})
	}
}
func scalarAuxEvent(t *testing.T, s StateV2, i ReceiptIndexV2, id string, at int64, kind EventKind, reason Reason) EventV2 {
	t.Helper()
	return sealFixture(t, "EventV2", EventV2{Protocol: "dynamics-event-v2", StreamID: i.StreamID, EventID: id, TransportAttemptID: "delivery", Key: s.Key, Kind: kind, ClockDomain: i.ClockDomain, AcceptedAtNS: at, AcceptedSeq: i.Cursor.AcceptedSeq + 1, Outcome: "not_applicable", Attribution: "not_applicable", Reason: reason})
}
func TestTransitionRetryAfterOtherEventsNoMutation(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i := scalarFixtureActive(t, cfg)
	e := scalarOutcomeEvent(t, s, i, 10, "passed", "memory")
	first, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	expose := scalarAuxEvent(t, first.State, first.Receipts, "exposure", 20, "exposure", "exposure_only")
	later, err := ApplyEvent(first.State, first.Receipts, expose, cfg)
	if err != nil {
		t.Fatal(err)
	}
	retry := e
	retry.TransportAttemptID = "retry"
	retry.ArrivedAtNS = scalarPointer(int64(10))
	retry = sealFixture(t, "EventV2", retry)
	result, err := ApplyEvent(later.State, later.Receipts, retry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptDisposition != "duplicate_event" || !reflect.DeepEqual(result.Receipt, first.Receipt) || !reflect.DeepEqual(result.State, later.State) || !reflect.DeepEqual(result.Receipts, later.Receipts) {
		t.Fatal("exact retry rewrote original/current state")
	}
	for _, change := range []func(*EventV2){func(x *EventV2) { x.AcceptedSeq++ }, func(x *EventV2) { x.AcceptedAtNS++ }, func(x *EventV2) { x.Strength = scalarPointer(.5) }} {
		v := e
		change(&v)
		v = sealFixture(t, "EventV2", v)
		_, err = ApplyEvent(later.State, later.Receipts, v, cfg)
		requireCode(t, err, "event_conflict")
	}
}
func TestTransitionEvidenceRepackagingNoDecay(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i := scalarFixtureActive(t, cfg)
	e := scalarOutcomeEvent(t, s, i, 10, "passed", "memory")
	first, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	newID := e
	newID.EventID = "repackaged"
	newID.AcceptedAtNS = 1000000010
	newID.AcceptedSeq = 3
	newID = sealFixture(t, "EventV2", newID)
	r, err := ApplyEvent(first.State, first.Receipts, newID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.State, first.State) || r.Cursor.AcceptedSeq != 3 || len(r.Receipts.Receipts) != 3 || len(r.Receipts.EvidenceConsumptions) != 1 || r.Receipt.DuplicateOfReceiptSHA256 == nil || *r.Receipt.DuplicateOfReceiptSHA256 != first.Receipt.ReceiptSHA256 || r.Receipt.Disposition != "non_learning" {
		t.Fatal("same evidence caused decay/jump or missed unique audit receipt")
	}
	changed := newID
	changed.EventID = "changed"
	changed.Outcome = "failed"
	changed = sealFixture(t, "EventV2", changed)
	_, err = ApplyEvent(first.State, first.Receipts, changed, cfg)
	requireCode(t, err, "evidence_conflict")
	retry, err := ApplyEvent(r.State, r.Receipts, newID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retry.Receipt, r.Receipt) || !reflect.DeepEqual(retry.Receipts, r.Receipts) {
		t.Fatal("new-id receipt retry did not preserve its own original receipt")
	}
}
func TestTransitionUnknownBeforeFirstQualifiedOutcome(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i := scalarFixtureActive(t, cfg)
	u := scalarOutcomeEvent(t, s, i, 10, "unknown", "unattributable")
	unknown, err := ApplyEvent(s, i, u, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown.Receipts.EvidenceConsumptions) != 0 {
		t.Fatal("unknown consumed evidence")
	}
	pass := u
	pass.EventID = "known_result"
	pass.AcceptedSeq = 3
	pass.Outcome = "passed"
	pass.Attribution = "memory"
	pass = sealFixture(t, "EventV2", pass)
	known, err := ApplyEvent(unknown.State, unknown.Receipts, pass, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if known.State.Value != .625 || known.State.StateRevision != 3 || len(known.Receipts.EvidenceConsumptions) != 1 {
		t.Fatal("unknown precluded first qualified learning")
	}
}
func TestTransitionInvalidationCatalogAndAudit(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i := scalarFixtureActive(t, cfg)
	e := scalarAuxEvent(t, s, i, "invalidate", 20, "invalidate", "source_deleted")
	r, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.State.Status != "invalidated" || r.State.StateRevision != 2 || r.State.Value != s.Value || *r.State.LastAppliedAtNS != *s.LastAppliedAtNS || *r.State.LastAppliedSeq != *s.LastAppliedSeq || r.Receipts.LifecycleFence.Epoch != i.LifecycleFence.Epoch+1 || r.Receipts.LifecycleFence.Entries[0].Reason != "source_deleted" {
		t.Fatal("invalidation changed scalar coordinates or failed catalog mutation")
	}
	again := scalarAuxEvent(t, r.State, r.Receipts, "again", 30, "invalidate", "source_revoked")
	audit, err := ApplyEvent(r.State, r.Receipts, again, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(audit.State, r.State) || !reflect.DeepEqual(audit.Receipts.LifecycleFence, r.Receipts.LifecycleFence) || audit.Receipt.Disposition != "non_learning" || audit.Cursor.AcceptedSeq != 3 {
		t.Fatal("already-invalidated audit modified catalog/state")
	}
	retry, err := ApplyEvent(audit.State, audit.Receipts, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retry.State, audit.State) || !reflect.DeepEqual(retry.Receipt, r.Receipt) {
		t.Fatal("old invalidate retry changed current state")
	}
}
func TestTransitionImmutabilityAndOwnedOutputs(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i := scalarFixtureActive(t, cfg)
	e := scalarOutcomeEvent(t, s, i, 10, "passed", "memory")
	beforeS, _ := CanonicalBytes("StateV2", s)
	beforeI, _ := CanonicalBytes("ReceiptIndexV2", i)
	beforeE, _ := CanonicalBytes("EventV2", e)
	r, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	*r.State.LastAppliedAtNS = 99
	r.Receipts.LifecycleFence.Entries[0].Reason = "source_deleted"
	r.Receipts.Receipts[0].EventID = "mutated"
	afterS, _ := CanonicalBytes("StateV2", s)
	afterI, _ := CanonicalBytes("ReceiptIndexV2", i)
	afterE, _ := CanonicalBytes("EventV2", e)
	if !bytes.Equal(beforeS, afterS) || !bytes.Equal(beforeI, afterI) || !bytes.Equal(beforeE, afterE) {
		t.Fatal("output mutation aliases input")
	}
}

func TestTransitionNewBoundaryRegressions(t *testing.T) {
	t.Run("fresh_arrival_after_acceptance", func(t *testing.T) {
		cfg := fixtureConfig(t, "B2")
		s, i := scalarFixtureActive(t, cfg)
		e := scalarOutcomeEvent(t, s, i, 20, "passed", "memory")
		e.ArrivedAtNS = scalarPointer(int64(21))
		e = sealFixture(t, "EventV2", e)
		if err := ValidateTyped("EventV2", e); err != nil {
			t.Fatal("generic Event metadata must allow late transport", err)
		}
		_, err := ApplyEvent(s, i, e, cfg)
		requireCode(t, err, "future_event")
	})
	t.Run("exposure_foreign_task_metadata", func(t *testing.T) {
		cfg := fixtureConfig(t, "B2")
		s, i := scalarFixtureActive(t, cfg)
		e := scalarOutcomeEvent(t, s, i, 20, "not_applicable", "not_applicable")
		e.Kind = "exposure"
		e.Strength = nil
		e.Evidence.TaskSHA256 = fixtureSHA("foreign task body")
		*e.Evidence = sealFixture(t, "EvidenceBinding", *e.Evidence)
		e = sealFixture(t, "EventV2", e)
		_, err := ApplyEvent(s, i, e, cfg)
		requireCode(t, err, "hash_mismatch")
	})
}
func TestTransitionHistoricalLateTransportRetry(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i := scalarFixtureActive(t, cfg)
	e := scalarOutcomeEvent(t, s, i, 10, "passed", "memory")
	first, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	retry := e
	retry.TransportAttemptID = "late_retry"
	retry.ArrivedAtNS = scalarPointer(int64(100))
	retry = sealFixture(t, "EventV2", retry)
	result, err := ApplyEvent(first.State, first.Receipts, retry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptDisposition != "duplicate_event" || !reflect.DeepEqual(result.State, first.State) || !reflect.DeepEqual(result.Receipts, first.Receipts) || !reflect.DeepEqual(result.Receipt, first.Receipt) {
		t.Fatal("late transport retry changed history")
	}
}

func TestTransitionTargetClockNotGlobalCursor(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	cfg.TauNS = 100
	cfg = sealFixture(t, "DynamicsConfigV2", cfg)
	pending, index, activate, _ := scalarFixturePendingConfig(t, cfg)
	otherKey := pending.Key
	otherKey.Target.ID = "n"
	otherKey.Target.ContentSHA256 = fixtureSHA("other memory")
	index.LifecycleFence.Entries = append(index.LifecycleFence.Entries, FenceEntry{Scope: otherKey.Scope, Target: otherKey.Target, Status: "current", SourceRefs: []EvidenceRef{}, Reason: "none"})
	sort.Slice(index.LifecycleFence.Entries, func(a, b int) bool {
		x, y := index.LifecycleFence.Entries[a], index.LifecycleFence.Entries[b]
		return scopeRefKey(x.Scope, x.Target) < scopeRefKey(y.Scope, y.Target)
	})
	index.LifecycleFence = sealFixture(t, "LifecycleFence", index.LifecycleFence)
	index = sealFixture(t, "ReceiptIndexV2", index)
	first, err := ApplyEvent(pending, index, activate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	other, e := scalarSeal[StateV2]("StateV2", StateV2HashProjection{Key: otherKey, Mode: "scalar", Value: .5, Status: "pending_activation", Reason: "none"})
	if e != nil {
		t.Fatal(e)
	}
	otherActivate := scalarAuxEvent(t, other, first.Receipts, "other_activation", 100, "activate", "activation")
	second, err := ApplyEvent(other, first.Receipts, otherActivate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	unknown := scalarAuxEvent(t, first.State, second.Receipts, "unknown", 110, "outcome", "unknown_outcome")
	unknown.Outcome = "unknown"
	unknown.Attribution = "unattributable"
	unknown = sealFixture(t, "EventV2", unknown)
	result, err := ApplyEvent(first.State, second.Receipts, unknown, cfg)
	if err != nil {
		t.Fatal(err)
	}
	scalarAssertNear(t, result.State.Value, .1839397205857211608, cfg)
	if *result.State.LastAppliedAtNS != 110 {
		t.Fatal("target scalar clock was not advanced")
	}
}
func TestTransitionOneNanosecondAtLargeAbsoluteTime(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	cfg.TauNS = 1
	cfg = sealFixture(t, "DynamicsConfigV2", cfg)
	s, i, e, _ := scalarFixturePendingConfig(t, cfg)
	e.AcceptedAtNS = math.MaxInt64 - 1
	e = sealFixture(t, "EventV2", e)
	active, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	unknown := scalarAuxEvent(t, active.State, active.Receipts, "unknown", math.MaxInt64, "outcome", "unknown_outcome")
	unknown.Outcome = "unknown"
	unknown.Attribution = "unattributable"
	unknown = sealFixture(t, "EventV2", unknown)
	result, err := ApplyEvent(active.State, active.Receipts, unknown, cfg)
	if err != nil {
		t.Fatal(err)
	}
	scalarAssertNear(t, result.State.Value, .1839397205857211608, cfg)
}
func TestTransitionR4AdapterLiteralAndZeroToleranceDiagnostic(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	cfg.TauNS = 1
	cfg.Atol = 0
	cfg.Rtol = 0
	cfg = sealFixture(t, "DynamicsConfigV2", cfg)
	s, i := scalarFixtureActive(t, cfg)
	e := scalarOutcomeEvent(t, s, i, 13, "unknown", "unattributable")
	r, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// This is an independent mathematical literal, not generated with the adapter.
	const idealRounded = .0248935341839319714896712078250308883158497960942116
	difference := math.Abs(r.State.Value - idealRounded)
	covered := difference <= cfg.Atol+cfg.Rtol*math.Abs(idealRounded)
	t.Logf("ZERO_TOLERANCE_DIAGNOSTIC got=%.18g idealRounded=%.18g abs_error=%.18g atol=0 rtol=0 covered=%t", r.State.Value, idealRounded, difference, covered)
	if cfg.Atol != 0 || cfg.Rtol != 0 {
		t.Fatal("caller tolerance changed")
	}
	// A separate explicitly requested regression tolerance covers the same oracle.
	if difference > 1e-12+1e-12*math.Abs(idealRounded) {
		t.Fatal("fixed engineering tolerance does not cover adapter; report instead of changing oracle")
	}
}
func TestTransitionGuardsAndFailureImmutability(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*StateV2, *ReceiptIndexV2, *EventV2, *DynamicsConfigV2)
		code   string
	}{
		{"scope", func(s *StateV2, i *ReceiptIndexV2, e *EventV2, c *DynamicsConfigV2) { e.Key.Scope.User = "other" }, "scope_mismatch"},
		{"new_old_revision", func(s *StateV2, i *ReceiptIndexV2, e *EventV2, c *DynamicsConfigV2) {
			*s, *i = scalarFixtureRevisedActive(t, *c)
			e.AcceptedSeq = i.Cursor.AcceptedSeq + 1
		}, "stale_target"},
		{"clock_domain", func(s *StateV2, i *ReceiptIndexV2, e *EventV2, c *DynamicsConfigV2) { e.ClockDomain = "other" }, "clock_domain_mismatch"},
		{"sequence_gap", func(s *StateV2, i *ReceiptIndexV2, e *EventV2, c *DynamicsConfigV2) { e.AcceptedSeq = 3 }, "sequence_regression"},
		{"time_regression", func(s *StateV2, i *ReceiptIndexV2, e *EventV2, c *DynamicsConfigV2) {
			e.AcceptedAtNS = 9
			e.ObservedAtNS = nil
		}, "time_regression"},
		{"unsupported_fastslow", func(s *StateV2, i *ReceiptIndexV2, e *EventV2, c *DynamicsConfigV2) { c.Mode = "fastslow" }, "unsupported_mode"},
		{"config_rule", func(s *StateV2, i *ReceiptIndexV2, e *EventV2, c *DynamicsConfigV2) {
			e.Task.RuleSHA256 = fixtureSHA("foreign rule")
			e.Evidence.RuleSHA256 = e.Task.RuleSHA256
			*e.Evidence = sealFixture(t, "EvidenceBinding", *e.Evidence)
		}, "hash_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureConfig(t, "B2")
			s, i := scalarFixtureActive(t, c)
			e := scalarOutcomeEvent(t, s, i, 20, "passed", "memory")
			tc.mutate(&s, &i, &e, &c)
			e = sealFixture(t, "EventV2", e)
			beforeS := scalarClone(s)
			beforeI := scalarClone(i)
			beforeE := scalarClone(e)
			_, err := ApplyEvent(s, i, e, c)
			requireCode(t, err, tc.code)
			if !reflect.DeepEqual(s, beforeS) || !reflect.DeepEqual(i, beforeI) || !reflect.DeepEqual(e, beforeE) {
				t.Fatal("failed transition mutated inputs")
			}
		})
	}
}
func TestTransitionCatalogRefPreservationAndOverflow(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i, e, _ := scalarFixturePendingConfig(t, cfg)
	refs := []EvidenceRef{{Namespace: "fixture", Collection: "sources", ID: "s", Revision: 3, ContentSHA256: fixtureSHA("source exact revision")}}
	i.LifecycleFence.Entries[0].SourceRefs = refs
	i.LifecycleFence = sealFixture(t, "LifecycleFence", i.LifecycleFence)
	i = sealFixture(t, "ReceiptIndexV2", i)
	active, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	invalidate := scalarAuxEvent(t, active.State, active.Receipts, "invalidate", 20, "invalidate", "source_deleted")
	r, err := ApplyEvent(active.State, active.Receipts, invalidate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Receipts.LifecycleFence.Entries[0].SourceRefs, refs) || r.Receipts.LifecycleFence.MinimumAcceptedSeq != active.Receipts.LifecycleFence.MinimumAcceptedSeq {
		t.Fatal("catalog refs/minimum were synthesized or compacted")
	}
	r.Receipts.LifecycleFence.Entries[0].SourceRefs[0].ID = "modified_output"
	if active.Receipts.LifecycleFence.Entries[0].SourceRefs[0].ID != "s" {
		t.Fatal("nested source refs alias input")
	}
	index := scalarClone(active.Receipts)
	index.LifecycleFence.Epoch = math.MaxInt64
	index.LifecycleFence = sealFixture(t, "LifecycleFence", index.LifecycleFence)
	index = sealFixture(t, "ReceiptIndexV2", index)
	before := scalarClone(index)
	_, err = ApplyEvent(active.State, index, invalidate, cfg)
	requireCode(t, err, "resource_limit")
	if !reflect.DeepEqual(index, before) {
		t.Fatal("epoch overflow changed input")
	}
}
func TestTransitionPendingInvalidateAndInvalidStateUse(t *testing.T) {
	c := fixtureConfig(t, "B2")
	p, i, e, _ := scalarFixturePendingConfig(t, c)
	e.Kind = "invalidate"
	e.Reason = "invalidated"
	e = sealFixture(t, "EventV2", e)
	_, err := ApplyEvent(p, i, e, c)
	requireCode(t, err, "invalid_state")
	s, i := scalarFixtureActive(t, c)
	inv := scalarAuxEvent(t, s, i, "invalidate", 20, "invalidate", "invalidated")
	dead, err := ApplyEvent(s, i, inv, c)
	if err != nil {
		t.Fatal(err)
	}
	outcome := scalarOutcomeEvent(t, dead.State, dead.Receipts, 30, "passed", "memory")
	_, err = ApplyEvent(dead.State, dead.Receipts, outcome, c)
	requireCode(t, err, "invalidated_target")
}

func TestTransitionRejectStaleStateWithinAcceptedPrefix(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	t.Run("old_pending_reactivates_existing_key", func(t *testing.T) {
		pending, i, e, _ := scalarFixturePendingConfig(t, cfg)
		active, err := ApplyEvent(pending, i, e, cfg)
		if err != nil {
			t.Fatal(err)
		}
		again := scalarAuxEvent(t, pending, active.Receipts, "another_activation", 20, "activate", "activation")
		_, err = ApplyEvent(pending, active.Receipts, again, cfg)
		requireCode(t, err, "snapshot_binding")
	})
	t.Run("old_scalar_misses_latest_own_event", func(t *testing.T) {
		old, i := scalarFixtureActive(t, cfg)
		first := scalarOutcomeEvent(t, old, i, 20, "unknown", "unattributable")
		newer, err := ApplyEvent(old, i, first, cfg)
		if err != nil {
			t.Fatal(err)
		}
		next := scalarOutcomeEvent(t, old, newer.Receipts, 30, "unknown", "unattributable")
		next.EventID = "next"
		next = sealFixture(t, "EventV2", next)
		_, err = ApplyEvent(old, newer.Receipts, next, cfg)
		requireCode(t, err, "snapshot_binding")
	})
}

func TestTransitionInitialOnlyHistoryPinsInitialValue(t *testing.T) {
	for _, kind := range []string{"activation_only", "after_exposure", "after_invalidation"} {
		t.Run(kind, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			s, i := scalarFixtureActive(t, cfg)
			if kind != "activation_only" {
				eventKind := EventKind("exposure")
				reason := Reason("exposure_only")
				if kind == "after_invalidation" {
					eventKind = "invalidate"
					reason = "invalidated"
				}
				e := scalarAuxEvent(t, s, i, "audit", 20, eventKind, reason)
				r, err := ApplyEvent(s, i, e, cfg)
				if err != nil {
					t.Fatal(err)
				}
				s, i = r.State, r.Receipts
			}
			s.Value = .75
			s = sealFixture(t, "StateV2", s)
			e := scalarAuxEvent(t, s, i, "next", 30, "exposure", "exposure_only")
			if s.Status == "invalidated" {
				e.Kind = "invalidate"
				e.Reason = "invalidated"
				e = sealFixture(t, "EventV2", e)
			}
			_, err := ApplyEvent(s, i, e, cfg)
			requireCode(t, err, "snapshot_binding")
		})
	}
}

func TestTransitionSemanticsArtifactBinding(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/research/memory-harness/scalar_transition_semantics_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := fixtureSHA(string(raw)); got != ScalarSemanticsSHA256 {
		t.Fatalf("scalar semantics raw hash = %s, compiled pin = %s", got, ScalarSemanticsSHA256)
	}
}

func TestTransitionConsumedClaimPrecedesNewCoordinates(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, i := scalarFixtureActive(t, cfg)
	e := scalarOutcomeEvent(t, s, i, 10, "passed", "memory")
	first, err := ApplyEvent(s, i, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	changed := e
	changed.EventID = "new-conflicting"
	changed.Strength = scalarPointer(.5)
	changed = sealFixture(t, "EventV2", changed)
	_, err = ApplyEvent(first.State, first.Receipts, changed, cfg)
	requireCode(t, err, "evidence_conflict")
	duplicate := e
	duplicate.EventID = "new-duplicate"
	duplicate = sealFixture(t, "EventV2", duplicate)
	_, err = ApplyEvent(first.State, first.Receipts, duplicate, cfg)
	requireCode(t, err, "sequence_regression")
}
func TestTransitionStateReasonMatchesAppliedOrFence(t *testing.T) {
	for _, kind := range []string{"activation", "passed", "invalidated"} {
		t.Run(kind, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			s, i := scalarFixtureActive(t, cfg)
			if kind == "passed" {
				e := scalarOutcomeEvent(t, s, i, 10, "passed", "memory")
				o, err := ApplyEvent(s, i, e, cfg)
				if err != nil {
					t.Fatal(err)
				}
				s, i = o.State, o.Receipts
			}
			if kind == "invalidated" {
				e := scalarAuxEvent(t, s, i, "delete", 11, "invalidate", "source_deleted")
				o, err := ApplyEvent(s, i, e, cfg)
				if err != nil {
					t.Fatal(err)
				}
				s, i = o.State, o.Receipts
			}
			s.Reason = "source_unknown"
			s = sealFixture(t, "StateV2", s)
			e := scalarAuxEvent(t, s, i, "after", 12, "exposure", "exposure_only")
			if kind == "invalidated" {
				e = scalarAuxEvent(t, s, i, "after", 12, "invalidate", "source_deleted")
			}
			_, err := ApplyEvent(s, i, e, cfg)
			requireCode(t, err, "snapshot_binding")
		})
	}
}

func TestTransitionEveryKeyHistoryIsStructurallyContinuous(t *testing.T) {
	for _, kind := range []string{"revision_gap", "second_activation", "other_key_gap"} {
		t.Run(kind, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			s, i := scalarFixtureActive(t, cfg)
			e := scalarOutcomeEvent(t, s, i, 11, "unknown", "unattributable")
			o, err := ApplyEvent(s, i, e, cfg)
			if err != nil {
				t.Fatal(err)
			}
			receipt := o.Receipt
			if kind == "second_activation" {
				receipt.BeforeStateRevision = 0
				receipt.AfterStateRevision = 1
				receipt.Reason = "activation"
				o.State.StateRevision = 1
				o.State.Value = cfg.Initial
				o.State.Reason = "activation"
				o.State.ActivatedAtNS = scalarPointer(e.AcceptedAtNS)
				o.State.ActivationAcceptedSeq = scalarPointer(e.AcceptedSeq)
				o.State.ActivationEventID = scalarPointer(e.EventID)
				o.State.ActivationPayloadSHA256 = scalarPointer(e.PayloadSHA256)
			} else {
				receipt.BeforeStateRevision = 4
				receipt.AfterStateRevision = 5
				o.State.StateRevision = 5
			}
			receipt = sealFixture(t, "ReceiptV2", receipt)
			o.Receipts.Receipts[1] = receipt
			o.Receipts = sealFixture(t, "ReceiptIndexV2", o.Receipts)
			o.State = sealFixture(t, "StateV2", o.State)
			if kind == "other_key_gap" {
				target := o.State.Key.Target
				target.Revision++
				target.ContentSHA256 = fixtureSHA("other current")
				o.State, err = ResetRevision(o.State, target, cfg)
				if err != nil {
					t.Fatal(err)
				}
				_, err = ApplyEvent(o.State, o.Receipts, e, cfg)
			} else {
				next := scalarAuxEvent(t, o.State, o.Receipts, "after", 12, "exposure", "exposure_only")
				_, err = ApplyEvent(o.State, o.Receipts, next, cfg)
			}
			requireCode(t, err, "snapshot_binding")
		})
	}
}

func TestTransitionStructuralMatrixRejectsRelatedMutations(t *testing.T) {
	for _, kind := range []string{"revision_regression", "contiguous_second_activation", "nonlearning_jump", "second_invalidation"} {
		t.Run(kind, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			state, index := scalarFixtureActive(t, cfg)
			event := scalarAuxEvent(t, state, index, "later", 11, "exposure", "exposure_only")
			if kind == "revision_regression" || kind == "contiguous_second_activation" {
				event = scalarOutcomeEvent(t, state, index, 11, "unknown", "unattributable")
			}
			if kind == "second_invalidation" {
				event = scalarAuxEvent(t, state, index, "delete", 11, "invalidate", "source_deleted")
				invalid, err := ApplyEvent(state, index, event, cfg)
				if err != nil {
					t.Fatal(err)
				}
				state, index = invalid.State, invalid.Receipts
				event = scalarAuxEvent(t, state, index, "delete_audit", 12, "invalidate", "source_deleted")
			}
			out, err := ApplyEvent(state, index, event, cfg)
			if err != nil {
				t.Fatal(err)
			}
			receipt := out.Receipt
			switch kind {
			case "revision_regression":
				receipt.BeforeStateRevision = 0
				receipt.AfterStateRevision = 1
				out.State.StateRevision = 1
			case "contiguous_second_activation":
				receipt.Reason = "activation"
			case "nonlearning_jump":
				receipt.BeforeStateRevision = 4
				receipt.AfterStateRevision = 4
				out.State.StateRevision = 4
			case "second_invalidation":
				receipt.Disposition = "invalidated"
				receipt.AfterStateRevision++
				out.State.StateRevision++
			}
			receipt = sealFixture(t, "ReceiptV2", receipt)
			out.Receipts.Receipts[len(out.Receipts.Receipts)-1] = receipt
			out.Receipts = sealFixture(t, "ReceiptIndexV2", out.Receipts)
			out.State = sealFixture(t, "StateV2", out.State)
			next := scalarAuxEvent(t, out.State, out.Receipts, "probe", 13, "exposure", "exposure_only")
			if kind == "second_invalidation" {
				next = scalarAuxEvent(t, out.State, out.Receipts, "probe", 13, "invalidate", "source_deleted")
			}
			_, err = ApplyEvent(out.State, out.Receipts, next, cfg)
			requireCode(t, err, "snapshot_binding")
		})
	}
}
