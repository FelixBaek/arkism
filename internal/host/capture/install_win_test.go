package capture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

type instGoldenFile struct {
	Exe  string `json:"exe"`
	TOML []struct {
		In     string  `json:"in"`
		Origin string  `json:"origin"`
		Out    *string `json:"out"`
	} `json:"toml"`
	Guidance []struct {
		In     string `json:"in"`
		Server string `json:"server"`
		Out    string `json:"out"`
	} `json:"guidance"`
	Deepseek []struct {
		In  string  `json:"in"`
		Out *string `json:"out"`
	} `json:"deepseek"`
	HooksIn  string `json:"hooks_in"`
	HooksOut string `json:"hooks_out"`
}

func instNoSSHHost(t *testing.T) {
	t.Setenv("ARKISM_SSH_HOST", "")
	os.Unsetenv("ARKISM_SSH_HOST")
}

// Golden: the TOML, guidance, DeepSeek and hook edits match Python byte for byte.
func TestInstGolden(t *testing.T) {
	instNoSSHHost(t)
	var g instGoldenFile
	boundaryGolden(t, "inst_windows.json", &g)
	for _, c := range g.TOML {
		out, err := instCodexConfig(c.In, g.Exe, c.Origin)
		if c.Out == nil {
			if err == nil {
				t.Errorf("toml %q accepted:\n%s", c.In, out)
			}
		} else if err != nil || out != *c.Out {
			t.Errorf("toml %q (%s): %v\n%q\nwant\n%q", c.In, c.Origin, err, out, *c.Out)
		}
	}
	for _, c := range g.Guidance {
		if out := instGuidance(c.In, c.Server); out != c.Out {
			t.Errorf("guidance %q:\n%q\nwant\n%q", c.In, out, c.Out)
		}
	}
	for _, c := range g.Deepseek {
		out, err := instDeepseekPatch(c.In, g.Exe)
		if c.Out == nil {
			if err == nil {
				t.Errorf("deepseek %q accepted", c.In)
			}
		} else if err != nil || out != *c.Out {
			t.Errorf("deepseek %q: %v\n%q\nwant\n%q", c.In, err, out, *c.Out)
		}
	}
	v, _ := loadsOrdered([]byte(g.HooksIn))
	data := pyNorm(v).(host.Obj)
	hooks := instChild(data, "hooks")
	instRemoveHoncho(hooks)
	handler := host.Obj{{Key: "type", Value: "command"}, {Key: "command", Value: "new"}, {Key: "timeout", Value: 45}}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		instMergeHooks(&hooks, event, handler)
	}
	instMergeHooks(&hooks, "SessionEnd", nil)
	data.Set("hooks", hooks)
	if out := instDumps(data) + "\n"; out != g.HooksOut {
		t.Errorf("hooks:\n%s\nwant\n%s", out, g.HooksOut)
	}
}

func TestInstMergeHooksRecognisesTheInstalledBinary(t *testing.T) {
	hooks := host.Obj{{Key: "Stop", Value: []any{
		host.Obj{{Key: "hooks", Value: []any{host.Obj{{Key: "command", Value: `"C:/u/.config/arkism/ARKISM.EXE" windows hook claude_code`}}}}},
		host.Obj{{Key: "hooks", Value: []any{host.Obj{{Key: "command", Value: `C:\u\.config\arkism\arkism.exe`}, {Key: "args", Value: []any{"windows", "hook", "zcode"}}}}}},
		host.Obj{{Key: "hooks", Value: []any{host.Obj{{Key: "command", Value: "keep"}}}}},
	}}}
	instMergeHooks(&hooks, "Stop", nil)
	if got := host.MustDumps(hooks, host.DumpOpts{}); got != `{"Stop": [{"hooks": [{"command": "keep"}]}]}` {
		t.Fatal(got)
	}
}

func TestInstDeepseekProfilesPreserveOverlaysAndReinstallWithoutDuplicates(t *testing.T) {
	instNoSSHHost(t)
	root := t.TempDir()
	var patches []string
	for _, name := range []string{"web", "desktop", "headless"} {
		profile := filepath.Join(root, ".dsh", "profiles", name)
		os.MkdirAll(profile, 0o755)
		os.WriteFile(filepath.Join(profile, "cordis.yml"), []byte("[]"), 0o644)
		os.WriteFile(filepath.Join(profile, "package.json"), []byte("{}"), 0o644)
		p := filepath.Join(profile, "cordis.patch.yml")
		os.WriteFile(p, []byte("- id: custom\n  disabled: true\n"), 0o644)
		patches = append(patches, p)
	}
	save := func(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }
	read := func() []string {
		var out []string
		for _, p := range patches {
			raw, _ := os.ReadFile(p)
			out = append(out, string(raw))
		}
		return out
	}
	if err := instInstallDeepseek(save, root, "relay.exe"); err != nil {
		t.Fatal(err)
	}
	before := read()
	if err := instInstallDeepseek(save, root, "relay.exe"); err != nil {
		t.Fatal(err)
	}
	for i, content := range read() {
		if content != before[i] || !strings.HasPrefix(content, "- id: custom\n  disabled: true\n") ||
			strings.Count(content, `"id": "arkism-mcp"`) != 1 || !strings.Contains(content, `"command": "relay.exe", "args": ["windows", "mcp", "deepseek"]`) {
			t.Fatal(content)
		}
	}
	guide, _ := os.ReadFile(filepath.Join(root, ".dsh", "AGENTS.md"))
	if !strings.Contains(string(guide), "새 대화를 시작할 때") {
		t.Fatal(string(guide))
	}
	for _, text := range []string{"# BEGIN ARKISM DEEPSEEK\n", "- id: arkism-mcp\n"} {
		if _, err := instDeepseekPatch(text, "relay"); err == nil {
			t.Fatal(text)
		}
	}
}

func TestInstGuidancePreservesSameLevelAndParentSections(t *testing.T) {
	for _, title := range []string{"스킬 사용", "지속 메모리"} {
		for _, heading := range []string{"# 보안 지침", "## Other", "##\tOther", "  # Parent", "##"} {
			preserved := heading + "\nKeep this rule.\n"
			before := "# Keep\n\n## " + title + "\nOld rule.\n### Old subsection\nOld details.\n\n" + preserved
			after := instGuidance(before, "skill-router")
			if !strings.Contains(after, preserved) || strings.Contains(after, "Old rule.") || strings.Contains(after, "Old details.") ||
				instGuidance(after, "skill-router") != after {
				t.Fatal(title, heading, after)
			}
		}
	}
}

func TestInstCodexQuotedTablesAreUpdatedAndUnhandledLayoutsFail(t *testing.T) {
	for _, q := range []string{`"%s"`, `'%s'`, `%s`} {
		f := func(s string) string { return strings.Replace(q, "%s", s, 1) }
		before := `  [ "mcp_servers" . ` + f("skill-router") + " ]\ncommand=\"old\"\nargs=[]\n\n[mcp_servers . " + f("honcho") + "]\ncommand=\"legacy\"\n  " + f("enabled") + "=true"
		out, err := instCodexConfig(before, "C:/arkism.exe", "codex")
		if err != nil || !strings.Contains(out, "[mcp_servers.skill-router]\ncommand = \"C:/arkism.exe\"\nargs = [\"windows\", \"mcp\", \"codex\"]\nenabled = true\n") ||
			!strings.Contains(out, "enabled = false\ncommand=\"legacy\"") || strings.Contains(out, "=true") {
			t.Fatal(out, err)
		}
	}
	if _, err := instCodexConfig("[mcp_servers]\nskill-router={command=\"old\",args=[]}\n", "x", "codex"); err != errInstTOML {
		t.Fatal(err)
	}
	if out, _ := instCodexConfig("[mcp_servers.honcho]", "x", "codex"); !strings.Contains(out, "[mcp_servers.honcho]\nenabled = false\n") {
		t.Fatal(out)
	}
}

func instInstallFixture(t *testing.T) string {
	instNoSSHHost(t)
	root := t.TempDir()
	return root
}

func instRun(t *testing.T, root string) {
	if _, err := instInstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local"), false); err != nil {
		t.Fatal(err)
	}
}

func instLoad(t *testing.T, path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := loadObjBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstInstallIsIdempotentPreservesUnrelatedSettingsAndBacksUpEveryChange(t *testing.T) {
	root := instInstallFixture(t)
	codex := filepath.Join(root, ".codex")
	os.Mkdir(codex, 0o755)
	before := "[mcp_servers.skill-router]\ncommand=\"old\"\nargs=[\"old.py\"]\n\n[mcp_servers.honcho]\nurl=\"https://old.example/mcp\"\n\n[mcp_servers.other]\ncommand=\"keep\"\nargs=[\"[literal]\"]\n"
	os.WriteFile(filepath.Join(codex, "config.toml"), []byte(before), 0o644)
	os.WriteFile(filepath.Join(codex, "hooks.json"), []byte(`{"hooks": {"Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "honcho writeback"}, {"type": "command", "command": "keep hook"}]}],`+
		`"SessionStart": [{"hooks": [{"type": "command", "command": "python arkism_contract.py"}]}]}}`), 0o644)
	os.WriteFile(filepath.Join(codex, "AGENTS.md"), []byte("# Keep\n\n## Other\nKeep this rule.\n\n## 스킬 사용\nOld rule.\n"), 0o644)
	zcode := filepath.Join(root, ".zcode", "cli", "config.json")
	os.MkdirAll(filepath.Dir(zcode), 0o755)
	os.WriteFile(zcode, []byte(`{"hooks": {"events": {"SessionEnd": [{"hooks": [{"command": "python", "args": ["arkism_windows.py", "hook", "zcode"]}]}], "Stop": [{"hooks": [{"command": "keep"}]}]}}}`), 0o644)
	instRun(t, root)
	instRun(t, root)

	zevents := instLoad(t, zcode)["hooks"].(map[string]any)["events"].(map[string]any)
	if len(zevents) != 3 || zevents["SessionEnd"] != nil || !eq(zevents["Stop"].([]any)[0].(map[string]any)["hooks"], []any{map[string]any{"command": "keep"}}) {
		t.Fatal(zevents)
	}
	config := lfText(t, filepath.Join(codex, "config.toml"))
	if !strings.Contains(config, "[mcp_servers.other]\ncommand=\"keep\"\nargs=[\"[literal]\"]\n") ||
		!strings.Contains(config, "[mcp_servers.honcho]\nenabled = false\n") || strings.Count(config, "[mcp_servers.skill-router]") != 1 {
		t.Fatal(config)
	}
	updated := instLoad(t, filepath.Join(codex, "hooks.json"))["hooks"].(map[string]any)
	stop := updated["Stop"].([]any)
	start := updated["SessionStart"].([]any)
	if !eq(stop[0].(map[string]any)["hooks"], []any{map[string]any{"type": "command", "command": "keep hook"}}) || len(stop) != 2 || len(start) != 2 ||
		start[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"] != "python arkism_contract.py" ||
		len(updated["UserPromptSubmit"].([]any)) != 1 || len(updated["SessionEnd"].([]any)) != 1 {
		t.Fatal(updated)
	}
	folder := filepath.Join(root, ".config", "arkism")
	hook := `"` + filepath.Join(folder, instClient) + `" windows hook codex`
	if runtime.GOOS == "windows" {
		cmd, _ := os.ReadFile(filepath.Join(folder, "hook-codex.cmd"))
		if !strings.Contains(string(cmd), hook) {
			t.Fatal(string(cmd))
		}
	} else {
		ours := updated["SessionEnd"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]
		if ours != hook || exists(filepath.Join(folder, "hook-codex.cmd")) {
			t.Fatal(ours)
		}
		if fi, err := os.Stat(filepath.Join(folder, instClient)); err != nil || fi.Mode()&0o100 == 0 {
			t.Fatal("installed client is not executable", err)
		}
	}
	if !isFile(filepath.Join(folder, instClient)) {
		t.Fatal("client not installed")
	}
	manifests, _ := filepath.Glob(filepath.Join(folder, "backups", "*", "manifest.json"))
	backedUp := false
	for _, m := range manifests {
		if saved, ok := instLoad(t, m)[filepath.Join(codex, "config.toml")].(string); ok {
			raw, _ := os.ReadFile(saved)
			backedUp = backedUp || string(raw) == before
		}
	}
	if len(manifests) != 2 || !backedUp {
		t.Fatal(manifests, backedUp)
	}
	guide, _ := os.ReadFile(filepath.Join(codex, "AGENTS.md"))
	zguide, _ := os.ReadFile(filepath.Join(root, ".zcode", "AGENTS.md"))
	if !strings.Contains(string(guide), "Keep this rule.") || strings.Contains(string(guide), "Old rule.") || strings.Count(string(guide), "## 스킬 사용") != 1 ||
		!strings.Contains(string(guide), "`skill-router` MCP") || !strings.Contains(string(zguide), "`arkism` MCP") {
		t.Fatal(string(guide))
	}
	claude := instLoad(t, filepath.Join(root, ".claude.json"))["mcpServers"].(map[string]any)["skill-router"]
	if !eq(claude, map[string]any{"command": filepath.Join(folder, instClient), "args": []any{"windows", "mcp", "claude_code"}}) {
		t.Fatal(claude)
	}
	if exists(filepath.Join(root, ".agents", "skills")) {
		t.Fatal("skills folder written")
	}
}

func TestInstInstallPointsInstalledGeminiAndGrokAtTheHubAndSkipsAbsentOnes(t *testing.T) {
	root := instInstallFixture(t)
	instRun(t, root)
	if exists(filepath.Join(root, ".gemini")) || exists(filepath.Join(root, ".grok")) {
		t.Fatal("absent clients written")
	}
	os.Mkdir(filepath.Join(root, ".gemini"), 0o755)
	old := `{"mcpServers": {"skill-router": {"command": "py", "args": ["C:/Users/x/.skill-router/skill_router.py"]}, "pencil": {"command": "pen"}},` +
		` "hooks": {"AfterAgent": [{"hooks": [{"type": "command", "command": "keep"}]}]}}`
	os.WriteFile(filepath.Join(root, ".gemini", "settings.json"), []byte(old), 0o644)
	os.Mkdir(filepath.Join(root, ".grok"), 0o755)
	os.WriteFile(filepath.Join(root, ".grok", "config.toml"), []byte("[cli]\ninstaller = \"npm\"\n\n[[marketplace.sources]]\nname = \"xAI Official\"\n"), 0o644)
	instRun(t, root)
	instRun(t, root)
	gemini := instLoad(t, filepath.Join(root, ".gemini", "settings.json"))
	oldData, _ := loadObjBytes([]byte(old))
	servers := gemini["mcpServers"].(map[string]any)
	if !eq(servers["skill-router"].(map[string]any)["args"], []any{"windows", "mcp", "gemini"}) || !eq(servers["pencil"], map[string]any{"command": "pen"}) ||
		!eq(gemini["hooks"], oldData["hooks"]) {
		t.Fatal(gemini)
	}
	grok := lfText(t, filepath.Join(root, ".grok", "config.toml"))
	if !strings.HasPrefix(grok, "[cli]\ninstaller = \"npm\"\n\n[[marketplace.sources]]\nname = \"xAI Official\"\n") ||
		strings.Count(grok, `args = ["windows", "mcp", "grok"]`) != 1 {
		t.Fatal(grok)
	}
	guide, _ := os.ReadFile(filepath.Join(root, ".gemini", "GEMINI.md"))
	if !strings.Contains(string(guide), "`skill-router` MCP") {
		t.Fatal(string(guide))
	}
}

func TestInstRefusedTOMLWritesNothing(t *testing.T) {
	root := instInstallFixture(t)
	os.Mkdir(filepath.Join(root, ".codex"), 0o755)
	os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("[mcp_servers]\nskill-router={command=\"old\"}\n"), 0o644)
	if _, err := instInstall(root, filepath.Join(root, "roaming"), filepath.Join(root, "local"), false); err != errInstTOML {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, ".claude")) || exists(filepath.Join(root, ".config", "arkism", instClient)) {
		t.Fatal("wrote after refusal")
	}
}

func TestInstRemoveAsideDeletesOnlyRenamedCopies(t *testing.T) {
	dir := t.TempDir()
	client := filepath.Join(dir, "arkism.exe")
	for _, name := range []string{"arkism.exe", "arkism.exe.old", "arkism.exe.old-1", "arkism.exe.old-2", "other.old-1"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	instRemoveAside(client)
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 2 || !isFile(client) || !isFile(filepath.Join(dir, "other.old-1")) {
		t.Fatal(left)
	}
}

// lfText reads a file the installer wrote; instText writes CRLF on Windows (Path.write_text), so it is read back as LF.
func lfText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}
