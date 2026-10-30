package capture

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestExtractorCLIByModel(t *testing.T) {
	for model, want := range map[string]string{"haiku": "claude", "claude-sonnet-4-5": "claude", "codex": "codex", "gpt-5.6-luna": "codex", "gemini": "gemini", "gemini-3-flash": "gemini", "grok-4": "claude"} {
		if got := extractorCLI(model); got != want {
			t.Errorf("extractorCLI(%q) = %q, want %q", model, got, want)
		}
	}
	if host.CLIRoleModel("gpt-5.6-luna", "haiku") != "gpt-5.6-luna" || host.CLIRoleModel("gemini-3-flash", "haiku") != "gemini-3-flash" || host.CLIRoleModel("opus 5.5", "haiku") != "claude-opus-5-5" {
		t.Fatal("a picked Codex or Gemini model must reach its CLI as named")
	}
	if claudeName("codex") != "codex" || claudeName("haiku") != "claude-haiku" {
		t.Fatal("generator name")
	}
}

func TestStrictSchemaRequiresEveryProperty(t *testing.T) {
	raw, err := strictSchema(`{"type":"object","properties":{"b":{"type":"array","uniqueItems":true,"items":{"type":"object","properties":{"y":{},"x":{}},"required":["x"]}},"a":{}},"required":["a"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, `"required":["a","b"]`) || !strings.Contains(got, `"required":["x","y"]`) || strings.Contains(got, "uniqueItems") {
		t.Fatal(got)
	}
}

func TestAnswerObjectInFence(t *testing.T) {
	out, err := answerObject("```json\n{\"claims\": []}\n```")
	if err != nil || out["claims"] == nil {
		t.Fatal(out, err)
	}
	if _, err := answerObject("no json here"); errorCode(err) != "extractor_no_json" {
		t.Fatal(err)
	}
}

// fakeCLI puts an executable script named name on a fresh PATH.
func fakeCLI(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCodexJSONReadsTheAnswerFile(t *testing.T) {
	dir := fakeCLI(t, "codex", `while [ $# -gt 0 ]; do [ "$1" = "-o" ] && out="$2"; shift; done; cat >/dev/null; printf '{"claims":[{"text":"x"}]}' > "$out"`+"\n")
	t.Setenv("ARKISM_CODEX_BIN", filepath.Join(dir, "codex"))
	capState = t.TempDir()
	out, err := codexJSON(context.Background(), "system", "user", "codex", `{"type":"object","properties":{"claims":{"type":"array"}}}`)
	if err != nil || len(out["claims"].([]any)) != 1 {
		t.Fatal(out, err)
	}
}

func TestGeminiJSONParsesTheResponse(t *testing.T) {
	dir := fakeCLI(t, "gemini", "cat >/dev/null; printf '%s' '{\"response\":\"```json\\n{\\\"claims\\\":[]}\\n```\"}'\n")
	t.Setenv("ARKISM_GEMINI_BIN", filepath.Join(dir, "gemini"))
	capState = t.TempDir()
	out, err := geminiJSON(context.Background(), "system", "user", "gemini", `{}`)
	if err != nil || out["claims"] == nil {
		t.Fatal(out, err)
	}
}

func TestRunExtractorCodes(t *testing.T) {
	capState = t.TempDir()
	if _, err := runExtractor(context.Background(), []string{filepath.Join(t.TempDir(), "nothing")}, ""); errorCode(err) != "extractor_missing" {
		t.Fatal(err)
	}
	dir := fakeCLI(t, "fail", "exit 3\n")
	if _, err := runExtractor(context.Background(), []string{filepath.Join(dir, "fail")}, ""); errorCode(err) != "extractor_exit_3" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	slow := fakeCLI(t, "slow", "sleep 2\n")
	if _, err := runExtractor(ctx, []string{filepath.Join(slow, "slow")}, ""); err != errTimeoutExpired {
		t.Fatal(err)
	}
}

func TestExtractorDefaultPicksTheSignedInCLI(t *testing.T) {
	home := t.TempDir()
	oldHome, oldClaude, oldModel := host.Home, host.ClaudeBin, capModel
	t.Cleanup(func() { host.Home, host.ClaudeBin, capModel = oldHome, oldClaude, oldModel })
	host.Home = home
	host.ClaudeBin = filepath.Join(home, "no-claude")
	for _, k := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	codex := fakeCLI(t, "codex", "exit 0\n")
	t.Setenv("ARKISM_CODEX_BIN", filepath.Join(codex, "codex"))
	t.Setenv("ARKISM_GEMINI_BIN", filepath.Join(home, "no-gemini"))
	if got := extractorDefault(); got != "haiku" { // codex installed but not signed in: nothing is ready
		t.Fatalf("no login: %q", got)
	}
	if extractorState("codex") != "login_needed" || extractorState("claude") != "missing" {
		t.Fatal(extractorState("codex"), extractorState("claude"))
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := extractorDefault(); got != "codex" {
		t.Fatalf("codex signed in: %q", got)
	}
	capModel = "codex"
	st := ExtractorStatus()
	if cli, _ := st.Get("cli"); cli != "codex" {
		t.Fatal(st)
	}
	if state, _ := st.Get("state"); state != "ready" {
		t.Fatal(st)
	}
}
