package main

import (
	"strings"
	"testing"
)

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

func TestGoogleLoginIsAllOrNothing(t *testing.T) {
	set := func(id, secret, emails, token string) {
		t.Setenv("MEMORY_GOOGLE_CLIENT_ID", id)
		t.Setenv("MEMORY_GOOGLE_CLIENT_SECRET", secret)
		t.Setenv("MEMORY_GOOGLE_ALLOWED_EMAILS", emails)
		t.Setenv("MEMORY_DESKTOP_TOKEN", token)
	}
	token := strings.Repeat("t", 32)
	set("", "", "", "")
	if g, err := googleLogin(); g != nil || err != nil {
		t.Fatalf("unset login must stay off: %+v %v", g, err)
	}
	for name, c := range map[string][4]string{
		"no secret":   {"id", "", "a@example.com", token},
		"no emails":   {"id", "secret", "", token},
		"short token": {"id", "secret", "a@example.com", token[:31]},
		"token only":  {"", "", "", token},
	} {
		set(c[0], c[1], c[2], c[3])
		if g, err := googleLogin(); g != nil || err == nil {
			t.Fatalf("%s: a partial login must be refused: %+v", name, g)
		}
	}
	set("id", "secret", " A@Example.com, ,b@example.com ", token)
	g, err := googleLogin()
	if err != nil || g.ClientID != "id" || g.ClientSecret != "secret" || g.DesktopToken != token {
		t.Fatalf("login: %+v %v", g, err)
	}
	if len(g.Allowed) != 2 || !g.Allowed["a@example.com"] || !g.Allowed["b@example.com"] {
		t.Fatalf("allowed emails are matched lowercase and trimmed, blanks dropped: %v", g.Allowed)
	}
}
