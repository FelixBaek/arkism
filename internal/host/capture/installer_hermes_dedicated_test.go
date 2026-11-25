package capture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestReinstallKeepsHermesDedicatedClient(t *testing.T) {
	root := instInstallFixture(t)
	cfg := filepath.Join(host.HermesHome(root), "config.yaml")
	dedicated := filepath.Join(root, "independent", instClient)
	for _, dir := range []string{filepath.Dir(cfg), filepath.Dir(dedicated)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(dedicated, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	stanza := host.YAMLMCPEntry("arkism", map[string]any{"type": "stdio", "command": dedicated, "args": []any{"windows", "mcp", "hermes"}})
	input, err := host.YAMLMCPAdd("mcp_servers:\n  other:\n    url: https://example.invalid/mcp\n", "arkism", stanza)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, instText(input), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{dedicated, dedicated, filepath.Join(root, ".config", "arkism", instClient)} {
		if i == 2 {
			if err := os.Remove(dedicated); err != nil {
				t.Fatal(err)
			}
		}
		instRun(t, root)
		raw, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defs, err := host.YAMLMCPServers(string(raw))
		if err != nil || defs["arkism"].Def["command"] != want || defs["other"].Def["url"] != "https://example.invalid/mcp" {
			t.Fatalf("install %d: unexpected config", i)
		}
	}
}
