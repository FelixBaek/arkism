package dynamicsv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func replayFixture(t *testing.T) (SnapshotV2, []EventV2, DynamicsConfigV2) {
	t.Helper()
	pending, index, activation, cfg := scalarFixturePending(t)
	initial := sealFixture(t, "SnapshotV2", SnapshotV2{Protocol: "dynamics-snapshot-v2", Scope: pending.Key.Scope, Dimension: pending.Key.Dimension, ConfigSHA256: cfg.ConfigSHA256, Cursor: index.Cursor, States: []StateV2{}, Receipts: index, StreamID: index.StreamID})
	first, err := ApplyEvent(pending, index, activation, cfg)
	if err != nil {
		t.Fatal(err)
	}
	outcome := scalarOutcomeEvent(t, first.State, first.Receipts, 20, "passed", "memory")
	second, err := ApplyEvent(first.State, first.Receipts, outcome, cfg)
	if err != nil {
		t.Fatal(err)
	}
	exposure := scalarAuxEvent(t, second.State, second.Receipts, "exposure", 30, "exposure", "exposure_only")
	return initial, []EventV2{activation, outcome, exposure}, cfg
}

func replayMust(t *testing.T, initial SnapshotV2, events []EventV2, cutoff int64, cfg DynamicsConfigV2) SnapshotV2 {
	t.Helper()
	out, stats, err := ReplayPrefix(initial, events, cutoff, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Error != nil || stats.RejectedCount != 0 || stats.InputCount != stats.AppliedCount+stats.NonLearningCount+stats.DuplicateEventCount {
		t.Fatalf("invalid success partition: %+v", stats)
	}
	if err = ValidateTyped("SnapshotV2", out); err != nil {
		t.Fatal(err)
	}
	return out
}

func replayAssertError(t *testing.T, out SnapshotV2, stats ReplayStatsV2, err error, code, path string, inputs, rejected int64) {
	t.Helper()
	var ce *ContractErrorV2
	if !errors.As(err, &ce) || string(ce.Code) != code || ce.Path != path {
		t.Fatalf("got %v, want %s %s", err, code, path)
	}
	if stats.Error == nil || *stats.Error != *ce || stats.InputCount != inputs || stats.RejectedCount != rejected || !reflect.DeepEqual(out, SnapshotV2{}) {
		t.Fatalf("error output: %+v, %+v", out, stats)
	}
	if stats.InputCount != stats.AppliedCount+stats.NonLearningCount+stats.DuplicateEventCount+stats.RejectedCount {
		t.Fatal("error partition")
	}
	if err = ValidateTyped("ReplayStatsV2", stats); err != nil {
		t.Fatal(err)
	}
}

func replayRaw(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func replayReseal(t *testing.T, s SnapshotV2) SnapshotV2 {
	t.Helper()
	sort.Slice(s.States, func(i, j int) bool {
		a, _ := canonical(reflect.ValueOf(s.States[i].Key))
		b, _ := canonical(reflect.ValueOf(s.States[j].Key))
		return string(a) < string(b)
	})
	s.Receipts.LifecycleFence = sealFixture(t, "LifecycleFence", s.Receipts.LifecycleFence)
	s.Receipts = sealFixture(t, "ReceiptIndexV2", s.Receipts)
	return sealFixture(t, "SnapshotV2", s)
}

func replayAddCatalog(t *testing.T, s SnapshotV2, ref RefV2) SnapshotV2 {
	t.Helper()
	s = scalarClone(s)
	s.Receipts.LifecycleFence.Entries = append(s.Receipts.LifecycleFence.Entries, FenceEntry{Scope: s.Scope, Target: ref, Status: "current", SourceRefs: []EvidenceRef{}, Reason: "none"})
	sort.Slice(s.Receipts.LifecycleFence.Entries, func(i, j int) bool {
		a := s.Receipts.LifecycleFence.Entries[i]
		b := s.Receipts.LifecycleFence.Entries[j]
		return scopeRefKey(a.Scope, a.Target) < scopeRefKey(b.Scope, b.Target)
	})
	return replayReseal(t, s)
}

func replayActivation(t *testing.T, original EventV2, ref RefV2, id string, seq, at int64) EventV2 {
	t.Helper()
	e := scalarClone(original)
	e.Key.Target = ref
	e.EventID = id
	e.AcceptedSeq = seq
	e.AcceptedAtNS = at
	return sealFixture(t, "EventV2", e)
}

func TestReplayPrefixOriginalCommandAndPins(t *testing.T) {
	b, err := os.ReadFile("../../../docs/research/memory-harness/scalar_replay_probe_semantics_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if fixtureSHA(string(b)) != ReplayProbeSemanticsSHA256 {
		t.Fatal("replay supplement mismatch")
	}
	initial, events, cfg := replayFixture(t)
	for cutoff := int64(0); cutoff <= 3; cutoff++ {
		t.Run(string(rune('0'+cutoff)), func(t *testing.T) {
			s := replayMust(t, initial, events, cutoff, cfg)
			if s.Cursor.AcceptedSeq != cutoff || len(s.Receipts.Receipts) != int(cutoff) {
				t.Fatal("wrong inclusive prefix")
			}
			again := replayMust(t, initial, events, cutoff, cfg)
			if !bytes.Equal(replayRaw(t, s), replayRaw(t, again)) {
				t.Fatal("nonreproducible snapshot")
			}
		})
	}
}

func TestReplayPrefixFullResumeAndReferenceComposition(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	full := replayMust(t, initial, events, 3, cfg)
	for cutoff := int64(0); cutoff <= 3; cutoff++ {
		t.Run(string(rune('0'+cutoff)), func(t *testing.T) {
			checkpoint := replayMust(t, initial, events, cutoff, cfg)
			for _, suffix := range [][]EventV2{events[cutoff:], events} {
				resumed := replayMust(t, checkpoint, suffix, 3, cfg)
				if !bytes.Equal(replayRaw(t, full), replayRaw(t, resumed)) {
					t.Fatal("full/resume wire bytes differ")
				}
			}
		})
	}
	pending, idx, _, _ := scalarFixturePending(t)
	state := pending
	for _, e := range events {
		r, err := ApplyEvent(state, idx, e, cfg)
		if err != nil {
			t.Fatal(err)
		}
		state, idx = r.State, r.Receipts
	}
	want := sealFixture(t, "SnapshotV2", SnapshotV2{Protocol: "dynamics-snapshot-v2", Scope: initial.Scope, Dimension: initial.Dimension, ConfigSHA256: cfg.ConfigSHA256, Cursor: idx.Cursor, States: []StateV2{state}, Receipts: idx, StreamID: idx.StreamID})
	if !bytes.Equal(replayRaw(t, full), replayRaw(t, want)) {
		t.Fatal("does not match independent single-transition composition")
	}
}

func TestReplayPrefixFutureBodyIsolation(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	want := replayMust(t, initial, events, 1, cfg)
	future := EventV2{EventID: "valid_future", AcceptedSeq: 2, Protocol: "broken", Strength: scalarPointer(math.NaN())}
	out, stats, err := ReplayPrefix(initial, []EventV2{events[0], future, {EventID: "!", AcceptedSeq: -1}}, 1, cfg)
	if err != nil || !bytes.Equal(replayRaw(t, out), replayRaw(t, want)) || stats.InputCount != 1 {
		t.Fatalf("future body/later headers inspected: %v %+v", err, stats)
	}
	out, stats, err = ReplayPrefix(initial, []EventV2{future}, 1, cfg)
	replayAssertError(t, out, stats, err, "incomplete_prefix", "$.cursor.accepted_seq", 0, 0)
}

func TestReplayPrefixMalformedHeadersAreAttempts(t *testing.T) {
	initial, _, cfg := replayFixture(t)
	cases := []struct {
		name, id   string
		seq        int64
		code, path string
	}{{"empty", "", 99, "invalid_value", "$.event_id"}, {"grammar", "!", 99, "invalid_value", "$.event_id"}, {"unicode", string([]byte{255}), 99, "invalid_unicode", "$.event_id"}, {"size", strings.Repeat("a", 4097), 99, "resource_limit", "$.event_id"}, {"zero", "valid", 0, "invalid_value", "$.accepted_seq"}, {"negative", "valid", -1, "invalid_value", "$.accepted_seq"}, {"ID_first", "!", 0, "invalid_value", "$.event_id"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, stats, err := ReplayPrefix(initial, []EventV2{{EventID: c.id, AcceptedSeq: c.seq}}, 0, cfg)
			replayAssertError(t, out, stats, err, c.code, c.path, 1, 1)
		})
	}
}

func TestReplayPrefixKnownIDFutureConflictNotHidden(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	checkpoint := replayMust(t, initial, events, 1, cfg)
	for _, seq := range []int64{2, math.MaxInt64} {
		t.Run(string(rune('a'+seq%2)), func(t *testing.T) {
			forged := events[0]
			forged.AcceptedSeq = seq
			forged = sealFixture(t, "EventV2", forged)
			out, stats, err := ReplayPrefix(checkpoint, []EventV2{forged}, 1, cfg)
			replayAssertError(t, out, stats, err, "event_conflict", "$.event_id", 1, 1)
		})
	}
	retry := events[0]
	retry.TransportAttemptID = "later"
	retry.ArrivedAtNS = scalarPointer(int64(1000))
	retry = sealFixture(t, "EventV2", retry)
	out, stats, err := ReplayPrefix(checkpoint, []EventV2{retry}, 1, cfg)
	if err != nil || stats.DuplicateEventCount != 1 || stats.InputCount != 1 || !reflect.DeepEqual(out, checkpoint) {
		t.Fatalf("historical transport retry rejected/changed: %v %+v", err, stats)
	}
}

func TestReplayPrefixMissingReversedAndFutureCheckpoint(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	t.Run("missing_cutoff", func(t *testing.T) {
		out, stats, err := ReplayPrefix(initial, events[:1], 2, cfg)
		replayAssertError(t, out, stats, err, "incomplete_prefix", "$.cursor.accepted_seq", 1, 0)
	})
	t.Run("reverse", func(t *testing.T) {
		s := replayMust(t, initial, events, 1, cfg)
		e := events[2]
		e.AcceptedSeq = 3
		out, stats, err := ReplayPrefix(s, []EventV2{e, events[1]}, 3, cfg)
		replayAssertError(t, out, stats, err, "sequence_regression", "$.accepted_seq", 1, 1)
	})
	t.Run("future_checkpoint", func(t *testing.T) {
		s := replayMust(t, initial, events, 2, cfg)
		out, stats, err := ReplayPrefix(s, []EventV2{}, 1, cfg)
		replayAssertError(t, out, stats, err, "prefix_mismatch", "$.cursor.accepted_seq", 0, 0)
	})
	t.Run("negative_cutoff", func(t *testing.T) {
		out, stats, err := ReplayPrefix(initial, []EventV2{}, -1, cfg)
		replayAssertError(t, out, stats, err, "invalid_value", "$.cutoff_accepted_seq", 0, 0)
	})
	t.Run("time_reverse", func(t *testing.T) {
		s := replayMust(t, initial, events, 1, cfg)
		e := scalarAuxEvent(t, s.States[0], s.Receipts, "backtime", 9, "exposure", "exposure_only")
		out, stats, err := ReplayPrefix(s, []EventV2{e}, 2, cfg)
		replayAssertError(t, out, stats, err, "time_regression", "$.accepted_at_ns", 1, 1)
	})
}

func TestReplayPrefixSameTimeInvalidationCutoffs(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	active := replayMust(t, initial, events, 1, cfg)
	inv := scalarAuxEvent(t, active.States[0], active.Receipts, "invalidate", 10, "invalidate", "source_deleted")
	before := replayMust(t, initial, []EventV2{events[0], inv}, 1, cfg)
	after, stats, err := ReplayPrefix(initial, []EventV2{events[0], inv}, 2, cfg)
	if err != nil || stats.AppliedCount != 2 || before.States[0].Status != "active" || after.States[0].Status != "invalidated" || after.Receipts.LifecycleFence.Epoch != 2 {
		t.Fatalf("same-time lifecycle cutoff: %v %+v", err, stats)
	}
	view, err := ReadAt(before, 10, before.States[0].Key.Target, cfg)
	if err != nil || view.CutoffAcceptedSeq != 1 {
		t.Fatal(err)
	}
	_, err = ReadAt(after, 10, after.States[0].Key.Target, cfg)
	requireCode(t, err, "invalid_state")
}

func TestReplayPrefixStatsPartitionAndDuplicateEvidenceOverlap(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	s := replayMust(t, initial, events, 2, cfg)
	duplicate := scalarClone(events[1])
	duplicate.EventID = "same_evidence"
	duplicate.AcceptedSeq = 3
	duplicate.AcceptedAtNS = 30
	duplicate = sealFixture(t, "EventV2", duplicate)
	dup, err := ApplyEvent(s.States[0], s.Receipts, duplicate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	exposure := scalarAuxEvent(t, dup.State, dup.Receipts, "expose", 40, "exposure", "exposure_only")
	input := []EventV2{events[0], events[0], events[1], duplicate, duplicate, exposure}
	out, stats, err := ReplayPrefix(initial, input, 4, cfg)
	if err != nil || stats.InputCount != 6 || stats.AppliedCount != 2 || stats.DuplicateEventCount != 2 || stats.DuplicateEvidenceCount != 1 || stats.NonLearningCount != 2 || len(out.Receipts.Receipts) != 4 || len(out.Receipts.EvidenceConsumptions) != 1 {
		t.Fatalf("counts/dedup: %v %+v", err, stats)
	}
	if stats.InputCount != stats.AppliedCount+stats.DuplicateEventCount+stats.NonLearningCount {
		t.Fatal("overlap double counted")
	}
	bad := scalarAuxEvent(t, out.States[0], out.Receipts, "gap", 50, "exposure", "exposure_only")
	bad.AcceptedSeq = 6
	bad = sealFixture(t, "EventV2", bad)
	input = append(input, bad, events[0])
	result, failed, err := ReplayPrefix(initial, input, 6, cfg)
	replayAssertError(t, result, failed, err, "sequence_regression", "$.accepted_seq", 7, 1)
}

func TestReplayPrefixDormantCatalogAndRevisionIsolation(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	older := events[0].Key.Target
	newer := older
	newer.Revision++
	newer.ContentSHA256 = fixtureSHA("new revision")
	other := older
	other.ID = "another"
	other.ContentSHA256 = fixtureSHA("another")
	initial = replayAddCatalog(t, initial, newer)
	initial = replayAddCatalog(t, initial, other)
	for _, order := range [][]RefV2{{older, newer, other}, {newer, older, other}} {
		t.Run(order[0].ContentSHA256[:8], func(t *testing.T) {
			input := []EventV2{}
			for i, ref := range order {
				input = append(input, replayActivation(t, events[0], ref, string(rune('a'+i)), int64(i+1), 10))
			}
			full := replayMust(t, initial, input, 3, cfg)
			if len(full.States) != 3 {
				t.Fatal("dormant/older state disappeared")
			}
			for _, state := range full.States {
				if state.Value != cfg.Initial || state.StateRevision != 1 || state.Status != "active" {
					t.Fatal("revision/evidence transfer")
				}
				if _, err := ReadAt(full, 10, state.Key.Target, cfg); err != nil {
					t.Fatal(err)
				}
			}
			mid := replayMust(t, initial, input, 1, cfg)
			resumed := replayMust(t, mid, input[1:], 3, cfg)
			if !reflect.DeepEqual(full, resumed) {
				t.Fatal("multi-key resume differs")
			}
		})
	}
	zero := replayMust(t, initial, []EventV2{}, 0, cfg)
	if len(zero.States) != 0 {
		t.Fatal("catalog eagerly created states")
	}
}

func TestReplayPrefixUnseenAndAliasTargets(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	t.Run("missing_catalog", func(t *testing.T) {
		ref := events[0].Key.Target
		ref.ID = "absent"
		e := replayActivation(t, events[0], ref, "new", 1, 10)
		out, stats, err := ReplayPrefix(initial, []EventV2{e}, 1, cfg)
		replayAssertError(t, out, stats, err, "source_unavailable", "$.lifecycle_fence.entries", 1, 1)
	})
	t.Run("nonactivate_missing", func(t *testing.T) {
		e := events[2]
		e.AcceptedSeq = 1
		e = sealFixture(t, "EventV2", e)
		out, stats, err := ReplayPrefix(initial, []EventV2{e}, 1, cfg)
		replayAssertError(t, out, stats, err, "missing_state", "$.states[*]", 1, 1)
	})
	t.Run("target_hash_alias", func(t *testing.T) {
		ref := events[0].Key.Target
		ref.ContentSHA256 = fixtureSHA("alias")
		e := replayActivation(t, events[0], ref, "alias", 1, 10)
		out, stats, err := ReplayPrefix(initial, []EventV2{e}, 1, cfg)
		replayAssertError(t, out, stats, err, "revision_mismatch", "$.key.target", 1, 1)
	})
	t.Run("catalog_alias", func(t *testing.T) {
		ref := events[0].Key.Target
		ref.ContentSHA256 = fixtureSHA("alias")
		s := replayAddCatalog(t, initial, ref)
		out, stats, err := ReplayPrefix(s, []EventV2{}, 0, cfg)
		replayAssertError(t, out, stats, err, "revision_mismatch", "$.key.target", 0, 0)
	})
}

func TestReplayPrefixCheckpointCompletenessAndStateBindings(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	active := replayMust(t, initial, events, 1, cfg)
	cases := []struct {
		name   string
		mutate func(*SnapshotV2)
	}{
		{"pruned_state", func(s *SnapshotV2) { s.States = []StateV2{} }},
		{"invented_value", func(s *SnapshotV2) { s.States[0].Value = .9; s.States[0] = sealFixture(t, "StateV2", s.States[0]) }},
		{"wrong_reason", func(s *SnapshotV2) {
			s.States[0].Reason = "unknown_outcome"
			s.States[0] = sealFixture(t, "StateV2", s.States[0])
		}},
		{"extra_state", func(s *SnapshotV2) {
			state := scalarClone(s.States[0])
			state.Key.Target.ID = "extra"
			state.Key.Target.ContentSHA256 = fixtureSHA("extra")
			state = sealFixture(t, "StateV2", state)
			*s = replayAddCatalog(t, *s, state.Key.Target)
			s.States = append(s.States, state)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := scalarClone(active)
			c.mutate(&s)
			s = replayReseal(t, s)
			out, stats, err := ReplayPrefix(s, []EventV2{}, 1, cfg)
			var ce *ContractErrorV2
			if !errors.As(err, &ce) {
				t.Fatalf("forged checkpoint accepted: %+v", out)
			}
			replayAssertError(t, out, stats, err, "snapshot_binding", ce.Path, 0, 0)
		})
	}
}

func TestReplayPrefixStructuralHistoryResealedCannotResurrect(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	active := replayMust(t, initial, events, 1, cfg)
	unknown := scalarOutcomeEvent(t, active.States[0], active.Receipts, 20, "unknown", "unattributable")
	s := replayMust(t, active, []EventV2{unknown}, 2, cfg)
	s.Receipts.Receipts[1].BeforeStateRevision = 2
	s.Receipts.Receipts[1].AfterStateRevision = 3
	s.Receipts.Receipts[1] = sealFixture(t, "ReceiptV2", s.Receipts.Receipts[1])
	s = replayReseal(t, s)
	out, stats, err := ReplayPrefix(s, []EventV2{}, 2, cfg)
	replayAssertError(t, out, stats, err, "snapshot_binding", "$.state", 0, 0)
}

func TestReplayPrefixUnsupportedRestoreAndInputLimits(t *testing.T) {
	initial, _, cfg := replayFixture(t)
	t.Run("operational", func(t *testing.T) {
		s := scalarClone(initial)
		s.Receipts.LifecycleFence.Namespace = "operational"
		out, stats, err := ReplayPrefix(s, []EventV2{}, 0, cfg)
		replayAssertError(t, out, stats, err, "unsupported_restore", "$.receipts.lifecycle_fence.namespace", 0, 0)
	})
	t.Run("earlier_bad_field", func(t *testing.T) {
		s := scalarClone(initial)
		s.Receipts.LifecycleFence.Namespace = "operational"
		s.Protocol = "broken"
		out, stats, err := ReplayPrefix(s, []EventV2{}, 0, cfg)
		replayAssertError(t, out, stats, err, "unsupported_protocol", "$.protocol", 0, 0)
	})
	t.Run("array_bound", func(t *testing.T) {
		out, stats, err := ReplayPrefix(initial, make([]EventV2, 100001), 0, cfg)
		replayAssertError(t, out, stats, err, "resource_limit", "$.events", 0, 0)
	})
	t.Run("max_cutoff_unreachable", func(t *testing.T) {
		out, stats, err := ReplayPrefix(initial, []EventV2{}, math.MaxInt64, cfg)
		replayAssertError(t, out, stats, err, "incomplete_prefix", "$.cursor.accepted_seq", 0, 0)
	})
}

func TestReplayPrefixOwnsOutputsAndNeverMutatesInputs(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	before := replayRaw(t, struct {
		S SnapshotV2
		E []EventV2
	}{initial, events})
	out := replayMust(t, initial, events, 3, cfg)
	if !bytes.Equal(before, replayRaw(t, struct {
		S SnapshotV2
		E []EventV2
	}{initial, events})) {
		t.Fatal("input mutated")
	}
	*out.States[0].LastAppliedAtNS = 999
	out.Receipts.LifecycleFence.Entries[0].SourceRefs = append(out.Receipts.LifecycleFence.Entries[0].SourceRefs, EvidenceRef{})
	out.Receipts.Receipts[0].EventID = "mutated"
	if !bytes.Equal(before, replayRaw(t, struct {
		S SnapshotV2
		E []EventV2
	}{initial, events})) {
		t.Fatal("output aliases input")
	}
	s := replayMust(t, initial, events, 1, cfg)
	preserved := replayRaw(t, s)
	copy := replayMust(t, s, []EventV2{}, 1, cfg)
	*copy.States[0].ActivatedAtNS = 777
	copy.Receipts.Receipts[0].EventID = "changed"
	if !bytes.Equal(preserved, replayRaw(t, s)) {
		t.Fatal("zero-delivery result aliases checkpoint")
	}
	events[1].Key.Target.ContentSHA256 = fixtureSHA("broken_event")
	brokenBefore := replayRaw(t, struct {
		S SnapshotV2
		E []EventV2
	}{initial, events})
	_, _, err := ReplayPrefix(initial, events, 3, cfg)
	if err == nil {
		t.Fatal("broken event accepted")
	}
	if !bytes.Equal(brokenBefore, replayRaw(t, struct {
		S SnapshotV2
		E []EventV2
	}{initial, events})) {
		t.Fatal("failed replay mutated inputs")
	}
}

func TestReplayPrefixPrimitiveNamespaceErrorsNotRestoreDomain(t *testing.T) {
	initial, _, cfg := replayFixture(t)
	for _, c := range []struct{ name, value, code string }{{"unicode", string([]byte{255}), "invalid_unicode"}, {"string_limit", strings.Repeat("x", 4097), "resource_limit"}, {"empty", "", "invalid_value"}} {
		t.Run(c.name, func(t *testing.T) {
			s := scalarClone(initial)
			s.Receipts.LifecycleFence.Namespace = c.value
			out, stats, err := ReplayPrefix(s, []EventV2{}, 0, cfg)
			replayAssertError(t, out, stats, err, c.code, "$.receipts.lifecycle_fence.namespace", 0, 0)
		})
	}
}

func TestReplayPrefixEventDomainAndClockBindings(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	cases := []struct {
		name       string
		mutate     func(*EventV2)
		code, path string
	}{
		{"scope", func(e *EventV2) { e.Key.Scope.Project = "foreign" }, "scope_mismatch", "$.key"},
		{"config", func(e *EventV2) { e.Key.ConfigSHA256 = fixtureSHA("wrongconfig") }, "config_mismatch", "$.key.config_sha256"},
		{"stream", func(e *EventV2) { e.StreamID = "foreign" }, "stream_mismatch", "$.stream_id"},
		{"clock", func(e *EventV2) { e.ClockDomain = "foreign" }, "clock_domain_mismatch", "$.clock_domain"},
		{"fresh_arrival", func(e *EventV2) { e.ArrivedAtNS = scalarPointer(int64(11)) }, "future_event", "$.arrived_at_ns"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := scalarClone(events[0])
			c.mutate(&e)
			e = sealFixture(t, "EventV2", e)
			out, stats, err := ReplayPrefix(initial, []EventV2{e}, 1, cfg)
			replayAssertError(t, out, stats, err, c.code, c.path, 1, 1)
		})
	}
}

func TestReplayPrefixNilEventsDistinctFromKnownEmpty(t *testing.T) {
	initial, _, cfg := replayFixture(t)
	t.Run("nil", func(t *testing.T) {
		out, stats, err := ReplayPrefix(initial, nil, 0, cfg)
		replayAssertError(t, out, stats, err, "invalid_type", "$.events", 0, 0)
	})
	t.Run("empty", func(t *testing.T) {
		out := replayMust(t, initial, []EventV2{}, 0, cfg)
		if !reflect.DeepEqual(out, initial) {
			t.Fatal("known empty changed genesis")
		}
	})
	t.Run("snapshot_precedes_nil", func(t *testing.T) {
		bad := scalarClone(initial)
		bad.SnapshotSHA256 = fixtureSHA("wrong")
		out, stats, err := ReplayPrefix(bad, nil, 0, cfg)
		replayAssertError(t, out, stats, err, "hash_mismatch", "$.snapshot_sha256", 0, 0)
	})
	t.Run("config_precedes_nil", func(t *testing.T) {
		bad := cfg
		bad.ConfigSHA256 = fixtureSHA("wrong")
		out, stats, err := ReplayPrefix(initial, nil, 0, bad)
		replayAssertError(t, out, stats, err, "hash_mismatch", "$.config_sha256", 0, 0)
	})
}

func TestReplayPrefixNonactivationAliasBeforeMissingState(t *testing.T) {
	initial, events, cfg := replayFixture(t)
	active := replayMust(t, initial, events, 1, cfg)
	for _, kind := range []EventKind{"exposure", "invalidate", "outcome"} {
		t.Run(string(kind), func(t *testing.T) {
			e := scalarAuxEvent(t, active.States[0], active.Receipts, "alias", 20, kind, "exposure_only")
			if kind == "invalidate" {
				e.Reason = "source_deleted"
			}
			if kind == "outcome" {
				e = scalarOutcomeEvent(t, active.States[0], active.Receipts, 20, "unknown", "unattributable")
				e.Task = nil
				e.Evidence = nil
			}
			e.Key.Target.ContentSHA256 = fixtureSHA("contradictory alias")
			e = sealFixture(t, "EventV2", e)
			out, stats, err := ReplayPrefix(active, []EventV2{e}, 2, cfg)
			replayAssertError(t, out, stats, err, "revision_mismatch", "$.key.target", 1, 1)
			e.PayloadSHA256 = fixtureSHA("bad payload")
			out, stats, err = ReplayPrefix(active, []EventV2{e}, 2, cfg)
			replayAssertError(t, out, stats, err, "hash_mismatch", "$.payload_sha256", 1, 1)
		})
	}
}
