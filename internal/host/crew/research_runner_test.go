package crew

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

const researchHelpFixture = `--safe-mode --restricted --tools --allowedTools --strict-mcp-config --mcp-config --setting-sources --settings --disable-slash-commands --no-chrome --no-session-persistence --permission-mode`

func researchTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(key, "")
	}
	patch(t, &host.AccountsChoose, func(string, string, string, string, string, []string) (map[string]any, string) { return nil, "" })
	patch(t, &researchCLIHelp, func(string, string, map[string]string) (string, error) { return researchHelpFixture, nil })
}

func TestResearchRestrictsToolsWithoutChangingModelOrEffort(t *testing.T) {
	researchTestEnv(t)
	t.Setenv("ARKISM_TOKEN", "inert-owner-token")
	t.Setenv("NODE_OPTIONS", "inert-preload")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "inert-oauth-token")
	calls := fakeSpawn(t, "report inert-oauth-token", "", 0)
	res, err := runNode(map[string]any{"research": true, "model": "claude-opus-5-5", "effort": "high", "task": "research", "access": "read"})
	if err != nil || res["ok"] != true || len(*calls) != 1 {
		t.Fatal(res, err, len(*calls))
	}
	c := (*calls)[0]
	for flag, value := range map[string]string{"--model": "claude-opus-5-5", "--effort": "high", "--tools": researchTools, "--allowedTools": researchTools,
		"--permission-mode": "dontAsk", "--mcp-config": `{"mcpServers":{}}`, "--setting-sources": "", "--settings": `{"disableAllHooks":true}`} {
		at := index(c.argv, flag)
		if at < 0 || at+1 >= len(c.argv) || c.argv[at+1] != value {
			t.Fatalf("%s did not retain its required value", flag)
		}
	}
	for _, flag := range []string{"--safe-mode", "--restricted", "--strict-mcp-config", "--disable-slash-commands", "--no-chrome", "--no-session-persistence"} {
		if index(c.argv, flag) < 0 {
			t.Fatal("missing", flag)
		}
	}
	if index(c.argv, "--max-turns") >= 0 || c.stdin != "research" || res["model"] != "claude-opus-5-5" || res["effort"] != "high" {
		t.Fatal("research changed execution choices", res)
	}
	if c.env["ARKISM_TOKEN"] != "" || c.env["NODE_OPTIONS"] != "" || c.env["CLAUDE_CODE_SAFE_MODE"] != "1" || c.cwd != filepath.Join(c.env["HOME"], "research") {
		t.Fatal("research inherited host privileges or workspace")
	}
	if res["output"] != "report ***" {
		t.Fatal("OAuth token was not redacted")
	}
	if _, err := os.Stat(c.env["HOME"]); !os.IsNotExist(err) {
		t.Fatal("temporary research profile was not removed")
	}
}

func TestResearchEnvironmentCopiesOnlyAuthAndPreservesExplicitReadFolder(t *testing.T) {
	source, cwd := t.TempDir(), t.TempDir()
	for file, body := range map[string]string{".credentials.json": `{"test":"inert"}`, "settings.json": `{"hooks":{"SessionStart":[]}}`, "mcp.json": "inert"} {
		if err := os.WriteFile(filepath.Join(source, file), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	env, gotCWD, cleanup, err := researchEnvironment(map[string]string{"CLAUDE_CONFIG_DIR": source, "PATH": "fixture", "ARKISM_RUNNER_TOKEN": "inert", "BASH_ENV": "hook", "ANTHROPIC_DEFAULT_OPUS_MODEL": "other-model", "OPENAI_BASE_URL": "https://another-cli.test"}, cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if gotCWD != cwd || env["PATH"] != "fixture" || env["ARKISM_RUNNER_TOKEN"] != "" || env["BASH_ENV"] != "" || env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "" || env["OPENAI_BASE_URL"] != "" {
		t.Fatal("research changed cwd or inherited unrelated configuration")
	}
	files, err := os.ReadDir(env["CLAUDE_CONFIG_DIR"])
	if err != nil || len(files) != 1 || files[0].Name() != ".credentials.json" {
		t.Fatal("research profile contains more than authentication", err)
	}
	if _, _, _, err := researchEnvironment(map[string]string{"ANTHROPIC_BASE_URL": "https://unsupported.test"}, cwd, true); err == nil {
		t.Fatal("unverified provider override was accepted")
	}
}

func TestResearchRefusesUnverifiedRunnersAndWriteAccessBeforeSpawn(t *testing.T) {
	researchTestEnv(t)
	for _, model := range []string{"gpt-6.1-sol", "gemini-3.8-flash", "glm-5", "grok-4"} {
		t.Run(model, func(t *testing.T) {
			res, calls, err := runWith(t, map[string]any{"research": true, "model": model, "access": "read"}, "invented report", "", 0)
			if res != nil || err == nil || !strings.Contains(err.Error(), "research_runner_unsupported") || len(calls) != 0 {
				t.Fatal(res, err, len(calls))
			}
		})
	}
	for _, access := range []string{"write", "full"} {
		_, calls, err := runWith(t, map[string]any{"research": true, "model": "opus", "access": access}, "", "", 0)
		if err == nil || !strings.Contains(err.Error(), "research_access_required") || len(calls) != 0 {
			t.Fatal(err, len(calls))
		}
	}
	if _, err := runNode(map[string]any{"research": true, "backend": "unverified", "task": "research"}); err == nil || !strings.Contains(err.Error(), "research_runner_unsupported") {
		t.Fatal("external research runner was not refused", err)
	}
}

func TestResearchRefusesPlainChatAPIWithoutCallingIt(t *testing.T) {
	keyFiles(t)
	fakeCustom(t, map[string][]string{"https://provider.test": {"research-custom"}}, map[string]string{"https://provider.test": "openai"})
	if _, err := addKey("custom", "Fixture", "inert-provider-key", "https://provider.test", nil, ""); err != nil {
		t.Fatal(err)
	}
	fakeProvider(t, "", func(*http.Request, []byte) { t.Fatal("research reached a tool-less chat API") })
	_, calls, err := runWith(t, map[string]any{"research": true, "model": "research-custom", "access": "read"}, "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "research_runner_unsupported") || len(calls) != 0 {
		t.Fatal(err, len(calls))
	}
}

func TestResearchCapabilityProbeFailsClosedAndOrdinaryRunsStayUnchanged(t *testing.T) {
	researchTestEnv(t)
	for _, missing := range strings.Fields(researchHelpFixture) {
		t.Run(missing, func(t *testing.T) {
			patch(t, &researchCLIHelp, func(string, string, map[string]string) (string, error) {
				return strings.ReplaceAll(researchHelpFixture, missing, ""), nil
			})
			_, calls, err := runWith(t, map[string]any{"research": true, "model": "opus", "access": "read"}, "", "", 0)
			if err == nil || !strings.Contains(err.Error(), "research_cli_outdated") || len(calls) != 0 {
				t.Fatal(err, len(calls))
			}
		})
	}
	patch(t, &researchCLIHelp, func(string, string, map[string]string) (string, error) { return "", errors.New("probe unavailable") })
	_, calls, err := runWith(t, map[string]any{"research": true, "model": "opus", "access": "read"}, "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "research_cli_unavailable") || len(calls) != 0 {
		t.Fatal(err, len(calls))
	}
	for _, access := range []string{"read", "write"} {
		res, calls, err := runWith(t, map[string]any{"model": "opus", "access": access, "effort": "max"}, "ordinary", "", 0)
		if err != nil || res["ok"] != true || len(calls) != 1 || index(calls[0].argv, "--restricted") >= 0 || index(calls[0].argv, "--effort") < 0 {
			t.Fatal("ordinary execution changed", res, err)
		}
	}
}
