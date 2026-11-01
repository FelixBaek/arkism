package memory

import "testing"

func TestNativeSearchDynamicConfigAndReturn(t *testing.T) {
	f, _, _, repo := nativeStageFixture(t)
	repo.lanes = [][]Hit{{repo.lanes[0][0]}, {repo.lanes[1][0]}}
	cfg := f.svc.NativeSearchShadow.config
	cfg.Mode = "dynamic"
	runtime, err := NewNativeSearchShadowRuntime(cfg)
	if err != nil {
		t.Fatal("explicit general serving config rejected", err)
	}
	f.svc.NativeSearchShadow = nil
	in := nativeSearchTestInput()
	in.Limit = 1
	in.IncludeInactive = false
	baseline, err := f.svc.Search(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.NativeSearchShadow = runtime
	out, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || len(out.Items) != 1 || len(baseline.Items) != 1 {
		t.Fatal("dynamic packing did not reach actual search return", err, len(out.Items), len(baseline.Items))
	}
	rec := runtime.Observations()[0]
	stages := runtime.StagesV2()[0]
	if rec.Protocol != "sys-native-service-dynamic-v1" || rec.Mode != "dynamic" || !rec.Served || rec.BaselineReturned || rec.FastSlow || rec.Status != "selected" || !stages.FinalEntered || stages.FinalErrorCode != "none" {
		t.Fatal("general serving observation misidentified", rec, stages)
	}
}

func TestNativeSearchDynamicFinalFences(t *testing.T) {
	for _, fault := range []string{"audit", "fence", "cancel"} {
		t.Run(fault, func(t *testing.T) { nativeRankingCheckFailedReturn(t, fault, "dynamic") })
	}
}
