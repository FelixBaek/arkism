package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// agentsArkism is a fake Arkism for `agents sync`: it hands out requests and keeps the reports.
type agentsArkism struct {
	mu       sync.Mutex
	requests []any
	reports  []map[string]any
	logins   map[string][]map[string]any
}

func (a *agentsArkism) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var body map[string]any
	if r.Method == "POST" {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	switch {
	case r.URL.Path == "/v1/agent_tools" && r.Method == "GET":
		json.NewEncoder(w).Encode(map[string]any{"agent_tools": map[string]any{"requests": a.requests}})
	case r.URL.Path == "/v1/agent_tools":
		a.reports = append(a.reports, body)
		w.Write([]byte(`{}`))
	case strings.HasPrefix(r.URL.Path, "/v1/agent_tools/requests/"):
		id := strings.Split(r.URL.Path, "/")[4]
		a.logins[id] = append(a.logins[id], body)
		w.Write([]byte(`{}`))
	case r.URL.Path == "/v1/code_reviews":
		w.Write([]byte(`{"code_reviews":{}}`))
	default:
		http.NotFound(w, r)
	}
}

// isolateAgents points every path the agents sync reads or writes into a temp home and a fake Arkism.
func isolateAgents(t *testing.T) (*agentsArkism, string, *[][]string) {
	t.Helper()
	home := t.TempDir()
	state := filepath.Join(home, "state")
	a := &agentsArkism{logins: map[string][]map[string]any{}}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	tok := filepath.Join(home, "owner.token")
	os.WriteFile(tok, []byte("t"), 0o600)

	saved := []any{agentsHome, agentsState, claudeJSON, claudeCredentials, tiState, toolsCache, updatesCache, claudeProjects,
		pcState, pcLinkDir, checksCache, reviewsCache, reviewSettings, reviewOrg,
		host.SkillsRoot, host.CaptureState, host.AgentsState, host.BaseURL, host.TokenFile}
	run, cs, xs, sd, lp, uc, ic := agentRun, claudeServers, codexServers, startDetached, lookPath, host.UtilityCounts, host.InjectedChars
	t.Cleanup(func() {
		agentsHome, agentsState, claudeJSON, claudeCredentials = saved[0].(string), saved[1].(string), saved[2].(string), saved[3].(string)
		tiState, toolsCache, updatesCache, claudeProjects = saved[4].(string), saved[5].(string), saved[6].(string), saved[7].(string)
		pcState, pcLinkDir, checksCache, reviewsCache = saved[8].(string), saved[9].(string), saved[10].(string), saved[11].(string)
		reviewSettings, reviewOrg = saved[12].(string), saved[13].(string)
		host.SkillsRoot, host.CaptureState, host.AgentsState = saved[14].(string), saved[15].(string), saved[16].(string)
		host.BaseURL, host.TokenFile = saved[17].(string), saved[18].(string)
		agentRun, claudeServers, codexServers, startDetached, lookPath, host.UtilityCounts, host.InjectedChars = run, cs, xs, sd, lp, uc, ic
	})
	agentsHome, agentsState = home, state
	claudeJSON, claudeCredentials = filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", ".credentials.json")
	tiState, toolsCache, updatesCache = state, filepath.Join(state, "mcp-tools.json"), filepath.Join(state, "skill-updates.json")
	claudeProjects = filepath.Join(home, ".claude", "projects")
	pcState = filepath.Join(home, ".local", "state")
	pcLinkDir = filepath.Join(pcState, "arkism-pc-link")
	checksCache = filepath.Join(state, "skill-checks.json")
	reviewsCache, reviewSettings, reviewOrg = filepath.Join(state, "code-reviews.json"), filepath.Join(state, "review-settings.json"), ""
	t.Setenv("ARKISM_REVIEW_REPOS", "")
	host.SkillsRoot, host.CaptureState, host.AgentsState = filepath.Join(home, "skills"), filepath.Join(home, "capture"), state
	host.BaseURL, host.TokenFile = srv.URL, tok
	claudeServers, codexServers = readClaudeServers, readCodexServers
	lookPath = func(string) (string, error) { return "", errors.New("no systemd-run") }
	host.UtilityCounts = func(string) (map[string][3]int, int, error) {
		return map[string][3]int{"m1": {3, 1, 0}, "m2": {5, 0, 0}, "m3": {3, 2, 0}}, 4, nil
	}
	host.InjectedChars = func(string) (int, int, error) { return 2, 900, nil }

	calls := &[][]string{}
	agentRun = func(_ time.Duration, args ...string) (string, error) {
		*calls = append(*calls, args)
		bin, rest := filepath.Base(args[0]), strings.Join(args[1:], " ")
		switch {
		case bin == "codex" && rest == "mcp list --json":
			return `[{"name":"cx","enabled":true,"transport":{"type":"stdio","command":"/nonexistent/uvx","args":["cx-mcp"]}},
				{"name":"remote","enabled":true,"auth_status":"not_logged_in","transport":{"type":"streamable_http","url":"https://r.example:8443/mcp","bearer_token_env_var":"TOK","http_headers":{"X":"1"}}},
				{"name":"idle","enabled":false,"transport":{"type":"streamable_http","url":"https://idle.example"}}]`, nil
		case bin == "claude" && rest == "plugin list --json":
			return `[{"id":"fmt@mk","enabled":true,"version":"1.2.0"},{"id":"b@mk","enabled":false},{"noid":1}]`, nil
		case bin == "claude" && rest == "mcp get needy":
			return "needy:\n  Status: ! Needs authentication\n", nil
		case bin == "claude" && strings.HasPrefix(rest, "plugin enable broken"):
			return "", runtimeError{"plugin not found"}
		}
		return "", nil
	}
	claude := map[string]any{"mcpServers": map[string]any{
		"docs":  map[string]any{"type": "http", "url": "https://docs.example/mcp"},
		"needy": map[string]any{"type": "http", "url": "https://needy.example/mcp"},
		"local": map[string]any{"type": "stdio", "command": "/nonexistent/npx", "args": []any{"-y", "@scope/pkg"}, "env": map[string]any{"K": "secret"}},
	}}
	raw, _ := json.Marshal(claude)
	os.WriteFile(claudeJSON, raw, 0o600)
	os.MkdirAll(filepath.Dir(claudeCredentials), 0o700)
	os.WriteFile(claudeCredentials, []byte(`{"mcpOAuth":{"docs|x":{"serverName":"docs","accessToken":"a","refreshToken":"r"}}}`), 0o600)
	// Fresh tool lists for the switched-on stdio servers, so nothing is started.
	cache := host.Obj{}
	for name, d := range map[string]map[string]any{"local": asMap(claude["mcpServers"].(map[string]any)["local"])} {
		cache.Set(name, host.Obj{{Key: "key", Value: defDigest(d)}, {Key: "at", Value: nowFloat()}, {Key: "tools", Value: []any{map[string]any{"name": "search", "desc": "", "risky": false}}}, {Key: "ok", Value: true}})
	}
	cx := map[string]any{"type": "stdio", "command": "/nonexistent/uvx", "args": []any{"cx-mcp"}, "env": map[string]any{}, "_enabled": true, "_auth": nil}
	cache.Set("cx", host.Obj{{Key: "key", Value: defDigest(cx)}, {Key: "at", Value: nowFloat()}, {Key: "tools", Value: []any{}}, {Key: "ok", Value: true}})
	if err := writeState(toolsCache, cache); err != nil {
		t.Fatal(err)
	}
	// One call to the local server today, so the inventory carries a call count.
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	os.MkdirAll(filepath.Join(claudeProjects, "p"), 0o700)
	os.WriteFile(filepath.Join(claudeProjects, "p", "s.jsonl"), []byte(`{"timestamp": "`+now+`", "message": {"content": [{"type": "tool_use", "id": "t1", "name": "mcp__local__search"}]}}`+"\n"+
		`{"timestamp": "`+now+`", "message": {"content": [{"type": "tool_result", "tool_use_id": "t1", "is_error": false}]}}`+"\n"), 0o600)
	return a, home, calls
}

func ranCLI(calls *[][]string, want string) bool {
	for _, c := range *calls {
		if strings.Join(append([]string{filepath.Base(c[0])}, c[1:]...), " ") == want {
			return true
		}
	}
	return false
}

func TestAgentsSyncAppliesRequestsAndReports(t *testing.T) {
	a, home, calls := isolateAgents(t)
	var detached [][]string
	startDetached = func(argv []string) error { detached = append(detached, argv); return nil }
	a.requests = []any{
		map[string]any{"id": "q1", "status": "pending", "agent": "claude_code", "kind": "mcp", "name": "docs", "enabled": false},
		map[string]any{"id": "q2", "status": "pending", "agent": "codex", "kind": "mcp", "name": "docs", "enabled": true},
		map[string]any{"id": "q3", "status": "pending", "agent": "claude_code", "kind": "plugin", "name": "fmt@mk", "enabled": false},
		map[string]any{"id": "q4", "status": "pending", "agent": "codex", "kind": "plugin", "name": "fmt@mk", "enabled": true},
		map[string]any{"id": "q5", "status": "pending", "agent": "nobody", "kind": "mcp", "name": "x", "enabled": true},
		map[string]any{"id": "q6", "status": "pending", "agent": "claude_code", "kind": "plugin_remove", "name": "old@mk", "enabled": false},
		map[string]any{"id": "L1", "status": "pending", "agent": "claude_code", "kind": "mcp_login", "name": "needy"},
		map[string]any{"id": "q8", "status": "done", "agent": "claude_code", "kind": "mcp", "name": "docs", "enabled": true},
		map[string]any{"id": "q9", "status": "pending", "agent": "cursor", "kind": "agent", "name": "cursor", "enabled": true},
		map[string]any{"id": "q10", "status": "pending", "agent": "claude_code", "kind": "mcp", "name": "ghost", "enabled": true},
		map[string]any{"id": "q11", "status": "pending", "agent": "codex", "kind": "mcp", "name": "cx", "enabled": false},
		map[string]any{"id": "q12", "status": "pending", "agent": "claude_code", "kind": "plugin", "name": "bad name!", "enabled": true},
		map[string]any{"id": "q13", "status": "pending", "agent": "claude_code", "kind": "plugin", "name": "broken", "enabled": true},
	}
	var out, errb bytes.Buffer
	if code := agentsMain([]string{"sync"}, nil, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	for _, want := range []string{"claude mcp remove docs -s user", "codex mcp add docs --url https://docs.example/mcp",
		"claude plugin disable fmt@mk", "claude plugin uninstall old@mk", "codex mcp remove cx"} {
		if !ranCLI(calls, want) {
			t.Errorf("did not run %q; ran %v", want, *calls)
		}
	}
	if len(detached) != 1 || !strings.Contains(strings.Join(detached[0], " "), "agents login L1 claude_code needy") {
		t.Fatalf("login start = %v", detached)
	}
	stash := loadStash()
	if asMap(orMap(stash["claude_code"])["docs"])["url"] != "https://docs.example/mcp" || orMap(stash["codex"])["cx"] == nil {
		t.Fatalf("stash = %v", stash)
	}
	if len(a.reports) != 1 {
		t.Fatalf("reports = %d", len(a.reports))
	}
	rep := a.reports[0]
	want := map[string]string{"q1": "done", "q2": "done", "q3": "done", "q4": "failed", "q5": "failed", "q6": "done",
		"q9": "done", "q10": "failed", "q11": "done", "q12": "failed", "q13": "failed"}
	results := rep["results"].([]any)
	if len(results) != len(want) {
		t.Fatalf("results = %v", results)
	}
	for _, r := range results {
		m := r.(map[string]any)
		if want[m["id"].(string)] != m["status"] {
			t.Errorf("result %v", m)
		}
	}
	servers := map[string]map[string]any{}
	for _, s := range rep["servers"].([]any) {
		servers[s.(map[string]any)["name"].(string)] = s.(map[string]any)
	}
	if tools := asList(servers["local"]["tools"]); len(tools) != 1 || asMap(tools[0])["name"] != "search" || orMap(servers["local"]["calls"])["today"] != float64(1) {
		t.Errorf("local tools/calls missing: %v", servers["local"])
	}
	if servers["local"]["target"] != "npx @scope/pkg" || strings.Contains(fmtJSON(servers), "secret") {
		t.Errorf("local = %v", servers["local"])
	}
	if servers["needy"]["note"] != "로그인 필요" || servers["docs"]["login"] != nil {
		t.Errorf("claude login marks: needy=%v docs=%v", servers["needy"], servers["docs"])
	}
	if l := orMap(servers["remote"]["login"]); l["codex"] != "needed" || servers["remote"]["target"] != "https://r.example:8443" {
		t.Errorf("remote = %v", servers["remote"])
	}
	if ag := orMap(servers["idle"]["agents"]); ag["codex"] != "off" {
		t.Errorf("disabled codex server shown on: %v", servers["idle"])
	}
	plugins := rep["plugins"].([]any)
	if len(plugins) != 2 || plugins[0].(map[string]any)["name"] != "b@mk" || plugins[1].(map[string]any)["version"] != "1.2.0" {
		t.Errorf("plugins = %v", plugins)
	}
	use := orMap(rep["memory_use"])
	items := asList(use["items"])
	if use["injections"] != float64(11) || use["used"] != float64(3) || use["inject_chars"] != float64(900) || len(items) != 3 ||
		asMap(items[0])["id"] != "m2" || asMap(items[1])["id"] != "m1" {
		t.Errorf("memory_use = %v", use)
	}
	if rep["memory_use_30"] == nil {
		t.Error("30-day memory use missing")
	}
	var cursor map[string]any
	for _, ag := range rep["agents"].([]any) {
		if ag.(map[string]any)["id"] == "cursor" {
			cursor = ag.(map[string]any)
		}
	}
	if cursor["enabled"] != true {
		t.Errorf("cursor agent = %v", cursor)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "memory-use.json")); err != nil {
		t.Error("memory use not cached", err)
	}
}

func fmtJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func TestAgentsSyncStopsOnMalformedRequestAndDownArkism(t *testing.T) {
	a, _, _ := isolateAgents(t)
	a.requests = []any{map[string]any{"id": "q1", "status": "pending", "agent": "codex", "kind": "mcp", "name": "docs"}}
	var out, errb bytes.Buffer
	if code := agentsMain([]string{"sync"}, nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "'enabled'") || len(a.reports) != 0 {
		t.Fatal(code, errb.String(), a.reports)
	}
	host.BaseURL = "http://127.0.0.1:1"
	errb.Reset()
	if code := agentsMain([]string{"sync"}, nil, &out, &errb); code != 1 || errb.Len() == 0 {
		t.Fatal(code, errb.String())
	}
}

func TestAgentsSyncKeepsReportingWhenMemoryUseFails(t *testing.T) {
	a, _, _ := isolateAgents(t)
	host.UtilityCounts = func(string) (map[string][3]int, int, error) { return nil, 0, errors.New("db down") }
	if err := syncAgents(); err != nil {
		t.Fatal(err)
	}
	if len(a.reports) != 1 || a.reports[0]["memory_use"] != nil {
		t.Fatalf("reports = %v", a.reports)
	}
}

func TestAgentsInventoryCommandAndUsage(t *testing.T) {
	isolateAgents(t)
	var out, errb bytes.Buffer
	if code := agentsMain([]string{"inventory"}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), `"needy"`) || !strings.Contains(out.String(), `"fmt@mk"`) ||
		!strings.Contains(out.String(), `"tools"`) || !strings.Contains(out.String(), `"calls"`) {
		t.Fatal(code, out.String(), errb.String())
	}
	for _, args := range [][]string{nil, {"login", "L1", "gemini", "x"}, {"login", "L1"}} {
		out.Reset()
		errb.Reset()
		if code := agentsMain(args, nil, &out, &errb); code != 2 || !strings.Contains(out.String(), "inventory") {
			t.Errorf("%v exit %d", args, code)
		}
	}
}

func TestMemoryUseIsCachedForHalfAnHour(t *testing.T) {
	isolateAgents(t)
	now := nowFloat()
	use, use30, err := memoryUse(now)
	if err != nil || objGet(use.(host.Obj), "days") != 7 || objGet(use30.(host.Obj), "days") != 30 {
		t.Fatal(use, use30, err)
	}
	host.UtilityCounts = func(string) (map[string][3]int, int, error) {
		t.Fatal("recomputed within 30 minutes")
		return nil, 0, nil
	}
	if _, _, err := memoryUse(now + 60); err != nil {
		t.Fatal(err)
	}
	host.UtilityCounts = func(string) (map[string][3]int, int, error) { return nil, 0, errors.New("x") }
	if _, _, err := memoryUse(now + memoryUseEvery + 1); err == nil {
		t.Fatal("a stale cache must be recomputed")
	}
	host.InjectedChars = func(string) (int, int, error) { return 0, 0, nil }
	host.UtilityCounts = func(string) (map[string][3]int, int, error) { return nil, 0, nil }
	got, err := memoryUseOver(now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Get("inject_chars"); ok {
		t.Fatal("no logged inject text must send nothing")
	}
}

func TestLoginRequestAndStepTalkToArkism(t *testing.T) {
	a, _, _ := isolateAgents(t)
	a.requests = []any{map[string]any{"id": "L1", "status": "waiting"}, map[string]any{"id": "L2", "status": "done"}}
	req, err := getLoginRequest("L2")
	if err != nil || req["status"] != "done" {
		t.Fatal(req, err)
	}
	if req, err := getLoginRequest("nope"); err != nil || req != nil {
		t.Fatal(req, err)
	}
	if err := postLoginStep("L1", host.Obj{{Key: "status", Value: "done"}}); err != nil {
		t.Fatal(err)
	}
	if got := a.logins["L1"]; len(got) != 1 || got[0]["status"] != "done" || got[0]["scope"] == nil {
		t.Fatalf("login steps = %v", got)
	}
}

func TestAgentsLoginSkipsWhileAnotherRunsAndReportsFailure(t *testing.T) {
	a, _, _ := isolateAgents(t)
	steps := []host.Obj{}
	ls := loginStep
	t.Cleanup(func() { loginStep = ls })
	loginStep = func(id string, body host.Obj) error { steps = append(steps, body); return nil }
	marker := filepath.Join(agentsState, "login-L9")
	os.MkdirAll(agentsState, 0o700)
	os.WriteFile(marker, nil, 0o600)
	if err := agentsLogin("L9", "claude_code", "needy"); err != nil || len(steps) != 0 || len(a.reports) != 0 {
		t.Fatal("a running sign-in must not be started twice", err, steps)
	}
	os.Remove(marker)
	// The CLI path does not exist: the sign-in fails, is reported, and the sync still runs.
	lc := loginCLIs["claude_code"]
	t.Cleanup(func() { loginCLIs["claude_code"] = lc })
	loginCLIs["claude_code"] = func() string { return filepath.Join(t.TempDir(), "absent-claude") }
	if err := agentsLogin("L9", "claude_code", "needy"); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || objGet(steps[0], "status") != "failed" || len(a.reports) != 1 {
		t.Fatalf("steps = %v reports = %d", steps, len(a.reports))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker left behind")
	}
}

func TestRunAgentCLIReportsTheLastLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "cli")
	os.WriteFile(cli, []byte("#!/bin/sh\ncase \"$1\" in\nok) echo \"child=$ARKISM_CAPTURE_CHILD\";;\nfail) echo first >&2; echo 'last words' >&2; exit 1;;\nout) echo 'only stdout'; exit 2;;\nquiet) exit 5;;\nslow) sleep 5;;\nesac\n"), 0o755)
	if out, err := runAgentCLI(5*time.Second, cli, "ok"); err != nil || strings.TrimSpace(out) != "child=1" {
		t.Fatal(out, err)
	}
	for arg, want := range map[string]string{"fail": "last words", "out": "only stdout", "quiet": "exit 5"} {
		if _, err := runAgentCLI(5*time.Second, cli, arg); err == nil || err.Error() != want {
			t.Errorf("%s: %v", arg, err)
		}
	}
	if _, err := runAgentCLI(50*time.Millisecond, cli, "slow"); !errors.Is(err, errTimeoutExpired) {
		t.Errorf("slow: %v", err)
	}
	if _, err := runAgentCLI(time.Second, filepath.Join(dir, "missing")); err == nil {
		t.Error("missing CLI must fail")
	}
}
