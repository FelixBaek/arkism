package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
