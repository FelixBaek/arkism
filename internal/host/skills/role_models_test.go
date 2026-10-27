package skills

import (
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// 스킬 보안 검사: an engine switched off in 설정 › 역할별 모델 is not run (its binary is dropped for the run).
func TestSkipScannersDropsSwitchedOffEngines(t *testing.T) {
	s, e, c, g := spectorBin, evaluatorBin, ciscoBin, scanGet
	t.Cleanup(func() { spectorBin, evaluatorBin, ciscoBin, scanGet = s, e, c, g })
	bin, err := os.Executable() // an executable path on every OS
	if err != nil {
		t.Fatal(err)
	}
	spectorBin, evaluatorBin, ciscoBin = bin, bin, bin
	scanGet = func(string, time.Duration) (map[string]any, error) { return nil, errors.New("down") }
	skipScanners()
	if got := InstalledScanners(); len(got) != 3 {
		t.Fatal("an unreachable server switched engines off", got)
	}
	scanGet = func(string, time.Duration) (map[string]any, error) {
		return map[string]any{"providers": map[string]any{"roles": map[string]any{"skill_scan": map[string]any{"off": []any{"cisco", "evaluator"}}}}}, nil
	}
	skipScanners()
	if got := InstalledScanners(); !slices.Equal(got, []string{"spector"}) || checkSource() != "builtin+nvidia-security" {
		t.Fatal("left", got, checkSource())
	}
}

// 스킬 요약: the picked model answers through the Claude CLI; without an endpoint its failure is the error.
func TestSummaryTextUsesPickedModel(t *testing.T) {
	m, u, cli := summaryModel, llmURL, summaryCLI
	t.Cleanup(func() { summaryModel, llmURL, summaryCLI = m, u, cli })
	summaryModel, llmURL = "claude-haiku-4-5", ""
	summaryCLI = func(system, user string) (string, error) { return `{"name":"x"}`, nil }
	if text, err := summaryText("system", "content"); err != nil || text != `{"name":"x"}` {
		t.Fatal(text, err)
	}
	summaryCLI = func(string, string) (string, error) { return "", errors.New("exit 1") }
	if _, err := summaryText("system", "content"); err == nil {
		t.Fatal("a failed pick without an endpoint passed")
	}
}
