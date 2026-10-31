package dynamicsv2

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"
)

func probeRequireError(t *testing.T, view StateViewV2, err error, code, path string) {
	t.Helper()
	var ce *ContractErrorV2
	if !errors.As(err, &ce) || string(ce.Code) != code || ce.Path != path || !reflect.DeepEqual(view, StateViewV2{}) {
		t.Fatalf("got view=%+v error=%v want %s %s", view, err, code, path)
	}
}

func TestReadAtOriginalCommandAndHundredReadOnlyProbes(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	s := replayMust(t, initial, events, 3, cfg)
	before := replayRaw(t, s)
	for i := 0; i < 100; i++ {
		v, err := ReadAt(s, 1000000020, s.States[0].Key.Target, cfg)
		if err != nil {
			t.Fatal(err)
		}
		want := s.States[0].Value / math.E
		scalarAssertNear(t, v.Value, want, cfg)
		if v.Status != "present" || v.Reason != s.States[0].Reason || v.SourceStateRevision != 2 || v.ActivatedAtNS != 10 || v.ActivationAcceptedSeq != 1 || v.SnapshotSHA256 != s.SnapshotSHA256 || v.SnapshotCursor != s.Cursor || v.CutoffAcceptedSeq != 3 || v.Key != s.States[0].Key {
			t.Fatal("view projection/anchor differs")
		}
		if !bytes.Equal(before, replayRaw(t, s)) {
			t.Fatal("probe changed snapshot/receipts/cursor")
		}
	}
}

func TestReadAtNoVirtualMissingOrPastView(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	s := replayMust(t, initial, events, 2, cfg)
	ref := s.States[0].Key.Target
	cases := []struct {
		name       string
		at         int64
		target     RefV2
		code, path string
	}{{"negative", -1, ref, "invalid_time", "$.at_ns"}, {"before_prefix", 19, ref, "time_regression", "$.at_ns"}}
	missing := ref
	missing.Revision++
	cases = append(cases, struct {
		name       string
		at         int64
		target     RefV2
		code, path string
	}{"missing", 20, missing, "missing_state", "$.states[*]"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := replayRaw(t, s)
			v, err := ReadAt(s, c.at, c.target, cfg)
			probeRequireError(t, v, err, c.code, c.path)
			if !bytes.Equal(before, replayRaw(t, s)) {
				t.Fatal("failed read mutated snapshot")
			}
		})
	}
	v, err := ReadAt(initial, 0, ref, cfg)
	probeRequireError(t, v, err, "missing_state", "$.states[*]")
}

func TestReadAtOneNanosecondAtLargeAbsoluteClock(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	activation := events[0]
	activation.AcceptedAtNS = 1 << 60
	activation = sealFixture(t, "EventV2", activation)
	s := replayMust(t, initial, []EventV2{activation}, 1, cfg)
	v, err := ReadAt(s, activation.AcceptedAtNS+1, s.States[0].Key.Target, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Independent direct-tau equation; one ns disappears if absolute clocks
	// are converted before subtraction. R4 adapter rounding remains tolerated.
	want := cfg.Initial * math.Exp(-1/float64(cfg.TauNS))
	scalarAssertNear(t, v.Value, want, cfg)
	if v.Value >= cfg.Initial {
		t.Fatal("one ns lost in float clock subtraction")
	}
	v, err = ReadAt(s, math.MaxInt64, s.States[0].Key.Target, cfg)
	if err != nil || math.IsNaN(v.Value) || v.Value < 0 || v.Value > cfg.Initial {
		t.Fatal("large legal request time overflowed")
	}
}

func TestReadAtDecayDisabledAndNoOutcomePolicyInference(t *testing.T) {
	for _, policy := range []PolicyID{"B2", "V3-P1-NOTIME", "V3-P1-NOOUT", "V3-P1-FD"} {
		t.Run(string(policy), func(t *testing.T) {
			cfg := fixtureConfig(t, policy)
			if cfg.DecayEnabled != (policy != "V3-P1-NOTIME") || cfg.OutcomeJumpEnabled != (policy != "V3-P1-NOOUT" && policy != "V3-P1-FD") {
				t.Fatal("policy fixture flags differ from declared case")
			}
			pending, index, activation, _ := scalarFixturePendingConfig(t, cfg)
			initial := sealFixture(t, "SnapshotV2", SnapshotV2{Protocol: "dynamics-snapshot-v2", Scope: pending.Key.Scope, Dimension: pending.Key.Dimension, ConfigSHA256: cfg.ConfigSHA256, Cursor: index.Cursor, States: []StateV2{}, Receipts: index, StreamID: index.StreamID})
			s := replayMust(t, initial, []EventV2{activation}, 1, cfg)
			v, err := ReadAt(s, 1000000010, s.States[0].Key.Target, cfg)
			if err != nil {
				t.Fatal(err)
			}
			want := cfg.Initial
			if cfg.DecayEnabled {
				want /= math.E
			}
			scalarAssertNear(t, v.Value, want, cfg)
		})
	}
}

func TestReadAtIntermediateProbesDoNotAffectFutureReplay(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	mid := replayMust(t, initial, events, 1, cfg)
	before := replayRaw(t, mid)
	for _, at := range []int64{10, 100, math.MaxInt64} {
		if _, err := ReadAt(mid, at, events[0].Key.Target, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, replayRaw(t, mid)) {
		t.Fatal("intermediate probes wrote state")
	}
	got := replayMust(t, mid, events[1:], 3, cfg)
	want := replayMust(t, initial, events, 3, cfg)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("read changes later replay")
	}
}

func TestReadAtWholeCheckpointMustBeCoherent(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	s := replayMust(t, initial, events, 1, cfg)
	t.Run("forged_value", func(t *testing.T) {
		bad := scalarClone(s)
		bad.States[0].Value = .8
		bad.States[0] = sealFixture(t, "StateV2", bad.States[0])
		bad = replayReseal(t, bad)
		v, err := ReadAt(bad, 10, bad.States[0].Key.Target, cfg)
		probeRequireError(t, v, err, "snapshot_binding", "$.state")
	})
	t.Run("bad_snapshot_hash", func(t *testing.T) {
		bad := scalarClone(s)
		bad.SnapshotSHA256 = fixtureSHA("wrong")
		v, err := ReadAt(bad, 10, bad.States[0].Key.Target, cfg)
		probeRequireError(t, v, err, "hash_mismatch", "$.snapshot_sha256")
	})
	t.Run("config", func(t *testing.T) {
		wrong := cfg
		wrong.Initial = .2
		wrong = sealFixture(t, "DynamicsConfigV2", wrong)
		v, err := ReadAt(s, 10, s.States[0].Key.Target, wrong)
		probeRequireError(t, v, err, "config_mismatch", "$.config_sha256")
	})
	t.Run("namespace", func(t *testing.T) {
		bad := scalarClone(s)
		bad.Receipts.LifecycleFence.Namespace = "operational"
		v, err := ReadAt(bad, 10, bad.States[0].Key.Target, cfg)
		probeRequireError(t, v, err, "unsupported_restore", "$.receipts.lifecycle_fence.namespace")
	})
}

func TestReadAtPendingErrorRespectsActualValidationOrder(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	pending, _, _, _ := scalarFixturePending(t)
	bad := scalarClone(initial)
	bad.States = []StateV2{pending}
	bad = sealFixture(t, "SnapshotV2", bad)
	t.Run("target_pending", func(t *testing.T) {
		v, err := ReadAt(bad, 10, pending.Key.Target, cfg)
		probeRequireError(t, v, err, "pending_activation", "$.states[*]")
	})
	t.Run("other_target", func(t *testing.T) {
		other := pending.Key.Target
		other.ID = "other"
		v, err := ReadAt(bad, 10, other, cfg)
		probeRequireError(t, v, err, "invalid_state", "$.states[*]")
	})
	t.Run("state_bad_hash", func(t *testing.T) {
		s := scalarClone(bad)
		s.States[0].StateSHA256 = fixtureSHA("wrong")
		v, err := ReadAt(s, 10, pending.Key.Target, cfg)
		probeRequireError(t, v, err, "hash_mismatch", "$.states[*].state_sha256")
	})
	t.Run("pending_bad_shape", func(t *testing.T) {
		s := scalarClone(bad)
		s.States[0].StateRevision = 1
		s.States[0] = sealFixture(t, "StateV2", s.States[0])
		v, err := ReadAt(s, 10, pending.Key.Target, cfg)
		probeRequireError(t, v, err, "invalid_state", "$.states[*]")
	})
	t.Run("earlier_namespace", func(t *testing.T) {
		s := scalarClone(bad)
		s.Receipts.LifecycleFence.Namespace = "operational"
		v, err := ReadAt(s, 10, pending.Key.Target, cfg)
		probeRequireError(t, v, err, "unsupported_restore", "$.receipts.lifecycle_fence.namespace")
	})
	t.Run("parent_hash_after_pending", func(t *testing.T) {
		s := scalarClone(bad)
		s.SnapshotSHA256 = fixtureSHA("wrong")
		v, err := ReadAt(s, 10, pending.Key.Target, cfg)
		probeRequireError(t, v, err, "pending_activation", "$.states[*]")
	})
	t.Run("first_pending_other", func(t *testing.T) {
		s := scalarClone(bad)
		other := scalarClone(pending)
		other.Key.Target.ID = "other"
		other = sealFixture(t, "StateV2", other)
		s.States = append(s.States, other)
		s = sealFixture(t, "SnapshotV2", s)
		v, err := ReadAt(s, 10, other.Key.Target, cfg)
		probeRequireError(t, v, err, "invalid_state", "$.states[*]")
	})
	t.Run("earlier_invalid_active", func(t *testing.T) {
		s := replayMust(t, initial, events, 1, cfg)
		s.States[0].StateRevision = 0
		s.States[0] = sealFixture(t, "StateV2", s.States[0])
		s.States = append(s.States, pending)
		s = sealFixture(t, "SnapshotV2", s)
		v, err := ReadAt(s, 10, pending.Key.Target, cfg)
		probeRequireError(t, v, err, "invalid_state", "$.states[*]")
	})
}

func TestReadAtFastSlowReplayedPrefix(t *testing.T) {
	cfg := fsConfig(t)
	pending := FastSlowState{}
	index := FastSlowIndex{}
	activation := FastSlowEvent{}
	pending.Fast, index.Fast, activation.Fast, _ = scalarFixturePendingConfig(t, cfg.Fast)
	pending.Slow, index.Slow, activation.Slow, _ = scalarFixturePendingConfig(t, cfg.Slow)
	initial := func(s StateV2, i ReceiptIndexV2) SnapshotV2 {
		return sealFixture(t, "SnapshotV2", SnapshotV2{Protocol: "dynamics-snapshot-v2", Scope: s.Key.Scope, Dimension: s.Key.Dimension, ConfigSHA256: s.Key.ConfigSHA256, Cursor: i.Cursor, States: []StateV2{}, Receipts: i, StreamID: i.StreamID})
	}
	first, err := ApplyEventFastSlow(pending, index, activation, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s1, i1 := fsNext(first)
	outcome := fsOutcome(t, s1, i1, 1000000010, "passed", "memory")
	events := []FastSlowEvent{activation, outcome}
	fast, slow, err := ReplayPrefixFastSlow(initial(pending.Fast, index.Fast), initial(pending.Slow, index.Slow), events, 2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref := fast.States[0].Key.Target
	before := [][]byte{replayRaw(t, fast), replayRaw(t, slow)}
	for n := 0; n < 10; n++ {
		v, err := ReadAtFastSlow(fast, slow, 3000000010, ref, cfg)
		if err != nil {
			t.Fatal(err)
		}
		scalarAssertNear(t, v.Fast.Value, .08011440871027234, cfg.Fast)
		scalarAssertNear(t, v.Slow.Value, .41524127461457117, cfg.Slow)
		scalarAssertNear(t, v.Value, .3314595581384965, cfg.Fast)
	}
	if !bytes.Equal(before[0], replayRaw(t, fast)) || !bytes.Equal(before[1], replayRaw(t, slow)) {
		t.Fatal("probe changed a lane snapshot")
	}
	// A slow lane cut before the outcome cannot be mixed with a later fast lane.
	_, early, err := ReplayPrefixFastSlow(initial(pending.Fast, index.Fast), initial(pending.Slow, index.Slow), events, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadAtFastSlow(fast, early, 3000000010, ref, cfg)
	requireCode(t, err, "snapshot_binding")
	fsRequirePath(t, err, fastSlowPairPath)
	mixed := []FastSlowEvent{activation, {Fast: outcome.Fast, Slow: scalarOutcomeEvent(t, s1.Slow, i1.Slow, 1000000010, "failed", "memory")}}
	_, _, err = ReplayPrefixFastSlow(initial(pending.Fast, index.Fast), initial(pending.Slow, index.Slow), mixed, 2, cfg)
	requireCode(t, err, "snapshot_binding")
	// Same receipts, different transport attempt: only the pair check sees it.
	observed := outcome
	observed.Slow.TransportAttemptID = "other-attempt"
	observed.Slow = sealFixture(t, "EventV2", observed.Slow)
	_, _, err = ReplayPrefixFastSlow(initial(pending.Fast, index.Fast), initial(pending.Slow, index.Slow), []FastSlowEvent{activation, observed}, 2, cfg)
	requireCode(t, err, "event_conflict")
	fsRequirePath(t, err, fastSlowPairPath)
	// A malformed pair after the cutoff cannot change the cutoff-1 prefix.
	cleanF, cleanS, err := ReplayPrefixFastSlow(initial(pending.Fast, index.Fast), initial(pending.Slow, index.Slow), events[:1], 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range [][]FastSlowEvent{mixed, {activation, observed}} {
		gotF, gotS, err := ReplayPrefixFastSlow(initial(pending.Fast, index.Fast), initial(pending.Slow, index.Slow), tail, 1, cfg)
		if err != nil || gotF.SnapshotSHA256 != cleanF.SnapshotSHA256 || gotS.SnapshotSHA256 != cleanS.SnapshotSHA256 {
			t.Fatalf("future pair changed the prefix: %v", err)
		}
	}
	// Lanes from different scopes never mix, even at the same cursor and target.
	other := slow
	other.Scope.Project = "other"
	_, err = ReadAtFastSlow(fast, other, 3000000010, ref, cfg)
	requireCode(t, err, "snapshot_binding")
	fsRequirePath(t, err, fastSlowPairPath)
	// Replay binds the lanes before any event, so an empty prefix cannot pair scopes.
	otherInitial := initial(pending.Slow, index.Slow)
	otherInitial.Scope.Project = "other"
	otherInitial = sealFixture(t, "SnapshotV2", otherInitial)
	for _, tail := range [][]FastSlowEvent{nil, events} {
		_, _, err = ReplayPrefixFastSlow(initial(pending.Fast, index.Fast), otherInitial, tail, 0, cfg)
		requireCode(t, err, "snapshot_binding")
		fsRequirePath(t, err, fastSlowPairPath)
	}
}
