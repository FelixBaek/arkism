package memory

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// These values are explicit arithmetic fixtures, never product/research defaults.
func nativeTestConfig(version string) NativeDynamicsConfig {
	initial, eta, unit, strength := 0.5, 0.2, 1.0, 0.5
	tau := int64(1000000000000)
	decay, positive := false, true
	history, deps := int64(64), int64(500)
	return NativeDynamicsConfig{Version: version, InitialValue: &initial, TauNS: &tau, EtaPositive: &eta, UnitStrength: &unit, ObservationStrength: &strength, DecayEnabled: &decay, PositiveLearning: &positive, MaxHistory: &history, MaxDependencies: &deps}
}
func nativeFixture(t *testing.T) (*fixture, MemoryOutcomeInput) {
	t.Helper()
	f, _, in := outcomeFixture(t)
	f.svc.NativeDynamics = &NativeDynamicsRuntime{Enabled: true}
	f.auth.actors["native"] = actor("native-consumer", roleService, false, allVerbs...)
	return f, in
}
func nativeActivate(t *testing.T, f *fixture, in MemoryOutcomeInput, version string) NativeDynamicsReceipt {
	t.Helper()
	r, e := f.svc.ActivateNativeMemoryDynamics(testCtx, "native", NativeDynamicsActivationInput{in.Scope, in.Target, in.Dimension, nativeTestConfig(version), "activate_" + version})
	if e != nil {
		t.Fatal("activate", e)
	}
	return r
}
func nativeAccept(t *testing.T, f *fixture, in MemoryOutcomeInput) (Ref, Ref) {
	t.Helper()
	ref, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
	if e != nil {
		t.Fatal("accept", e)
	}
	return ref, Ref{Collection: colJobs, ID: nativeCaptureID(in.Scope, ref), Revision: 1}
}
func nativeView(t *testing.T, f *fixture, in MemoryOutcomeInput, version string) NativeDynamicsView {
	t.Helper()
	r, e := f.svc.ReadNativeDynamicsSnapshot(testCtx, "native", NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, version, time.Now().UnixNano()})
	if e != nil {
		t.Fatal("query", e)
	}
	return r
}
func nativeKindRows(f *fixture, role string) []Record {
	out := []Record{}
	for _, r := range f.repo.rows(colJobs) {
		if r.Kind == nativeDynamicsPrefix+role {
			out = append(out, r)
		}
	}
	return out
}
func (x fakeTx) NativeDynamicsRows(ctx context.Context, scope Scope, role, dependency, after string, limit int) ([]Record, error) {
	if limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	out := []Record{}
	for _, r := range x.store[colJobs] {
		if r.Scope == scope && r.Kind == nativeDynamicsPrefix+role && r.ID > after && r.NativeDynamics != nil && (dependency == "" || slices.Contains(r.NativeDynamics.DependencyKeys, dependency)) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (x fakeTx) NativeDynamicsPendingCaptures(ctx context.Context, scope Scope, limit int) ([]Record, error) {
	if limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	out := []Record{}
	for _, r := range x.store[colJobs] {
		if r.Scope == scope && r.Kind == nativeDynamicsPrefix+"capture" && (r.Status == JobStatusPending || r.Status == JobStatusRetry) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (x fakeTx) NativeDynamicsPendingPurges(ctx context.Context, s Scope, n int) ([]Record, error) {
	if n < 1 || n > 500 {
		return nil, ErrInvalid
	}
	out := []Record{}
	for _, r := range x.store[colJobs] {
		if r.Scope == s && r.Kind == nativeDynamicsPrefix+"purge" && (r.Status == JobStatusPending || r.Status == JobStatusRetry) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}
func nativeDrainPurge(t *testing.T, s *Service, token string, scope Scope, ref Ref) {
	t.Helper()
	for range 2048 {
		done, e := s.ProcessNativeDynamicsPurge(testCtx, token, scope, ref, 64)
		if e != nil {
			t.Fatal("purge", e)
		}
		if done {
			return
		}
	}
	t.Fatal("purge did not terminate")
}
func TestNativeDynamicsExplicitConfigAndActivation(t *testing.T) {
	f, in := nativeFixture(t)
	bad := nativeTestConfig("v1")
	bad.InitialValue = nil
	if _, e := f.svc.ActivateNativeMemoryDynamics(testCtx, "native", NativeDynamicsActivationInput{in.Scope, in.Target, in.Dimension, bad, "bad"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("missing coefficient defaulted", e)
	}
	r := nativeActivate(t, f, in, "v1")
	if r.Disposition != "activated" || r.ProcessingSeq == nil || *r.ProcessingSeq != 1 {
		t.Fatal("activation missing")
	}
	v := nativeView(t, f, in, "v1")
	if v.Value == nil || *v.Value != 0.5 || v.Status != "known" {
		t.Fatal("initial state")
	}
	repeat := nativeActivate(t, f, in, "v1")
	if repeat.AppliedNow || len(nativeKindRows(f, "activation")) != 1 {
		t.Fatal("activation repeated")
	}
}
func TestNativeDynamicsSourceModeStampIntegrity(t *testing.T) {
	f, in := nativeFixture(t)
	_, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
	if e != nil {
		t.Fatal(e)
	}
	eval := f.repo.rows("evaluations")[0]
	if eval.NativeOutcomeCaptureStamp == nil || eval.NativeOutcomeCaptureStamp.Mode != "not_enrolled" || len(nativeKindRows(f, "capture")) != 0 {
		t.Fatal("absence treated as enrolled")
	}
	f, in = nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	ref, captureRef := nativeAccept(t, f, in)
	eval, _ = f.repo.Get(testCtx, "evaluations", in.Scope, ref.ID)
	if eval.NativeOutcomeCaptureStamp.Mode != "enrolled" {
		t.Fatal("no stamp")
	}
	if e := f.repo.Delete(testCtx, colJobs, in.Scope, captureRef.ID, 1); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in); e == nil {
		t.Fatal("missing enrolled capture treated as off")
	}
}
func TestNativeDynamicsLegacyRetryNoBackfill(t *testing.T) {
	f, in := nativeFixture(t)
	f.svc.NativeDynamics = nil
	ref, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
	if e != nil {
		t.Fatal(e)
	}
	r, _ := f.repo.Get(testCtx, "evaluations", in.Scope, ref.ID)
	if r.NativeOutcomeCaptureStamp == nil || r.NativeOutcomeCaptureStamp.Mode != "off" {
		t.Fatal("off not recorded")
	}
	f.svc.NativeDynamics = &NativeDynamicsRuntime{Enabled: true}
	nativeActivate(t, f, in, "v1")
	if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in); e != nil {
		t.Fatal(e)
	}
	if len(nativeKindRows(f, "capture")) != 0 {
		t.Fatal("backfilled old off source")
	}
	r.NativeOutcomeCaptureStamp = nil
	f.repo.mu.Lock()
	f.repo.store["evaluations"][recKey(in.Scope, r.ID)] = r
	f.repo.mu.Unlock()
	if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in); e != nil {
		t.Fatal(e)
	}
	if len(nativeKindRows(f, "capture")) != 0 {
		t.Fatal("legacy absence backfilled")
	}
}
func TestNativeDynamicsSeenAndQualifiedPositiveConsumption(t *testing.T) {
	for _, passed := range []bool{false, true} {
		t.Run(map[bool]string{false: "false", true: "true"}[passed], func(t *testing.T) {
			checkNativeDynamicsBooleanConsumption(t, passed)
		})
	}
}
func TestNativeDynamicsCrossConfigDuplicateAndVerdictConflict(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
		t.Fatal(e)
	}
	nativeActivate(t, f, in, "v2")
	in.EventID = "new_transport"
	_, captureRef = nativeAccept(t, f, in)
	r, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef)
	if e != nil || r.Disposition != "duplicate_observation" || r.AppliedNow {
		t.Fatal("relearned", e)
	}
	if v := nativeView(t, f, in, "v2"); v.Value == nil || *v.Value != 0.5 {
		t.Fatal("version reinitialized by old evidence")
	}
	in.EventID = "conflicting_verdict"
	in.Passed = false
	_, captureRef = nativeAccept(t, f, in)
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); !errors.Is(e, ErrConflict) {
		t.Fatal("changed verdict accepted", e)
	}
	if len(nativeKindRows(f, "consumption")) != 1 {
		t.Fatal("consumption changed")
	}
}
func TestNativeDynamicsCASCommitErrorZeroResult(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	failure := errors.New("native isolated commit failure")
	f.svc.Repo = nativeCommitFailureRepo{Repository: f.repo, err: failure}
	r, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef)
	if !errors.Is(e, failure) || !reflect.DeepEqual(r, NativeDynamicsReceipt{}) {
		t.Fatal("failed commit leaked success", e)
	}
}
func TestNativeDynamicsCaptureBeforeApplyRecovery(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	if len(nativeKindRows(f, "event")) != 0 || len(nativeKindRows(f, "capture")) != 1 {
		t.Fatal("premature application")
	}
	results, e := f.svc.DrainNativeDynamicsCaptures(testCtx, "native", in.Scope, 64)
	if e != nil || len(results) != 1 || !results[0].AppliedNow {
		t.Fatal("recovery", e)
	}
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
		t.Fatal(e)
	}
	if len(nativeKindRows(f, "event")) != 1 {
		t.Fatal("recovery duplicated event")
	}
}
func TestNativeDynamicsCompleteProofClosure(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
		t.Fatal(e)
	}
	r, _ := f.repo.Get(testCtx, "agent_runs", in.Scope, "outcome-run")
	run := *r.Run
	run.Outcome = "running"
	r.Run = &run
	f.repo.mu.Lock()
	f.repo.store["agent_runs"][recKey(in.Scope, r.ID)] = r
	f.repo.mu.Unlock()
	if v, e := f.svc.ReadNativeDynamicsSnapshot(testCtx, "native", NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, "v1", time.Now().UnixNano()}); e == nil && v.Value != nil {
		t.Fatal("old run contribution published")
	}
}
func TestNativeDynamicsInvalidationAndPublication(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return invalidateNativeDynamicsDependencyTx(ctx, tx, in.Scope, in.Evidence[0], "dependency_erased")
	}); e != nil {
		t.Fatal(e)
	}
	nativeDrainPurge(t, f.svc, "native", in.Scope, Ref{Collection: colJobs, ID: nativePurgeID(in.Scope, in.Evidence[0]), Revision: 1})
	v, e := f.svc.ReadNativeDynamicsSnapshot(testCtx, "native", NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, "v1", time.Now().UnixNano()})
	if e != nil || v.Status != "invalidated" || v.Value != nil {
		t.Fatal("invalidated state exposed", e)
	}
	for _, row := range nativeKindRows(f, "capture") {
		if row.NativeDynamics.Input != nil || len(row.NativeDynamics.Dependencies) != 0 {
			t.Fatal("native source identifiers not scrubbed")
		}
	}
}
func TestNativeDynamicsNumericNoImplicitDefaults(t *testing.T) {
	cfg := nativeTestConfig("v1")
	bad := cfg
	bad.ObservationStrength = nil
	if nativeConfigValid(bad) {
		t.Fatal("strength defaulted")
	}
	zero := 0.0
	cfg.InitialValue = &zero
	if !nativeConfigValid(cfg) {
		t.Fatal("explicit0 rejected")
	}
	nan := math.NaN()
	cfg.EtaPositive = &nan
	if nativeConfigValid(cfg) {
		t.Fatal("nonfinite accepted")
	}
	f, in := nativeFixture(t)
	missing, e := f.svc.ReadNativeDynamicsSnapshot(testCtx, "native", NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, "missing", time.Now().UnixNano()})
	if e != nil || missing.Status != "missing" || missing.Value != nil {
		t.Fatal("missing became0", e)
	}
}

type nativeCommitFailureRepo struct {
	Repository
	err error
}

func (r nativeCommitFailureRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if e := fn(ctx, tx); e != nil {
			return e
		}
		return r.err
	})
}

func checkNativeDynamicsBooleanConsumption(t *testing.T, passed bool) {
	t.Helper()

	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = passed
	_, captureRef := nativeAccept(t, f, in)
	receipt, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef)
	if e != nil {
		t.Fatal(e)
	}
	if !receipt.AppliedNow || receipt.LearningConsumed != passed || len(nativeKindRows(f, "seen")) != 1 {
		t.Fatal("qualification mismatch")
	}
	if len(nativeKindRows(f, "consumption")) != map[bool]int{true: 1, false: 0}[passed] {
		t.Fatal("false consumption")
	}
	v := nativeView(t, f, in, "v1")
	want := 0.5
	if passed {
		want = 0.55
	}
	if v.Value == nil || math.Abs(*v.Value-want) > 1e-15 {
		t.Fatal("numeric jump", v)
	}
	again, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef)
	if e != nil || again.AppliedNow {
		t.Fatal("retry reapplied", e)
	}
}
