package memory

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

// Lane rates and weight are test literals, not chosen coefficients.
const nativeFSWeight = 0.25

func nativeFSSeal(t *testing.T, c dyn.DynamicsConfigV2) dyn.DynamicsConfigV2 {
	t.Helper()
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	var p dyn.DynamicsConfigV2HashProjection
	if e = json.Unmarshal(raw, &p); e != nil {
		t.Fatal(e)
	}
	if c.ConfigSHA256, e = dyn.CanonicalSHA256("DynamicsConfigV2HashProjection", p); e != nil {
		t.Fatal(e)
	}
	return c
}

// nativeFSRuntime builds a runtime whose slow lane is the policy's fast lane at
// ten times tau and a lower positive rate.
func nativeFSRuntime(t *testing.T, mode, policy string) *NativeSearchShadowRuntime {
	t.Helper()
	cfg := nativeSearchTestRuntime(t, policy).config
	entry, _ := nativeSearchPolicy(cfg)
	slow := *entry.Dynamics
	slow.TauNS, slow.EtaPositive = 10*slow.TauNS, 0.1
	cfg.Mode, cfg.FastSlow = mode, &NativeSearchFastSlow{Slow: nativeFSSeal(t, slow), FastWeight: nativeFSWeight}
	r, e := NewNativeSearchShadowRuntime(cfg)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func nativeFSNativeConfig(t *testing.T, r *NativeSearchShadowRuntime) NativeDynamicsConfig {
	t.Helper()
	out := nativeSearchMatchingConfig(t, r)
	tau, eta := r.config.FastSlow.Slow.TauNS, r.config.FastSlow.Slow.EtaPositive
	out.SlowTauNS, out.SlowEtaPositive = &tau, &eta
	return out
}

func TestNativeSearchFastSlowRuntimeConfig(t *testing.T) {
	base := nativeFSRuntime(t, "shadow", "V3-P1-NOTIME").config
	if _, e := NewNativeSearchShadowRuntime(base); e != nil {
		t.Fatal("shadow with lanes", e)
	}
	refused := map[string]func(*NativeSearchShadowConfig){
		"on_without_lanes":   func(c *NativeSearchShadowConfig) { c.Mode, c.FastSlow = "on", nil },
		"dynamic_mode":       func(c *NativeSearchShadowConfig) { c.Mode = "dynamic" },
		"zero_weight":        func(c *NativeSearchShadowConfig) { c.FastSlow.FastWeight = 0 },
		"weight_one":         func(c *NativeSearchShadowConfig) { c.FastSlow.FastWeight = 1 },
		"slow_not_slower":    func(c *NativeSearchShadowConfig) { c.FastSlow.Slow.TauNS = 1 },
		"slow_hash_broken":   func(c *NativeSearchShadowConfig) { c.FastSlow.Slow.ConfigSHA256 = strings.Repeat("0", 64) },
		"policy_no_dynamics": func(c *NativeSearchShadowConfig) { c.Policy = "B0" },
	}
	for name, edit := range refused {
		t.Run(name, func(t *testing.T) {
			c := base
			lanes := *base.FastSlow
			c.FastSlow = &lanes
			edit(&c)
			if _, e := NewNativeSearchShadowRuntime(c); !errors.Is(e, ErrInvalid) {
				t.Fatal("accepted", e)
			}
		})
	}
	on := base
	on.Mode = "on"
	if _, e := NewNativeSearchShadowRuntime(on); e != nil {
		t.Fatal("on with lanes", e)
	}
}

func TestNativeDynamicsSlowLaneConfigBytes(t *testing.T) {
	scalar := nativeTestConfig("v")
	raw, _ := json.Marshal(scalar)
	if strings.Contains(string(raw), "slow_") {
		t.Fatal("scalar config bytes changed", string(raw))
	}
	tau, eta := 10**scalar.TauNS, 0.1
	pair := scalar
	pair.SlowTauNS, pair.SlowEtaPositive = &tau, &eta
	if !nativeConfigValid(pair) || nativeConfigHash(pair) == nativeConfigHash(scalar) {
		t.Fatal("slow lane not bound into the config hash")
	}
	for name, c := range map[string]NativeDynamicsConfig{
		"tau_only":    func() NativeDynamicsConfig { c := scalar; c.SlowTauNS = &tau; return c }(),
		"eta_only":    func() NativeDynamicsConfig { c := scalar; c.SlowEtaPositive = &eta; return c }(),
		"not_slower":  func() NativeDynamicsConfig { c := pair; c.SlowTauNS = scalar.TauNS; return c }(),
		"eta_outside": func() NativeDynamicsConfig { c := pair; big := 1.5; c.SlowEtaPositive = &big; return c }(),
	} {
		if nativeConfigValid(c) {
			t.Fatal("accepted", name)
		}
	}
}

// The worker advances both lanes on one observation; a repeated capture is a
// duplicate and leaves the stored state byte for byte the same.
func TestNativeDynamicsSlowLaneWorkerAndReplay(t *testing.T) {
	f, in := nativeFixture(t)
	r := nativeFSRuntime(t, "shadow", "V3-P1-NOTIME")
	cfg := nativeFSNativeConfig(t, r)
	if _, e := f.svc.ActivateNativeMemoryDynamics(testCtx, "native", NativeDynamicsActivationInput{Scope: scopeA, Target: in.Target, Dimension: in.Dimension, Config: cfg, RequestID: "fs-activate"}); e != nil {
		t.Fatal(e)
	}
	in.Passed = true
	_, capture := nativeAccept(t, f, in)
	first, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", scopeA, capture)
	if e != nil || !first.LearningConsumed {
		t.Fatal(e, first.Disposition)
	}
	state := nativeKindRows(f, "state")
	if len(state) != 1 {
		t.Fatal(len(state))
	}
	st := state[0].NativeDynamics.State
	fast, _ := dyn.NativeAdvanceScalarNumeric(*cfg.InitialValue, 0, 0, *cfg.TauNS, false, "pass", *cfg.EtaPositive, *cfg.UnitStrength, cfg.ObservationStrength)
	slow, _ := dyn.NativeAdvanceScalarNumeric(*cfg.InitialValue, 0, 0, *cfg.SlowTauNS, false, "pass", *cfg.SlowEtaPositive, *cfg.UnitStrength, cfg.ObservationStrength)
	if st.Value == nil || st.SlowValue == nil || *st.Value != fast || *st.SlowValue != slow || fast == slow {
		t.Fatal("lanes", st.Value, st.SlowValue, fast, slow)
	}
	view := nativeView(t, f, in, cfg.Version)
	if view.SlowValue == nil || *view.SlowValue != slow || *view.Value != fast {
		t.Fatal("view lanes", view)
	}
	before := structuredText(nativeKindRows(f, "state"))
	again, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", scopeA, capture)
	if e != nil || again.LearningConsumed || again.AppliedNow || structuredText(nativeKindRows(f, "state")) != before {
		t.Fatal("replay changed state", e, again.Disposition)
	}
}

// nativeFSSearch activates two memories with lanes, passes one, then searches.
func nativeFSSearchFixture(t *testing.T, r *NativeSearchShadowRuntime) (*fixture, SearchInput, Ref) {
	t.Helper()
	f, in := nativeFixture(t)
	other := f.activeMemory("설계 설계 설계 second independent candidate", "설계")
	for _, target := range []Ref{in.Target, {Collection: colMemories, ID: other.ID, Revision: other.Revision}} {
		if _, e := f.svc.ActivateNativeMemoryDynamics(testCtx, "native", NativeDynamicsActivationInput{Scope: scopeA, Target: target, Dimension: in.Dimension, Config: nativeFSNativeConfig(t, r), RequestID: "fs-" + target.ID}); e != nil {
			t.Fatal(e)
		}
	}
	in.Passed = true
	_, capture := nativeAccept(t, f, in)
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", scopeA, capture); e != nil {
		t.Fatal(e)
	}
	q := nativeSearchTestInput()
	q.Query = "설계"
	return f, q, in.Target
}

func nativeFSIDs(b ContextBundle) []string {
	out := []string{}
	for _, h := range b.Items {
		out = append(out, h.ID)
	}
	return out
}

func nativeFSStrip(b ContextBundle) string {
	b.ContextID = ""
	return structuredText(b)
}

func TestNativeSearchFastSlowModes(t *testing.T) {
	off, e := NewNativeSearchShadowRuntime(NativeSearchShadowConfig{Mode: "off"})
	if e != nil {
		t.Fatal(e)
	}
	shadow, on := nativeFSRuntime(t, "shadow", "V3-P1-NOTIME"), nativeFSRuntime(t, "on", "V3-P1-NOTIME")
	f, q, passed := nativeFSSearchFixture(t, on)
	search := func(r *NativeSearchShadowRuntime) ContextBundle {
		t.Helper()
		f.svc.NativeSearchShadow = r
		out, e := f.svc.Search(testCtx, "native", q)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	absent := search(nil)
	if got := search(off); nativeFSStrip(got) != nativeFSStrip(absent) || len(off.Observations()) != 0 {
		t.Fatal("off changed the answer or recorded")
	}
	if got := search(shadow); nativeFSStrip(got) != nativeFSStrip(absent) {
		t.Fatal("shadow changed the answer")
	}
	rec := shadow.Observations()
	if len(rec) != 1 || rec[0].Status != "selected" || !rec[0].FastSlow || rec[0].Served || rec[0].SourceCount != 2 {
		t.Fatal("shadow record", rec)
	}
	served := search(on)
	got := on.Observations()
	if len(got) != 1 || got[0].Status != "selected" || !got[0].Served || !reflect.DeepEqual(got[0].SelectedRefs, rec[0].SelectedRefs) {
		t.Fatal("on record", got)
	}
	ids := nativeFSIDs(served)
	if len(ids) != 2 || ids[0] != passed.ID || reflect.DeepEqual(ids, nativeFSIDs(absent)) {
		t.Fatal("on did not rank by the fast/slow state", ids, nativeFSIDs(absent))
	}
	if served.ContextData == absent.ContextData || served.TokensUsed == 0 {
		t.Fatal("served context not packed from the served ranking")
	}
}

// The published value is the mixed lane value, not the fast lane alone.
func TestNativeSearchFastSlowPublishedValue(t *testing.T) {
	r := nativeFSRuntime(t, "on", "V3-P1-NOTIME")
	f, q, passed := nativeFSSearchFixture(t, r)
	hits := []Hit{}
	for _, row := range f.repo.rows(colMemories) {
		if row.Status == "active" {
			hits = append(hits, HitFromRecord(colMemories, row))
		}
	}
	snap, code := nativeFreezeSearch(scopeA, [][]Hit{hits}, nil)
	if code != "" {
		t.Fatal(code)
	}
	actor, e := f.svc.authorize(testCtx, "native", "search:memories", scopeA, "", "")
	if e != nil {
		t.Fatal(e)
	}
	values, _, _, code := f.svc.nativePublishSearchValues(testCtx, "native", q, actor, snap, r.config)
	if code != "" || len(values) != 2 {
		t.Fatal(code, len(values))
	}
	cfg := nativeFSNativeConfig(t, r)
	fast, _ := dyn.NativeAdvanceScalarNumeric(*cfg.InitialValue, 0, 0, *cfg.TauNS, false, "pass", *cfg.EtaPositive, *cfg.UnitStrength, cfg.ObservationStrength)
	slow, _ := dyn.NativeAdvanceScalarNumeric(*cfg.InitialValue, 0, 0, *cfg.SlowTauNS, false, "pass", *cfg.SlowEtaPositive, *cfg.UnitStrength, cfg.ObservationStrength)
	for _, v := range values {
		want := *cfg.InitialValue
		if v.Target.ID == passed.ID {
			want = nativeFSWeight*fast + (1-nativeFSWeight)*slow
		}
		if *v.Value != want {
			t.Fatal("mixed value", v.Target.ID, *v.Value, want)
		}
	}
	// A slow lane whose rates differ from the stored native slow lane is refused.
	other := r.config
	lanes := *other.FastSlow
	lanes.Slow.EtaPositive = 0.2
	lanes.Slow = nativeFSSeal(t, lanes.Slow)
	other.FastSlow = &lanes
	if _, _, _, code = f.svc.nativePublishSearchValues(testCtx, "native", q, actor, snap, other); code != "invalid_source" {
		t.Fatal("mismatched slow lane read", code)
	}
	// A scalar runtime on the same native rows reads the fast lane alone.
	scalar := nativeSearchTestRuntime(t, "V3-P1-NOTIME")
	values, _, _, code = f.svc.nativePublishSearchValues(testCtx, "native", q, actor, snap, scalar.config)
	for _, v := range values {
		if v.Target.ID == passed.ID && *v.Value != fast {
			t.Fatal("scalar read", *v.Value, fast)
		}
	}
	if code != "" {
		t.Fatal(code)
	}
}
