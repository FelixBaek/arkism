package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestTargetsNeverCarryArgumentsOrCredentials(t *testing.T) {
	for _, c := range []struct {
		d    map[string]any
		want string
	}{
		{map[string]any{"type": "stdio", "command": "/usr/bin/npx", "args": []any{"-y", "@github/mcp-server", "--token", "ghp_x"}}, "npx @github/mcp-server"},
		{map[string]any{"command": "/usr/bin/python3", "args": []any{"/srv/x/router.py"}}, "python3"},
		{map[string]any{"type": "http", "url": "https://user:secret@mcp.example.com:8443/mcp?key=abc"}, "https://mcp.example.com:8443"},
	} {
		if got := safeTarget(c.d); got != c.want {
			t.Fatalf("%v: %q", c.d, got)
		}
	}
}

func TestCodexAddArguments(t *testing.T) {
	got, _ := toCodexArgs("wake", map[string]any{"command": "wake-mcp", "args": []any{"--db", "x"}, "env": map[string]any{"A": "1"}})
	if strings.Join(got, " ") != "mcp add wake --env A=1 -- wake-mcp --db x" {
		t.Fatal(got)
	}
	got, _ = toCodexArgs("cf", map[string]any{"type": "http", "url": "https://mcp.cloudflare.com/mcp"})
	if strings.Join(got, " ") != "mcp add cf --url https://mcp.cloudflare.com/mcp" {
		t.Fatal(got)
	}
	if _, err := toCodexArgs("h", map[string]any{"type": "http", "url": "https://x", "headers": map[string]any{"Authorization": "Bearer y"}}); err == nil {
		t.Fatal("headers accepted")
	}
}

func fakeAgentRun(t *testing.T) *[][]string {
	t.Helper()
	calls := &[][]string{}
	old := agentRun
	agentRun = func(_ time.Duration, args ...string) (string, error) { *calls = append(*calls, args); return "", nil }
	t.Cleanup(func() { agentRun = old })
	return calls
}

func TestUnsupportedAgentsAndBadNamesFailWithoutRunningAnything(t *testing.T) {
	calls := fakeAgentRun(t)
	// An empty home: a real ~/.cursor on the machine running the tests must not turn the Cursor switch on.
	home := t.TempDir()
	h, st := agentsHome, agentsState
	agentsHome, agentsState = home, filepath.Join(home, "state")
	t.Cleanup(func() { agentsHome, agentsState = h, st })
	for _, req := range []map[string]any{
		{"agent": "cursor", "kind": "mcp", "name": "wake", "enabled": true},
		{"agent": "codex", "kind": "plugin", "name": "p", "enabled": true},
		{"agent": "claude_code", "kind": "mcp", "name": "a b; rm", "enabled": true},
	} {
		var rt runtimeError
		if err := applyRequest(req, map[string]any{}); err == nil || !asRuntime(err, &rt) {
			t.Fatal(req, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatal(*calls)
	}
}

func asRuntime(err error, rt *runtimeError) bool { r, ok := err.(runtimeError); *rt = r; return ok }

func TestFoundAgentsSkillCountsAndOwnerChanges(t *testing.T) {
	home := t.TempDir()
	h, st := agentsHome, agentsState
	agentsHome, agentsState = home, filepath.Join(home, "state")
	t.Cleanup(func() { agentsHome, agentsState = h, st })
	write(t, filepath.Join(home, ".cursor/skills/review/SKILL.md"), "x")
	rows := func() map[string]host.Obj {
		out := map[string]host.Obj{}
		for _, a := range agentsInventory(loadRegistry()) {
			out[str(objGet(a.(host.Obj), "id"))] = a.(host.Obj)
		}
		return out
	}
	r := rows()
	if objGet(r["cursor"], "found") != true || objGet(r["cursor"], "enabled") != false || objGet(r["cursor"], "skills") != 1 || objGet(r["codex"], "enabled") != true {
		t.Fatal(r["cursor"], r["codex"])
	}
	for _, req := range []map[string]any{
		{"agent": "cursor", "kind": "agent", "name": "Cursor", "enabled": true, "skills_dir": "~/.cursor/skills"},
		{"agent": "codex", "kind": "agent", "name": "Codex", "enabled": false},
		{"agent": "aider", "kind": "agent", "name": "Aider", "enabled": true, "skills_dir": "~/.aider/skills"},
		{"agent": "shared", "kind": "shared", "name": "~/team/skills", "enabled": true},
	} {
		if err := applyRequest(req, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	r = rows()
	if objGet(r["cursor"], "enabled") != true || objGet(r["codex"], "enabled") != false || objGet(r["aider"], "custom") != true || !isDir(filepath.Join(home, ".aider/skills")) {
		t.Fatal(r)
	}
	if got := loadRegistry().shared; strings.Join(got, ",") != "~/.agents/skills,~/team/skills" {
		t.Fatal(got)
	}
	if err := applyRequest(map[string]any{"agent": "Bad Name", "kind": "agent", "name": "x", "enabled": true}, map[string]any{}); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestAddsWithTheAgentsCLIAndRefusesDuplicates(t *testing.T) {
	calls := fakeAgentRun(t)
	cs, xs := claudeServers, codexServers
	claudeServers = func() map[string]any { return map[string]any{"github": map[string]any{}} }
	codexServers = func() map[string]any { return map[string]any{} }
	t.Cleanup(func() { claudeServers, codexServers = cs, xs })
	if err := applyRequest(map[string]any{"agent": "claude_code", "kind": "mcp_add", "name": "wake", "enabled": true, "command": []any{"wake-mcp", "--db", "x"}}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := applyRequest(map[string]any{"agent": "codex", "kind": "mcp_add", "name": "cf", "enabled": true, "url": "https://mcp.cloudflare.com/mcp"}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := applyRequest(map[string]any{"agent": "claude_code", "kind": "mcp_add", "name": "github", "enabled": true, "url": "https://x"}, map[string]any{}); err == nil {
		t.Fatal("duplicate accepted")
	}
	c := *calls
	if strings.Join(c[0][1:4], " ") != "mcp add-json wake" || !strings.Contains(c[0][4], `"args": ["--db", "x"]`) {
		t.Fatal(c[0])
	}
	if strings.Join(c[1][1:], " ") != "mcp add cf --url https://mcp.cloudflare.com/mcp" {
		t.Fatal(c[1])
	}
}

func TestCodexLoginMark(t *testing.T) {
	old := claudeNeedsLogin
	claudeNeedsLogin = func(string, map[string]any) bool { return false }
	t.Cleanup(func() { claudeNeedsLogin = old })
	e := serverEntry("cf", map[string]any{"type": "http", "url": "https://x"}, map[string]any{"cf": map[string]any{"type": "http"}}, map[string]any{"cf": map[string]any{"_auth": "not_logged_in"}})
	if canon(t, objGet(e, "login")) != `{"codex":"needed"}` || objGet(e, "note") != "로그인 필요" {
		t.Fatal(e)
	}
}

func TestLoginRunsInItsOwnUnitUnderSystemd(t *testing.T) {
	t.Setenv("INVOCATION_ID", "x")
	lp, sd, st := lookPath, startDetached, agentsState
	agentsState = t.TempDir()
	var argv []string
	lookPath = func(string) (string, error) { return "/usr/bin/systemd-run", nil }
	startDetached = func(a []string) error { argv = a; return nil }
	t.Cleanup(func() { lookPath, startDetached, agentsState = lp, sd, st })
	startLogin(map[string]any{"id": "r1", "agent": "codex", "name": "cloudflare-api"})
	if strings.Join(argv[:4], " ") != "systemd-run --quiet --collect --unit=arkism-login-r1" ||
		strings.Join(argv[len(argv)-5:], " ") != "agents login r1 codex cloudflare-api" || argv[len(argv)-6] != host.Executable() {
		t.Fatal(argv)
	}
}

func TestAKilledSignInStopsBlockingAfterTwentyMinutes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "login-r1")
	write(t, marker, "")
	if !loginBusy(marker) {
		t.Fatal("fresh marker not busy")
	}
	old := time.Now().Add(-(loginMaxSeconds + 1) * time.Second)
	os.Chtimes(marker, old, old)
	if loginBusy(marker) {
		t.Fatal("stale marker still busy")
	}
}

func TestErrorTextNeverCarriesTheRedirect(t *testing.T) {
	if got := lastLine("Paste the URL:\r\nhttp://127.0.0.1:5/callback?code=SECRETCODE&state=s1\r\n"); strings.Contains(got, "SECRETCODE") {
		t.Fatal(got)
	}
	if got := lastLine("error: invalid_client\n"); got != "error: invalid_client" {
		t.Fatal(got)
	}
}

func TestSyncAgentsApplyCountsResultsAndStopsOnAMalformedRequest(t *testing.T) {
	calls := fakeAgentRun(t)
	home := t.TempDir()
	h, st, cs, xs := agentsHome, agentsState, claudeServers, codexServers
	agentsHome, agentsState = home, filepath.Join(home, "state")
	claudeServers = func() map[string]any { return map[string]any{"github": map[string]any{}} }
	codexServers = func() map[string]any { return map[string]any{} }
	t.Cleanup(func() { agentsHome, agentsState, claudeServers, codexServers = h, st, cs, xs })
	doc := map[string]any{"requests": []any{
		map[string]any{"status": "done", "id": "old"},
		map[string]any{"status": "pending", "id": "1", "agent": "cursor", "kind": "agent", "name": "Cursor", "enabled": true, "skills_dir": "~/.cursor/skills"},
		map[string]any{"status": "pending", "id": "2", "agent": "codex", "kind": "plugin", "name": "p", "enabled": true},
		map[string]any{"status": "pending", "id": "3", "agent": "claude_code", "kind": "mcp_add", "name": "wake", "enabled": true, "command": []any{"wake-mcp"}},
		map[string]any{"status": "pending", "id": "4", "agent": "claude_code", "kind": "mcp_add", "name": "github", "enabled": true, "url": "https://x"},
	}}
	results, done, failed, err := syncAgentsApply(doc)
	if err != nil || done != 2 || failed != 2 || len(results) != 4 {
		t.Fatal(results, done, failed, err)
	}
	status := map[string]any{}
	for _, r := range results {
		status[str(objGet(r.(host.Obj), "id"))] = objGet(r.(host.Obj), "status")
	}
	if status["1"] != "done" || status["2"] != "failed" || status["3"] != "done" || status["4"] != "failed" || len(*calls) != 1 {
		t.Fatal(status, *calls)
	}
	if _, _, _, err := syncAgentsApply(map[string]any{"requests": []any{map[string]any{"status": "pending", "kind": "agent"}}}); err == nil || err.Error() != "'agent'" {
		t.Fatal(err)
	}
}

func TestInventoryLiveKeepsServersSwitchedOnForSomeAgent(t *testing.T) {
	defs := map[string]any{"claude": map[string]any{}, "codex-on": map[string]any{}, "codex-off": map[string]any{}, "gemini-on": map[string]any{}, "nobody": map[string]any{}}
	codex := map[string]any{"codex-on": map[string]any{}, "codex-off": map[string]any{"_enabled": false}}
	others := map[string]map[string]any{"gemini": {"gemini-on": map[string]any{}, "codex-off": map[string]any{"_enabled": false}}}
	live := inventoryLive(defs, map[string]any{"claude": map[string]any{}}, codex, others)
	if strings.Join(sortedKeys(live), ",") != "claude,codex-on,gemini-on" {
		t.Fatal(sortedKeys(live))
	}
}
