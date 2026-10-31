package dynamicsv2

import (
	"errors"
	"math"
	"reflect"
	"sort"

	"github.com/FelixBaek/arkism/internal/memoryharness"
)

// ScalarSemanticsSHA256 binds the separately reviewed scalar implementation rules.
const ScalarSemanticsSHA256 = "fd81476b36dddf784e49b7613b1ac4b3adf9e808630a84b07cb9941646d089f5"

// ApplyEvent computes one pure scalar transition. It performs no I/O or DB commit.
// A successful result owns its mutable pointers/slices independently of its inputs.
func ApplyEvent(previous StateV2, index ReceiptIndexV2, event EventV2, cfg DynamicsConfigV2) (TransitionV2, error) {
	if err := scalarInputs(previous, index, event, cfg); err != nil {
		return TransitionV2{}, err
	}
	if original, ok := scalarOriginal(index, event.EventID); ok {
		return scalarRetry(previous, index, event, original)
	}
	if err := scalarFreshContext(previous, index, event); err != nil {
		return TransitionV2{}, err
	}
	consumption, found, err := scalarEvidence(index, event)
	if err != nil {
		return TransitionV2{}, err
	}
	if err := scalarFreshCoordinates(index, event); err != nil {
		return TransitionV2{}, err
	}
	if found {
		return scalarDuplicateEvidence(previous, index, event, consumption)
	}
	return scalarFresh(previous, index, event, cfg, consumption)
}
func scalarInputs(state StateV2, index ReceiptIndexV2, event EventV2, cfg DynamicsConfigV2) error {
	values := []struct {
		name  string
		value any
	}{{"StateV2", state}, {"ReceiptIndexV2", index}, {"EventV2", event}, {"DynamicsConfigV2", cfg}}
	for _, v := range values {
		if err := ValidateTyped(v.name, v.value); err != nil {
			return err
		}
	}
	if state.Key.ConfigSHA256 != cfg.ConfigSHA256 || event.Key.ConfigSHA256 != cfg.ConfigSHA256 {
		return contractError("config_mismatch", "$.key.config_sha256")
	}
	if state.Key.Scope != event.Key.Scope || state.Key.Dimension != event.Key.Dimension || state.Key.ContractSHA256 != event.Key.ContractSHA256 {
		return contractError("scope_mismatch", "$.key")
	}
	if !sameMemoryIdentity(state.Key.Target, event.Key.Target) {
		return contractError("target_mismatch", "$.key.target")
	}
	if cfg.ClockDomain != index.ClockDomain || event.ClockDomain != index.ClockDomain {
		return contractError("clock_domain_mismatch", "$.clock_domain")
	}
	if event.StreamID != index.StreamID {
		return contractError("stream_mismatch", "$.stream_id")
	}
	if err := scalarRuleBindings(event, cfg); err != nil {
		return err
	}
	return scalarIndexContext(state, index, cfg)
}
func scalarRuleBindings(e EventV2, c DynamicsConfigV2) error {
	if e.Task != nil && e.Evidence != nil && e.Task.TaskSHA256 != e.Evidence.TaskSHA256 {
		return contractError("hash_mismatch", "$.evidence.task_sha256")
	}
	if e.Task != nil && e.Task.RuleSHA256 != c.EvidenceRuleSHA256 {
		return contractError("hash_mismatch", "$.task.rule_sha256")
	}
	if e.Evidence != nil {
		if e.Evidence.RuleSHA256 != c.EvidenceRuleSHA256 {
			return contractError("hash_mismatch", "$.evidence.rule_sha256")
		}
		if e.Evidence.Target != e.Key.Target || e.Evidence.Dimension != e.Key.Dimension {
			return contractError("snapshot_binding", "$.evidence")
		}
	}
	return nil
}
func scalarIndexContext(state StateV2, index ReceiptIndexV2, cfg DynamicsConfigV2) error {
	if err := scalarReceiptHistory(index); err != nil {
		return err
	}
	for _, r := range index.Receipts {
		if r.Key.Scope != state.Key.Scope || r.Key.Dimension != state.Key.Dimension || r.Key.ConfigSHA256 != state.Key.ConfigSHA256 || r.Key.ContractSHA256 != state.Key.ContractSHA256 {
			return contractError("snapshot_binding", "$.receipts[*].key")
		}
	}
	if state.LastAppliedSeq != nil && (*state.LastAppliedSeq > index.Cursor.AcceptedSeq || *state.LastAppliedAtNS > index.Cursor.AcceptedAtNS) {
		return contractError("snapshot_binding", "$.state")
	}
	if err := scalarStateHistory(state, index, cfg); err != nil {
		return err
	}
	snapshot := SnapshotV2{Scope: state.Key.Scope, Dimension: state.Key.Dimension, ConfigSHA256: state.Key.ConfigSHA256, Cursor: index.Cursor, Receipts: index, StreamID: index.StreamID}
	return checkPrefixHash(snapshot, "$")
}
func scalarOriginal(index ReceiptIndexV2, id string) (ReceiptV2, bool) {
	for _, r := range index.Receipts {
		if r.EventID == id {
			return r, true
		}
	}
	return ReceiptV2{}, false
}
func scalarRetry(state StateV2, index ReceiptIndexV2, e EventV2, r ReceiptV2) (TransitionV2, error) {
	if e.Key != r.Key || e.PayloadSHA256 != r.PayloadSHA256 || e.AcceptedSeq != r.AcceptedSeq || e.AcceptedAtNS != r.AcceptedAtNS {
		return TransitionV2{}, contractError("event_conflict", "$.event_id")
	}
	if state.Key.Target.Revision < e.Key.Target.Revision || state.Key.Target.Revision == e.Key.Target.Revision && state.Key.Target.ContentSHA256 != e.Key.Target.ContentSHA256 {
		return TransitionV2{}, contractError("revision_mismatch", "$.key.target")
	}
	return scalarTransition(state.StateSHA256, scalarClone(state), scalarClone(index), scalarClone(r), "duplicate_event")
}
func scalarFreshContext(state StateV2, index ReceiptIndexV2, e EventV2) error {
	if state.Key.Target != e.Key.Target {
		return contractError("stale_target", "$.key.target")
	}
	if state.Status == "pending_activation" && e.Kind != "activate" {
		return contractError("invalid_state", "$.state")
	}
	if state.Status != "pending_activation" && e.Kind == "activate" {
		return contractError("invalid_state", "$.state")
	}
	return scalarFenceGuard(state, index.LifecycleFence, e)
}
func scalarFreshCoordinates(index ReceiptIndexV2, e EventV2) error {
	if e.ArrivedAtNS != nil && *e.ArrivedAtNS > e.AcceptedAtNS {
		return contractError("future_event", "$.arrived_at_ns")
	}
	if index.Cursor.AcceptedSeq == math.MaxInt64 {
		return contractError("resource_limit", "$.cursor.accepted_seq")
	}
	if e.AcceptedSeq != index.Cursor.AcceptedSeq+1 {
		return contractError("sequence_regression", "$.accepted_seq")
	}
	if e.AcceptedAtNS < index.Cursor.AcceptedAtNS {
		return contractError("time_regression", "$.accepted_at_ns")
	}
	if len(index.Receipts) >= 200000 {
		return contractError("resource_limit", "$.receipts")
	}
	return nil
}
func scalarFencePosition(f LifecycleFence, key DynamicsKeyV2) int {
	for i, e := range f.Entries {
		if e.Scope == key.Scope && e.Target == key.Target {
			return i
		}
	}
	return -1
}
func scalarFenceGuard(s StateV2, f LifecycleFence, e EventV2) error {
	i := scalarFencePosition(f, e.Key)
	if i < 0 {
		return contractError("source_unavailable", "$.lifecycle_fence.entries")
	}
	if s.Status == "invalidated" && e.Kind == "invalidate" {
		if f.Entries[i].Status != "invalidated" {
			return contractError("invalid_state", "$.lifecycle_fence.entries")
		}
		return nil
	}
	if f.Entries[i].Status != "current" || s.Status == "invalidated" {
		return contractError("invalidated_target", "$.lifecycle_fence.entries")
	}
	return nil
}
func scalarEvidence(index ReceiptIndexV2, e EventV2) (EvidenceConsumption, bool, error) {
	if e.Kind != "outcome" || e.Task == nil || e.Evidence == nil {
		return EvidenceConsumption{}, false, nil
	}
	identity := EvidenceIdentityProjectionV2{Scope: e.Key.Scope, Target: e.Key.Target, Dimension: e.Key.Dimension, TaskRef: e.Task.TaskRef, RunRef: e.Task.RunRef, TrialRef: e.Task.TrialRef, CheckerSHA256: e.Task.CheckerSHA256, EvidenceRefs: scalarClone(e.Evidence.Refs)}
	identitySHA, err := CanonicalSHA256("EvidenceIdentityProjectionV2", identity)
	if err != nil {
		return EvidenceConsumption{}, false, err
	}
	claim := EvidenceClaimProjectionV2{Outcome: e.Outcome, Attribution: e.Attribution, Strength: scalarClone(e.Strength), ObservedAtNS: scalarClone(e.ObservedAtNS), Task: scalarClone(*e.Task), Evidence: scalarClone(*e.Evidence)}
	claimSHA, err := CanonicalSHA256("EvidenceClaimProjectionV2", claim)
	if err != nil {
		return EvidenceConsumption{}, false, err
	}
	c := EvidenceConsumption{IdentitySHA256: identitySHA, ClaimSHA256: claimSHA, FirstEventID: e.EventID}
	for _, old := range index.EvidenceConsumptions {
		if old.IdentitySHA256 != identitySHA {
			continue
		}
		if old.ClaimSHA256 != claimSHA {
			return EvidenceConsumption{}, false, contractError("evidence_conflict", "$.evidence")
		}
		return old, true, nil
	}
	return c, false, nil
}
func scalarDuplicateEvidence(previous StateV2, index ReceiptIndexV2, e EventV2, c EvidenceConsumption) (TransitionV2, error) {
	p := scalarReceiptFields(previous, previous, e, "non_learning", "duplicate_evidence")
	p.EvidenceConsumptionSHA256 = scalarPointer(c.IdentitySHA256)
	p.DuplicateOfReceiptSHA256 = scalarPointer(c.FirstReceiptSHA256)
	r, err := scalarSeal[ReceiptV2]("ReceiptV2", p)
	if err != nil {
		return TransitionV2{}, err
	}
	return scalarCommit(previous, scalarClone(previous), index, e, r, nil, "duplicate_evidence")
}
func scalarFresh(previous StateV2, index ReceiptIndexV2, e EventV2, cfg DynamicsConfigV2, consumption EvidenceConsumption) (TransitionV2, error) {
	next, disposition, reason, err := scalarState(previous, e, cfg)
	if err != nil {
		return TransitionV2{}, err
	}
	p := scalarReceiptFields(previous, next, e, disposition, reason)
	learn := e.Kind == "outcome" && qualified(e)
	if learn {
		p.EvidenceConsumptionSHA256 = scalarPointer(consumption.IdentitySHA256)
	}
	receipt, err := scalarSeal[ReceiptV2]("ReceiptV2", p)
	if err != nil {
		return TransitionV2{}, err
	}
	var added *EvidenceConsumption
	if learn {
		consumption.FirstReceiptSHA256 = receipt.ReceiptSHA256
		added = &consumption
	}
	return scalarCommit(previous, next, index, e, receipt, added, AttemptDisposition(disposition))
}
func scalarState(previous StateV2, e EventV2, c DynamicsConfigV2) (StateV2, ReceiptDisposition, Reason, error) {
	next := scalarClone(previous)
	switch e.Kind {
	case "activate":
		if next.Value != c.Initial {
			return StateV2{}, "", "", contractError("config_mismatch", "$.value")
		}
		next.Status = "active"
		next.StateRevision = 1
		next.ActivatedAtNS = scalarPointer(e.AcceptedAtNS)
		next.ActivationAcceptedSeq = scalarPointer(e.AcceptedSeq)
		next.ActivationEventID = scalarPointer(e.EventID)
		next.ActivationPayloadSHA256 = scalarPointer(e.PayloadSHA256)
		next.LastAppliedAtNS = scalarPointer(e.AcceptedAtNS)
		next.LastAppliedSeq = scalarPointer(e.AcceptedSeq)
		next.Reason = "activation"
	case "outcome":
		return scalarOutcome(next, e, c)
	case "exposure":
		return next, "non_learning", "exposure_only", nil
	case "invalidate":
		return scalarInvalidation(next, e)
	default:
		return StateV2{}, "", "", contractError("unsupported_type", "$.kind")
	}
	sealed, err := sealScalarState(next)
	return sealed, "applied", "activation", err
}
func scalarOutcome(next StateV2, e EventV2, c DynamicsConfigV2) (StateV2, ReceiptDisposition, Reason, error) {
	if next.StateRevision == math.MaxInt64 {
		return StateV2{}, "", "", contractError("resource_limit", "$.state_revision")
	}
	dt := e.AcceptedAtNS - *next.LastAppliedAtNS
	if dt < 0 {
		return StateV2{}, "", "", contractError("time_regression", "$.accepted_at_ns")
	}
	value := next.Value
	var err error
	if c.DecayEnabled {
		value, err = scalarDecay(value, dt, c.TauNS)
		if err != nil {
			return StateV2{}, "", "", err
		}
	}
	if qualified(e) && c.OutcomeJumpEnabled {
		value = scalarJump(value, e, c)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return StateV2{}, "", "", contractError("invalid_state", "$.value")
	}
	next.Value = value
	next.StateRevision++
	next.LastAppliedAtNS = scalarPointer(e.AcceptedAtNS)
	next.LastAppliedSeq = scalarPointer(e.AcceptedSeq)
	reason := scalarOutcomeReason(e)
	next.Reason = reason
	sealed, err := sealScalarState(next)
	return sealed, "applied", reason, err
}
func scalarDecay(value float64, dtNS, tauNS int64) (float64, error) {
	dtSeconds := float64(dtNS) / 1e9
	halfLifeSeconds := (float64(tauNS) / 1e9) * math.Ln2
	value, err := memoryharness.Decay(value, dtSeconds, halfLifeSeconds)
	if err == nil {
		return value, nil
	}
	var native *memoryharness.ContractError
	if errors.As(err, &native) {
		return 0, contractError(native.Code, native.Path)
	}
	return 0, contractError("io_error", "$")
}
func scalarJump(value float64, e EventV2, c DynamicsConfigV2) float64 {
	u := c.UnitStrength * *e.Strength
	if e.Outcome == "passed" {
		a := c.EtaPositive * u
		return value + a*(1-value)
	}
	a := c.EtaNegative * u
	return value * (1 - a)
}
func scalarOutcomeReason(e EventV2) Reason {
	if qualified(e) {
		if e.Outcome == "passed" {
			return "passed_independent"
		}
		return "failed_attributable"
	}
	if e.Outcome == "unknown" {
		return "unknown_outcome"
	}
	if e.Outcome == "failed" {
		return "failed_unattributable"
	}
	if e.Task == nil || e.Task.Qualification != "synthetic_independent" {
		return "task_unknown"
	}
	return "source_unknown"
}
func scalarInvalidation(next StateV2, e EventV2) (StateV2, ReceiptDisposition, Reason, error) {
	if next.Status == "invalidated" {
		return next, "non_learning", "invalidated", nil
	}
	if next.StateRevision == math.MaxInt64 {
		return StateV2{}, "", "", contractError("resource_limit", "$.state_revision")
	}
	next.Status = "invalidated"
	next.StateRevision++
	next.Reason = e.Reason
	sealed, err := sealScalarState(next)
	return sealed, "invalidated", "invalidated", err
}
func scalarReceiptFields(before, after StateV2, e EventV2, d ReceiptDisposition, reason Reason) ReceiptV2HashProjection {
	return ReceiptV2HashProjection{StreamID: e.StreamID, EventID: e.EventID, PayloadSHA256: e.PayloadSHA256, Key: e.Key, AcceptedAtNS: e.AcceptedAtNS, AcceptedSeq: e.AcceptedSeq, Disposition: d, BeforeStateRevision: before.StateRevision, AfterStateRevision: after.StateRevision, Reason: reason}
}
func scalarCommit(before, next StateV2, index ReceiptIndexV2, e EventV2, r ReceiptV2, consumption *EvidenceConsumption, attempt AttemptDisposition) (TransitionV2, error) {
	out := scalarClone(index)
	out.Receipts = append(out.Receipts, r)
	if consumption != nil {
		out.EvidenceConsumptions = append(out.EvidenceConsumptions, *consumption)
		sort.Slice(out.EvidenceConsumptions, func(i, j int) bool {
			return out.EvidenceConsumptions[i].IdentitySHA256 < out.EvidenceConsumptions[j].IdentitySHA256
		})
	}
	if e.Kind == "invalidate" && before.Status == "active" {
		f, err := scalarInvalidatedFence(out.LifecycleFence, e)
		if err != nil {
			return TransitionV2{}, err
		}
		out.LifecycleFence = f
	}
	step := StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: index.Cursor.PrefixSHA256, StreamID: index.StreamID, ClockDomain: index.ClockDomain, AcceptedSeq: e.AcceptedSeq, AcceptedAtNS: e.AcceptedAtNS, PayloadSHA256: e.PayloadSHA256}
	h, err := CanonicalSHA256("StreamAcceptedStepProjectionV2", step)
	if err != nil {
		return TransitionV2{}, err
	}
	out.Cursor = StreamCursorV2{StreamID: index.StreamID, ClockDomain: index.ClockDomain, AcceptedAtNS: e.AcceptedAtNS, AcceptedSeq: e.AcceptedSeq, PrefixSHA256: h}
	projected, err := project(reflect.ValueOf(out), reflect.TypeFor[ReceiptIndexV2HashProjection]())
	if err != nil {
		return TransitionV2{}, err
	}
	out, err = scalarSeal[ReceiptIndexV2]("ReceiptIndexV2", projected.Interface())
	if err != nil {
		return TransitionV2{}, err
	}
	return scalarTransition(before.StateSHA256, next, out, r, attempt)
}
func scalarInvalidatedFence(f LifecycleFence, e EventV2) (LifecycleFence, error) {
	if f.Epoch == math.MaxInt64 {
		return LifecycleFence{}, contractError("resource_limit", "$.lifecycle_fence.epoch")
	}
	i := scalarFencePosition(f, e.Key)
	if i < 0 {
		return LifecycleFence{}, contractError("source_unavailable", "$.lifecycle_fence.entries")
	}
	f.Entries[i].Status = "invalidated"
	f.Entries[i].Reason = e.Reason
	f.Epoch++
	p, err := project(reflect.ValueOf(f), reflect.TypeFor[LifecycleFenceHashProjection]())
	if err != nil {
		return LifecycleFence{}, err
	}
	return scalarSeal[LifecycleFence]("LifecycleFence", p.Interface())
}
func scalarTransition(beforeSHA string, state StateV2, index ReceiptIndexV2, r ReceiptV2, d AttemptDisposition) (TransitionV2, error) {
	p := TransitionV2HashProjection{BeforeStateSHA256: beforeSHA, State: state, Receipts: index, Receipt: r, Cursor: index.Cursor, AttemptDisposition: d}
	return scalarSeal[TransitionV2]("TransitionV2", p)
}
func sealScalarState(s StateV2) (StateV2, error) {
	p, err := project(reflect.ValueOf(s), reflect.TypeFor[StateV2HashProjection]())
	if err != nil {
		return StateV2{}, err
	}
	return scalarSeal[StateV2]("StateV2", p.Interface())
}

// scalarSeal accepts only a named compiled projection and returns a complete wire
// record after setting its exact domain hash; no placeholder record is published.
func scalarSeal[T any](name string, projection any) (T, error) {
	var zero T
	projectionName, ok := fixedProjections[name]
	if !ok {
		return zero, contractError("unsupported_type", "$")
	}
	if err := ValidateTyped(projectionName, projection); err != nil {
		return zero, err
	}
	wire := reflect.New(reflect.TypeFor[T]()).Elem()
	rule := fixedObjects[name]
	if wire.Type() != rule.typ {
		return zero, contractError("invalid_type", "$")
	}
	src := reflect.ValueOf(projection)
	for i := 0; i < src.NumField(); i++ {
		wire.FieldByName(src.Type().Field(i).Name).Set(scalarCloneValue(src.Field(i)))
	}
	b, err := canonical(src)
	if err != nil {
		return zero, err
	}
	wire.FieldByName(fixedHashFields[name]).SetString(domainSHA(projectionName, b))
	result := wire.Interface().(T)
	if err = ValidateTyped(name, result); err != nil {
		return zero, err
	}
	return result, nil
}
func scalarPointer[T any](v T) *T { return &v }
func scalarClone[T any](v T) T    { return scalarCloneValue(reflect.ValueOf(v)).Interface().(T) }
func scalarCloneValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(scalarCloneValue(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(scalarCloneValue(v.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			out.Field(i).Set(scalarCloneValue(v.Field(i)))
		}
		return out
	default:
		return v
	}
}

func scalarPendingHistory(s StateV2, index ReceiptIndexV2, cfg DynamicsConfigV2) error {
	if err := scalarPendingConfig(s, cfg); err != nil {
		return err
	}
	for _, r := range index.Receipts {
		if r.Key == s.Key {
			return contractError("snapshot_binding", "$.state")
		}
	}
	return nil
}
func scalarStateHistory(s StateV2, index ReceiptIndexV2, cfg DynamicsConfigV2) error {
	if s.Status == "pending_activation" {
		return scalarPendingHistory(s, index, cfg)
	}
	var latest, applied, activation *ReceiptV2
	invalidated := false
	for i := range index.Receipts {
		r := &index.Receipts[i]
		if r.Key != s.Key {
			continue
		}
		latest = r
		if r.Disposition == "applied" {
			if invalidated {
				return contractError("snapshot_binding", "$.state")
			}
			applied = r
		}
		if r.Disposition == "invalidated" {
			invalidated = true
		}
		if r.AcceptedSeq == *s.ActivationAcceptedSeq {
			activation = r
		}
	}
	if latest == nil || applied == nil || activation == nil {
		return contractError("snapshot_binding", "$.state")
	}
	if latest.AfterStateRevision != s.StateRevision || applied.AcceptedSeq != *s.LastAppliedSeq || applied.AcceptedAtNS != *s.LastAppliedAtNS {
		return contractError("snapshot_binding", "$.state")
	}
	if invalidated != (s.Status == "invalidated") {
		return contractError("snapshot_binding", "$.state")
	}
	if applied.AcceptedSeq == activation.AcceptedSeq && s.Value != cfg.Initial {
		return contractError("snapshot_binding", "$.state")
	}
	if err := scalarStateReason(s, index.LifecycleFence, applied.Reason); err != nil {
		return err
	}
	return scalarActivationHistory(s, *activation)
}
func scalarStateReason(s StateV2, fence LifecycleFence, applied Reason) error {
	expected := applied
	if s.Status == "invalidated" {
		i := scalarFencePosition(fence, s.Key)
		if i < 0 || fence.Entries[i].Status != "invalidated" {
			return contractError("snapshot_binding", "$.state")
		}
		expected = fence.Entries[i].Reason
	}
	if s.Reason != expected {
		return contractError("snapshot_binding", "$.state")
	}
	return nil
}
func scalarActivationHistory(s StateV2, r ReceiptV2) error {
	if r.Disposition != "applied" || r.Reason != "activation" || r.BeforeStateRevision != 0 || r.AfterStateRevision != 1 {
		return contractError("snapshot_binding", "$.state")
	}
	if r.EventID != *s.ActivationEventID || r.PayloadSHA256 != *s.ActivationPayloadSHA256 || r.AcceptedAtNS != *s.ActivatedAtNS {
		return contractError("snapshot_binding", "$.state")
	}
	return nil
}

func scalarPendingConfig(s StateV2, cfg DynamicsConfigV2) error {
	if s.Status == "pending_activation" && s.Value != cfg.Initial {
		return contractError("snapshot_binding", "$.state")
	}
	return nil
}

type scalarReceiptProgress struct {
	revision    int64
	invalidated bool
}

func scalarReceiptHistory(index ReceiptIndexV2) error {
	histories := make(map[DynamicsKeyV2]scalarReceiptProgress)
	for _, r := range index.Receipts {
		previous, exists := histories[r.Key]
		next, ok := scalarReceiptStep(previous, exists, r)
		if !ok {
			return contractError("snapshot_binding", "$.state")
		}
		histories[r.Key] = next
	}
	return nil
}
func scalarReceiptStep(previous scalarReceiptProgress, exists bool, r ReceiptV2) (scalarReceiptProgress, bool) {
	if !exists {
		return scalarReceiptProgress{revision: 1}, r.Disposition == "applied" && r.Reason == "activation" && r.BeforeStateRevision == 0 && r.AfterStateRevision == 1
	}
	if r.BeforeStateRevision != previous.revision {
		return previous, false
	}
	if previous.invalidated {
		return previous, r.Disposition == "non_learning" && r.Reason == "invalidated" && r.AfterStateRevision == previous.revision
	}
	switch r.Disposition {
	case "applied":
		if !scalarOutcomeReceiptReason(r.Reason) {
			return previous, false
		}
	case "invalidated":
		if r.Reason != "invalidated" {
			return previous, false
		}
		previous.invalidated = true
	case "non_learning":
		return previous, (r.Reason == "exposure_only" || r.Reason == "duplicate_evidence") && r.AfterStateRevision == previous.revision
	default:
		return previous, false
	}
	if previous.revision == math.MaxInt64 || r.AfterStateRevision != previous.revision+1 {
		return previous, false
	}
	previous.revision = r.AfterStateRevision
	return previous, true
}
func scalarOutcomeReceiptReason(r Reason) bool {
	switch r {
	case "passed_independent", "failed_attributable", "unknown_outcome", "failed_unattributable", "task_unknown", "source_unknown":
		return true
	default:
		return false
	}
}

// FastSlowConfig pairs two complete scalar configs for the DYN-02 fastslow
// substage. It is a development record outside dynamics-contract-v2: each lane
// keeps mode "scalar" and its own config hash, and no coefficient has a default.
type FastSlowConfig struct {
	Fast       DynamicsConfigV2
	Slow       DynamicsConfigV2
	FastWeight float64
}

// FastSlowState, FastSlowIndex and FastSlowEvent hold one value per lane. The two
// lane events are the same logical event sealed under each lane's config hash.
type FastSlowState struct{ Fast, Slow StateV2 }
type FastSlowIndex struct{ Fast, Slow ReceiptIndexV2 }
type FastSlowEvent struct{ Fast, Slow EventV2 }
type FastSlowTransition struct{ Fast, Slow TransitionV2 }

const fastSlowPairPath = "$.fast_slow.pair"

// ApplyEventFastSlow runs the reviewed scalar transition once per lane. Lanes
// must stand at the same accepted history; either both succeed or neither does.
func ApplyEventFastSlow(previous FastSlowState, index FastSlowIndex, event FastSlowEvent, cfg FastSlowConfig) (FastSlowTransition, error) {
	if err := checkFastSlowConfig(cfg); err != nil {
		return FastSlowTransition{}, err
	}
	if !pairedStates(previous.Fast, previous.Slow) || !pairedReceipts(index.Fast, index.Slow) {
		return FastSlowTransition{}, contractError("snapshot_binding", fastSlowPairPath)
	}
	if err := checkFastSlowEvent(event); err != nil {
		return FastSlowTransition{}, err
	}
	fast, err := ApplyEvent(previous.Fast, index.Fast, event.Fast, cfg.Fast)
	if err != nil {
		return FastSlowTransition{}, err
	}
	slow, err := ApplyEvent(previous.Slow, index.Slow, event.Slow, cfg.Slow)
	if err != nil {
		return FastSlowTransition{}, err
	}
	// Paired states, receipts and events make every lane decision identical;
	// only the numeric values differ by rate.
	return FastSlowTransition{Fast: fast, Slow: slow}, nil
}

// fastSlowValue mixes two lane values; callers have already checked the config.
func fastSlowValue(fast, slow float64, cfg FastSlowConfig) float64 {
	return cfg.FastWeight*fast + (1-cfg.FastWeight)*slow
}

func checkFastSlowConfig(cfg FastSlowConfig) error {
	for _, c := range []DynamicsConfigV2{cfg.Fast, cfg.Slow} {
		if err := ValidateTyped("DynamicsConfigV2", c); err != nil {
			return err
		}
	}
	f, s := cfg.Fast, cfg.Slow
	// An unset (zero) weight or a weight of one would silently drop a lane.
	if !(cfg.FastWeight > 0 && cfg.FastWeight < 1) {
		return contractError("invalid_value", "$.fast_weight")
	}
	if f.TauNS >= s.TauNS {
		return contractError("config_mismatch", "$.fast_slow.tau_ns")
	}
	// Only rates differ between lanes; shared fields keep the mixed initial value
	// and the evidence/clock semantics identical to the scalar substage.
	if f.ClockDomain != s.ClockDomain || f.Clock != s.Clock || f.Initial != s.Initial || f.UnitStrength != s.UnitStrength || f.NegativePolicy != s.NegativePolicy || f.DecayEnabled != s.DecayEnabled || f.OutcomeJumpEnabled != s.OutcomeJumpEnabled || f.EvidenceRuleSHA256 != s.EvidenceRuleSHA256 {
		return contractError("config_mismatch", "$.fast_slow")
	}
	return nil
}

// Lane config hashes are checked by ApplyEvent/ReplayPrefix against each lane.
func checkFastSlowEvent(e FastSlowEvent) error {
	a, b := scalarClone(e.Fast), scalarClone(e.Slow)
	a.Key.ConfigSHA256, b.Key.ConfigSHA256 = "", ""
	a.PayloadSHA256, b.PayloadSHA256 = "", ""
	if !reflect.DeepEqual(a, b) {
		return contractError("event_conflict", fastSlowPairPath)
	}
	return nil
}

// pairedStates requires the same target and lifecycle position in both lanes.
func pairedStates(a, b StateV2) bool {
	ka, kb := a.Key, b.Key
	ka.ConfigSHA256, kb.ConfigSHA256 = "", ""
	return ka == kb && a.StateRevision == b.StateRevision && a.Status == b.Status && reflect.DeepEqual(a.LastAppliedSeq, b.LastAppliedSeq)
}

// pairedSnapshots requires one scope, dimension, accepted history and lane states.
func pairedSnapshots(a, b SnapshotV2) bool {
	if a.Scope != b.Scope || a.Dimension != b.Dimension || len(a.States) != len(b.States) || !pairedReceipts(a.Receipts, b.Receipts) {
		return false
	}
	for i := range a.States {
		if !pairedStates(a.States[i], b.States[i]) {
			return false
		}
	}
	return true
}

// pairedReceipts requires the same accepted history; values may differ by rate.
func pairedReceipts(a, b ReceiptIndexV2) bool {
	if a.StreamID != b.StreamID || a.Cursor.AcceptedSeq != b.Cursor.AcceptedSeq || a.Cursor.AcceptedAtNS != b.Cursor.AcceptedAtNS || len(a.Receipts) != len(b.Receipts) || len(a.EvidenceConsumptions) != len(b.EvidenceConsumptions) {
		return false
	}
	for i := range a.EvidenceConsumptions {
		if a.EvidenceConsumptions[i].IdentitySHA256 != b.EvidenceConsumptions[i].IdentitySHA256 || a.EvidenceConsumptions[i].ClaimSHA256 != b.EvidenceConsumptions[i].ClaimSHA256 {
			return false
		}
	}
	for i := range a.Receipts {
		x, y := a.Receipts[i], b.Receipts[i]
		kx, ky := x.Key, y.Key
		kx.ConfigSHA256, ky.ConfigSHA256 = "", ""
		if kx != ky || x.EventID != y.EventID || x.AcceptedSeq != y.AcceptedSeq || x.AcceptedAtNS != y.AcceptedAtNS || x.Disposition != y.Disposition || x.Reason != y.Reason || x.BeforeStateRevision != y.BeforeStateRevision || x.AfterStateRevision != y.AfterStateRevision {
			return false
		}
	}
	return true
}

// Validate checks a lane pair for callers outside this package (the product
// search wiring) with the same rules as the fast/slow transition.
func (cfg FastSlowConfig) Validate() error { return checkFastSlowConfig(cfg) }

// MixFastSlow mixes two lane values read at the same time after checking cfg.
func MixFastSlow(fast, slow float64, cfg FastSlowConfig) (float64, error) {
	if err := checkFastSlowConfig(cfg); err != nil {
		return 0, err
	}
	return fastSlowValue(fast, slow, cfg), nil
}
