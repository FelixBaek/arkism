package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// OpenCode ("mcp", command as one list) and Cursor ("mcpServers") switch off and on like the other
// file-edited agents; a server added from another agent is written in each one's own format.
func TestOpenCodeAndCursorMCPSwitchAndAdd(t *testing.T) {
	home := t.TempDir()
	h, st, cs, xs := agentsHome, agentsState, claudeServers, codexServers
	agentsHome, agentsState = home, filepath.Join(home, "state")
	claudeServers = func() map[string]any { return map[string]any{} }
	codexServers = func() map[string]any { return map[string]any{} }
	t.Cleanup(func() { agentsHome, agentsState, claudeServers, codexServers = h, st, cs, xs })

	oc := filepath.Join(home, ".config", "opencode", "opencode.json")
	os.MkdirAll(filepath.Dir(oc), 0o755)
	ocText := "{\n  \"model\": \"x\",\n  \"mcp\": {\n    \"pen\": {\n      \"type\": \"local\",\n      \"command\": [\"pen\", \"--stdio\"],\n      \"environment\": {\"A\": \"1\"}\n    }\n  }\n}\n"
	os.WriteFile(oc, []byte(ocText), 0o644)
	cur := filepath.Join(home, ".cursor", "mcp.json")
	os.MkdirAll(filepath.Dir(cur), 0o755)
	os.WriteFile(cur, []byte("{\"mcpServers\": {}}\n"), 0o644)

	servers := fileAgentServers()
	pen := servers["opencode"]["pen"].(map[string]any)
	if pen["command"] != "pen" || !sameMCP(pen["args"], []any{"--stdio"}) || !sameMCP(pen["env"], map[string]any{"A": "1"}) || !enabledOf(pen) {
		t.Fatal(pen)
	}
	stash := map[string]any{}
	// On for Cursor: the definition comes from OpenCode, written in Cursor's shape.
	if err := switches["cursor"]("pen", true, stash); err != nil {
		t.Fatal(err)
	}
	if got := fileAgentServers()["cursor"]["pen"].(map[string]any); got["command"] != "pen" || got["type"] != "stdio" || !sameMCP(got["args"], []any{"--stdio"}) {
		t.Fatal(got)
	}
	// Off and on again for OpenCode restores the entry exactly as it was written.
	if err := switches["opencode"]("pen", false, stash); err != nil {
		t.Fatal(err)
	}
	if _, has := fileAgentServers()["opencode"]["pen"]; has {
		t.Fatal("still on")
	}
	if err := switches["opencode"]("pen", true, stash); err != nil {
		t.Fatal(err)
	}
	if got := fileAgentServers()["opencode"]["pen"]; !sameJSON(got, pen) {
		t.Fatal(got)
	}
	// Added to OpenCode from Arkism: local, one command list, enabled.
	if err := addServer(map[string]any{"agent": "opencode", "name": "web", "url": "https://example.com/mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := addServer(map[string]any{"agent": "opencode", "name": "tool", "command": []any{"tool", "serve"}}); err != nil {
		t.Fatal(err)
	}
	native := toOpenCode(map[string]any{"type": "stdio", "command": "tool", "args": []any{"serve"}})
	if got := fileAgentServers()["opencode"]; got["web"].(map[string]any)["url"] != "https://example.com/mcp" || got["tool"].(map[string]any)["command"] != "tool" ||
		len(native) != 3 || !sameMCP(native[1].Value, []any{"tool", "serve"}) {
		t.Fatal(got, native)
	}
	for _, id := range []string{"opencode", "cursor"} {
		if !containsStr(routerRuntimes, id) || !containsStr(pcOrigins, id) {
			t.Fatal(id)
		}
	}
}

func sameMCP(a, b any) bool { return reflect.DeepEqual(a, b) }

// Hermes Agent's config.yaml: a server switched off and on comes back exactly as written, one added
// from another agent is written as a YAML entry, and the rest of the file (comments too) stays.
func TestHermesMCPSwitchAndAdd(t *testing.T) {
	home := t.TempDir()
	h, st, cs, xs := agentsHome, agentsState, claudeServers, codexServers
	agentsHome, agentsState = home, filepath.Join(home, "state")
	claudeServers = func() map[string]any {
		return map[string]any{"pen": map[string]any{"type": "stdio", "command": "pen", "args": []any{"--stdio"}}}
	}
	codexServers = func() map[string]any { return map[string]any{} }
	t.Setenv("HERMES_HOME", "")
	t.Cleanup(func() { agentsHome, agentsState, claudeServers, codexServers = h, st, cs, xs })

	cfg := filepath.Join(host.HermesHome(home), "config.yaml")
	os.MkdirAll(filepath.Dir(cfg), 0o700)
	text := "model:\n  default: x  # mine\nmcp_servers:\n  web:\n    url: https://example.com/mcp\n    timeout: 30\n"
	os.WriteFile(cfg, []byte(text), 0o600)

	if got := fileAgentServers()["hermes"]["web"].(map[string]any); got["url"] != "https://example.com/mcp" || got["type"] != "http" {
		t.Fatal(got)
	}
	stash := map[string]any{}
	if err := switches["hermes"]("web", false, stash); err != nil {
		t.Fatal(err)
	}
	if err := switches["hermes"]("web", true, stash); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(cfg); string(raw) != text {
		t.Fatalf("not restored:\n%s", raw)
	}
	if err := switches["hermes"]("pen", true, stash); err != nil { // the definition comes from Claude Code
		t.Fatal(err)
	}
	if got := fileAgentServers()["hermes"]["pen"].(map[string]any); got["command"] != "pen" || !sameMCP(got["args"], []any{"--stdio"}) {
		t.Fatal(got)
	}
	if raw, _ := os.ReadFile(cfg); !strings.HasPrefix(string(raw), "model:\n  default: x  # mine\n") {
		t.Fatalf("other settings changed:\n%s", raw)
	}
	if !containsStr(routerRuntimes, "hermes") || !containsStr(pcOrigins, "hermes") || !containsStr(kitMCPAgents, "hermes") {
		t.Fatal("hermes missing from the runtime lists")
	}
	// a layout the YAML editor refuses leaves the file untouched
	os.WriteFile(cfg, []byte("mcp_servers: {web: {url: x}}\n"), 0o600)
	if err := switches["hermes"]("pen", true, stash); err == nil {
		t.Fatal("flow style was edited")
	}
}

// The agents table finds Hermes Agent, counts its category/name skills and shows its CLI version.
func TestHermesAgentRow(t *testing.T) {
	home := t.TempDir()
	h, st, run, lp := agentsHome, agentsState, agentRun, lookPath
	agentsHome, agentsState = home, filepath.Join(home, "state")
	calls := 0
	agentRun = func(_ time.Duration, args ...string) (string, error) {
		calls++
		return "Hermes Agent v0.19.0 (2026.7.20)\nInstall directory: /x\n", nil
	}
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { agentsHome, agentsState, agentRun, lookPath = h, st, run, lp })
	hh := strings.Replace(hermesDir, "~", home, 1)
	for _, d := range []string{"skills/email/himalaya", "skills/devops/deploy", "skills/solo"} {
		os.MkdirAll(filepath.Join(hh, d), 0o700)
		os.WriteFile(filepath.Join(hh, d, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o600)
	}
	bin := filepath.Join(home, ".local", "bin", "hermes")
	os.MkdirAll(filepath.Dir(bin), 0o700)
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700)
	var row host.Obj
	for i := 0; i < 2; i++ {
		for _, a := range agentsInventory(registry{added: host.Obj{}}) {
			if o := a.(host.Obj); objGet(o, "id") == "hermes" {
				row = o
			}
		}
	}
	if objGet(row, "found") != true || objGet(row, "enabled") != true || objGet(row, "skills") != 3 || objGet(row, "version") != "0.19.0" || calls != 1 {
		t.Fatal(row, calls)
	}
}
