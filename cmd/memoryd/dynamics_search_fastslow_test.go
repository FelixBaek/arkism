package main

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/FelixBaek/arkism/internal/memory"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

// daemonFastSlowBlock returns a fast_slow block whose slow lane is the shadow
// policy's lane at ten times tau; rates and weight are test literals.
func daemonFastSlowBlock(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../docs/research/memory-harness/policy_registry.json")
	if err != nil {
		t.Fatal(err)
	}
	var registry dyn.PolicyRegistryV2
	if err = json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	var slow dyn.DynamicsConfigV2
	for _, e := range registry.Entries {
		if e.PolicyID == "V3-P1-NOTIME" {
			slow = *e.Dynamics
		}
	}
	slow.TauNS, slow.EtaPositive = 10*slow.TauNS, 0.1
	b, _ := json.Marshal(slow)
	var p dyn.DynamicsConfigV2HashProjection
	if err = json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if slow.ConfigSHA256, err = dyn.CanonicalSHA256("DynamicsConfigV2HashProjection", p); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"slow_dynamics": slow, "fast_weight": 0.25}
}

func TestDynamicsIntegrationSearchModeFastSlow(t *testing.T) {
	api := daemonSearchModeAPI(t)
	block := daemonFastSlowBlock(t)
	for _, mode := range []string{"shadow", "on"} {
		t.Run(mode, func(t *testing.T) {
			cfg := api
			cfg.DynamicsSearchConfig = daemonSearchModeFiles(t, func(m map[string]any) { m["mode"], m["fast_slow"] = mode, block })
			got, err := prepareDaemonDynamics(cfg)
			if err != nil || got.dynamicsSearch == nil || got.dynamicsSearch.Mode != mode || got.dynamicsSearch.FastSlow == nil || got.dynamicsSearch.FastSlow.FastWeight != 0.25 {
				t.Fatal("fast/slow mode not carried", mode, err)
			}
		})
	}
	edited := func(edit func(map[string]any)) map[string]any {
		out := map[string]any{}
		for k, v := range block {
			out[k] = v
		}
		edit(out)
		return out
	}
	refused := map[string]func(map[string]any){
		"on_without_fast_slow": func(m map[string]any) { m["mode"] = "on" },
		"off_with_fast_slow":   func(m map[string]any) { daemonBareMode("off")(m); m["fast_slow"] = block },
		"zero_weight":          func(m map[string]any) { m["fast_slow"] = edited(func(b map[string]any) { b["fast_weight"] = 0 }) },
		"no_slow_lane":         func(m map[string]any) { m["fast_slow"] = edited(func(b map[string]any) { delete(b, "slow_dynamics") }) },
		"unknown_lane_field":   func(m map[string]any) { m["fast_slow"] = edited(func(b map[string]any) { b["medium"] = 1 }) },
		"policy_no_dynamics":   func(m map[string]any) { m["policy"], m["fast_slow"] = "B0", block },
	}
	for name, edit := range refused {
		t.Run(name, func(t *testing.T) {
			cfg := api
			cfg.DynamicsSearchConfig = daemonSearchModeFiles(t, edit)
			if _, err := prepareDaemonDynamics(cfg); !errors.Is(err, memory.ErrInvalid) {
				t.Fatal("accepted", name, err)
			}
		})
	}
}
