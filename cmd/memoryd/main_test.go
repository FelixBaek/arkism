package main

import "testing"

func TestCostRatesFromEnv(t *testing.T) {
	for _, name := range []string{"MEMORY_PRICING_REVISION", "SKILL_LLM_INPUT_USD_PER_MILLION_TOKENS", "SKILL_LLM_OUTPUT_USD_PER_MILLION_TOKENS", "JEV_USD_PER_REQUEST"} {
		t.Setenv(name, "")
	}
	t.Setenv("MEMORY_PRICING_REVISION", "prices-2026-09-21")
	t.Setenv("JEV_USD_PER_REQUEST", "0.0015")
	rates, err := costRatesFromEnv()
	if err != nil || rates.JEVRequestPicoUSD != 1_500_000_000 || rates.Revision != "prices-2026-09-21" {
		t.Fatalf("rates: %+v %v", rates, err)
	}
	t.Setenv("MEMORY_PRICING_REVISION", "")
	if _, err = costRatesFromEnv(); err == nil {
		t.Fatal("priced configuration without revision accepted")
	}
}
