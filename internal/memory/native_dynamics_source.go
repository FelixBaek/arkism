package memory

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"time"
)

func resolveAcceptedNativeOutcomeTx(ctx context.Context, tx Tx, evaluation Ref, original MemoryOutcomeInput) (acceptedNativeOutcome, error) {
	var staged acceptedNativeOutcome
	err := func() error {
		// Serializable transactions may replay the callback. Never retain an earlier result.
		staged = acceptedNativeOutcome{}
		if err := ctx.Err(); err != nil {
			return err
		}
		read := func(ref Ref) (Record, error) {
			return nativeAcceptedRecordTx(ctx, tx, original.Scope, ref)
		}
		r, err := read(evaluation)
		if err != nil {
			return err
		}
		if !canonicalNativeOutcome(r, evaluation, original) {
			return ErrConflict
		}
		if err := nativeValidateAcceptedEvidenceTx(ctx, tx, original, r, read); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		staged = acceptedNativeOutcome{evaluation: evaluation, original: original, evaluator: r.CreatedBy, createdAt: r.CreatedAt, requestSHA: r.RequestHash}
		return nil
	}()
	if err != nil {
		return acceptedNativeOutcome{}, err
	}
	return staged, nil
}

func nativeProofRefs(r Record, col string) []Ref {
	out := []Ref{}
	for _, source := range r.Sources {
		out = append(out, Ref{Collection: "sources", ID: source.SourceID})
	}
	if col == "execution_traces" {
		for _, ref := range r.Relations {
			if ref.Collection == "agent_runs" {
				out = append(out, ref)
			}
		}
	}
	return out
}
func nativeDependencyHash(r Record) string {
	// Canonical acceptance provenance is hidden from API JSON, but remains part of the currentness proof.
	return nativeHash("dependency_snapshot", struct {
		Record       Record
		RequestHash  string
		CaptureStamp *NativeOutcomeCaptureStamp
	}{r, r.RequestHash, r.NativeOutcomeCaptureStamp})
}
func nativeRoots(evaluation Ref, in MemoryOutcomeInput) []Ref {
	return append(slices.Clone(in.Evidence), evaluation, in.Target, in.Trace)
}
func nativeSortDeps(deps []NativeDynamicsDependency) {
	sort.Slice(deps, func(i, j int) bool { return structuredText(deps[i].Ref) < structuredText(deps[j].Ref) })
}
func nativeCollectProofTx(ctx context.Context, tx Tx, scope Scope, roots []Ref) ([]NativeDynamicsDependency, error) {
	queue := slices.Clone(roots)
	seen := map[string]bool{}
	out := []NativeDynamicsDependency{}
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		key := ref.Collection + "/" + ref.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(seen) > 500 {
			return nil, ErrInvalid
		}
		r, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
		if err != nil {
			return nil, err
		}
		if !nativeLifecycleProofSupported(r, ref.Collection) || r.Scope != scope || r.ID != ref.ID || !live(r) || r.ContentErased || r.PrivacyQuarantined || ref.Revision > 0 && r.Revision != ref.Revision {
			return nil, ErrConflict
		}
		out = append(out, NativeDynamicsDependency{Ref: Ref{Collection: ref.Collection, ID: r.ID, Revision: r.Revision}, Hash: nativeDependencyHash(r)})
		queue = append(queue, nativeProofRefs(r, ref.Collection)...)
	}
	nativeSortDeps(out)
	return out, nil
}
func (s *Service) nativeAuthorizeProof(ctx context.Context, token string, scope Scope, roots []Ref) ([]NativeDynamicsDependency, error) {
	queue := slices.Clone(roots)
	seen := map[string]bool{}
	out := []NativeDynamicsDependency{}
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		key := ref.Collection + "/" + ref.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(seen) > 500 {
			return nil, ErrInvalid
		}
		if _, err := s.authorize(ctx, token, "read:"+ref.Collection, scope, ref.ID, ""); err != nil {
			return nil, err
		}
		r, err := s.Repo.Get(ctx, ref.Collection, scope, ref.ID)
		if err != nil {
			return nil, err
		}
		if !nativeLifecycleProofSupported(r, ref.Collection) || r.Scope != scope || r.ID != ref.ID || !live(r) || r.ContentErased || r.PrivacyQuarantined || ref.Revision > 0 && r.Revision != ref.Revision {
			return nil, ErrConflict
		}
		out = append(out, NativeDynamicsDependency{Ref: Ref{Collection: ref.Collection, ID: r.ID, Revision: r.Revision}, Hash: nativeDependencyHash(r)})
		queue = append(queue, nativeProofRefs(r, ref.Collection)...)
	}
	nativeSortDeps(out)
	return out, nil
}
func nativeValidateProofTx(ctx context.Context, tx Tx, scope Scope, deps []NativeDynamicsDependency) error {
	if locker, ok := tx.(readLocker); ok {
		groups := map[string][]string{}
		for _, dep := range deps {
			groups[dep.Ref.Collection] = append(groups[dep.Ref.Collection], dep.Ref.ID)
		}
		cols := make([]string, 0, len(groups))
		for col := range groups {
			cols = append(cols, col)
		}
		slices.Sort(cols)
		for _, col := range cols {
			if _, err := locker.LockForRead(ctx, scope, col, groups[col]); err != nil {
				return err
			}
		}
	}
	for _, dep := range deps {
		if err := nativeRejectInvalidatedTx(ctx, tx, scope, dep.Ref); err != nil {
			return err
		}
		r, err := tx.Get(ctx, dep.Ref.Collection, scope, dep.Ref.ID)
		if err != nil {
			return err
		}
		if !nativeLifecycleProofSupported(r, dep.Ref.Collection) || r.Scope != scope || r.ID != dep.Ref.ID || r.Revision != dep.Ref.Revision || !live(r) || r.ContentErased || r.PrivacyQuarantined || nativeDependencyHash(r) != dep.Hash {
			return ErrConflict
		}
	}
	return nil
}
func nativeCurrentApprovedTx(ctx context.Context, tx Tx, key NativeDynamicsKey) (Record, error) {
	r, err := tx.Get(ctx, colMemories, key.Scope, key.Target.ID)
	if err != nil {
		return Record{}, err
	}
	if r.Scope != key.Scope || r.Revision != key.Target.Revision || !live(r) || r.ContentErased || r.PrivacyQuarantined || r.ApprovedBy == "" {
		return Record{}, ErrConflict
	}
	if err := checkSources(ctx, tx, r); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (s *Service) nativeCreationStampTx(ctx context.Context, tx Tx, in MemoryOutcomeInput) (*NativeOutcomeCaptureStamp, error) {
	stamp := &NativeOutcomeCaptureStamp{Protocol: nativeDynamicsProtocol, Mode: "off", Basis: "runtime_disabled"}
	if s.NativeDynamics == nil || !s.NativeDynamics.Enabled {
		return stamp, nil
	}
	if _, ok := tx.(nativeDynamicsTxQueries); !ok {
		return nil, ErrUnavailable
	}
	row, err := nativeGetTx(ctx, tx, in.Scope, nativeEnrollmentID(in.Scope, in.Target, in.Dimension), "enrollment")
	if errors.Is(err, ErrNotFound) {
		stamp.Mode = "not_enrolled"
		stamp.Basis = "enrollment_absent_at_creation"
		return stamp, nil
	}
	if err != nil {
		return nil, err
	}
	p := row.NativeDynamics
	stamp.Mode = "enrolled"
	stamp.Basis = "current_enrollment"
	ref := nativeRef(row)
	stamp.Enrollment = &ref
	stamp.ConfigHash = p.ConfigHash
	stamp.Version = p.Key.Version
	return stamp, nil
}
func stageNativeOutcomeCaptureTx(ctx context.Context, tx Tx, actor Decision, in MemoryOutcomeInput, evaluation Ref, stamp *NativeOutcomeCaptureStamp) error {
	if stamp == nil || stamp.Mode != "enrolled" {
		return nil
	}
	stored, err := tx.Get(ctx, "evaluations", in.Scope, evaluation.ID)
	if err != nil {
		return err
	}
	if stored.CreatedBy != actor.PrincipalID || actor.Role != roleService || stored.NativeOutcomeCaptureStamp == nil || !reflect.DeepEqual(stored.NativeOutcomeCaptureStamp, stamp) {
		return ErrConflict
	}
	snapshot, err := resolveAcceptedNativeOutcomeTx(ctx, tx, evaluation, in)
	if err != nil {
		return err
	}
	deps, err := nativeCollectProofTx(ctx, tx, in.Scope, nativeRoots(evaluation, in))
	if err != nil {
		return err
	}
	if err := nativeValidateProofTx(ctx, tx, in.Scope, deps); err != nil {
		return err
	}
	key := NativeDynamicsKey{Scope: in.Scope, Target: in.Target, Dimension: in.Dimension, Version: stamp.Version}
	payload := NativeDynamicsPayload{Protocol: nativeDynamicsProtocol, Key: key, Input: &in, Evaluation: evaluation, Stamp: stamp, ConfigHash: stamp.ConfigHash, Dependencies: deps, DependencyKeys: nativeDependencyKeys(in.Scope, deps), SourceAcceptedAt: snapshot.createdAt}
	row := nativeJob(in.Scope, "capture", nativeCaptureID(in.Scope, evaluation), payload)
	row.Status = JobStatusPending
	row.CreatedAt = stored.CreatedAt
	row.UpdatedAt = stored.CreatedAt
	return nativeInsertTx(ctx, tx, row)
}
func nativeCaptureIntegrityTx(ctx context.Context, tx Tx, in MemoryOutcomeInput, r Record) error {
	stamp := r.NativeOutcomeCaptureStamp
	if stamp == nil {
		return nil
	} // Unstamped legacy is unknown; never backfill.
	if stamp.Protocol != nativeDynamicsProtocol {
		return ErrConflict
	}
	if stamp.Mode != "enrolled" {
		if stamp.Mode != "off" && stamp.Mode != "not_enrolled" {
			return ErrConflict
		}
		return nil
	}
	row, err := nativeGetTx(ctx, tx, in.Scope, nativeCaptureID(in.Scope, Ref{Collection: "evaluations", ID: r.ID, Revision: 1}), "capture")
	if err != nil {
		return err
	}
	p := row.NativeDynamics
	if p.Input == nil || structuredText(*p.Input) != structuredText(in) || !reflect.DeepEqual(p.Stamp, stamp) {
		return ErrConflict
	}
	return nil
}

// Keep no original source time from an ephemeral processing clock.
func nativeActivationTime() time.Time { return time.Now().UTC() }

func nativeAcceptedRecordTx(ctx context.Context, tx Tx, scope Scope, ref Ref) (Record, error) {
	r, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
	if err != nil {
		return Record{}, err
	}
	if r.ID != ref.ID || r.Scope != scope || r.Revision != ref.Revision || !live(r) || r.ContentErased || r.PrivacyQuarantined {
		return Record{}, ErrConflict
	}
	if err := checkSources(ctx, tx, r); err != nil {
		return Record{}, err
	}
	return r, nil
}

func nativeValidateAcceptedEvidenceTx(ctx context.Context, tx Tx, original MemoryOutcomeInput, r Record, read func(Ref) (Record, error)) error {
	target, err := read(original.Target)
	if err != nil {
		return err
	}
	if target.CreatedBy == r.CreatedBy || target.ApprovedBy == r.CreatedBy {
		return ErrDenied
	}
	trace, err := read(original.Trace)
	if err != nil {
		return err
	}
	if trace.CreatedBy == r.CreatedBy {
		return ErrDenied
	}
	completed, err := memoryOutcomeTraceCompleted(ctx, tx, original.Scope, trace)
	if err != nil {
		return err
	}
	if !completed {
		return ErrConflict
	}
	sources, relations, err := memoryOutcomeEvidence(original, trace, r.CreatedBy, read)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(r.Sources, sources) || !reflect.DeepEqual(r.Relations, relations) {
		return ErrConflict
	}
	if err := checkEvaluationEvidence(ctx, tx, r); err != nil {
		return err
	}
	return nil
}
