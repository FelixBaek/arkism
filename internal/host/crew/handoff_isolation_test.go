package crew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandoffIsolationKeepsOnlySelectedAuthentication(t *testing.T) {
	for _, cli := range []string{"claude", "codex"} {
		t.Run(cli, func(t *testing.T) {
			profile := t.TempDir()
			auth := ".credentials.json"
			configKey := "CLAUDE_CONFIG_DIR"
			if cli == "codex" {
				auth = "auth.json"
				configKey = "CODEX_HOME"
			}
			if err := os.WriteFile(filepath.Join(profile, auth), []byte(`{"synthetic_auth":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(profile, "config.toml"), []byte("owner MCP settings"), 0600)
			original := map[string]string{"HOME": t.TempDir(), "PATH": "/bin", configKey: profile, "ARKISM_TOKEN_FILE": "/owner.token", "ARKISM_RUNNER_TOKEN_FILE": "/runner.token", "ARBITRARY_SECRET": "secret", "HTTP_PROXY": "http://proxy"}
			env, cleanup, err := isolatedHandoffEnv(original, cli)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			for _, key := range []string{"ARKISM_TOKEN_FILE", "ARKISM_RUNNER_TOKEN_FILE", "ARBITRARY_SECRET", "HTTP_PROXY"} {
				if env[key] != "" {
					t.Fatal("inherited authority", key)
				}
			}
			if env["HOME"] == original["HOME"] || env[configKey] == profile {
				t.Fatal("global profile reused")
			}
			if raw, err := os.ReadFile(filepath.Join(env[configKey], auth)); err != nil || string(raw) != `{"synthetic_auth":true}` {
				t.Fatal("authentication missing", err)
			}
			if _, err := os.Stat(filepath.Join(env[configKey], "config.toml")); !os.IsNotExist(err) {
				t.Fatal("settings inherited")
			}
			path := env["HOME"]
			cleanup()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("temporary credentials survived")
			}
		})
	}
}

func TestHandoffCodexRejectsMergedMCPConfiguration(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "server"}[configured], func(t *testing.T) {
			root := t.TempDir()
			cli := filepath.Join(root, "codex")
			output := "[]"
			if configured {
				output = `[{"name":"owner","enabled":true}]`
			}
			if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			env, cleanup, err := isolatedHandoffEnv(map[string]string{}, "codex")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			err = verifyHandoffCodex(cli, root, env)
			if (err != nil) != configured {
				t.Fatal("effective MCP configuration not enforced", err)
			}
			raw, _ := os.ReadFile(filepath.Join(env["CODEX_HOME"], "config.toml"))
			if !strings.Contains(string(raw), `trust_level = "untrusted"`) {
				t.Fatal("project trust missing")
			}
		})
	}
}

func TestHandoffDisablesInheritedToolsAndExtensions(t *testing.T) {
	claude := isolatedHandoffCommand([]string{"claude", "-p"}, "claude")
	if index(claude, "--strict-mcp-config") < 0 || index(claude, `{"mcpServers":{}}`) < 0 || index(claude, "--setting-sources") < 0 {
		t.Fatal(claude)
	}
	codex := strings.Join(isolatedHandoffCommand([]string{"codex", "exec", "-"}, "codex"), " ")
	for _, feature := range []string{"apps", "plugins", "hooks", "multi_agent", "remote_plugin", "skill_mcp_dependency_install"} {
		if !strings.Contains(codex, "features."+feature+"=false") {
			t.Fatal(codex)
		}
	}
}
