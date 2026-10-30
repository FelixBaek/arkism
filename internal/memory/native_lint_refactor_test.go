package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type nativeLintReadSpyTx struct {
	nativeFaultTx
	evaluation     Ref
	protected      []Ref
	failEvaluation bool
	protectedReads *int
}

func (x nativeLintReadSpyTx) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if x.failEvaluation && col == x.evaluation.Collection && id == x.evaluation.ID {
		return Record{}, ErrUnavailable
	}
	for _, ref := range x.protected {
		if col == ref.Collection && id == ref.ID {
			*x.protectedReads++
			return Record{}, errors.New("protected metadata must not be read after source rejection")
		}
	}
	return x.Tx.Get(ctx, col, scope, id)
}
func TestNativeLintSourceRejectionErrorPriority(t *testing.T) {
	for _, mode := range []string{"request_hash", "unsupported_spec", "evaluation_store_error"} {
		t.Run(mode, func(t *testing.T) { checkNativeLintSourceErrorPriority(t, mode) })
	}
}
func checkNativeLintSourceErrorPriority(t *testing.T, mode string) {
	t.Helper()
	f, ref, in := dynamicsOutcomeAccepted(t, false)
	r, e := f.repo.Get(testCtx, "evaluations", in.Scope, ref.ID)
	if e != nil {
		t.Fatal(e)
	}
	expected := ErrConflict
	switch mode {
	case "request_hash":
		r.RequestHash = "invalid original request fingerprint"
	case "unsupported_spec":
		v := *r.Evaluation
		v.SpecRef = &Ref{Collection: colEvalTasks, ID: "unrequested-spec", Revision: 1}
		r.Evaluation = &v
	case "evaluation_store_error":
		expected = ErrUnavailable
	}
	f.repo.mu.Lock()
	f.repo.store["evaluations"][recKey(in.Scope, ref.ID)] = r
	f.repo.mu.Unlock()
	reads := 0
	e = f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		snapshot, err := resolveAcceptedNativeOutcomeTx(ctx, nativeLintReadSpyTx{nativeFaultTx: nativeFaultTx{Tx: tx}, evaluation: ref, protected: []Ref{in.Target, in.Trace}, failEvaluation: mode == "evaluation_store_error", protectedReads: &reads}, ref, in)
		if !reflect.DeepEqual(snapshot, acceptedNativeOutcome{}) {
			t.Error("rejected source leaked an accepted snapshot")
		}
		return err
	})
	if !errors.Is(e, expected) {
		t.Fatal("source error lost priority", e)
	}
	if reads != 0 {
		t.Fatal("rejected source read unrelated protected metadata", reads)
	}
}
