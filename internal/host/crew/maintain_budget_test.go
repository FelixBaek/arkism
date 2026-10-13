package crew

import (
	"bytes"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestMaintainWaitsWhenClaudeUsageIsHigh(t *testing.T) {
	p, a, m, k, mm := host.AccountsProfiles, host.AccountsAccount, memoriesFn, knownPairsFn, mMetric
	t.Cleanup(func() {
		host.AccountsProfiles, host.AccountsAccount, memoriesFn, knownPairsFn, mMetric = p, a, m, k, mm
	})
	knownPairsFn = func([]map[string]any) (map[string]bool, error) { return map[string]bool{}, nil }
	mMetric = func(map[string]any) {}
	t.Setenv("ARKISM_LLM_BUDGET_PCT", "")
	t.Setenv("ARKISM_MAINTAIN_LLM", "1")
	host.AccountsProfiles = func(string, string, string) []map[string]any {
		return []map[string]any{{"id": "claude", "cli": "claude"}}
	}
	left := 5
	host.AccountsAccount = func(map[string]any, string, string, bool) (map[string]any, error) {
		return map[string]any{"usage_status": "ok", "quotas": []any{map[string]any{"window": "5h", "left_pct": left, "resets_at": "2999-01-01T00:00:00+00:00"}}}, nil
	}
	ran := false
	memoriesFn = func() ([]map[string]any, error) { ran = true; return nil, nil }
	var out, errOut bytes.Buffer
	if code := maintainMain([]string{"run"}, nil, &out, &errOut); code != 0 || ran || !strings.Contains(out.String(), "95%") {
		t.Fatal(code, ran, out.String(), errOut.String())
	}
	// Under the limit, or the model judge off: the night runs.
	left = 50
	out.Reset()
	if code := maintainMain([]string{"run"}, nil, &out, &errOut); code != 0 || !ran || !strings.Contains(out.String(), "found 0") {
		t.Fatal(code, ran, out.String(), errOut.String())
	}
	left, ran = 5, false
	t.Setenv("ARKISM_MAINTAIN_LLM", "0")
	if code := maintainMain([]string{"run"}, nil, &out, &errOut); code != 0 || !ran {
		t.Fatal(code, ran)
	}
}
