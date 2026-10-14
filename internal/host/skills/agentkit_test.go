package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// kitEnv points every path the agent kit reads at a temp home with no agent config.
func kitEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldHome, oldState, oldHostState, oldMCP, oldPlugins := agentsHome, agentsState, host.AgentsState, host.McpRoot, host.PluginsRoot
	oldClaude, oldCodex, oldRun := claudeServers, codexServers, agentRun
	agentsHome, agentsState, host.AgentsState = home, filepath.Join(home, "state"), filepath.Join(home, "state")
	host.McpRoot, host.PluginsRoot = filepath.Join(home, "kit", "mcp"), filepath.Join(home, "kit", "plugins")
	claudeServers = func() map[string]any { return map[string]any{} }
	codexServers = func() map[string]any { return map[string]any{} }
	agentRun = func(time.Duration, ...string) (string, error) { t.Fatal("unexpected agent CLI call"); return "", nil }
	t.Cleanup(func() {
		agentsHome, agentsState, host.AgentsState, host.McpRoot, host.PluginsRoot = oldHome, oldState, oldHostState, oldMCP, oldPlugins
		claudeServers, codexServers, agentRun = oldClaude, oldCodex, oldRun
	})
	return home
}

func kitFixture(t *testing.T, home string) string {
	plugin := filepath.Join(host.PluginsRoot, "demo")
	write(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), `{"name": "demo", "description": "PDF 도구 묶음"}`)
	write(t, filepath.Join(plugin, "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: d\n---\nbody")
	write(t, filepath.Join(plugin, ".mcp.json"), `{"mcpServers": {"demo-pdf": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/pdf", "args": ["--stdio"]}}}`)
	write(t, filepath.Join(plugin, "hooks", "hooks.json"), `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/start.sh"}]}]}}`)
	write(t, filepath.Join(host.McpRoot, "files", kitManifest), `{"name": "files", "command": "npx", "args": ["files-mcp"], "env": {"ROOT": "/data"}, "description": "file search"}`)
	for _, d := range []string{".claude", ".codex", ".gemini"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	return plugin
}

func kitRows(rows []any) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		o := r.(host.Obj)
		out[str(objGet(o, "kind"))+" "+str(objGet(o, "name"))+" "+str(objGet(o, "agent"))] = str(objGet(o, "state"))
	}
	return out
}

func TestAgentKitPlanAndApply(t *testing.T) {
	home := kitEnv(t)
	plugin := kitFixture(t, home)
	want := map[string]string{
		"skills demo all":       "via_hub",
		"mcp files claude_code": "planned", "mcp files codex": "planned", "mcp files gemini": "planned",
		"mcp demo-pdf claude_code": "planned", "mcp demo-pdf codex": "planned", "mcp demo-pdf gemini": "planned",
		"hooks demo claude_code": "planned", "hooks demo codex": "planned",
	}
	got := kitRows(kitRun(false))
	if !sameJSON(got, want) {
		t.Fatal(got)
	}
	if exists(filepath.Join(home, ".gemini", "settings.json")) || exists(filepath.Join(home, ".claude", "settings.json")) {
		t.Fatal("dry run wrote a file")
	}
	// The plugin's skills reach every agent through the hub sync.
	if roots := syncRoots(); !strings.Contains(host.MustDumps(roots[1].root, host.DumpOpts{}), "plugins") || len(discover(roots[1:2])) != 1 {
		t.Fatal("plugin skills are not a sync root")
	}

	var calls []string
	agentRun = func(_ time.Duration, argv ...string) (string, error) {
		calls = append(calls, strings.Join(argv[1:], " "))
		return "", nil
	}
	got = kitRows(kitRun(true))
	for k, v := range want {
		if v == "planned" {
			want[k] = "applied"
		}
	}
	if !sameJSON(got, want) {
		t.Fatal(got)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "mcp add-json demo-pdf") || !strings.Contains(joined, filepath.Join(plugin, "bin", "pdf")) ||
		!strings.Contains(joined, "mcp add files --env ROOT=/data -- npx files-mcp") {
		t.Fatal(joined)
	}
	gemini, _ := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if !strings.Contains(string(gemini), `"demo-pdf"`) || !strings.Contains(string(gemini), `"files"`) {
		t.Fatal(string(gemini))
	}
	for _, f := range []string{".claude/settings.json", ".codex/hooks.json"} {
		raw, _ := os.ReadFile(filepath.Join(home, f))
		if !strings.Contains(string(raw), filepath.Join(plugin, "hooks", "start.sh")) {
			t.Fatal(f, string(raw))
		}
	}
	if exists(filepath.Join(home, ".gemini", "hooks.json")) || strings.Contains(string(gemini), "hooks") {
		t.Fatal("hooks went to Gemini")
	}
	// A second run finds everything in place and changes nothing.
	agentRun = func(time.Duration, ...string) (string, error) { t.Fatal("rerun called a CLI"); return "", nil }
	claudeServers = func() map[string]any { return map[string]any{"files": map[string]any{}, "demo-pdf": map[string]any{}} }
	write(t, filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.files]\ncommand = \"npx\"\n\n[mcp_servers.demo-pdf]\ncommand = \"x\"\n")
	for k, v := range kitRows(kitRun(true)) {
		if v != "exists" && v != "via_hub" {
			t.Fatal(k, v)
		}
	}
}

func TestRouteSkillsOffersMCPAndPluginsWithKindAndState(t *testing.T) {
	swapRouter(t)
	home := agentsHome
	kitFixture(t, home)
	// files is switched off in Gemini (stashed) and live nowhere else; the plugin's server is not installed.
	saveStash(map[string]any{"gemini": map[string]any{"files": map[string]any{"command": "npx"}}})
	calls := fakeRequest(t, `{"selected_id":"mcp-files","provider":"clef","judgments":[{"id":"mcp-files","suitability":3},{"id":"plugin-demo","suitability":1}],"degraded":[]}`)
	out, err := routeSkills("search a file")
	if err != nil {
		t.Fatal(err)
	}
	extras := asList((*calls)[0].payload.(map[string]any)["extra_candidates"])
	if len(extras) != 1 || asMap(extras[0])["kind"] != "mcp" || asMap(extras[0])["id"] != "mcp-files" {
		t.Fatal(extras)
	}
	got := asList(field(out, "skills"))[0]
	if field(got, "kind") != "mcp" || field(got, "state") != "disabled" || !strings.Contains(str(field(got, "note")), "next agent session") {
		t.Fatal(got)
	}

	fakeRequest(t, `{"selected_id":"","provider":"rules","judgments":[{"id":"plugin-demo","suitability":1},{"id":"skill-v1","suitability":0.5}],"degraded":[]}`, versionReply)
	out, err = routeSkills("pdf 만들기")
	if err != nil {
		t.Fatal(err)
	}
	skills := asList(field(out, "skills"))
	if len(skills) != 2 || field(skills[0], "kind") != "plugin" || field(skills[0], "state") != "not_installed" ||
		field(skills[1], "kind") != "skill" || field(skills[1], "state") != "enabled" {
		t.Fatal(out)
	}

	// enable_tool restores the stashed entry through the 연결 › MCP switch.
	if _, err := enableTool("mcp", "files"); err != nil {
		t.Fatal(err)
	}
	gemini, _ := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if !strings.Contains(string(gemini), `"files"`) {
		t.Fatal(string(gemini))
	}
	if _, err := enableTool("mcp", "files"); err == nil {
		t.Fatal("enabled twice")
	}
}
