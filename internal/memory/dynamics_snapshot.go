package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
)

type DynamicsLoadSnapshot struct {
	Scope        Scope
	ConfigSHA256 string
	Generation   int64
	Head         int64
	Tail         int64
	AsOfNS       int64
	PublishedNS  int64
	States       map[string]Record
}

func cloneDynamicsSnapshot(in *DynamicsLoadSnapshot) *DynamicsLoadSnapshot {
	if in == nil {
		return nil
	}
	out := *in
	out.States = make(map[string]Record, len(in.States))
	for id, row := range in.States {
		// Storage roundtrip clones every nested pointer and preserves scheduler NS.
		raw, err := storeMarshal(row)
		if err != nil {
			return nil
		}
		var cloned Record
		if storeUnmarshal(raw, &cloned) != nil {
			return nil
		}
		out.States[id] = cloned
	}
	return &out
}
func (r *DynamicsLoadRuntime) installDynamicsSnapshot(snapshot *DynamicsLoadSnapshot) bool {
	cloned := cloneDynamicsSnapshot(snapshot)
	if cloned == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.snapshots[cloned.Scope]
	if previous != nil && previous.Generation >= cloned.Generation {
		return false
	}
	r.snapshots[cloned.Scope] = cloned
	return true
}

// DynamicsSnapshot returns a private clone. Cache publication time never replaces
// its coherent as-of clock; an old accepted prefix stays old during backlog.
func (r *DynamicsLoadRuntime) DynamicsSnapshot(scope Scope, nowNS int64) (*DynamicsLoadSnapshot, error) {
	if !r.configured(scope) {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := r.snapshots[scope]
	if snapshot == nil || snapshot.ConfigSHA256 != r.hash || nowNS < snapshot.AsOfNS || nowNS-snapshot.AsOfNS > r.config.Limits.AllowedFreshnessNS {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	return cloneDynamicsSnapshot(snapshot), nil
}
func (s *Service) PublishDynamicsSnapshot(ctx context.Context, token string, scope Scope) error {
	if !s.DynamicsLoad.configured(scope) {
		return ErrDynamicsRuntimeBinding
	}
	bound, err := s.nativeActor(ctx, token, scope, "write:jobs")
	if err != nil {
		return err
	}
	var snapshot *DynamicsLoadSnapshot
	err = s.Repo.Transaction(bound, func(ctx context.Context, tx Tx) error {
		snapshot = nil
		var e error
		snapshot, e = s.buildDynamicsSnapshotTx(ctx, tx, scope, false)
		return e
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if snapshot != nil {
		s.DynamicsLoad.installDynamicsSnapshot(snapshot)
	}
	return nil
}

// RepairDynamicsPublication is the explicit verify-then-republish step for a
// withheld queue. It needs the same service write:jobs authority as Publish and
// succeeds only without a live lease or pending purge; an expired lease, which
// no worker can reclaim while withheld, is cleared so its claim stays fenced.
// The snapshot is rebuilt from canonical rows in the same transaction; a
// nonempty tail publishes only the applied prefix, as in Publish, and with no
// applied prefix yet only the hold is released. The worker refuses to claim
// while withheld, so requiring a drained queue here would strand queued work.
func (s *Service) RepairDynamicsPublication(ctx context.Context, token string, scope Scope) error {
	if !s.DynamicsLoad.configured(scope) {
		return ErrDynamicsRuntimeBinding
	}
	bound, err := s.nativeActor(ctx, token, scope, "write:jobs")
	if err != nil {
		return err
	}
	var snapshot *DynamicsLoadSnapshot
	err = s.Repo.Transaction(bound, func(ctx context.Context, tx Tx) error {
		snapshot = nil
		applied, e := s.releaseDynamicsHoldTx(ctx, tx, scope)
		if e != nil || !applied {
			return e
		}
		snapshot, e = s.buildDynamicsSnapshotTx(ctx, tx, scope, true)
		if e == nil && snapshot == nil {
			e = ErrDynamicsSnapshotUnavailable
		}
		return e
	})
	if err != nil {
		return err
	}
	if snapshot != nil {
		s.DynamicsLoad.installDynamicsSnapshot(snapshot)
	}
	return nil
}

// releaseDynamicsHoldTx clears the hold and closes a known repair interval;
// its end is the validation time of the rebuild that follows in this transaction. It
// reports whether an applied prefix exists to republish; with none, no stale
// value can be served and the worker's next normal Publish follows its first apply.
func (s *Service) releaseDynamicsHoldTx(ctx context.Context, tx Tx, scope Scope) (bool, error) {
	c, e := dynamicsLockCoordinator(ctx, tx, scope)
	if e != nil {
		return false, e
	}
	if e = s.dynamicsBinding(ctx, tx, scope, c); e != nil {
		return false, e
	}
	p := c.DynamicsLoad
	if !p.Withheld {
		return false, ErrConflict
	}
	now := dynamicsNowNS()
	if p.LeaseUntilNS > now {
		return false, ErrDynamicsSnapshotUnavailable
	}
	if p.RepairStartNS != nil {
		if now < *p.RepairStartNS {
			return false, ErrConflict
		}
		p.LastRepairStartNS, p.LastRepairEndNS = p.RepairStartNS, &now
	}
	p.LeaseToken, p.LeaseUntilNS = "", 0
	p.Withheld, p.RepairStartNS = false, nil
	// The release itself publishes nothing, but it must not outrun a restore or a pending purge.
	if e = nativeCheckRestorePublicationTx(ctx, tx); e != nil {
		return false, e
	}
	if e = dynamicsPublicationHeldTx(ctx, tx, scope, p); e != nil {
		return false, e
	}
	if e = dynamicsReplace(ctx, tx, c); e != nil {
		return false, e
	}
	return p.AsOfNS > 0 || p.Count == 0, nil
}

// Called within the existing authoritative native read transaction. Cached
// scalar bytes are eligible only if canonical currentness and revision match;
// the original native read still performs source/ACL/restore checks.
func (s *Service) dynamicsSnapshotReadTx(ctx context.Context, tx Tx, scope Scope, previews []nativeSearchSourcePreview) error {
	if !s.DynamicsLoad.configured(scope) {
		if _, canonical := tx.(dynamicsLoadQueries); !canonical {
			return nil
		}
	}
	c, err := dynamicsLockCoordinator(ctx, tx, scope)
	if errors.Is(err, ErrNotFound) && !s.DynamicsLoad.configured(scope) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.dynamicsBinding(ctx, tx, scope, c); err != nil {
		return err
	}
	snapshot, err := s.DynamicsLoad.DynamicsSnapshot(scope, dynamicsNowNS())
	if err != nil {
		return err
	}
	p := c.DynamicsLoad
	if p.Withheld || snapshot.Generation != p.PublicationGeneration || snapshot.Head != p.Head || snapshot.Tail != p.Tail {
		return ErrDynamicsSnapshotUnavailable
	}
	for i, preview := range previews {
		cached, ok := snapshot.States[preview.row.ID]
		if !ok || cached.Revision != preview.row.Revision || structuredText(cached.NativeDynamics) != structuredText(preview.row.NativeDynamics) {
			return ErrDynamicsSnapshotUnavailable
		}
		previews[i].row = cached
	}
	return nil
}

func (s *Service) buildDynamicsSnapshotTx(ctx context.Context, tx Tx, scope Scope, repaired bool) (*DynamicsLoadSnapshot, error) {
	c, e := dynamicsLockCoordinator(ctx, tx, scope)
	if e != nil {
		return nil, e
	}
	if e = s.dynamicsBinding(ctx, tx, scope, c); e != nil {
		return nil, e
	}
	if e = nativeCheckRestorePublicationTx(ctx, tx); e != nil {
		return nil, e
	}
	p := c.DynamicsLoad
	l := s.DynamicsLoad.config.Limits
	now := dynamicsNowNS()
	if now < p.AsOfNS || now < p.PublishedNS {
		return nil, ErrConflict
	}
	if e = dynamicsPublicationHeldTx(ctx, tx, scope, p); e != nil {
		return nil, e
	}
	if !repaired && p.PublishedNS > 0 && now-p.PublishedNS < l.PublishIntervalNS {
		return nil, nil
	}
	states, e := dynamicsSnapshotStatesTx(ctx, tx, scope, l)
	if e != nil {
		return nil, e
	}
	if p.PublicationGeneration == math.MaxInt64 {
		return nil, ErrConflict
	}
	// Empty-tail proof in this serializable transaction witnesses state through
	// now. A nonempty tail permits only the actually applied accepted prefix.
	if p.Count == 0 {
		p.AsOfNS = now
	}
	if p.AsOfNS <= 0 {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	p.PublicationGeneration++
	p.PublishedNS = now
	snapshot := &DynamicsLoadSnapshot{Scope: scope, ConfigSHA256: p.ConfigSHA256, Generation: p.PublicationGeneration, Head: p.Head, Tail: p.Tail, AsOfNS: p.AsOfNS, PublishedNS: now, States: states}
	fullBytes, e := storeMarshal(snapshot)
	if e != nil {
		return nil, e
	}
	if int64(len(fullBytes)) > l.SnapshotMaxBytes {
		return nil, ErrDynamicsSnapshotUnavailable
	}

	witness, e := dynamicsSnapshotWitness(snapshot)
	if e != nil {
		return nil, e
	}
	c.RequestHash = witness
	if e = dynamicsReplace(ctx, tx, c); e != nil {
		return nil, e
	}
	return snapshot, nil
}

// A pending purge, a hold or a lease blocks every publication.
func dynamicsPublicationHeldTx(ctx context.Context, tx Tx, scope Scope, p *DynamicsLoadPayload) error {
	purges, e := tx.(nativeDynamicsTxQueries).NativeDynamicsPendingPurges(ctx, scope, 1)
	if e != nil {
		return e
	}
	if len(purges) > 0 || p.Withheld || p.LeaseToken != "" {
		return ErrDynamicsSnapshotUnavailable
	}
	return nil
}
func dynamicsSnapshotStatesTx(ctx context.Context, tx Tx, scope Scope, l DynamicsLoadLimits) (map[string]Record, error) {
	rows, e := tx.(nativeDynamicsTxQueries).NativeDynamicsRows(ctx, scope, "state", "", "", int(l.SnapshotMaxItems)+1)
	if e != nil {
		return nil, e
	}
	if int64(len(rows)) > l.SnapshotMaxItems {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	states := make(map[string]Record, len(rows))
	for _, row := range rows {
		states[row.ID] = row
	}
	raw, e := storeMarshal(states)
	if e != nil {
		return nil, e
	}
	if int64(len(raw)) > l.SnapshotMaxBytes {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	return states, nil
}

// The witness belongs to the SYS05 coordinator's previously unused RequestHash.
// storeMarshal includes private native state. Canonical JSON sorts every map;
// UseNumber preserves the exact int64 ns/revision tokens. Scope,
// raw config binding, clocks and queue coordinates are part of the commitment.
func dynamicsSnapshotWitness(snapshot *DynamicsLoadSnapshot) (string, error) {
	raw, err := storeMarshal(snapshot)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return ContentHash("sys05-snapshot-state-witness-v1\x00" + string(canonical)), nil
}

// RefreshDynamicsSnapshot copies an existing privileged publication into this
// process only. It cannot create a publication, advance its clocks, or write DB
// state. Source authorization stays outside the Tx to support restricted pool1.
func (s *Service) RefreshDynamicsSnapshot(ctx context.Context, token string, scope Scope) error {
	// This maintenance read may run on a repository carrying a worker binding.
	// Its SQL/RLS identity must follow the explicit reader token as HTTP does.
	ctx = ContextWithPostgresToken(ctx, token)
	if !s.DynamicsLoad.configured(scope) {
		return ErrDynamicsRuntimeBinding
	}
	bound, err := s.nativeActor(ctx, token, scope, "read:jobs")
	if err != nil {
		return err
	}
	preview, err := s.previewPublishedDynamicsSnapshot(bound, scope)
	if err != nil {
		return dynamicsRefreshError(err)
	}
	proofs, err := s.authorizeDynamicsRefresh(bound, token, preview)
	if err != nil {
		return dynamicsRefreshError(err)
	}
	var snapshot *DynamicsLoadSnapshot
	err = s.Repo.Transaction(bound, func(ctx context.Context, tx Tx) error {
		snapshot = nil
		current, e := s.readPublishedDynamicsSnapshotTx(ctx, tx, scope)
		if e != nil {
			return e
		}
		before, e := dynamicsSnapshotWitness(preview)
		if e != nil {
			return e
		}
		after, e := dynamicsSnapshotWitness(current)
		if e != nil {
			return e
		}
		if before != after {
			return ErrDynamicsSnapshotUnavailable
		}
		if e = validateDynamicsRefreshStatesTx(ctx, tx, current, proofs); e != nil {
			return e
		}
		snapshot = current
		return ctx.Err()
	})
	if err != nil {
		return dynamicsRefreshError(err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !s.DynamicsLoad.installRefreshedDynamicsSnapshot(snapshot) {
		return ErrDynamicsSnapshotUnavailable
	}
	return nil
}
func dynamicsRefreshError(err error) error {
	if errors.Is(err, ErrDenied) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.Join(ErrDynamicsSnapshotUnavailable, err)
}
func (s *Service) previewPublishedDynamicsSnapshot(ctx context.Context, scope Scope) (*DynamicsLoadSnapshot, error) {
	var snapshot *DynamicsLoadSnapshot
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		snapshot, e = s.readPublishedDynamicsSnapshotTx(ctx, tx, scope)
		return e
	})
	return snapshot, err
}
func (s *Service) readPublishedDynamicsSnapshotTx(ctx context.Context, tx Tx, scope Scope) (*DynamicsLoadSnapshot, error) {
	c, err := tx.Get(ctx, colJobs, scope, dynamicsCoordinatorID(scope))
	if err != nil {
		return nil, err
	}
	if err = dynamicsValidateRow(c, scope, "coordinator"); err != nil {
		return nil, err
	}
	if err = s.dynamicsBinding(ctx, tx, scope, c); err != nil {
		return nil, err
	}
	if err = nativeCheckRestorePublicationTx(ctx, tx); err != nil {
		return nil, err
	}
	if err = validDynamicsRefreshPublication(c, s.DynamicsLoad.config.Limits, dynamicsNowNS()); err != nil {
		return nil, err
	}
	q, ok := tx.(nativeDynamicsTxQueries)
	if !ok {
		return nil, ErrUnavailable
	}
	purges, err := q.NativeDynamicsPendingPurges(ctx, scope, 1)
	if err != nil {
		return nil, err
	}
	if len(purges) > 0 {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	states, err := dynamicsSnapshotStatesTx(ctx, tx, scope, s.DynamicsLoad.config.Limits)
	if err != nil {
		return nil, err
	}
	p := c.DynamicsLoad
	snapshot := &DynamicsLoadSnapshot{Scope: scope, ConfigSHA256: p.ConfigSHA256, Generation: p.PublicationGeneration, Head: p.Head, Tail: p.Tail, AsOfNS: p.AsOfNS, PublishedNS: p.PublishedNS, States: states}
	raw, err := storeMarshal(snapshot)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > s.DynamicsLoad.config.Limits.SnapshotMaxBytes {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	witness, err := dynamicsSnapshotWitness(snapshot)
	if err != nil {
		return nil, err
	}
	if witness != c.RequestHash {
		return nil, ErrDynamicsSnapshotUnavailable
	}
	return snapshot, nil
}
func validDynamicsRefreshPublication(c Record, l DynamicsLoadLimits, now int64) error {
	p := c.DynamicsLoad
	if c.ID != dynamicsCoordinatorID(c.Scope) || !sourceHashPattern.MatchString(c.RequestHash) {
		return ErrDynamicsSnapshotUnavailable
	}
	if p.Withheld || p.LeaseToken != "" || p.LeaseUntilNS != 0 || p.PublicationGeneration < 1 {
		return ErrDynamicsSnapshotUnavailable
	}
	if p.AsOfNS <= 0 || p.PublishedNS < p.AsOfNS || now < p.PublishedNS || now-p.AsOfNS > l.AllowedFreshnessNS {
		return ErrDynamicsSnapshotUnavailable
	}
	return nil
}
func (s *Service) authorizeDynamicsRefresh(ctx context.Context, token string, snapshot *DynamicsLoadSnapshot) (map[string][]NativeDynamicsDependency, error) {
	proofs := make(map[string][]NativeDynamicsDependency, len(snapshot.States))
	for id, row := range snapshot.States {
		p := row.NativeDynamics
		if p == nil || p.State == nil {
			return nil, ErrConflict
		}
		if p.State.Status != "active" {
			if p.State.Value != nil || p.State.SlowValue != nil {
				return nil, ErrConflict
			}
			continue
		}
		key := p.Key
		if key.Scope != snapshot.Scope || id != nativeStateID(key) || key.Target.Collection != colMemories || !validWorkCheckpointRef(key.Target) || !nativeDimension(key.Dimension) || !validRunLabel(key.Version, 128) {
			return nil, ErrConflict
		}
		if _, err := s.authorize(ctx, token, "read:memories", snapshot.Scope, key.Target.ID, ""); err != nil {
			return nil, err
		}
		deps, err := s.nativeAuthorizeProof(ctx, token, snapshot.Scope, nativeDepRefs(p.Dependencies))
		if err != nil {
			return nil, err
		}
		proofs[id] = deps
	}
	return proofs, nil
}
func validateDynamicsRefreshStatesTx(ctx context.Context, tx Tx, snapshot *DynamicsLoadSnapshot, proofs map[string][]NativeDynamicsDependency) error {
	now := dynamicsNowNS()
	for id, row := range snapshot.States {
		p := row.NativeDynamics
		if p == nil || p.State == nil {
			return ErrConflict
		}
		if p.State.Status != "active" {
			if p.State.Value != nil || p.State.SlowValue != nil {
				return ErrConflict
			}
			continue
		}
		deps, ok := proofs[id]
		if !ok {
			return ErrConflict
		}
		key := p.Key
		query := NativeDynamicsQuery{Scope: snapshot.Scope, Target: key.Target, Dimension: key.Dimension, Version: key.Version, CutoffNS: now}
		view, err := nativeReadSnapshotTx(ctx, tx, query, key, row, deps)
		if err != nil {
			return err
		}
		if view.Status != "known" || view.Value == nil {
			return ErrDynamicsSnapshotUnavailable
		}
	}
	return ctx.Err()
}
func (r *DynamicsLoadRuntime) installRefreshedDynamicsSnapshot(snapshot *DynamicsLoadSnapshot) bool {
	cloned := cloneDynamicsSnapshot(snapshot)
	if cloned == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prior := r.snapshots[cloned.Scope]
	if prior != nil && prior.Generation >= cloned.Generation {
		if prior.Generation != cloned.Generation {
			return false
		}
		previous, err := dynamicsSnapshotWitness(prior)
		if err != nil {
			return false
		}
		current, err := dynamicsSnapshotWitness(cloned)
		return err == nil && previous == current
	}
	r.snapshots[cloned.Scope] = cloned
	return true
}
