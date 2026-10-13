package capture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestInstallAddsTheGatewayTokenToClaudeSettingsThatUseTheGateway(t *testing.T) {
	root := instInstallFixture(t)
	t.Setenv("ARKISM_GATEWAY_LISTEN", "127.0.0.1:8099")
	settings := filepath.Join(root, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	os.WriteFile(settings, []byte(`{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:8099", "ENABLE_TOOL_SEARCH": "true",`+
		`"ANTHROPIC_CUSTOM_HEADERS": "X-Other: keep\nx-arkism-gateway-token: stale"}, "theme": "dark"}`), 0o644)
	instRun(t, root)
	instRun(t, root) // idempotent

	token, err := host.ReadGatewayToken(filepath.Join(root, ".config", "arkism", "gateway.token"))
	if err != nil {
		t.Fatal(err)
	}
	data := instLoad(t, settings)
	env := data["env"].(map[string]any)
	if env["ANTHROPIC_CUSTOM_HEADERS"] != host.GatewayTokenHeader+": "+token+"\nX-Other: keep" {
		t.Fatalf("%q", env["ANTHROPIC_CUSTOM_HEADERS"])
	}
	if env["CLAUDE_CODE_GATEWAY_HINT_HEADERS"] != "1" || env["ENABLE_TOOL_SEARCH"] != "true" || env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8099" ||
		data["theme"] != "dark" || data["hooks"] == nil {
		t.Fatal(data)
	}
	// Claude Code splits the value on newlines and each line at the first colon.
	found := false
	for _, line := range strings.Split(env["ANTHROPIC_CUSTOM_HEADERS"].(string), "\n") {
		name, value, _ := strings.Cut(line, ":")
		found = found || strings.TrimSpace(name) == host.GatewayTokenHeader && strings.TrimSpace(value) == token
	}
	if !found {
		t.Fatal("header not parseable")
	}
}

func TestInstallKeepsAHintHeaderChoiceAndLeavesOtherSettingsAlone(t *testing.T) {
	root := instInstallFixture(t)
	t.Setenv("ARKISM_GATEWAY_LISTEN", "127.0.0.1:8099")
	settings := filepath.Join(root, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	os.WriteFile(settings, []byte(`{"env": {"ANTHROPIC_BASE_URL": "http://localhost:8099/", "CLAUDE_CODE_GATEWAY_HINT_HEADERS": "0"}}`), 0o644)
	instRun(t, root)
	env := instLoad(t, settings)["env"].(map[string]any)
	if env["CLAUDE_CODE_GATEWAY_HINT_HEADERS"] != "0" || !strings.HasPrefix(env["ANTHROPIC_CUSTOM_HEADERS"].(string), host.GatewayTokenHeader+": ") {
		t.Fatal(env)
	}
	if info, err := os.Stat(filepath.Join(root, ".config", "arkism", "gateway.token")); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal(err)
	}

	for _, base := range []string{"", "https://api.anthropic.com", "http://127.0.0.1:9999", "http://10.0.0.5:8099", "https://127.0.0.1:8099"} {
		other := instInstallFixture(t)
		path := filepath.Join(other, ".claude", "settings.json")
		os.MkdirAll(filepath.Dir(path), 0o755)
		body := `{"env": {"KEEP": "1"}}`
		if base != "" {
			body = `{"env": {"ANTHROPIC_BASE_URL": "` + base + `"}}`
		}
		os.WriteFile(path, []byte(body), 0o644)
		instRun(t, other)
		env := instLoad(t, path)["env"].(map[string]any)
		if _, set := env["ANTHROPIC_CUSTOM_HEADERS"]; set || env["CLAUDE_CODE_GATEWAY_HINT_HEADERS"] != nil {
			t.Fatal(base, env)
		}
		if exists(filepath.Join(other, ".config", "arkism", "gateway.token")) {
			t.Fatal(base, "token made for settings that do not use the gateway")
		}
	}
}
