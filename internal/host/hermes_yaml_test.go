package host

import (
	"errors"
	"strings"
	"testing"
)

const hermesConfig = `# my settings
model:
  provider: anthropic  # keep me
mcp_servers:
  # the search one
  search:
    url: "https://mcp.example.com/sse"   # remote
    transport: sse
    headers:
      Authorization: Bearer x
  files:
    command: npx
    args:
    - -y
    - "@scope/files"
    env:
      ROOT: /tmp
    timeout: 120
    enabled: false
toolsets: [web]
`

func TestYAMLMCPServersReadsEntries(t *testing.T) {
	servers, err := YAMLMCPServers(hermesConfig)
	if err != nil {
		t.Fatal(err)
	}
	files := servers["files"].Def
	if files["type"] != "stdio" || files["command"] != "npx" || files["_enabled"] != false {
		t.Fatalf("files = %#v", files)
	}
	if args := files["args"].([]any); len(args) != 2 || args[1] != "@scope/files" {
		t.Fatalf("args = %#v", args)
	}
	if files["env"].(map[string]any)["ROOT"] != "/tmp" || files["timeout"] != nil {
		t.Fatalf("env/timeout = %#v", files)
	}
	search := servers["search"].Def
	if search["type"] != "sse" || search["url"] != "https://mcp.example.com/sse" || search["_enabled"] != true {
		t.Fatalf("search = %#v", search)
	}
	if !strings.HasPrefix(servers["search"].Raw, "search:\n  url:") {
		t.Fatalf("raw = %q", servers["search"].Raw)
	}
}

func TestYAMLMCPRoundTripKeepsOtherLines(t *testing.T) {
	entry := YAMLMCPEntry("arkism", map[string]any{"type": "stdio", "command": `C:\Users\me\arkism.exe`, "args": []any{"windows", "mcp", "hermes"}})
	out, err := YAMLMCPAdd(hermesConfig, "arkism", entry)
	if err != nil {
		t.Fatal(err)
	}
	servers, err := YAMLMCPServers(out)
	if err != nil || servers["arkism"].Def["command"] != `C:\Users\me\arkism.exe` || len(servers) != 3 {
		t.Fatalf("after add: %v %#v", err, servers["arkism"].Def)
	}
	if !strings.Contains(out, "toolsets: [web]\n") || !strings.Contains(out, "# keep me") {
		t.Fatalf("other lines changed:\n%s", out)
	}
	back, err := YAMLMCPRemove(out, "arkism")
	if err != nil || back != hermesConfig {
		t.Fatalf("remove did not restore the file (%v):\n%s", err, back)
	}
	// a removed entry put back as written restores the file exactly
	raw := servers["files"].Raw
	without, _ := YAMLMCPRemove(hermesConfig, "files")
	again, err := YAMLMCPAdd(without, "files", raw)
	if err != nil || again != hermesConfig {
		t.Fatalf("raw restore (%v):\n%s", err, again)
	}
}

func TestYAMLMCPAddCreatesAndEmpties(t *testing.T) {
	entry := YAMLMCPEntry("arkism", map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp"})
	for _, text := range []string{"", "model:\n  default: x", "mcp_servers_old: 1\n", "mcp_servers: {}\nmodel: {}\n", "mcp_servers:\nmodel: {}\n"} {
		out, err := YAMLMCPAdd(text, "arkism", entry)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		servers, err := YAMLMCPServers(out)
		if err != nil || servers["arkism"].Def["url"] != "http://127.0.0.1:1/mcp" {
			t.Fatalf("%q -> %q: %v", text, out, err)
		}
		gone, err := YAMLMCPRemove(out, "arkism")
		if err != nil || !strings.Contains(gone, "mcp_servers: {}\n") {
			t.Fatalf("%q emptied to %q (%v)", text, gone, err)
		}
	}
}

func TestYAMLMCPRefusesUnsafeLayouts(t *testing.T) {
	for _, text := range []string{
		"mcp_servers: {a: {command: x}}\n",
		"mcp_servers:\n  a: {command: x}\n",
		"mcp_servers:\n  - a\n",
		"mcp_servers:\n\ta:\n",
		"mcp_servers:\n  a: &base\n    command: x\n",
		"mcp_servers:\n  a:\n    <<: *base\n",
		"mcp_servers:\n  a:\n    command: x\nmcp_servers:\n  b:\n    command: y\n",
		"model: x\n---\nmcp_servers:\n  a:\n    command: x\n",
	} {
		if _, err := YAMLMCPAdd(text, "arkism", "arkism:\n  command: x\n"); !errors.Is(err, ErrYAMLLayout) {
			t.Fatalf("%q: want ErrYAMLLayout, got %v", text, err)
		}
	}
}
