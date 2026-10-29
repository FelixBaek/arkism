package memory

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

type nativeDynamicsTxQueries interface {
	NativeDynamicsRows(context.Context, Scope, string, string, string, int) ([]Record, error)
	NativeDynamicsPendingCaptures(context.Context, Scope, int) ([]Record, error)
	NativeDynamicsPendingPurges(context.Context, Scope, int) ([]Record, error)
}

func nativeGetTx(ctx context.Context, tx Tx, scope Scope, id, role string) (Record, error) {
	r, err := tx.Get(ctx, colJobs, scope, id)
	if err != nil {
		return Record{}, err
	}
	if r.Scope != scope || r.ID != id || r.Kind != nativeDynamicsPrefix+role || r.NativeDynamics == nil || r.NativeDynamics.Protocol != nativeDynamicsProtocol {
		return Record{}, ErrConflict
	}
	p, err := nativePayload(r)
	if err != nil {
		return Record{}, err
	}
	r.NativeDynamics = p
	return r, nil
}
func nativeInsertTx(ctx context.Context, tx Tx, r Record) error {
	p, err := nativePayload(r)
	if err != nil {
		return err
	}
	r.NativeDynamics = p
	return tx.Insert(ctx, colJobs, r)
}
func nativeReplaceTx(ctx context.Context, tx Tx, r Record) error {
	p, e := nativePayload(r)
	if e != nil {
		return e
	}
	r.NativeDynamics = p
	before := r.Revision
	if before == math.MaxInt64 {
		return ErrInvalid
	}
	r.Revision++
	return tx.Replace(ctx, colJobs, r, before)
}
func nativeBlockTx(ctx context.Context, tx Tx, row Record, reason string) error {
	row.NativeDynamics.Reason = reason
	row.Status = JobStatusFailed
	return nativeReplaceTx(ctx, tx, row)
}
func nativeMergeDeps(a, b []NativeDynamicsDependency) ([]NativeDynamicsDependency, error) {
	seen := map[Ref]string{}
	for _, d := range append(slices.Clone(a), b...) {
		if old, ok := seen[d.Ref]; ok && old != d.Hash {
			return nil, ErrConflict
		}
		seen[d.Ref] = d.Hash
	}
	out := make([]NativeDynamicsDependency, 0, len(seen))
	for ref, h := range seen {
		out = append(out, NativeDynamicsDependency{ref, h})
	}
	nativeSortDeps(out)
	return out, nil
}
func nativeIdentityTx(ctx context.Context, tx Tx, scope Scope, in MemoryOutcomeInput) (string, string, error) {
	type pin struct {
		Ref           Ref
		ContentSHA256 string
	}
	pins := make([]pin, 0, len(in.Evidence))
	for _, ref := range in.Evidence {
		r, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
		if err != nil {
			return "", "", err
		}
		pins = append(pins, pin{ref, ContentHash(r.Text)})
	}
	slices.SortFunc(pins, func(a, b pin) int {
		if structuredText(a.Ref) < structuredText(b.Ref) {
			return -1
		}
		if structuredText(a.Ref) > structuredText(b.Ref) {
			return 1
		}
		return 0
	})
	identity := nativeHash("observation_identity", struct {
		Scope         Scope
		Target        Ref
		Dimension     string
		Trace         Ref
		CheckerSHA256 string
		Pins          []pin
	}{scope, in.Target, in.Dimension, in.Trace, in.CheckerSHA256, pins})
	claim := nativeHash("observation_claim", struct {
		Identity    string
		Passed      bool
		Attribution string
	}{identity, in.Passed, "unattributable"})
	return identity, claim, nil
}
func nativeSeenID(scope Scope, identity string) string {
	return nativeID("seen", struct {
		Scope    Scope
		Identity string
	}{scope, identity})
}
func nativeConsumptionID(scope Scope, identity string) string {
	return nativeID("consumption", struct {
		Scope    Scope
		Identity string
	}{scope, identity})
}
func nativeSetCaptureReceiptTx(ctx context.Context, tx Tx, capture Record, receipt NativeDynamicsReceipt) error {
	capture.NativeDynamics.Receipt = &receipt
	capture.NativeDynamics.Reason = ""
	capture.Status = JobStatusDone
	return nativeReplaceTx(ctx, tx, capture)
}
func nativeAdvanceTx(ctx context.Context, tx Tx, capture, state Record, cfg NativeDynamicsConfig, deps []NativeDynamicsDependency) (NativeDynamicsReceipt, error) {
	p := capture.NativeDynamics
	st := state.NativeDynamics.State
	if p.Input == nil || st == nil || st.Value == nil || st.Status != "active" {
		return NativeDynamicsReceipt{}, ErrConflict
	}
	identity, claim, err := nativeIdentityTx(ctx, tx, p.Key.Scope, *p.Input)
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	seen, err := nativeGetTx(ctx, tx, p.Key.Scope, nativeSeenID(p.Key.Scope, identity), "seen")
	if err == nil {
		return nativeDuplicateObservationTx(ctx, tx, capture, seen, claim)
	}
	if !errors.Is(err, ErrNotFound) {
		return NativeDynamicsReceipt{}, err
	}
	if len(st.History) >= int(*cfg.MaxHistory) || len(deps) > int(*cfg.MaxDependencies) || st.LastSeq == math.MaxInt64 {
		return NativeDynamicsReceipt{}, ErrInvalid
	}
	value, sourceNS, learning, err := nativeAdvanceNumeric(p, st, cfg)
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	merged, err := nativeMergeDeps(state.NativeDynamics.Dependencies, deps)
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	if len(merged) > int(*cfg.MaxDependencies) {
		return NativeDynamicsReceipt{}, ErrInvalid
	}
	seq := st.LastSeq + 1
	eventID := nativeID("event", struct {
		Key NativeDynamicsKey
		Seq int64
	}{p.Key, seq})
	receipt := NativeDynamicsReceipt{Protocol: nativeDynamicsProtocol, Disposition: "applied", Capture: nativeRef(capture), Event: Ref{Collection: colJobs, ID: eventID, Revision: 1}, State: Ref{Collection: colJobs, ID: state.ID, Revision: state.Revision + 1}, ProcessingSeq: &seq, SourceAcceptedAt: p.SourceAcceptedAt, AppliedNow: true, LearningConsumed: learning}
	eventPayload := NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Key: p.Key, IdentitySHA: identity, ClaimSHA: claim, ObservedPassed: &p.Input.Passed, Receipt: &receipt, Dependencies: deps, DependencyKeys: nativeDependencyKeys(p.Key.Scope, deps), SourceAcceptedAt: p.SourceAcceptedAt}
	if e := nativePersistObservationTx(ctx, tx, p.Key.Scope, eventID, identity, eventPayload, learning); e != nil {
		return NativeDynamicsReceipt{}, e
	}
	st.Value = &value
	st.LastSeq = seq
	st.LastModelNS = sourceNS
	st.History = append(slices.Clone(st.History), nativeRef(capture))
	state.NativeDynamics.Dependencies = merged
	state.NativeDynamics.DependencyKeys = nativeDependencyKeys(p.Key.Scope, merged)
	if err := nativeReplaceTx(ctx, tx, state); err != nil {
		return NativeDynamicsReceipt{}, err
	}
	checkpoint := nativeJob(p.Key.Scope, "checkpoint", nativeID("checkpoint", struct {
		Key NativeDynamicsKey
		Seq int64
	}{p.Key, seq}), NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Key: p.Key, Receipt: &receipt, Dependencies: merged, DependencyKeys: nativeDependencyKeys(p.Key.Scope, merged)})
	if err := nativeInsertTx(ctx, tx, checkpoint); err != nil {
		return NativeDynamicsReceipt{}, err
	}
	if err := nativeSetCaptureReceiptTx(ctx, tx, capture, receipt); err != nil {
		return NativeDynamicsReceipt{}, err
	}
	return receipt, nil
}
func nativeDuplicateObservationTx(ctx context.Context, tx Tx, capture, seen Record, claim string) (NativeDynamicsReceipt, error) {
	p := seen.NativeDynamics
	if p.ClaimSHA != claim || p.Receipt == nil {
		return NativeDynamicsReceipt{}, ErrConflict
	}
	receipt := *p.Receipt
	receipt.Disposition = "duplicate_observation"
	receipt.Capture = nativeRef(capture)
	receipt.AppliedNow = false
	receipt.LearningConsumed = false
	if err := nativeSetCaptureReceiptTx(ctx, tx, capture, receipt); err != nil {
		return NativeDynamicsReceipt{}, err
	}
	return receipt, nil
}
func nativeInsertOrSameTx(ctx context.Context, tx Tx, row Record) error {
	old, err := tx.Get(ctx, colJobs, row.Scope, row.ID)
	if errors.Is(err, ErrNotFound) {
		return nativeInsertTx(ctx, tx, row)
	}
	if err != nil {
		return err
	}
	if structuredText(old.NativeDynamics) != structuredText(row.NativeDynamics) {
		return ErrConflict
	}
	return nil
}
func nativeTimeNS(at time.Time) (int64, error) {
	c := nativeOutcomeClock(at)
	if c.State != "known" {
		return 0, ErrInvalid
	}
	return *c.UnixNS, nil
}

func nativeAdvanceNumeric(p *NativeDynamicsPayload, st *NativeDynamicsState, cfg NativeDynamicsConfig) (float64, int64, bool, error) {
	clock := nativeOutcomeClock(p.SourceAcceptedAt)
	if clock.State != "known" || *clock.UnixNS < st.LastModelNS {
		return 0, 0, false, ErrConflict
	}
	operation := "decay_only"
	var strength *float64
	learning := p.Input.Passed && *cfg.PositiveLearning
	if learning {
		operation = "pass"
		strength = cfg.ObservationStrength
	}
	value, e := dynamicsv2.NativeAdvanceScalarNumeric(*st.Value, st.LastModelNS, *clock.UnixNS, *cfg.TauNS, *cfg.DecayEnabled, operation, *cfg.EtaPositive, *cfg.UnitStrength, strength)
	return value, *clock.UnixNS, learning, e
}
func nativePersistObservationTx(ctx context.Context, tx Tx, scope Scope, eventID, identity string, payload NativeDynamicsPayload, learning bool) error {
	if e := nativeInsertTx(ctx, tx, nativeJob(scope, "event", eventID, payload)); e != nil {
		return e
	}
	if e := nativeInsertTx(ctx, tx, nativeJob(scope, "seen", nativeSeenID(scope, identity), payload)); e != nil {
		return e
	}
	if learning {
		return nativeInsertTx(ctx, tx, nativeJob(scope, "consumption", nativeConsumptionID(scope, identity), payload))
	}
	return nil
}
