package dynamicsv2

import (
	"errors"
	"reflect"
	"sort"
)

// ReplayProbeSemanticsSHA256 binds the separately reviewed scalar replay rules.
const ReplayProbeSemanticsSHA256 = "8fa13f0282c7af1b8b3fb523466e352126fa9f6954eb24b03840981250f57b9d"

// ReplayPrefix evaluates supplied deliveries in physical order through an inclusive
// accepted-sequence cutoff. It supports synthetic accepted-prefix checkpoints,
// not authorization to restore an operational backup. Errors return no snapshot.
func ReplayPrefix(initial SnapshotV2, events []EventV2, cutoff int64, cfg DynamicsConfigV2) (SnapshotV2, ReplayStatsV2, error) {
	stats := ReplayStatsV2{}
	if err := replayInputs(initial, events, cutoff, cfg); err != nil {
		return replayFailure(stats, err)
	}
	next := scalarClone(initial)
	positions := replayPositions(next.States)
	known := make(map[string]ReceiptV2, len(next.Receipts.Receipts))
	for _, receipt := range next.Receipts.Receipts {
		known[receipt.EventID] = receipt
	}
	for _, event := range events {
		if err := replayHeader(event); err != nil {
			stats.InputCount++
			stats.RejectedCount++
			return replayFailure(stats, err)
		}
		original, retry := known[event.EventID]
		if !retry && event.AcceptedSeq > cutoff {
			break // A valid NEW future header does not authorize reading its body.
		}
		stats.InputCount++
		transition, position, err := replayDelivery(next, positions, event, original, retry, cfg)
		if err != nil {
			stats.RejectedCount++
			return replayFailure(stats, err)
		}
		replayCount(&stats, transition)
		if retry {
			continue
		}
		if position < 0 {
			positions[transition.State.Key] = len(next.States)
			next.States = append(next.States, transition.State)
		} else {
			next.States[position] = transition.State
		}
		next.Receipts, next.Cursor = transition.Receipts, transition.Cursor
		known[event.EventID] = transition.Receipt
	}
	if next.Cursor.AcceptedSeq != cutoff {
		return replayFailure(stats, contractError("incomplete_prefix", "$.cursor.accepted_seq"))
	}
	out, err := replaySeal(next)
	if err != nil {
		return replayFailure(stats, err)
	}
	return out, stats, nil
}

func replayInputs(s SnapshotV2, events []EventV2, cutoff int64, cfg DynamicsConfigV2) error {
	if err := replaySnapshot(s, cfg); err != nil {
		return err
	}
	if events == nil {
		return contractError("invalid_type", "$.events")
	}
	if cutoff < 0 {
		return contractError("invalid_value", "$.cutoff_accepted_seq")
	}
	if s.Cursor.AcceptedSeq > cutoff {
		return contractError("prefix_mismatch", "$.cursor.accepted_seq")
	}
	if len(events) > 100000 {
		return contractError("resource_limit", "$.events")
	}
	return nil
}

func replayHeader(e EventV2) error {
	if err := stringValue(e.EventID, fieldRule{ref: "LegacyID"}, "$.event_id"); err != nil {
		return err
	}
	if e.AcceptedSeq <= 0 {
		return contractError("invalid_value", "$.accepted_seq")
	}
	return nil
}

func replayDelivery(s SnapshotV2, positions map[DynamicsKeyV2]int, e EventV2, original ReceiptV2, retry bool, cfg DynamicsConfigV2) (TransitionV2, int, error) {
	if err := ValidateTyped("EventV2", e); err != nil {
		return TransitionV2{}, -1, err
	}
	if err := replayEventDomain(s, e); err != nil {
		return TransitionV2{}, -1, err
	}
	key := e.Key
	if retry {
		key = original.Key
	}
	position, exists := positions[key]
	var state StateV2
	if exists {
		state = s.States[position]
	} else {
		position = -1
		var err error
		state, err = replayPending(s, e, cfg)
		if err != nil {
			return TransitionV2{}, position, err
		}
	}
	transition, err := ApplyEvent(state, s.Receipts, e, cfg)
	return transition, position, err
}

func replayEventDomain(s SnapshotV2, e EventV2) error {
	if e.Key.Scope != s.Scope || e.Key.Dimension != s.Dimension {
		return contractError("scope_mismatch", "$.key")
	}
	if e.Key.ConfigSHA256 != s.ConfigSHA256 {
		return contractError("config_mismatch", "$.key.config_sha256")
	}
	return nil
}

func replayPending(s SnapshotV2, e EventV2, cfg DynamicsConfigV2) (StateV2, error) {
	if err := replayTargetAlias(s, e.Key.Target); err != nil {
		return StateV2{}, err
	}
	if e.Kind != "activate" {
		return StateV2{}, contractError("missing_state", "$.states[*]")
	}
	if len(s.States) >= 100000 {
		return StateV2{}, contractError("resource_limit", "$.states")
	}
	if scalarFencePosition(s.Receipts.LifecycleFence, e.Key) < 0 {
		return StateV2{}, contractError("source_unavailable", "$.lifecycle_fence.entries")
	}
	var lower *StateV2
	for i := range s.States {
		state := &s.States[i]
		if sameMemoryIdentity(state.Key.Target, e.Key.Target) && state.Key.Target.Revision < e.Key.Target.Revision {
			if lower == nil || state.Key.Target.Revision > lower.Key.Target.Revision {
				lower = state
			}
		}
	}
	if lower != nil {
		return ResetRevision(*lower, e.Key.Target, cfg)
	}
	return scalarSeal[StateV2]("StateV2", StateV2HashProjection{Key: e.Key, Mode: cfg.Mode, Value: cfg.Initial, StateRevision: 0, Status: "pending_activation", Reason: "none"})
}

func replayTargetAlias(s SnapshotV2, target RefV2) error {
	for _, entry := range s.Receipts.LifecycleFence.Entries {
		if entry.Scope == s.Scope && sameMemoryIdentity(entry.Target, target) && entry.Target.Revision == target.Revision && entry.Target.ContentSHA256 != target.ContentSHA256 {
			return contractError("revision_mismatch", "$.key.target")
		}
	}
	return nil
}

func replayCount(stats *ReplayStatsV2, result TransitionV2) {
	if result.AttemptDisposition == "duplicate_event" {
		stats.DuplicateEventCount++
		return
	}
	if result.Receipt.Disposition == "non_learning" {
		stats.NonLearningCount++
		if result.Receipt.Reason == "duplicate_evidence" {
			stats.DuplicateEvidenceCount++
		}
		return
	}
	stats.AppliedCount++ // Includes accepted lifecycle invalidation.
}

func replayFailure(stats ReplayStatsV2, err error) (SnapshotV2, ReplayStatsV2, error) {
	var ce *ContractErrorV2
	if errors.As(err, &ce) {
		stats.Error = scalarPointer(*ce)
	} else {
		stats.Error = &ContractErrorV2{Code: "invalid_value", Path: "$"}
	}
	return SnapshotV2{}, stats, err
}

func replayPositions(states []StateV2) map[DynamicsKeyV2]int {
	positions := make(map[DynamicsKeyV2]int, len(states))
	for i, state := range states {
		positions[state.Key] = i
	}
	return positions
}

func replaySeal(s SnapshotV2) (SnapshotV2, error) {
	sort.Slice(s.States, func(i, j int) bool {
		a, _ := canonical(reflect.ValueOf(s.States[i].Key))
		b, _ := canonical(reflect.ValueOf(s.States[j].Key))
		return string(a) < string(b)
	})
	p := SnapshotV2HashProjection{Protocol: s.Protocol, Scope: s.Scope, Dimension: s.Dimension, ConfigSHA256: s.ConfigSHA256, Cursor: s.Cursor, States: s.States, Receipts: s.Receipts, StreamID: s.StreamID}
	return scalarSeal[SnapshotV2]("SnapshotV2", p)
}

// replaySnapshot supplements the frozen wire validator with scalar checkpoint
// context. Receipt/fence groups are read-only views, not independent transitions.
func replaySnapshot(s SnapshotV2, cfg DynamicsConfigV2) error {
	if err := replayTypedSnapshot(s); err != nil {
		return err
	}
	if err := ValidateTyped("DynamicsConfigV2", cfg); err != nil {
		return err
	}
	if cfg.ConfigSHA256 != s.ConfigSHA256 {
		return contractError("config_mismatch", "$.config_sha256")
	}
	if cfg.ClockDomain != s.Cursor.ClockDomain {
		return contractError("clock_domain_mismatch", "$.cursor.clock_domain")
	}
	if err := replayAliases(s); err != nil {
		return err
	}
	return replayStateBindings(s, cfg)
}

func replayTypedSnapshot(s SnapshotV2) error {
	err := ValidateTyped("SnapshotV2", s)
	var ce *ContractErrorV2
	if errors.As(err, &ce) && ce.Code == "invalid_value" && ce.Path == "$.receipts.lifecycle_fence.namespace" && s.Receipts.LifecycleFence.Namespace != "" && s.Receipts.LifecycleFence.Namespace != "synthetic" {
		return contractError("unsupported_restore", ce.Path)
	}
	return err
}

func replayAliases(s SnapshotV2) error {
	seen := make(map[string]string)
	for _, state := range s.States {
		if err := replayAlias(seen, state.Key.Scope, state.Key.Target); err != nil {
			return err
		}
	}
	for _, entry := range s.Receipts.LifecycleFence.Entries {
		if err := replayAlias(seen, entry.Scope, entry.Target); err != nil {
			return err
		}
	}
	return nil
}

func replayAlias(seen map[string]string, scope ScopeV2, ref RefV2) error {
	sha := ref.ContentSHA256
	ref.ContentSHA256 = ""
	key := scopeRefKey(scope, ref)
	if previous, ok := seen[key]; ok && previous != sha {
		return contractError("revision_mismatch", "$.key.target")
	}
	seen[key] = sha
	return nil
}

func replayStateBindings(s SnapshotV2, cfg DynamicsConfigV2) error {
	if err := scalarReceiptHistory(s.Receipts); err != nil {
		return err
	}
	positions := replayPositions(s.States)
	groups := make(map[DynamicsKeyV2][]ReceiptV2)
	for _, receipt := range s.Receipts.Receipts {
		if _, exists := positions[receipt.Key]; !exists {
			return contractError("snapshot_binding", "$.states")
		}
		groups[receipt.Key] = append(groups[receipt.Key], receipt)
	}
	fences := make(map[string]FenceEntry)
	for _, entry := range s.Receipts.LifecycleFence.Entries {
		fences[scopeRefKey(entry.Scope, entry.Target)] = entry
	}
	for _, state := range s.States {
		index := s.Receipts
		index.Receipts = groups[state.Key]
		index.LifecycleFence.Entries = nil
		if entry, exists := fences[scopeRefKey(state.Key.Scope, state.Key.Target)]; exists {
			index.LifecycleFence.Entries = []FenceEntry{entry}
		}
		if err := scalarStateHistory(state, index, cfg); err != nil {
			return err
		}
	}
	return nil
}

// ReplayPrefixFastSlow replays each lane's accepted prefix with the same cutoff.
// Only deliveries a lane actually consumed are pair-checked, so events after the
// cutoff never change the result; any lane disagreement returns no snapshot.
func ReplayPrefixFastSlow(fast, slow SnapshotV2, events []FastSlowEvent, cutoff int64, cfg FastSlowConfig) (SnapshotV2, SnapshotV2, error) {
	if err := checkFastSlowConfig(cfg); err != nil {
		return SnapshotV2{}, SnapshotV2{}, err
	}
	if !pairedSnapshots(fast, slow) {
		return SnapshotV2{}, SnapshotV2{}, contractError("snapshot_binding", fastSlowPairPath)
	}
	fastEvents := make([]EventV2, len(events))
	slowEvents := make([]EventV2, len(events))
	for i, e := range events {
		fastEvents[i], slowEvents[i] = e.Fast, e.Slow
	}
	f, fs, err := ReplayPrefix(fast, fastEvents, cutoff, cfg.Fast)
	if err != nil {
		return SnapshotV2{}, SnapshotV2{}, err
	}
	s, ss, err := ReplayPrefix(slow, slowEvents, cutoff, cfg.Slow)
	if err != nil {
		return SnapshotV2{}, SnapshotV2{}, err
	}
	if !reflect.DeepEqual(fs, ss) || !pairedSnapshots(f, s) {
		return SnapshotV2{}, SnapshotV2{}, contractError("snapshot_binding", fastSlowPairPath)
	}
	for _, e := range events[:fs.InputCount] {
		if err := checkFastSlowEvent(e); err != nil {
			return SnapshotV2{}, SnapshotV2{}, err
		}
	}
	return f, s, nil
}
