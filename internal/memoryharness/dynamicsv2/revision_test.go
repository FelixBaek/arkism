package dynamicsv2

import (
	"sort"
	"testing"
)

func TestRevisionPendingReset(t *testing.T) {
	state, _, _, cfg := scalarFixturePending(t)
	target := state.Key.Target
	target.Revision++
	result, err := ResetRevision(state, target, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending_activation" || result.StateRevision != 0 || result.Value != cfg.Initial || result.ActivatedAtNS != nil {
		t.Fatal("reset fields differ")
	}
}

func TestRevisionExactNoopAndOwnedPointers(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, _ := scalarFixtureActive(t, cfg)
	r, err := ResetRevision(s, s.Key.Target, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.StateSHA256 != s.StateSHA256 || r.Value != s.Value || r.StateRevision != s.StateRevision {
		t.Fatal("same-ref reset was not exact no-op")
	}
	*r.LastAppliedAtNS = 999
	if *s.LastAppliedAtNS != 10 {
		t.Fatal("no-op output pointer aliases input")
	}
}
func TestRevisionIdentityAndConfigGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RefV2, *DynamicsConfigV2)
		code   string
	}{
		{"different_id", func(r *RefV2, c *DynamicsConfigV2) { r.ID = "other" }, "target_mismatch"},
		{"different_origin", func(r *RefV2, c *DynamicsConfigV2) { r.Origin = "fixture_alt" }, "target_mismatch"},
		{"same_revision_changed_hash", func(r *RefV2, c *DynamicsConfigV2) { r.ContentSHA256 = fixtureSHA("different content") }, "revision_mismatch"},
		{"config_mismatch_before_noop", func(r *RefV2, c *DynamicsConfigV2) { c.Initial = .75; *c = sealFixture(t, "DynamicsConfigV2", *c) }, "config_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig(t, "B2")
			s, _ := scalarFixtureActive(t, cfg)
			target := s.Key.Target
			tc.change(&target, &cfg)
			_, err := ResetRevision(s, target, cfg)
			requireCode(t, err, tc.code)
		})
	}
}
func TestRevisionHistoricalRetryReturnsCurrentPendingState(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, index, original, _ := scalarFixturePendingConfig(t, cfg)
	active, err := ApplyEvent(s, index, original, cfg)
	if err != nil {
		t.Fatal(err)
	}
	newRef := active.State.Key.Target
	newRef.Revision = 2
	newRef.ContentSHA256 = fixtureSHA("new content")
	current, err := ResetRevision(active.State, newRef, cfg)
	if err != nil {
		t.Fatal(err)
	}
	retry := original
	retry.TransportAttemptID = "retry"
	retry.ArrivedAtNS = scalarPointer(int64(100))
	retry = sealFixture(t, "EventV2", retry)
	result, err := ApplyEvent(current, active.Receipts, retry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.StateSHA256 != current.StateSHA256 || result.State.Key.Target != newRef || result.State.Status != "pending_activation" || result.Receipt.ReceiptSHA256 != active.Receipt.ReceiptSHA256 {
		t.Fatal("historical retry revived old scalar state")
	}
}
func TestRevisionHigherPendingKeepsOldHistoryExternal(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	s, index := scalarFixtureActive(t, cfg)
	passed := scalarOutcomeEvent(t, s, index, 10, "passed", "memory")
	learned, err := ApplyEvent(s, index, passed, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref := learned.State.Key.Target
	ref.Revision = 2
	ref.ContentSHA256 = fixtureSHA("new memory")
	next, err := ResetRevision(learned.State, ref, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.Value != .5 || next.StateRevision != 0 || next.ActivatedAtNS != nil || next.LastAppliedSeq != nil || next.Key.Target != ref {
		t.Fatal("old scalar value/anchors migrated")
	}
	if len(learned.Receipts.EvidenceConsumptions) != 1 || learned.State.Value != .625 {
		t.Fatal("external old history changed")
	}
}

func TestRevisionNewActivationAndOlderEventFence(t *testing.T) {
	cfg := fixtureConfig(t, "B2")
	old, index := scalarFixtureActive(t, cfg)
	nextRef := old.Key.Target
	nextRef.Revision = 2
	nextRef.ContentSHA256 = fixtureSHA("revised content")
	pending, err := ResetRevision(old, nextRef, cfg)
	if err != nil {
		t.Fatal(err)
	}
	index.LifecycleFence.Entries[0].Status = "invalidated"
	index.LifecycleFence.Entries[0].Reason = "invalidated"
	index.LifecycleFence.Entries = append(index.LifecycleFence.Entries, FenceEntry{Scope: pending.Key.Scope, Target: nextRef, Status: "current", SourceRefs: []EvidenceRef{}, Reason: "none"})
	index.LifecycleFence.Epoch++
	sort.Slice(index.LifecycleFence.Entries, func(a, b int) bool {
		x, y := index.LifecycleFence.Entries[a], index.LifecycleFence.Entries[b]
		return scopeRefKey(x.Scope, x.Target) < scopeRefKey(y.Scope, y.Target)
	})
	index.LifecycleFence = sealFixture(t, "LifecycleFence", index.LifecycleFence)
	index = sealFixture(t, "ReceiptIndexV2", index)
	activate := scalarAuxEvent(t, pending, index, "new_activation", 20, "activate", "activation")
	current, err := ApplyEvent(pending, index, activate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if current.State.StateRevision != 1 || *current.State.ActivatedAtNS != 20 || current.State.Key.Target != nextRef {
		t.Fatal("new revision did not use actual new activation")
	}
	_, err = ResetRevision(current.State, old.Key.Target, cfg)
	requireCode(t, err, "revision_regression")
	oldEvent := scalarOutcomeEvent(t, old, current.Receipts, 30, "passed", "memory")
	_, err = ApplyEvent(current.State, current.Receipts, oldEvent, cfg)
	requireCode(t, err, "stale_target")
}

func scalarFixtureRevisedActive(t *testing.T, cfg DynamicsConfigV2) (StateV2, ReceiptIndexV2) {
	t.Helper()
	old, index := scalarFixtureActive(t, cfg)
	target := old.Key.Target
	target.Revision = 2
	target.ContentSHA256 = fixtureSHA("revised memory")
	pending, err := ResetRevision(old, target, cfg)
	if err != nil {
		t.Fatal(err)
	}
	index.LifecycleFence.Entries[0].Status = "invalidated"
	index.LifecycleFence.Entries[0].Reason = "invalidated"
	index.LifecycleFence.Entries = append(index.LifecycleFence.Entries, FenceEntry{Scope: pending.Key.Scope, Target: target, Status: "current", SourceRefs: []EvidenceRef{}, Reason: "none"})
	index.LifecycleFence.Epoch++
	sort.Slice(index.LifecycleFence.Entries, func(a, b int) bool {
		x, y := index.LifecycleFence.Entries[a], index.LifecycleFence.Entries[b]
		return scopeRefKey(x.Scope, x.Target) < scopeRefKey(y.Scope, y.Target)
	})
	index.LifecycleFence = sealFixture(t, "LifecycleFence", index.LifecycleFence)
	index = sealFixture(t, "ReceiptIndexV2", index)
	e := scalarAuxEvent(t, pending, index, "new_activation", 20, "activate", "activation")
	r, err := ApplyEvent(pending, index, e, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r.State, r.Receipts
}

func TestRevisionPendingValueCheckedBeforeReset(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_ref", true: "new_ref"}[newer], func(t *testing.T) {
			s, _, _, cfg := scalarFixturePending(t)
			s.Value = .75
			s = sealFixture(t, "StateV2", s)
			target := s.Key.Target
			if newer {
				target.Revision++
				target.ContentSHA256 = fixtureSHA("next pending")
			}
			_, err := ResetRevision(s, target, cfg)
			requireCode(t, err, "snapshot_binding")
		})
	}
}
func TestRevisionPendingValueCheckedBeforeHistoricalRetry(t *testing.T) {
	s, index, original, cfg := scalarFixturePending(t)
	active, err := ApplyEvent(s, index, original, cfg)
	if err != nil {
		t.Fatal(err)
	}
	target := s.Key.Target
	target.Revision++
	target.ContentSHA256 = fixtureSHA("new pending")
	pending, err := ResetRevision(active.State, target, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pending.Value = .75
	pending = sealFixture(t, "StateV2", pending)
	_, err = ApplyEvent(pending, active.Receipts, original, cfg)
	requireCode(t, err, "snapshot_binding")
}

func TestRevisionFastSlowResetIsolatesNewRevision(t *testing.T) {
	cfg := fsConfig(t)
	s, i := fsActive(t, cfg)
	r, err := ApplyEventFastSlow(s, i, fsOutcome(t, s, i, 1000000010, "passed", "memory"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s1, i1 := fsNext(r)
	target := s1.Fast.Key.Target
	target.Revision++
	target.ContentSHA256 = fixtureSHA("revision-2")
	reset, err := ResetRevisionFastSlow(s1, target, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []StateV2{reset.Fast, reset.Slow} {
		if lane.Status != "pending_activation" || lane.Value != cfg.Fast.Initial || lane.StateRevision != 0 || lane.ActivatedAtNS != nil || lane.Key.Target != target {
			t.Fatal("new revision copied old learned value or anchor")
		}
	}
	old := fsOutcome(t, s1, i1, 1000000020, "passed", "memory")
	for _, lane := range []*EventV2{&old.Fast, &old.Slow} {
		lane.EventID = "old-revision"
		*lane = sealFixture(t, "EventV2", *lane)
	}
	_, err = ApplyEventFastSlow(reset, i1, old, cfg)
	requireCode(t, err, "stale_target")
	_, err = ResetRevisionFastSlow(reset, s1.Fast.Key.Target, cfg)
	requireCode(t, err, "revision_regression")
	// Lanes at different lifecycle positions are not silently re-aligned.
	_, err = ResetRevisionFastSlow(FastSlowState{Fast: s1.Fast, Slow: s.Slow}, target, cfg)
	requireCode(t, err, "snapshot_binding")
	fsRequirePath(t, err, fastSlowPairPath)
}
