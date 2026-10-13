package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlite "github.com/ncruces/go-sqlite3"
)

// OpenCode and Cursor get the Arkism MCP entry only where their folder exists; the user's own
// servers stay, a second install changes nothing, and disconnecting takes out exactly our entry.
func TestInstOpenCodeAndCursorGetTheMCPServerAndUninstallTakesOnlyItOut(t *testing.T) {
	root := instInstallFixture(t)
	instRun(t, root)
	if exists(filepath.Join(root, ".cursor")) || exists(filepath.Join(root, ".config", "opencode")) {
		t.Fatal("absent agents written")
	}
	oc := filepath.Join(root, ".config", "opencode")
	os.MkdirAll(oc, 0o755)
	ocBefore := `{"$schema": "https://opencode.ai/config.json", "model": "x/y", "mcp": {"mine": {"type": "local", "command": ["npx", "mine"], "enabled": false}}}`
	os.WriteFile(filepath.Join(oc, "opencode.json"), []byte(ocBefore), 0o644)
	os.MkdirAll(filepath.Join(root, ".cursor"), 0o755)
	os.WriteFile(filepath.Join(root, ".cursor", "mcp.json"), []byte(`{"mcpServers": {"mine": {"command": "x"}}}`), 0o644)
	instRun(t, root)
	instRun(t, root)

	client := filepath.Join(root, ".config", "arkism", instClient)
	ocData := instLoad(t, filepath.Join(oc, "opencode.json"))
	servers := ocData["mcp"].(map[string]any)
	if ocData["model"] != "x/y" || len(servers) != 2 || !eq(servers["mine"], map[string]any{"type": "local", "command": []any{"npx", "mine"}, "enabled": false}) ||
		!eq(servers["arkism"], map[string]any{"type": "local", "command": []any{client, "windows", "mcp", "opencode"}, "enabled": true}) {
		t.Fatal(ocData)
	}
	guide, _ := os.ReadFile(filepath.Join(oc, "AGENTS.md"))
	if strings.Count(string(guide), "## 지속 메모리") != 1 || !strings.Contains(string(guide), "`arkism` MCP") || !strings.Contains(string(guide), "새 대화를 시작할 때") {
		t.Fatal(string(guide))
	}
	cursor := instLoad(t, filepath.Join(root, ".cursor", "mcp.json"))["mcpServers"].(map[string]any)
	if len(cursor) != 2 || !eq(cursor["arkism"], map[string]any{"type": "stdio", "command": client, "args": []any{"windows", "mcp", "cursor"}}) {
		t.Fatal(cursor)
	}
	if exists(filepath.Join(root, ".cursor", "hooks.json")) {
		t.Fatal("cursor hooks written")
	}

	if _, err := instUninstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local")); err != nil {
		t.Fatal(err)
	}
	ocAfter := instLoad(t, filepath.Join(oc, "opencode.json"))
	want, _ := loadObjBytes([]byte(ocBefore))
	if !eq(ocAfter, want) {
		t.Fatal(ocAfter)
	}
	if c := instLoad(t, filepath.Join(root, ".cursor", "mcp.json"))["mcpServers"].(map[string]any); len(c) != 1 || c["mine"] == nil {
		t.Fatal(c)
	}
	if exists(filepath.Join(oc, "AGENTS.md")) {
		t.Fatal("opencode guide left")
	}
}

// A config with comments (OpenCode reads JSONC) is skipped, untouched, without failing the install.
func TestInstOpenCodeCommentedConfigIsSkippedNotRewritten(t *testing.T) {
	root := instInstallFixture(t)
	oc := filepath.Join(root, ".config", "opencode")
	os.MkdirAll(oc, 0o755)
	text := "{\n  // mine\n  \"model\": \"x\"\n}\n"
	os.WriteFile(filepath.Join(oc, "opencode.json"), []byte(text), 0o644)
	summary, err := instInstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local"), false)
	if err != nil {
		t.Fatal(err)
	}
	skipped, _ := summary.Get("skipped")
	if raw, _ := os.ReadFile(filepath.Join(oc, "opencode.json")); string(raw) != text || len(skipped.([]any)) != 1 {
		t.Fatal(string(raw), skipped)
	}
	if _, err := instUninstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local")); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(oc, "opencode.json")); string(raw) != text {
		t.Fatal(string(raw))
	}
}

// OpenCode's opencode.db has ZCode's session/message/part tables (ZCode is a fork); the usage list
// reads it with the same reader, and the root's glob finds the channel databases but not the WAL.
func TestUsageReadsOpenCodeDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		// Columns as in packages/core/src/session/sql.ts (OpenCode v1.19), trimmed to the ones that matter.
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT, slug TEXT NOT NULL, directory TEXT NOT NULL,
			title TEXT NOT NULL, version TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, time_archived INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`INSERT INTO session VALUES ('ses_1', 'p', NULL, 'a', '/home/u/site', 'Fix login', '1.19.0', 1, 1, NULL)`,
		`INSERT INTO message VALUES ('msg_1', 'ses_1', 1, 1, '{"role":"user","time":{"created":1791302936235},"agent":"build"}'),
			('msg_2', 'ses_1', 2, 2, '{"role":"assistant","time":{"created":1791302940000},"modelID":"claude-sonnet-5","providerID":"anthropic","tokens":{"input":100,"output":20,"reasoning":5,"cache":{"read":300,"write":10}}}')`,
		`INSERT INTO part VALUES ('prt_1', 'msg_2', 'ses_1', 2, 2, '{"type":"tool","tool":"bash","callID":"c"}')`,
	} {
		if err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	f, err := usageParse("opencode", "pc", path)
	if err != nil || len(f.Sessions) != 1 {
		t.Fatal(err, f.Sessions)
	}
	s := f.Sessions[0]
	if s.Sid != "ses_1" || s.Title != "Fix login" || s.Project != "site" || s.Messages != 1 || len(s.Tools) != 1 || s.Tools[0].K != "bash" ||
		len(s.Days) != 1 || s.Days[0].Models[0].Model != "claude-sonnet-5" || s.Days[0].Models[0].T != (usageTok{Input: 100, Output: 25, CacheRead: 300, CacheWrite: 10, Requests: 1}) {
		t.Fatalf("%+v %+v", s, s.Days)
	}
	for _, name := range []string{"opencode-dev.db", "opencode.db-wal", "opencode.db-shm"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0o644)
	}
	var glob string
	for _, r := range usageSessionRoots() {
		if r.origin == "opencode" {
			glob = r.glob
		}
	}
	if got := globAll(dir, glob); len(got) != 2 || filepath.Base(got[0]) != "opencode-dev.db" || filepath.Base(got[1]) != "opencode.db" {
		t.Fatal(glob, got)
	}
}
