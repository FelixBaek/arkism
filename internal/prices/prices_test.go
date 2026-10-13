package prices

import "testing"

func TestNormalizeAndLookup(t *testing.T) {
	for in, want := range map[string]string{"opus 5.5": "claude-opus-5-5", "anthropic/claude-opus-5-5": "claude-opus-5-5",
		"openai/gpt-6.1-sol": "gpt-6.1-sol", "Sonnet 5": "claude-sonnet-5", "deepseek-v4-flash": "deepseek-v4-flash"} {
		if got := Normalize(in); got != want {
			t.Fatal(in, got)
		}
	}
	for _, m := range []string{"opus 5.5", "gpt-6.1-sol", "deepseek-v4-flash", "glm-5", "gemini-3-pro", "gemini-3.8-flash", "codex-mini"} {
		if _, ok := Of(m); !ok {
			t.Fatal("unpriced", m)
		}
	}
	if _, ok := Of("astra-2"); ok {
		t.Fatal("guessed a price")
	}
	if c, ok := Cost("opus 5.5", 1_000_000, 1_000_000); !ok || c != 24 {
		t.Fatal(c, ok)
	}
	if p, _ := Of("deepseek-v4-flash"); p.In >= 0.28 {
		t.Fatal("flash took the general row", p)
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 || EstimateTokens("abcd") != 1 || EstimateTokens("안녕하세요") != 5 || EstimateTokens("a") != 1 {
		t.Fatal(EstimateTokens("abcd"), EstimateTokens("안녕하세요"))
	}
}
