package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type nativeMainRefAuditTx struct {
	nativeFaultTx
	reads *int
}

func (x nativeMainRefAuditTx) Get(ctx context.Context, col string, s Scope, id string) (Record, error) {
	if isEvalRecordCollection(col) {
		*x.reads++
		return Record{}, errors.New("unexpected extra evaluation reference read")
	}
	return x.Tx.Get(ctx, col, s, id)
}

type nativeMainRefAuditRepo struct {
	Repository
	reads *int
}

func (x nativeMainRefAuditRepo) Get(ctx context.Context, col string, s Scope, id string) (Record, error) {
	if isEvalRecordCollection(col) {
		*x.reads++
		return Record{}, errors.New("unexpected extra evaluation reference read")
	}
	return x.Repository.Get(ctx, col, s, id)
}
func (x nativeMainRefAuditRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return x.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, nativeMainRefAuditTx{nativeFaultTx{Tx: tx}, x.reads})
	})
}

type nativeMainRefField struct {
	name, col string
	set       func(*Evaluation, *Ref)
}

func nativeMainStoreExtra(t *testing.T, f *fixture, in MemoryOutcomeInput, extra Ref, state string) {
	t.Helper()
	if state == "missing" {
		return
	}
	sc := in.Scope
	if state == "foreign_scope" {
		sc.UserID = "foreign-main-user"
	}
	revision := int64(1)
	if state == "stale" {
		revision = 2
	}
	if e := f.repo.Insert(testCtx, extra.Collection, Record{ID: extra.ID, Scope: sc, Revision: revision, Status: "active"}); e != nil {
		t.Fatal(e)
	}
}
func nativeMainModifiedEvaluation(t *testing.T, f *fixture, in MemoryOutcomeInput, ref Ref, field nativeMainRefField, state string) Record {
	t.Helper()
	r, e := f.repo.Get(testCtx, "evaluations", in.Scope, ref.ID)
	if e != nil {
		t.Fatal(e)
	}
	oldEvaluation := *r.Evaluation
	r.Evaluation = &oldEvaluation
	extra := Ref{Collection: field.col, ID: "native-main-extra-ref", Revision: 1}
	field.set(r.Evaluation, &extra)
	nativeMainStoreExtra(t, f, in, extra, state)
	f.repo.mu.Lock()
	f.repo.store["evaluations"][recKey(in.Scope, r.ID)] = r
	f.repo.mu.Unlock()
	return r
}
func nativeMainAssertCanonicalReject(t *testing.T, f *fixture, in MemoryOutcomeInput, r Record, ref Ref) {
	t.Helper()
	if nativeOutcomeEvaluation(r, in.Passed) || canonicalNativeOutcome(r, ref, in) {
		t.Error("noncanonical writer evaluation ref accepted")
	}
	reads := 0
	e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		v, err := resolveAcceptedNativeOutcomeTx(ctx, nativeMainRefAuditTx{nativeFaultTx{Tx: tx}, &reads}, ref, in)
		if !reflect.DeepEqual(v, acceptedNativeOutcome{}) {
			t.Error("rejected source leaked accepted result")
		}
		return err
	})
	if !errors.Is(e, ErrConflict) {
		t.Error("expected canonical conflict", e)
	}
	if reads != 0 {
		t.Error("extra evaluation ancestry was read", reads)
	}
}
func nativeMainAssertNoLearningOrReads(t *testing.T, f *fixture, in MemoryOutcomeInput, ref, capture Ref) {
	t.Helper()
	before := nativeView(t, f, in, "v1")
	reads := 0
	f.svc.Repo = nativeMainRefAuditRepo{f.repo, &reads}
	if _, e := f.svc.PrepareMemoryOutcomeIntent(testCtx, "native", ref, in); !errors.Is(e, ErrConflict) {
		t.Error("prepare accepted noncanonical source", e)
	}
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, capture); e == nil {
		t.Error("modified source learned")
	}
	f.svc.Repo = f.repo
	after := nativeView(t, f, in, "v1")
	if !reflect.DeepEqual(before, after) {
		t.Error("rejected source mutated learning state")
	}
	for _, role := range []string{"seen", "consumption", "event"} {
		if len(nativeKindRows(f, role)) != 0 {
			t.Error("rejected source wrote", role)
		}
	}
	if reads != 0 {
		t.Error("unsupported evaluation ref was read", reads)
	}
}
func nativeMainRejectedRefCase(t *testing.T, field nativeMainRefField, state string, passed bool) {
	t.Helper()
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = passed
	ref, capture := nativeAccept(t, f, in)
	r := nativeMainModifiedEvaluation(t, f, in, ref, field, state)
	nativeMainAssertCanonicalReject(t, f, in, r, ref)
	nativeMainAssertNoLearningOrReads(t, f, in, ref, capture)
}
func TestNativeMainEvaluationRefsFailClosed(t *testing.T) {
	fields := []nativeMainRefField{
		{"spec", colEvalTasks, func(e *Evaluation, r *Ref) { e.SpecRef = r }},
		{"trial", colEvalTrials, func(e *Evaluation, r *Ref) { e.TrialRef = r }},
		{"calibration", colJudgeCalibrations, func(e *Evaluation, r *Ref) { e.CalibrationRef = r }},
	}
	for _, field := range fields {
		for _, state := range []string{"valid", "foreign_scope", "stale", "missing"} {
			for _, passed := range []bool{false, true} {
				t.Run(field.name+"/"+state+"/"+map[bool]string{false: "false", true: "true"}[passed], func(t *testing.T) { nativeMainRejectedRefCase(t, field, state, passed) })
			}
		}
	}
}
func TestNativeMainNilEvaluationRefsStillLearn(t *testing.T) {
	for _, passed := range []bool{false, true} {
		t.Run(map[bool]string{false: "false", true: "true"}[passed], func(t *testing.T) {
			f, in := nativeFixture(t)
			nativeActivate(t, f, in, "v1")
			in.Passed = passed
			ref, capture := nativeAccept(t, f, in)
			r, e := f.repo.Get(testCtx, "evaluations", in.Scope, ref.ID)
			if e != nil {
				t.Fatal(e)
			}
			if len(evaluationRefs(r.Evaluation)) != 0 || !canonicalNativeOutcome(r, ref, in) {
				t.Fatal("current writer nil-ref canonical source rejected")
			}
			if _, e = f.svc.PrepareMemoryOutcomeIntent(testCtx, "native", ref, in); e != nil {
				t.Fatal(e)
			}
			receipt, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, capture)
			if e != nil || !receipt.AppliedNow || receipt.LearningConsumed != passed {
				t.Fatal("nil-ref normal path", e)
			}
		})
	}
}
