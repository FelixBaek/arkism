package dynamicsv2

import "errors"

// ReadAt computes a scalar view of the supplied accepted prefix. Future request
// times are extrapolations, never proof that later events do not exist. No state,
// receipt, cursor or activation coordinate is created or updated by this read.
func ReadAt(snapshot SnapshotV2, atNS int64, target RefV2, cfg DynamicsConfigV2) (StateViewV2, error) {
	if err := replaySnapshot(snapshot, cfg); err != nil {
		return StateViewV2{}, probePendingError(snapshot, target, err)
	}
	if err := ValidateTyped("RefV2", target); err != nil {
		return StateViewV2{}, err
	}
	if atNS < 0 {
		return StateViewV2{}, contractError("invalid_time", "$.at_ns")
	}
	if atNS < snapshot.Cursor.AcceptedAtNS {
		return StateViewV2{}, contractError("time_regression", "$.at_ns")
	}
	for _, state := range snapshot.States {
		if state.Key.Target == target {
			return probeState(snapshot, state, atNS, cfg)
		}
	}
	return StateViewV2{}, contractError("missing_state", "$.states[*]")
}

func probeState(snapshot SnapshotV2, state StateV2, atNS int64, cfg DynamicsConfigV2) (StateViewV2, error) {
	if state.Status != "active" {
		return StateViewV2{}, contractError("invalid_state", "$.states[*]")
	}
	value := state.Value
	if cfg.DecayEnabled {
		var err error
		value, err = scalarDecay(value, atNS-*state.LastAppliedAtNS, cfg.TauNS)
		if err != nil {
			return StateViewV2{}, err
		}
	}
	view := StateViewV2{Key: state.Key, AtNS: atNS, ClockDomain: snapshot.Cursor.ClockDomain, CutoffAcceptedSeq: snapshot.Cursor.AcceptedSeq, SnapshotSHA256: snapshot.SnapshotSHA256, SnapshotCursor: snapshot.Cursor, SourceStateRevision: state.StateRevision, ActivatedAtNS: *state.ActivatedAtNS, ActivationAcceptedSeq: *state.ActivationAcceptedSeq, Value: value, Status: "present", Reason: state.Reason, StreamID: snapshot.StreamID, Scope: snapshot.Scope, Dimension: snapshot.Dimension, ConfigSHA256: snapshot.ConfigSHA256}
	if err := ValidateTyped("StateViewV2", view); err != nil {
		return StateViewV2{}, err
	}
	return view, nil
}

// Only the parent Snapshot's specific pending-row ban is re-presented here.
// Every individual row must have passed its own shape/hash checks. A parent
// selfhash is checked after that ban, so it is not claimed verified in this case.
func probePendingError(snapshot SnapshotV2, target RefV2, err error) error {
	var ce *ContractErrorV2
	if !errors.As(err, &ce) || ce.Code != "invalid_state" || ce.Path != "$.states[*]" {
		return err
	}
	for _, state := range snapshot.States {
		if ValidateTyped("StateV2", state) != nil {
			return err
		}
	}
	for _, state := range snapshot.States {
		if state.Status == "pending_activation" {
			if state.Key.Target == target {
				return contractError("pending_activation", "$.states[*]")
			}
			return err
		}
	}
	return err
}

// FastSlowView is one lane-pair read at the same accepted prefix and time.
type FastSlowView struct {
	Fast, Slow StateViewV2
	Value      float64
}

// ReadAtFastSlow reads both lanes and mixes them. Both snapshots must share scope
// and accepted history, so neither lane can see a later or different event.
func ReadAtFastSlow(fast, slow SnapshotV2, atNS int64, target RefV2, cfg FastSlowConfig) (FastSlowView, error) {
	if err := checkFastSlowConfig(cfg); err != nil {
		return FastSlowView{}, err
	}
	if !pairedSnapshots(fast, slow) {
		return FastSlowView{}, contractError("snapshot_binding", fastSlowPairPath)
	}
	f, err := ReadAt(fast, atNS, target, cfg.Fast)
	if err != nil {
		return FastSlowView{}, err
	}
	s, err := ReadAt(slow, atNS, target, cfg.Slow)
	if err != nil {
		return FastSlowView{}, err
	}
	return FastSlowView{Fast: f, Slow: s, Value: fastSlowValue(f.Value, s.Value, cfg)}, nil
}
