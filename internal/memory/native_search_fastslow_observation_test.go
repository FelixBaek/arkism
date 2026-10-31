package memory

import (
	"context"
	"reflect"
	"testing"
)

// The second transaction is the response fence, after native publication read.
type nativeFSFinalFaultRepo struct {
	Repository
	calls       int
	beforeFinal func()
}

func (r *nativeFSFinalFaultRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.calls++
	if r.calls == 2 {
		r.beforeFinal()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return r.Repository.Transaction(ctx, fn)
}

func TestNativeSearchFastSlowObservationReturns(t *testing.T) {
	for _, mode := range []string{"on", "shadow", "fallback"} {
		t.Run(mode, func(t *testing.T) { nativeFSCheckReturn(t, mode) })
	}
}

func nativeFSCheckReturn(t *testing.T, mode string) {
	t.Helper()
	r := nativeFSRuntime(t, "on", "V3-P1-NOTIME")
	f, q, _ := nativeFSSearchFixture(t, r)
	baseline, e := f.svc.Search(testCtx, "native", q)
	if e != nil {
		t.Fatal(e)
	}
	if mode == "shadow" {
		r.config.Mode = "shadow"
	}
	if mode == "fallback" {
		r.config.FastSlow.Slow.EtaPositive = 0.2
		r.config.FastSlow.Slow = nativeFSSeal(t, r.config.FastSlow.Slow)
	}
	f.svc.NativeSearchShadow = r
	out, e := f.svc.Search(testCtx, "native", q)
	if e != nil {
		t.Fatal(e)
	}
	rec, stage := r.Observations()[0], r.StagesV2()[0]
	if rec.BaselineSHA != nativeSearchBundleHash(baseline) || stage.BaselineSHA != rec.BaselineSHA {
		t.Fatal("baseline hash overwritten", rec, stage)
	}
	if !stage.FinalEntered || stage.FinalErrorCode != "none" || len(stage.FinalRejected) != 0 {
		t.Fatal("response fence", stage)
	}

	if mode == "on" {
		nativeFSCheckNativeObservation(t, rec, stage, out)
	} else {
		nativeFSCheckBaselineObservation(t, mode, rec, stage, out, baseline)
	}
}

func nativeFSCheckNativeObservation(t *testing.T, rec NativeSearchShadowObservation, stage NativeSearchShadowStages, out ContextBundle) {
	t.Helper()

	if !rec.Served || rec.BaselineReturned || stage.BaselineReturned || rec.Status != "selected" || stage.Status != "selected" {
		t.Fatal("native return mislabeled", rec, stage)
	}
	if stage.BaselineCurrentRefs != nil || stage.BaselineHistoryRefs != nil {
		t.Fatal("native refs labeled actual baseline", stage)
	}
	refs := []string{}
	for _, h := range out.Items {
		refs = append(refs, contextHitRefSHA(h))
	}
	if !reflect.DeepEqual(refs, stage.EmittedCurrentRefs) || nativeSearchBundleHash(out) == rec.BaselineSHA {
		t.Fatal("fixture did not return different native ranking", stage)
	}
}

func nativeFSCheckBaselineObservation(t *testing.T, mode string, rec NativeSearchShadowObservation, stage NativeSearchShadowStages, out, baseline ContextBundle) {
	t.Helper()

	if rec.Served || !rec.BaselineReturned || !stage.BaselineReturned || nativeFSStrip(out) != nativeFSStrip(baseline) {
		t.Fatal("baseline return changed", rec, stage)
	}
	refs := []string{}
	for _, h := range out.Items {
		refs = append(refs, contextHitRefSHA(h))
	}
	if !reflect.DeepEqual(refs, stage.BaselineCurrentRefs) {
		t.Fatal("actual baseline refs", stage)
	}
	if mode == "fallback" && rec.Status != "fallback" {
		t.Fatal("did not exercise fallback", rec)
	}
}

func TestNativeSearchFastSlowObservationFailedReturn(t *testing.T) {
	for _, fault := range []string{"audit", "fence", "cancel"} {
		t.Run(fault, func(t *testing.T) { nativeFSCheckFailedReturn(t, fault) })
	}
}

func nativeFSCheckFailedReturn(t *testing.T, fault string) {
	t.Helper()
	r := nativeFSRuntime(t, "on", "V3-P1-NOTIME")
	f, q, passed := nativeFSSearchFixture(t, r)
	baseline, e := f.svc.Search(testCtx, "native", q)
	if e != nil {
		t.Fatal(e)
	}
	f.svc.NativeSearchShadow = r
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	repo := &nativeFSFinalFaultRepo{Repository: f.repo, beforeFinal: func() {
		switch fault {
		case "audit":
			f.repo.failAudit = true
		case "cancel":
			cancel()
		case "fence":
			row := f.repo.store[colMemories][recKey(scopeA, passed.ID)]
			row.Revision++
			f.repo.store[colMemories][recKey(scopeA, passed.ID)] = row
		}
	}}
	f.svc.Repo = repo
	out, e := f.svc.Search(ctx, "native", q)
	if e == nil || len(out.Items) != 0 || out.ContextData != "" || repo.calls != 2 {
		t.Fatal("failed response escaped", e, out, repo.calls)
	}

	rec, stage := r.Observations()[0], r.StagesV2()[0]
	nativeFSCheckFailedObservation(t, fault, rec, stage, baseline)
}

func nativeFSCheckFailedObservation(t *testing.T, fault string, rec NativeSearchShadowObservation, stage NativeSearchShadowStages, baseline ContextBundle) {
	t.Helper()

	if rec.Served || rec.BaselineReturned || stage.BaselineReturned || rec.Status != "computed_not_returned" || stage.Status != "computed_not_returned" {
		t.Fatal("failed native return reported served", rec, stage)
	}
	if rec.BaselineSHA != nativeSearchBundleHash(baseline) || stage.BaselineCurrentRefs != nil || stage.BaselineHistoryRefs != nil {
		t.Fatal("failed response fabricated baseline", rec, stage)
	}
	if fault == "fence" && len(stage.FinalRejected) != 1 {
		t.Fatal("fence rejection missing", stage)
	}
	if fault == "audit" && (!stage.FinalEntered || stage.FinalErrorCode != "transaction_abort" || len(stage.FinalRejected) != 0) {
		t.Fatal("audit abort guessed ref", stage)
	}
	if fault == "cancel" && stage.FinalEntered {
		t.Fatal("cancel entered transaction", stage)
	}
}
