package memory

import (
	"context"
	"errors"
	"math"
	"time"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

type nativeSearchSourcePreview struct {
	hit  Hit
	key  NativeDynamicsKey
	row  Record
	deps []NativeDynamicsDependency
}
type nativeSearchSourceBinding struct {
	Protocol                         string
	NativeProtocol                   string
	NativeConfigSHA                  string
	StateRefSHA                      string
	StateRevision, Epoch, ModelNS    int64
	LineageSHA                       string
	RankerSHA, ArithmeticDynamicsSHA string
	ProjectionNS                     int64
	ValueProjectionNS                int64
}

func (s *Service) nativePublishSearchValues(ctx context.Context, token string, in SearchInput, actor Decision, snap *nativeSearchSnapshot, cfg NativeSearchShadowConfig) ([]nativeBridgeValue, time.Time, string, string) {
	entry, ok := nativeSearchPolicy(cfg)
	if !ok {
		return nil, time.Time{}, "", "unsupported_policy"
	}
	if entry.PolicyID != "B0" && len(snap.history) > 0 {
		return nil, time.Time{}, "", "unsupported_context"
	}
	state, importance := false, false
	for _, lane := range entry.Ranker.Lanes {
		state = state || lane.Kind == "state"
		importance = importance || lane.Kind == "importance"
	}
	if importance {
		return nil, time.Time{}, "", "missing_feature"
	}
	if !state {
		now := time.Now().UTC()
		return []nativeBridgeValue{}, now, nativeSearchHash("CandidateOnlyRead", struct{ Protocol, Candidates string }{"native-candidate-publication-v1", snap.digest}), ""
	}
	if actor.Role != roleService {
		return nil, time.Time{}, "", "native_service_role_required"
	}
	if !validRunLabel(cfg.NativeVersion, 128) || !nativeDimension(cfg.Dimension) {
		return nil, time.Time{}, "", "invalid_native_binding"
	}
	bound, err := s.nativeMaintenanceActor(bindRequestActor(ctx, actor), token, in.Scope, "read:jobs")
	if err != nil {
		return nil, time.Time{}, "", nativeSearchSourceError(ctx, err)
	}
	previews, err := s.nativeSearchSourcePreviews(bound, token, in, snap, cfg)
	if err != nil {
		return nil, time.Time{}, "", nativeSearchSourceError(ctx, err)
	}
	values, bindings, err := s.nativeSearchReadValues(bound, in, previews, entry)
	if err != nil {
		return nil, time.Time{}, "", nativeSearchSourceError(ctx, err)
	}
	published := time.Now().UTC()
	if nativeBridgeClockOf(published).UnixNS == nil {
		return nil, time.Time{}, "", "unrepresentable_clock"
	}
	if code := nativeSearchProjectPublication(values, bindings, entry, published); code != "" {
		return nil, time.Time{}, "", code
	}
	digest := nativeSearchHash("AuthenticatedReadPublication", struct {
		Protocol     string
		Scope        Scope
		CandidateSHA string
		Bindings     []nativeSearchSourceBinding
		PublishedAt  time.Time
	}{"native-read-publication-v1", in.Scope, snap.digest, bindings, published})
	if digest == "" {
		return nil, time.Time{}, "", "invalid_source"
	}
	return values, published, digest, ""
}
func (s *Service) nativeSearchSourcePreviews(ctx context.Context, token string, in SearchInput, snap *nativeSearchSnapshot, cfg NativeSearchShadowConfig) ([]nativeSearchSourcePreview, error) {
	out := make([]nativeSearchSourcePreview, 0, len(snap.unique))
	for _, hit := range snap.unique {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if hit.Origin != "memory" || hit.Collection != colMemories {
			return nil, ErrInvalid
		}
		key := NativeDynamicsKey{Scope: in.Scope, Target: Ref{Collection: hit.Collection, ID: hit.ID, Revision: hit.Revision}, Dimension: cfg.Dimension, Version: cfg.NativeVersion}
		row, err := s.Repo.Get(ctx, colJobs, in.Scope, nativeStateID(key))
		if err != nil {
			return nil, err
		}
		payload, err := nativePayload(row)
		if err != nil {
			return nil, err
		}
		if payload.Key != key || payload.State == nil || payload.State.Status != "active" {
			return nil, ErrConflict
		}
		deps, err := s.nativeAuthorizeProof(ctx, token, in.Scope, nativeDepRefs(payload.Dependencies))
		if err != nil {
			return nil, err
		}
		row.NativeDynamics = payload
		out = append(out, nativeSearchSourcePreview{hit, key, row, deps})
	}
	return out, nil
}
func nativeSearchSourceValueTx(ctx context.Context, tx Tx, in SearchInput, p nativeSearchSourcePreview, entry dyn.PolicyEntry, projection time.Time) (nativeBridgeValue, nativeSearchSourceBinding, error) {
	var empty nativeBridgeValue
	var noBinding nativeSearchSourceBinding
	clock := nativeBridgeClockOf(projection)
	if clock.UnixNS == nil {
		return empty, noBinding, ErrInvalid
	}
	view, err := nativeReadSnapshotTx(ctx, tx, NativeDynamicsQuery{Scope: in.Scope, Target: p.key.Target, Dimension: p.key.Dimension, Version: p.key.Version, CutoffNS: *clock.UnixNS}, p.key, p.row, p.deps)
	if err != nil {
		return empty, noBinding, err
	}
	if view.Status != "known" || view.Value == nil || math.IsNaN(*view.Value) || math.IsInf(*view.Value, 0) {
		return empty, noBinding, ErrConflict
	}
	memory, err := nativeCurrentApprovedTx(ctx, tx, p.key)
	if err != nil {
		return empty, noBinding, err
	}
	if searchHitHash(HitFromRecord(colMemories, memory)) != searchHitHash(p.hit) {
		return empty, noBinding, ErrConflict
	}
	config, err := nativeGetTx(ctx, tx, in.Scope, nativeConfigID(in.Scope, p.key.Version), "config")
	if err != nil {
		return empty, noBinding, err
	}
	if !nativeSearchCompatibleConfig(config.NativeDynamics.Config, entry.Dynamics) {
		return empty, noBinding, ErrInvalid
	}
	current, err := nativeGetTx(ctx, tx, in.Scope, p.row.ID, "state")
	if err != nil {
		return empty, noBinding, err
	}
	if current.Revision != p.row.Revision || current.NativeDynamics.Key != p.key {
		return empty, noBinding, ErrConflict
	}
	st := current.NativeDynamics.State
	if st == nil {
		return empty, noBinding, ErrConflict
	}
	lineage := nativeSearchHash("ValidatedLineage", current.NativeDynamics.Dependencies)
	dynamicsSHA := ""
	if entry.Dynamics != nil {
		dynamicsSHA = entry.Dynamics.ConfigSHA256
	}
	binding := nativeSearchSourceBinding{Protocol: "native-read-publication-v1", NativeProtocol: nativeDynamicsProtocol, NativeConfigSHA: current.NativeDynamics.ConfigHash, StateRefSHA: nativeSearchHash("StateRef", nativeRef(current)), StateRevision: current.Revision, Epoch: st.Epoch, ModelNS: st.LastModelNS, LineageSHA: lineage, RankerSHA: entry.Ranker.ConfigSHA256, ArithmeticDynamicsSHA: dynamicsSHA, ProjectionNS: *clock.UnixNS}
	guard := nativeSearchHash("ValidatedState", struct {
		Ref     Ref
		Payload *NativeDynamicsPayload
	}{nativeRef(current), current.NativeDynamics})
	value := *view.Value
	active := st.ActivationAt
	yes := true
	v := nativeBridgeValue{Scope: in.Scope, Target: nativeBridgeMaterialOf(p.hit), Kind: "state", ConfigSHA: entry.Ranker.ConfigSHA256, DynamicsSHA: dynamicsSHA, LineageSHA: lineage, SourceScope: in.Scope, SourceID: current.ID, SourceRevision: current.Revision, SourceGuardSHA: guard, ClockDomain: "native-server-read-publication-v1", ObservedAt: projection, Current: &yes, Status: "present", Value: &value, ActivatedAt: &active}
	return v, binding, nil
}
func nativeSearchCompatibleConfig(native *NativeDynamicsConfig, arithmetic *dyn.DynamicsConfigV2) bool {
	if native == nil || arithmetic == nil || !nativeConfigValid(*native) {
		return false
	}
	// This binds the applicable scalar view settings. Native pass-only provenance remains its own protocol, never synthetic event qualification.
	return *native.InitialValue == arithmetic.Initial && *native.TauNS == arithmetic.TauNS && *native.EtaPositive == arithmetic.EtaPositive && *native.UnitStrength == arithmetic.UnitStrength && *native.DecayEnabled == arithmetic.DecayEnabled && *native.PositiveLearning == arithmetic.OutcomeJumpEnabled
}
func nativeSearchSourceError(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "shadow_deadline"
	}
	if errors.Is(err, ErrDenied) {
		return "native_source_unauthorized"
	}
	if errors.Is(err, ErrNotFound) {
		return "missing_state"
	}
	if errors.Is(err, ErrConflict) {
		return "stale_source"
	}
	return "invalid_source"
}

func (s *Service) nativeSearchReadValues(bound context.Context, in SearchInput, previews []nativeSearchSourcePreview, entry dyn.PolicyEntry) ([]nativeBridgeValue, []nativeSearchSourceBinding, error) {
	var values []nativeBridgeValue
	var bindings []nativeSearchSourceBinding
	err := s.Repo.Transaction(bound, func(ctx context.Context, tx Tx) error {
		values = nil
		bindings = nil
		if err := nativeCheckRestorePublicationTx(ctx, tx); err != nil {
			return err
		}
		if e := s.dynamicsSnapshotReadTx(ctx, tx, in.Scope, previews); e != nil {
			return e
		}
		if len(previews) == 0 {
			return ctx.Err()
		}
		if _, e := nativeGetTx(ctx, tx, in.Scope, previews[0].row.ID, "state"); e != nil {
			return e
		}
		projection := time.Now().UTC()
		for _, preview := range previews {
			value, binding, e := nativeSearchSourceValueTx(ctx, tx, in, preview, entry, projection)
			if e != nil {
				return e
			}
			values = append(values, value)
			bindings = append(bindings, binding)
		}
		return ctx.Err()
	})
	return values, bindings, err
}

func nativeSearchProjectPublication(values []nativeBridgeValue, bindings []nativeSearchSourceBinding, entry dyn.PolicyEntry, published time.Time) string {
	for i := range values {
		if entry.Dynamics == nil || values[i].Value == nil {
			return "invalid_source"
		}
		observed := nativeBridgeClockOf(values[i].ObservedAt)
		if observed.UnixNS == nil {
			return "unrepresentable_clock"
		}
		value, e := dyn.NativeAdvanceScalarNumeric(*values[i].Value, *observed.UnixNS, *nativeBridgeClockOf(published).UnixNS, entry.Dynamics.TauNS, entry.Dynamics.DecayEnabled, "decay_only", entry.Dynamics.EtaPositive, entry.Dynamics.UnitStrength, nil)
		if e != nil {
			return "invalid_source"
		}
		values[i].Value = &value
		bindings[i].ValueProjectionNS = *nativeBridgeClockOf(published).UnixNS
		values[i].AvailableAt = published
		values[i].PublishedAt = published
	}
	return ""
}
