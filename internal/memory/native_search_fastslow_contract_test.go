package memory

import (
	"encoding/json"
	"testing"
)

func nativeFSContractMetadata(t *testing.T, r *NativeSearchShadowRuntime, wantMode string) {
	t.Helper()
	observations := r.Observations()
	if len(observations) != 1 {
		t.Fatal("missing observation", len(observations))
	}
	rec := observations[0]
	raw, e := json.Marshal(rec)
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	if e = json.Unmarshal(raw, &fields); e != nil {
		t.Fatal(e)
	}
	if rec.Protocol != "sys-native-service-fast-slow-v1" || rec.ContractSHA != ContentHash(string(nativeSearchFastSlowContract)) || fields["mode"] != wantMode || !rec.FastSlow {
		t.Fatalf("fast/slow observation misstates its contract: %s", raw)
	}
}

func TestNativeSearchFastSlowObservationContract(t *testing.T) {
	for _, mode := range []string{"shadow", "on"} {
		t.Run(mode, func(t *testing.T) {
			r := nativeFSRuntime(t, mode, "V3-P1-NOTIME")
			f, q, _ := nativeFSSearchFixture(t, r)
			f.svc.NativeSearchShadow = r
			if _, e := f.svc.Search(testCtx, "native", q); e != nil {
				t.Fatal(e)
			}
			nativeFSContractMetadata(t, r, mode)
		})
	}
}

// Freeze a representative legacy scalar fallback byte for byte, including
// omitted fast_slow/served/mode fields and the immutable v1 contract hash.
func TestNativeSearchScalarObservationLegacyJSON(t *testing.T) {
	r := nativeSearchTestRuntime(t, "V3-P1-NOTIME")
	svc := &Service{NativeSearchShadow: r}
	a := svc.beginNativeSearchShadow(testCtx, nativeSearchTestInput(), "sources", nil, nil)
	a.finish()
	raw, e := json.Marshal(r.Observations()[0])
	if e != nil {
		t.Fatal(e)
	}
	const want = `{"protocol":"sys-native-service-shadow-v1","contract_sha256":"e39f79fcb64b494da784c8c1c4df17786c95dfc03bea73ef0de1b3b06c0aab8a","scope_sha256":"f79ea5a76f80dd0c3edcff3c70db40f01f45acbf98d2e7c61aa61907f7688c92","candidate_sha256":"","published_sha256":"","arithmetic_only_sha256":"","actual_baseline_sha256":"","status":"fallback","reason":"unsupported_collection","baseline_returned":false,"candidate_count":0,"entry_count":0,"source_count":0,"local_steps":0,"selected_scoped_ref_sha256":[]}`
	if string(raw) != want {
		t.Fatalf("legacy scalar JSON changed:\n%s", raw)
	}
}

func TestNativeSearchFastSlowContractFallbackAndRejectedReturn(t *testing.T) {
	for _, mode := range []string{"shadow", "on"} {
		for _, fault := range []string{"source", "audit"} {
			t.Run(mode+"/"+fault, func(t *testing.T) { nativeFSContractCheckReturn(t, mode, fault) })
		}
	}
}

func nativeFSContractCheckReturn(t *testing.T, mode, fault string) {
	t.Helper()
	r := nativeFSRuntime(t, mode, "V3-P1-NOTIME")
	f, q, _ := nativeFSSearchFixture(t, r)
	baseline, err := f.svc.Search(testCtx, "native", q)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.NativeSearchShadow = r
	if fault == "source" {
		f.svc.Repo = &nativeSearchBlockedSourceRepo{Repository: f.repo}
	} else {
		f.svc.Repo = &nativeFSFinalFaultRepo{Repository: f.repo, beforeFinal: func() { f.repo.failAudit = true }}
	}
	out, err := f.svc.Search(testCtx, "native", q)
	nativeFSContractMetadata(t, r, mode)
	rec, stage := r.Observations()[0], r.StagesV2()[0]
	if stage.Protocol != NativeSearchStagesProtocol || stage.ContractSHA != ContentHash(string(nativeSearchStagesContract)) {
		t.Fatal("stages contract changed")
	}
	if fault == "source" {
		nativeFSContractCheckFallback(t, err, rec, stage, out, baseline)
		return
	}
	nativeFSContractCheckRejected(t, err, rec, stage, out, baseline)
}

func nativeFSContractCheckFallback(t *testing.T, err error, rec NativeSearchShadowObservation, stage NativeSearchShadowStages, out, baseline ContextBundle) {
	t.Helper()
	if err != nil || rec.Status != "fallback" || rec.Served || !rec.BaselineReturned || nativeFSStrip(out) != nativeFSStrip(baseline) {
		t.Fatal("source fallback semantics", err, rec)
	}
	if rec.ArithmeticSHA != "" || len(rec.SelectedRefs) != 0 || stage.FullRankedRefs != nil {
		t.Fatal("uncomputed ranking fabricated", rec, stage)
	}
	if !stage.BaselineReturned || len(stage.BaselineCurrentRefs) != 2 {
		t.Fatal("actual baseline refs missing", stage)
	}
}

func nativeFSContractCheckRejected(t *testing.T, err error, rec NativeSearchShadowObservation, stage NativeSearchShadowStages, out, baseline ContextBundle) {
	t.Helper()
	if err == nil || len(out.Items) != 0 || out.ContextData != "" || rec.Status != "computed_not_returned" || rec.Served || rec.BaselineReturned || stage.BaselineReturned || stage.BaselineCurrentRefs != nil || stage.BaselineHistoryRefs != nil {
		t.Fatal("failed return claim", err, rec, stage)
	}
	if rec.BaselineSHA != nativeSearchBundleHash(baseline) {
		t.Fatal("computed baseline hash lost")
	}
}

func TestNativeSearchFastSlowContractOffAndLegacyDecode(t *testing.T) {
	for _, pair := range []bool{false, true} {
		cfg := NativeSearchShadowConfig{Mode: "off"}
		if pair {
			cfg = nativeFSRuntime(t, "shadow", "V3-P1-NOTIME").config
			cfg.Mode = "off"
		}
		r, e := NewNativeSearchShadowRuntime(cfg)
		if e != nil {
			t.Fatal(e)
		}
		svc := &Service{NativeSearchShadow: r}
		if svc.beginNativeSearchShadow(testCtx, nativeSearchTestInput(), colMemories, nil, nil) != nil || len(r.Observations()) != 0 {
			t.Fatal("off emitted observation")
		}
	}
	old := `{"protocol":"sys-native-service-shadow-v1","contract_sha256":"e39f79fcb64b494da784c8c1c4df17786c95dfc03bea73ef0de1b3b06c0aab8a","status":"fallback","selected_scoped_ref_sha256":[]}`
	var decoded NativeSearchShadowObservation
	if e := json.Unmarshal([]byte(old), &decoded); e != nil {
		t.Fatal(e)
	}
	if decoded.Mode != "" || decoded.FastSlow || decoded.Protocol != "sys-native-service-shadow-v1" {
		t.Fatal("legacy decode changed", decoded)
	}
	raw, e := json.Marshal(decoded)
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	if e = json.Unmarshal(raw, &fields); e != nil {
		t.Fatal(e)
	}
	if _, ok := fields["mode"]; ok {
		t.Fatal("legacy mode field added")
	}
	if _, ok := fields["fast_slow"]; ok {
		t.Fatal("legacy fast_slow field added")
	}
}

func TestNativeSearchFastSlowContractDescriptor(t *testing.T) {
	var contract map[string]any
	if e := json.Unmarshal(nativeSearchFastSlowContract, &contract); e != nil {
		t.Fatal(e)
	}
	if contract["protocol"] != "sys-native-service-fast-slow-contract-v1" || contract["observation_protocol"] != "sys-native-service-fast-slow-v1" {
		t.Fatal("wrong descriptor protocol")
	}
	if ContentHash(string(nativeSearchShadowContract)) != "e39f79fcb64b494da784c8c1c4df17786c95dfc03bea73ef0de1b3b06c0aab8a" {
		t.Fatal("frozen v1 contract changed")
	}
	for _, key := range []string{"configuration", "return_semantics", "observation_meaning", "source_evidence", "unknown", "telemetry", "scope"} {
		if _, ok := contract[key]; !ok {
			t.Fatal("descriptor missing", key)
		}
	}
}

func TestNativeSearchFastSlowContractEarlyFallback(t *testing.T) {
	for _, mode := range []string{"shadow", "on"} {
		r := nativeFSRuntime(t, mode, "V3-P1-NOTIME")
		svc := &Service{NativeSearchShadow: r}
		attempt := svc.beginNativeSearchShadow(testCtx, nativeSearchTestInput(), "sources", nil, nil)
		attempt.finish()
		nativeFSContractMetadata(t, r, mode)
		rec := r.Observations()[0]
		if rec.BaselineSHA != "" || rec.ArithmeticSHA != "" || rec.SelectedRefs == nil || len(rec.SelectedRefs) != 0 || r.StagesV2()[0].FullRankedRefs != nil {
			t.Fatal("early unknown evidence fabricated", rec)
		}
		if rec.Reason != "unsupported_collection" || rec.Status != "fallback" || rec.Served || rec.BaselineReturned {
			t.Fatal("early fallback return fabricated", rec)
		}
	}
}

// A failed arithmetic result still has a digest. Its initial empty SelectedRefs
// is unobserved, not a known-empty selected ranking. Only the test work budget
// is exhausted; the existing sealed policy and coefficients stay unchanged.
func TestNativeSearchFastSlowContractFailedArithmetic(t *testing.T) {
	r := nativeFSRuntime(t, "on", "V3-P1-NOTIME")
	cfg := r.config
	cfg.MaxSteps = 1
	r, err := NewNativeSearchShadowRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f, q, _ := nativeFSSearchFixture(t, r)
	baseline, err := f.svc.Search(testCtx, "native", q)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.NativeSearchShadow = r
	out, err := f.svc.Search(testCtx, "native", q)
	if err != nil {
		t.Fatal(err)
	}
	nativeFSContractMetadata(t, r, "on")
	rec, stage := r.Observations()[0], r.StagesV2()[0]
	if rec.Status != "fallback" || rec.Reason != "work_limit" || rec.ArithmeticSHA == "" || rec.SelectedRefs == nil || len(rec.SelectedRefs) != 0 || stage.FullRankedRefs != nil || rec.Served || !rec.BaselineReturned || nativeFSStrip(out) != nativeFSStrip(baseline) {
		t.Fatal("failed arithmetic fabricated selected ranking", rec, stage)
	}
}
