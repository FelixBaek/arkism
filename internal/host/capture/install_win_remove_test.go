package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Disconnecting takes out exactly what connecting put in: the user's own hooks, MCP servers and
// notes stay, files the installer created are removed, and every change is backed up.
func TestInstUninstallUndoesTheInstallAndKeepsEverythingElse(t *testing.T) {
	root := instInstallFixture(t)
	codex := filepath.Join(root, ".codex")
	os.MkdirAll(codex, 0o755)
	os.WriteFile(filepath.Join(codex, "config.toml"), []byte("[mcp_servers.other]\ncommand=\"keep\"\n"), 0o644)
	os.WriteFile(filepath.Join(codex, "hooks.json"), []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "keep hook"}]}]}}`), 0o644)
	os.WriteFile(filepath.Join(codex, "AGENTS.md"), []byte("# Mine\n\n## Other\nKeep this rule.\n"), 0o644)
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"model": "opus", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "mine"}]}]}}`), 0o644)
	os.WriteFile(filepath.Join(root, ".claude.json"), []byte(`{"mcpServers": {"mine": {"command": "x"}}}`), 0o644)
	instRun(t, root)

	summary, err := instUninstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local"))
	if err != nil {
		t.Fatal(err)
	}
	config := lfText(t, filepath.Join(codex, "config.toml"))
	if strings.Contains(config, "skill-router") || !strings.Contains(config, "[mcp_servers.other]\ncommand=\"keep\"") {
		t.Fatal(config)
	}
	hooks := instLoad(t, filepath.Join(codex, "hooks.json"))["hooks"].(map[string]any)
	if len(hooks) != 1 || !eq(hooks["Stop"].([]any)[0].(map[string]any)["hooks"], []any{map[string]any{"type": "command", "command": "keep hook"}}) {
		t.Fatal(hooks)
	}
	settings := instLoad(t, filepath.Join(root, ".claude", "settings.json"))
	if settings["model"] != "opus" || len(settings["hooks"].(map[string]any)) != 1 {
		t.Fatal(settings)
	}
	servers := instLoad(t, filepath.Join(root, ".claude.json"))["mcpServers"].(map[string]any)
	if len(servers) != 1 || servers["mine"] == nil {
		t.Fatal(servers)
	}
	guide := lfText(t, filepath.Join(codex, "AGENTS.md"))
	if guide != "# Mine\n\n## Other\nKeep this rule.\n" {
		t.Fatalf("%q", guide)
	}
	// Created by the installer only: gone again.
	for _, path := range []string{filepath.Join(root, ".zcode", "AGENTS.md"), filepath.Join(root, ".claude", "CLAUDE.md"), filepath.Join(root, ".config", "arkism", "hook-codex.cmd")} {
		if exists(path) {
			raw, _ := os.ReadFile(path)
			t.Fatalf("%s left: %q", path, raw)
		}
	}
	zcode := instLoad(t, filepath.Join(root, ".zcode", "cli", "config.json"))
	if s := zcode["mcp"].(map[string]any)["servers"].(map[string]any); s["arkism"] != nil {
		t.Fatal(zcode)
	}
	files, _ := summary.Get("files")
	backup, _ := summary.Get("backup")
	manifest := instLoad(t, filepath.Join(backup.(string), "manifest.json"))
	if len(files.([]any)) == 0 || len(manifest) != len(files.([]any)) {
		t.Fatal(files, manifest)
	}
	// Running it again changes nothing and leaves no empty backup folder.
	again, err := instUninstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local"))
	againFiles, _ := again.Get("files")
	againBackup, _ := again.Get("backup")
	if err != nil || len(againFiles.([]any)) != 0 || exists(againBackup.(string)) {
		t.Fatal(again, err)
	}
}
