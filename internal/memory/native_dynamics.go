package memory

import (
	"context"
	"errors"
	"math"
	"reflect"

	"github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

func (s *Service) nativeActor(ctx context.Context, token string, scope Scope, action string) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	if s == nil || s.Repo == nil || s.NativeDynamics == nil || !s.NativeDynamics.Enabled {
		return ctx, ErrUnavailable
	}
	return s.nativeMaintenanceActor(ctx, token, scope, action)
}

func (s *Service) nativeMaintenanceActor(ctx context.Context, token string, scope Scope, action string) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	if s == nil || s.Repo == nil {
		return ctx, ErrUnavailable
	}
	who, err := s.authorize(ctx, token, action, scope, "", "")
	if err != nil {
		return ctx, err
	}
	if who.Role != roleService {
		return ctx, ErrDenied
	}
	ctx = bindRequestActor(ctx, who)
	if _, err := s.authorize(ctx, token, "read:jobs", scope, "", ""); err != nil {
		return ctx, err
	}
	return ctx, nil
}

func (s *Service) ActivateNativeMemoryDynamics(ctx context.Context, token string, in NativeDynamicsActivationInput) (NativeDynamicsReceipt, error) {
	if !validScope(in.Scope) || in.Target.Collection != colMemories || !validWorkCheckpointRef(in.Target) || !nativeDimension(in.Dimension) || !validRunLabel(in.RequestID, 128) || !nativeConfigValid(in.Config) {
		return NativeDynamicsReceipt{}, ErrInvalid
	}
	cfg, err := nativeClone(in.Config)
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	ctx, err = s.nativeActor(ctx, token, in.Scope, "write:jobs")
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	deps, err := s.nativeAuthorizeProof(ctx, token, in.Scope, []Ref{in.Target})
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	key := NativeDynamicsKey{Scope: in.Scope, Target: in.Target, Dimension: in.Dimension, Version: cfg.Version}
	hash := nativeConfigHash(cfg)
	request := nativeHash("activation_request", struct {
		Key                   NativeDynamicsKey
		ConfigHash, RequestID string
	}{key, hash, in.RequestID})
	var result NativeDynamicsReceipt
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		result = NativeDynamicsReceipt{}
		var transactionErr error
		result, transactionErr = nativeActivateDynamicsTx(ctx, tx, in, cfg, deps, key, hash, request)
		return transactionErr
	})
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return NativeDynamicsReceipt{}, err
	}
	return result, nil
}
func nativeSupersedeEnrollmentTx(ctx context.Context, tx Tx, key NativeDynamicsKey) error {
	old, err := nativeGetTx(ctx, tx, key.Scope, nativeEnrollmentID(key.Scope, key.Target, key.Dimension), "enrollment")
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if old.NativeDynamics.Key == key {
		return ErrConflict
	}
	state, err := nativeGetTx(ctx, tx, key.Scope, nativeStateID(old.NativeDynamics.Key), "state")
	if err != nil {
		return err
	}
	if state.NativeDynamics.State == nil || state.NativeDynamics.State.Epoch == math.MaxInt64 {
		return ErrConflict
	}
	state.NativeDynamics.State.Status = "superseded"
	state.NativeDynamics.State.Value = nil
	state.NativeDynamics.State.Epoch++
	return nativeReplaceTx(ctx, tx, state)
}

func (s *Service) ProcessNativeDynamicsCapture(ctx context.Context, token string, scope Scope, capture Ref) (NativeDynamicsReceipt, error) {
	if capture.Collection != colJobs || !validRunLabel(capture.ID, 256) || capture.Revision < 1 || !validScope(scope) {
		return NativeDynamicsReceipt{}, ErrInvalid
	}
	ctx, err := s.nativeActor(ctx, token, scope, "write:jobs")
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	row, err := s.Repo.Get(ctx, colJobs, scope, capture.ID)
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	p, err := nativeCapturePreview(row, scope, capture)
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	if _, err := s.authorize(ctx, token, "read:agent_runs", scope, "", ""); err != nil {
		return NativeDynamicsReceipt{}, err
	}
	deps, err := s.nativeAuthorizeProof(ctx, token, scope, nativeRoots(p.Evaluation, *p.Input))
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	statePreview, err := s.Repo.Get(ctx, colJobs, scope, nativeStateID(p.Key))
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	if statePreview.NativeDynamics == nil {
		return NativeDynamicsReceipt{}, ErrConflict
	}
	// Every previous contributing source remains a publication/admission dependency.
	priorDeps, err := s.nativeAuthorizeProof(ctx, token, scope, nativeDepRefs(statePreview.NativeDynamics.Dependencies))
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	var result NativeDynamicsReceipt
	var blocked error
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		result = NativeDynamicsReceipt{}
		blocked = nil
		var transactionErr error
		result, blocked, transactionErr = nativeProcessCaptureTx(ctx, tx, scope, capture, p, deps, priorDeps, statePreview)
		return transactionErr
	})
	if err != nil {
		return NativeDynamicsReceipt{}, err
	}
	if blocked != nil {
		return NativeDynamicsReceipt{}, blocked
	}
	if err := ctx.Err(); err != nil {
		return NativeDynamicsReceipt{}, err
	}
	return result, nil
}
func nativeDepRefs(deps []NativeDynamicsDependency) []Ref {
	refs := make([]Ref, 0, len(deps))
	for _, d := range deps {
		refs = append(refs, d.Ref)
	}
	return refs
}

func (s *Service) ReadNativeDynamicsSnapshot(ctx context.Context, token string, in NativeDynamicsQuery) (NativeDynamicsView, error) {
	out := NativeDynamicsView{Status: "missing", Authority: AuthorityDataOnly}
	if !validScope(in.Scope) || in.Target.Collection != colMemories || !validWorkCheckpointRef(in.Target) || !nativeDimension(in.Dimension) || !validRunLabel(in.Version, 128) {
		return NativeDynamicsView{}, ErrInvalid
	}
	ctx, err := s.nativeActor(ctx, token, in.Scope, "read:jobs")
	if err != nil {
		return NativeDynamicsView{}, err
	}
	if _, e := s.authorize(ctx, token, "read:memories", in.Scope, in.Target.ID, ""); e != nil {
		return NativeDynamicsView{}, e
	}
	key := NativeDynamicsKey{in.Scope, in.Target, in.Dimension, in.Version}
	row, err := s.Repo.Get(ctx, colJobs, in.Scope, nativeStateID(key))
	if errors.Is(err, ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return NativeDynamicsView{}, err
	}
	if row.NativeDynamics == nil || row.NativeDynamics.State == nil {
		return NativeDynamicsView{}, ErrConflict
	}
	if row.NativeDynamics.State.Status != "active" {
		out.Status = "invalidated"
		return out, nil
	}
	deps, err := s.nativeAuthorizeProof(ctx, token, in.Scope, nativeDepRefs(row.NativeDynamics.Dependencies))
	if err != nil {
		return NativeDynamicsView{}, err
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = NativeDynamicsView{Status: "missing", Authority: AuthorityDataOnly}
		var transactionErr error
		out, transactionErr = nativeReadSnapshotTx(ctx, tx, in, key, row, deps)
		return transactionErr
	})
	if err != nil {
		return NativeDynamicsView{}, err
	}
	if err := ctx.Err(); err != nil {
		return NativeDynamicsView{}, err
	}
	return out, nil
}

func (s *Service) DrainNativeDynamicsCaptures(ctx context.Context, token string, scope Scope, limit int) ([]NativeDynamicsReceipt, error) {
	if limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	ctx, err := s.nativeActor(ctx, token, scope, "write:jobs")
	if err != nil {
		return nil, err
	}
	var refs []Ref
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		refs = nil
		q, ok := tx.(nativeDynamicsTxQueries)
		if !ok {
			return ErrUnavailable
		}
		rows, e := q.NativeDynamicsPendingCaptures(ctx, scope, limit)
		if e != nil {
			return e
		}
		for _, r := range rows {
			if r.Status == JobStatusPending || r.Status == JobStatusRetry {
				refs = append(refs, nativeRef(r))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []NativeDynamicsReceipt{}
	for _, ref := range refs {
		r, e := s.ProcessNativeDynamicsCapture(ctx, token, scope, ref)
		if e != nil {
			return out, e
		}
		out = append(out, r)
	}
	return out, nil
}

func nativeCapturePreview(row Record, scope Scope, capture Ref) (*NativeDynamicsPayload, error) {
	if row.Kind != nativeDynamicsPrefix+"capture" || row.Scope != scope || row.NativeDynamics == nil || row.NativeDynamics.Input == nil || row.Revision < capture.Revision {
		return nil, ErrConflict
	}
	p, e := nativePayload(row)
	if e != nil {
		return nil, e
	}
	if p.Key.Scope != scope || p.Input.Scope != scope || p.Stamp == nil || p.Stamp.Mode != "enrolled" {
		return nil, ErrConflict
	}
	return p, nil
}
func nativeValidateCaptureSourceTx(ctx context.Context, tx Tx, scope Scope, cp *NativeDynamicsPayload) error {
	snapshot, e := resolveAcceptedNativeOutcomeTx(ctx, tx, cp.Evaluation, *cp.Input)
	if e != nil {
		return e
	}
	source, e := tx.Get(ctx, "evaluations", scope, cp.Evaluation.ID)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(source.NativeOutcomeCaptureStamp, cp.Stamp) || !snapshot.createdAt.Equal(cp.SourceAcceptedAt) {
		return ErrConflict
	}
	return nil
}
func nativeCurrentConfigTx(ctx context.Context, tx Tx, scope Scope, cp *NativeDynamicsPayload) (NativeDynamicsConfig, error) {
	enrollment, e := nativeGetTx(ctx, tx, scope, nativeEnrollmentID(scope, cp.Key.Target, cp.Key.Dimension), "enrollment")
	if e != nil {
		return NativeDynamicsConfig{}, e
	}
	if enrollment.NativeDynamics.Key != cp.Key || enrollment.NativeDynamics.ConfigHash != cp.ConfigHash {
		return NativeDynamicsConfig{}, ErrConflict
	}
	config, e := nativeGetTx(ctx, tx, scope, nativeConfigID(scope, cp.Key.Version), "config")
	if e != nil {
		return NativeDynamicsConfig{}, e
	}
	cfg := config.NativeDynamics.Config
	if cfg == nil || !nativeConfigValid(*cfg) || nativeConfigHash(*cfg) != cp.ConfigHash {
		return NativeDynamicsConfig{}, ErrConflict
	}
	return *cfg, nil
}

func nativeActivateDynamicsTx(ctx context.Context, tx Tx, in NativeDynamicsActivationInput, cfg NativeDynamicsConfig, deps []NativeDynamicsDependency, key NativeDynamicsKey, hash, request string) (result NativeDynamicsReceipt, resultErr error) {
	if _, ok := tx.(nativeDynamicsTxQueries); !ok {
		return result, ErrUnavailable
	}
	if err := nativeValidateProofTx(ctx, tx, in.Scope, deps); err != nil {
		return result, err
	}
	if e := nativeEnsureCatalogTx(ctx, tx, in.Scope); e != nil {
		return result, e
	}
	memory, err := nativeCurrentApprovedTx(ctx, tx, key)
	if err != nil {
		return result, err
	}
	state, err := nativeGetTx(ctx, tx, in.Scope, nativeStateID(key), "state")
	if err == nil {
		p := state.NativeDynamics
		if p.IdentitySHA != request || p.ConfigHash != hash || p.State == nil || p.State.Status != "active" || p.Receipt == nil {
			return result, ErrConflict
		}
		result = *p.Receipt
		result.AppliedNow = false
		result.Disposition = "duplicate_activation"
		return result, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return result, err
	}
	return nativeCreateActivationTx(ctx, tx, in, cfg, deps, key, hash, request, memory)
}

func nativeProcessCaptureTx(ctx context.Context, tx Tx, scope Scope, capture Ref, p *NativeDynamicsPayload, deps, priorDeps []NativeDynamicsDependency, statePreview Record) (result NativeDynamicsReceipt, blocked error, resultErr error) {
	current, e := nativeGetTx(ctx, tx, scope, capture.ID, "capture")
	if e != nil {
		return result, blocked, e
	}
	cp := current.NativeDynamics
	if cp.Input == nil || structuredText(cp.Input) != structuredText(p.Input) || !reflect.DeepEqual(cp.Stamp, p.Stamp) {
		return result, blocked, ErrConflict
	}
	if e := nativeValidateProofTx(ctx, tx, scope, deps); e != nil {
		return result, blocked, e
	}
	if e := nativeValidateCaptureSourceTx(ctx, tx, scope, cp); e != nil {
		return result, blocked, e
	}
	if cp.Receipt != nil {
		result = *cp.Receipt
		result.AppliedNow = false
		result.LearningConsumed = false
		result.Disposition = "duplicate_capture"
		return result, blocked, nil
	}
	state, cfg, e := nativeResolveCaptureStateTx(ctx, tx, scope, cp, priorDeps, statePreview)
	if e != nil {
		return result, blocked, e
	}
	result, e = nativeAdvanceTx(ctx, tx, current, state, cfg, deps)
	if errors.Is(e, ErrConflict) || errors.Is(e, ErrInvalid) {
		blocked = e
		return result, blocked, nativeBlockTx(ctx, tx, current, "admission_conflict")
	}
	return result, blocked, e
}

func nativeReadSnapshotTx(ctx context.Context, tx Tx, in NativeDynamicsQuery, key NativeDynamicsKey, row Record, deps []NativeDynamicsDependency) (out NativeDynamicsView, resultErr error) {
	out = NativeDynamicsView{Status: "missing", Authority: AuthorityDataOnly}

	current, e := nativeGetTx(ctx, tx, in.Scope, row.ID, "state")
	if e != nil {
		return out, e
	}
	if current.Revision != row.Revision {
		return out, ErrConflict
	}
	p := current.NativeDynamics
	st := p.State
	if st == nil || st.Value == nil || st.Status != "active" {
		out.Status = "invalidated"
		return out, nil
	}
	if e := nativeValidateProofTx(ctx, tx, in.Scope, deps); e != nil {
		return out, e
	}
	if _, e := nativeCurrentApprovedTx(ctx, tx, key); e != nil {
		return out, e
	}
	if !reflect.DeepEqual(deps, p.Dependencies) {
		return out, ErrConflict
	}
	enrollment, e := nativeGetTx(ctx, tx, in.Scope, nativeEnrollmentID(in.Scope, in.Target, in.Dimension), "enrollment")
	if e != nil {
		return out, e
	}
	if enrollment.NativeDynamics.Key != key {
		return out, ErrConflict
	}
	cfgRow, e := nativeGetTx(ctx, tx, in.Scope, nativeConfigID(in.Scope, in.Version), "config")
	if e != nil {
		return out, e
	}
	cfg := cfgRow.NativeDynamics.Config
	if cfg == nil || !nativeConfigValid(*cfg) || nativeConfigHash(*cfg) != p.ConfigHash {
		return out, ErrConflict
	}
	if in.CutoffNS < st.LastModelNS {
		out.Status = "future"
		return out, nil
	}
	value, e := dynamicsv2.NativeAdvanceScalarNumeric(*st.Value, st.LastModelNS, in.CutoffNS, *cfg.TauNS, *cfg.DecayEnabled, "decay_only", *cfg.EtaPositive, *cfg.UnitStrength, nil)
	if e != nil {
		return out, e
	}
	out = NativeDynamicsView{Status: "known", Value: &value, State: nativeRef(current), Epoch: st.Epoch, LastSeq: st.LastSeq, ConfigHash: p.ConfigHash, Authority: AuthorityDataOnly}
	return out, nil
}

func nativeCreateActivationTx(ctx context.Context, tx Tx, in NativeDynamicsActivationInput, cfg NativeDynamicsConfig, deps []NativeDynamicsDependency, key NativeDynamicsKey, hash, request string, memory Record) (result NativeDynamicsReceipt, resultErr error) {
	payload := NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Key: NativeDynamicsKey{Scope: in.Scope, Version: cfg.Version}, Config: &cfg, ConfigHash: hash}
	if err := nativeInsertOrSameTx(ctx, tx, nativeJob(in.Scope, "config", nativeConfigID(in.Scope, cfg.Version), payload)); err != nil {
		return result, err
	}
	if err := nativeSupersedeEnrollmentTx(ctx, tx, key); err != nil {
		return result, err
	}
	eventID := nativeID("activation", key)
	rawNow := nativeActivationTime()
	activation := nativeJob(in.Scope, "activation", eventID, NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Key: key, ConfigHash: hash, Config: &cfg, Dependencies: deps, DependencyKeys: nativeDependencyKeys(in.Scope, deps), SourceAcceptedAt: rawNow})
	activation.CreatedAt, activation.UpdatedAt = rawNow, rawNow
	if e := nativeInsertTx(ctx, tx, activation); e != nil {
		return result, e
	}
	storedActivation, e := nativeGetTx(ctx, tx, in.Scope, eventID, "activation")
	if e != nil {
		return result, e
	}
	now := storedActivation.CreatedAt
	ns, err := nativeTimeNS(now)
	if err != nil {
		return result, err
	}
	initial := *cfg.InitialValue
	st := NativeDynamicsState{Value: &initial, LastModelNS: ns, LastSeq: 1, Epoch: 1, Status: "active", ActivationAt: now, MemoryCreatedAt: memory.CreatedAt, MemoryUpdatedAt: memory.UpdatedAt, Anchor: "native_derived_state_activation", History: []Ref{}}
	seq := int64(1)
	result = NativeDynamicsReceipt{Protocol: nativeDynamicsProtocol, Disposition: "activated", Event: Ref{Collection: colJobs, ID: eventID, Revision: 1}, State: Ref{Collection: colJobs, ID: nativeStateID(key), Revision: 1}, ProcessingSeq: &seq, SourceAcceptedAt: now, AppliedNow: true}
	payload = NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Key: key, ConfigHash: hash, State: &st, Receipt: &result, IdentitySHA: request, Dependencies: deps, DependencyKeys: nativeDependencyKeys(in.Scope, deps)}
	if err := nativeInsertTx(ctx, tx, nativeJob(in.Scope, "state", nativeStateID(key), payload)); err != nil {
		return result, err
	}
	enrollment := nativeJob(in.Scope, "enrollment", nativeEnrollmentID(in.Scope, in.Target, in.Dimension), NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Key: key, ConfigHash: hash, Dependencies: deps, DependencyKeys: nativeDependencyKeys(in.Scope, deps)})
	old, e := tx.Get(ctx, colJobs, in.Scope, enrollment.ID)
	if e == nil {
		old.NativeDynamics = enrollment.NativeDynamics
		old.Status = enrollment.Status
		old.UpdatedAt = now
		return result, nativeReplaceTx(ctx, tx, old)
	}
	if !errors.Is(e, ErrNotFound) {
		return result, e
	}
	return result, nativeInsertTx(ctx, tx, enrollment)
}

func nativeResolveCaptureStateTx(ctx context.Context, tx Tx, scope Scope, cp *NativeDynamicsPayload, priorDeps []NativeDynamicsDependency, statePreview Record) (Record, NativeDynamicsConfig, error) {
	state, e := nativeGetTx(ctx, tx, scope, nativeStateID(cp.Key), "state")
	if e != nil {
		return Record{}, NativeDynamicsConfig{}, e
	}
	if state.Revision != statePreview.Revision {
		return Record{}, NativeDynamicsConfig{}, ErrConflict
	}
	if e := nativeValidateProofTx(ctx, tx, scope, priorDeps); e != nil {
		return Record{}, NativeDynamicsConfig{}, e
	}
	if _, e := nativeCurrentApprovedTx(ctx, tx, cp.Key); e != nil {
		return Record{}, NativeDynamicsConfig{}, e
	}
	cfg, e := nativeCurrentConfigTx(ctx, tx, scope, cp)
	if e != nil {
		return Record{}, NativeDynamicsConfig{}, e
	}
	return state, cfg, nil
}
