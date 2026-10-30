package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
)

const agentsUsage = `Sync this host's agent MCP servers and plugins with Arkism.

  sync       apply the owner's pending on/off switches with the agents' own CLIs,
             then report what each agent has configured (run every minute by
             arkism-agents.timer)
  login ID   run one MCP sign-in the owner asked for (started by sync): the agent's
             CLI prints its sign-in address, the desktop app opens it and hands
             back the redirect, which is replayed here on the CLI's loopback
  inventory  print what would be reported, without applying or sending anything

What leaves this host is names, transport and the program or host only: never
arguments, environment, headers or tokens. A server switched off for an agent is
kept here (STASH, 0600, with its full definition) so switching it back on restores
it exactly; switching it on for an agent that never had it copies another agent's
definition. The same report carries the skill quality and security scores that
skill-checks last wrote (see that command for what leaves the host).
`

func init() { host.Register("agents", agentsMain) }

var (
	agentsHome        = host.Home
	claudeBin         = host.Env("ARKISM_CLAUDE_BIN", filepath.Join(host.Home, ".local/bin/claude"))
	codexBin          = host.Env("ARKISM_CODEX_BIN", filepath.Join(host.Home, ".local/bin/codex"))
	claudeJSON        = filepath.Join(host.Home, ".claude.json")
	claudeCredentials = filepath.Join(host.Home, ".claude/.credentials.json")
	agentsState       = host.AgentsState
	agentNameRx       = regexp.MustCompile(`^[A-Za-z0-9@._/][A-Za-z0-9@._/-]{0,127}$`) // no leading "-": names are CLI arguments
	packageRx         = regexp.MustCompile(`^@?[a-z0-9][a-z0-9._-]*(/[a-z0-9._-]+)?(@[0-9A-Za-z.^~-]+)?$`)
	agentIDRx         = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	loginIDRx         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

	// Test seams.
	agentRun      = runAgentCLI
	claudeServers = readClaudeServers
	codexServers  = readCodexServers
	loginStep     = postLoginStep
	loginRequest  = getLoginRequest
	lookPath      = exec.LookPath
	startDetached = func(argv []string) error {
		cmd := exec.Command(argv[0], argv[1:]...)
		newSession(cmd)
		return cmd.Start()
	}
	loginCLIs = map[string]func() string{"claude_code": func() string { return claudeBin }, "codex": func() string { return codexBin }}
)

func stashPath() string    { return filepath.Join(agentsState, "agent-stash.json") }
func registryPath() string { return filepath.Join(agentsState, "agents.json") }

// runAgentCLI runs an agent CLI; a failure carries the CLI's last output line.
func runAgentCLI(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.WaitDelay = time.Second // a grandchild holding the pipe must not outlive the timeout
	cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", errTimeoutExpired
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return "", err
	}
	if err != nil {
		text := errOut.String()
		if text == "" {
			text = out.String()
		}
		if text = pyStrip(text); text != "" {
			lines := splitLines(text)
			return "", runtimeError{runeCut(lines[len(lines)-1], 200)}
		}
		return "", runtimeError{fmt.Sprintf("exit %d", exit.ExitCode())}
	}
	return out.String(), nil
}

// ---------------------------------------------------------------- reading

func readJSONMap(path string) (map[string]any, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		return nil, false
	}
	m, _ := v.(map[string]any)
	return m, true
}

// readClaudeServers is Claude Code's user-scope MCP servers, in Claude's own JSON shape.
func readClaudeServers() map[string]any {
	doc, _ := readJSONMap(claudeJSON)
	out := map[string]any{}
	for k, v := range orMap(doc["mcpServers"]) {
		out[k] = v
	}
	return out
}

// readCodexServers is Codex's servers in Claude's shape (+ codex's own enabled/auth state).
func readCodexServers() map[string]any {
	raw, err := agentRun(60*time.Second, codexBin, "mcp", "list", "--json")
	if err != nil {
		return map[string]any{}
	}
	v, err := host.Loads(raw)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	for _, r := range asList(v) {
		row := asMap(r)
		t := orMap(row["transport"])
		var d map[string]any
		if t["type"] == "stdio" {
			d = map[string]any{"type": "stdio", "command": t["command"], "args": firstOr(t["args"], []any{}), "env": firstOr(t["env"], map[string]any{})}
		} else {
			d = map[string]any{"type": "http", "url": t["url"]}
			if truthy(t["bearer_token_env_var"]) {
				d["bearer_token_env_var"] = t["bearer_token_env_var"]
			}
			if truthy(t["http_headers"]) {
				d["headers"] = t["http_headers"]
			}
		}
		d["_enabled"] = getOr(row, "enabled", true)
		d["_auth"] = row["auth_status"]
		out[pyStr(row["name"])] = d
	}
	return out
}

func claudePluginRows() []map[string]any {
	raw, err := agentRun(60*time.Second, claudeBin, "plugin", "list", "--json")
	if err != nil {
		return nil
	}
	v, err := host.Loads(raw)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, p := range asList(v) {
		if truthy(asMap(p)["id"]) {
			out = append(out, asMap(p))
		}
	}
	return out
}

func loadStash() map[string]any {
	m, _ := readJSONMap(stashPath())
	if m == nil {
		return map[string]any{}
	}
	return m
}

func saveStash(stash map[string]any) error {
	if err := os.MkdirAll(agentsState, 0o700); err != nil {
		return err
	}
	text, err := dumpIndent(stash, 1, false)
	if err != nil {
		return err
	}
	tmp := filepath.Join(agentsState, "agent-stash.tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o666); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, stashPath())
}

func enabledOf(d map[string]any) bool { return truthy(getOr(d, "_enabled", true)) }

func isStdio(d map[string]any) bool {
	return d["type"] == "stdio" || truthy(d["command"]) && !truthy(d["url"])
}

// safeTarget is the program or host of a server definition, never its arguments or secrets.
func safeTarget(d map[string]any) string {
	if !isStdio(d) {
		u, err := url.Parse(str(d["url"]))
		if err != nil || u.Hostname() == "" {
			return ""
		}
		port := ""
		if p, err := strconv.Atoi(u.Port()); err == nil && p != 0 {
			port = ":" + strconv.Itoa(p)
		}
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + port
	}
	prog := filepath.Base(str(d["command"]))
	if str(d["command"]) == "" {
		prog = ""
	}
	pkg := ""
	for _, a := range asList(d["args"]) {
		if s := str(a); !strings.HasPrefix(s, "-") {
			pkg = s
			break
		}
	}
	if pkg != "" && packageRx.MatchString(pkg) {
		return prog + " " + pkg
	}
	return prog
}

func transportOf(d map[string]any) string {
	if isStdio(d) {
		return "stdio"
	}
	if d["type"] == "sse" {
		return "sse"
	}
	return "http"
}

// claudeSignedIn is true when Claude Code holds a usable OAuth token for this server (names only are read).
func claudeSignedIn(name string) bool {
	doc, ok := readJSONMap(claudeCredentials)
	if !ok {
		return false
	}
	now := nowFloat() * 1000
	for _, v := range orMap(doc["mcpOAuth"]) {
		e := asMap(v)
		if e["serverName"] == name && truthy(e["accessToken"]) && (truthy(e["refreshToken"]) || numFloat(firstOr(e["expiresAt"], 0)) > now) {
			return true
		}
	}
	return false
}

// claudeNeedsLogin asks Claude itself about an http server it has no token for (cached 10 minutes).
var claudeNeedsLogin = func(name string, d map[string]any) bool {
	if isStdio(d) || claudeSignedIn(name) {
		return false
	}
	cacheFile := filepath.Join(agentsState, "claude-auth.json")
	cache, _ := readJSONMap(cacheFile)
	if cache == nil {
		cache = map[string]any{}
	}
	hit := asList(cache[name])
	if len(hit) == 2 && nowFloat()-numFloat(hit[0]) < 600 {
		return truthy(hit[1])
	}
	out, err := agentRun(40*time.Second, claudeBin, "mcp", "get", name)
	if err != nil {
		return len(hit) == 2 && truthy(hit[1])
	}
	needed := strings.Contains(out, "Needs authentication")
	cache[name] = []any{nowFloat(), needed}
	if os.MkdirAll(agentsState, 0o700) == nil {
		_ = os.WriteFile(cacheFile, []byte(host.MustDumps(cache, host.DumpOpts{})), 0o666) // a cache
	}
	return needed
}

// serverEntry is one server's row; others are the file-edited agents' servers (Gemini CLI, ZCode, Grok).
func serverEntry(name string, d map[string]any, claude, codex map[string]any, others ...map[string]map[string]any) host.Obj {
	agents := host.Obj{{Key: "claude_code", Value: "off"}, {Key: "codex", Value: "off"}}
	for _, a := range mcpFileAgents {
		agents.Set(a.id, "off")
	}
	_, inClaude := claude[name]
	cd, inCodex := codex[name]
	if inClaude {
		agents.Set("claude_code", "on")
	}
	if inCodex && enabledOf(asMap(cd)) {
		agents.Set("codex", "on")
	}
	for _, o := range others {
		for _, a := range mcpFileAgents {
			if od, ok := o[a.id][name]; ok && enabledOf(asMap(od)) {
				agents.Set(a.id, "on")
			}
		}
	}
	entry := host.Obj{{Key: "name", Value: name}, {Key: "transport", Value: transportOf(d)}, {Key: "target", Value: safeTarget(d)}, {Key: "agents", Value: agents}}
	login := host.Obj{}
	if objGet(agents, "codex") == "on" && asMap(cd)["_auth"] == "not_logged_in" {
		login.Set("codex", "needed")
	}
	if objGet(agents, "claude_code") == "on" && claudeNeedsLogin(name, asMap(claude[name])) {
		login.Set("claude_code", "needed")
	}
	if len(login) > 0 {
		entry.Set("login", login)
		entry.Set("note", "로그인 필요")
	}
	return entry
}

func inventory() ([]any, []any, error) {
	claude, codex, stash, others := claudeServers(), codexServers(), loadStash(), fileAgentServers()
	var srcs []map[string]any
	for _, a := range mcpFileAgents {
		srcs = append(srcs, orMap(stash[a.id]))
	}
	srcs = append(srcs, orMap(stash["codex"]), orMap(stash["claude_code"]))
	for _, a := range mcpFileAgents {
		srcs = append(srcs, others[a.id])
	}
	defs := map[string]any{}
	for _, src := range append(srcs, codex, claude) {
		for k, v := range src {
			defs[k] = v
		}
	}
	servers := []any{}
	for _, name := range sortedKeys(defs) {
		servers = append(servers, serverEntry(name, asMap(defs[name]), claude, codex, others))
	}
	// Each server's tools (asked once a day) and today's calls from the transcripts. Only servers
	// switched on for some agent are started to ask for their tools; switched-off ones stay off.
	tools, err := serverTools(inventoryLive(defs, claude, codex, others), 1, nil)
	if err != nil {
		return nil, nil, err
	}
	calls := toolCalls(time.Time{})
	for i, s := range servers {
		e := s.(host.Obj)
		name := str(objGet(e, "name"))
		if t := objGet(tools, name); truthy(t) {
			e.Set("tools", t)
		}
		if c := objGet(calls, normServer(name)); truthy(c) {
			e.Set("calls", c)
		}
		servers[i] = e // Set may grow e past the row stored in servers
	}
	return servers, inventoryPlugins(), nil
}

// inventoryLive is the servers switched on for some agent.
func inventoryLive(defs, claude, codex map[string]any, others map[string]map[string]any) map[string]any {
	live := map[string]any{}
	for n, d := range defs {
		_, inClaude := claude[n]
		cd, inCodex := codex[n]
		on := inClaude || inCodex && enabledOf(asMap(cd))
		for _, a := range mcpFileAgents {
			od, ok := others[a.id][n]
			on = on || ok && enabledOf(asMap(od))
		}
		if on {
			live[n] = d
		}
	}
	return live
}

// inventoryPlugins is Claude Code's plugins as server-list rows.
func inventoryPlugins() []any {
	rows := claudePluginRows()
	sort.SliceStable(rows, func(i, j int) bool { return pyStr(rows[i]["id"]) < pyStr(rows[j]["id"]) })
	plugins := []any{}
	for _, row := range rows {
		state := "off"
		if truthy(row["enabled"]) {
			state = "on"
		}
		entry := host.Obj{{Key: "name", Value: row["id"]}, {Key: "transport", Value: "plugin"}, {Key: "target", Value: "Claude Code 플러그인"},
			{Key: "agents", Value: host.Obj{{Key: "claude_code", Value: state}}}}
		for _, kv := range pluginContents(str(row["installPath"])) {
			if truthy(kv.Value) {
				entry.Set(kv.Key, kv.Value)
			}
		}
		if v := runeCut(pyStr(firstOr(row["version"], objGet(entry, "version"), "")), 40); v != "" {
			entry.Set("version", v)
		} else {
			entry.Del("version")
		}
		plugins = append(plugins, entry)
	}
	return plugins
}

// ---------------------------------------------------------------- agents and their folders (설정 › 에이전트)

// sharedSkills is the one skills dir Codex and Gemini CLI share on this host.
const sharedSkills = "~/.agents/skills"

type knownAgent struct {
	id, name string
	skills   []string
	chats    string
	home     string // the folder whose presence means "installed here"
}

var knownAgents = []knownAgent{
	{"claude_code", "Claude Code", []string{"~/.claude/skills"}, "~/.claude/projects", "~/.claude"},
	{"codex", "Codex", []string{"~/.codex/skills", sharedSkills}, "~/.codex/sessions", "~/.codex"},
	{"gemini", "Gemini CLI", []string{"~/.gemini/skills", sharedSkills}, "~/.gemini/tmp", "~/.gemini"},
	{"zcode", "ZCode", []string{"~/.zcode/skills"}, "~/.zcode/sessions", "~/.zcode"},
	{"grok", "Grok", []string{"~/.grok/skills"}, "~/.grok/sessions", "~/.grok"},
	{"cursor", "Cursor", []string{"~/.cursor/skills"}, "", "~/.cursor"},
	{"opencode", "OpenCode", []string{"~/.config/opencode/skills"}, "", "~/.config/opencode"},
	{"windsurf", "Windsurf", []string{"~/.windsurf/skills"}, "", "~/.windsurf"},
	{"amp", "Amp", []string{"~/.config/agents/skills"}, "", "~/.config/agents"},
	{"droid", "Droid", []string{"~/.factory/skills"}, "", "~/.factory"},
	{"pi", "Pi", []string{"~/.pi/agent/skills"}, "", "~/.pi"},
}

// defaultOn agents are read by Arkism from the start; the rest only once the owner adds them.
var defaultOn = []string{"claude_code", "codex", "gemini", "zcode", "grok"}

func expand(d string) string {
	if strings.HasPrefix(d, "~") {
		return agentsHome + d[1:]
	}
	return d
}

type registry struct {
	added   host.Obj
	removed []string
	shared  []string
}

func loadRegistry() registry {
	doc := readOrdered(registryPath())
	reg := registry{added: asObj(objGet(doc, "added")), shared: []string{sharedSkills}}
	if reg.added == nil {
		reg.added = host.Obj{}
	}
	for _, r := range asList(objGet(doc, "removed")) {
		reg.removed = append(reg.removed, pyStr(r))
	}
	if s := asList(objGet(doc, "shared")); len(s) > 0 {
		reg.shared = nil
		for _, d := range s {
			reg.shared = append(reg.shared, pyStr(d))
		}
	}
	return reg
}

func saveRegistry(reg registry) error {
	if err := os.MkdirAll(agentsState, 0o700); err != nil {
		return err
	}
	text, err := dumpIndent(host.Obj{{Key: "added", Value: reg.added}, {Key: "removed", Value: stringsAny(reg.removed)}, {Key: "shared", Value: stringsAny(reg.shared)}}, 1, false)
	if err != nil {
		return err
	}
	return os.WriteFile(registryPath(), []byte(text), 0o666)
}

func countSkills(dirs []string) int {
	names := map[string]bool{}
	for _, d := range dirs {
		entries, err := os.ReadDir(expand(d))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if isFile(filepath.Join(expand(d), e.Name(), "SKILL.md")) {
				names[e.Name()] = true
			}
		}
	}
	return len(names)
}

// agentsInventory is every known or added agent: folders, whether it is on this host, whether Arkism reads it, skill count.
func agentsInventory(reg registry) []any {
	out := []any{}
	known := map[string]bool{}
	for _, a := range knownAgents {
		known[a.id] = true
		_, added := reg.added.Get(a.id)
		enabled := (containsStr(defaultOn, a.id) || added) && !containsStr(reg.removed, a.id)
		out = append(out, host.Obj{{Key: "id", Value: a.id}, {Key: "name", Value: a.name}, {Key: "skills_dirs", Value: stringsAny(a.skills)},
			{Key: "chats_dir", Value: a.chats}, {Key: "found", Value: exists(expand(a.home))}, {Key: "enabled", Value: enabled}, {Key: "skills", Value: countSkills(a.skills)}})
	}
	for _, kv := range reg.added {
		if known[kv.Key] || containsStr(reg.removed, kv.Key) {
			continue
		}
		row := asObj(kv.Value)
		var dirs []string
		if d := objGet(row, "skills_dir"); truthy(d) {
			dirs = []string{pyStr(d)}
		}
		found := false
		for _, d := range dirs {
			found = found || exists(expand(d))
		}
		out = append(out, host.Obj{{Key: "id", Value: kv.Key}, {Key: "name", Value: firstOr(objGet(row, "name"), kv.Key)}, {Key: "skills_dirs", Value: stringsAny(dirs)},
			{Key: "chats_dir", Value: firstOr(objGet(row, "chats_dir"), "")}, {Key: "found", Value: found}, {Key: "enabled", Value: true}, {Key: "custom", Value: true},
			{Key: "skills", Value: countSkills(dirs)}})
	}
	return out
}

// applyAgentToggle adds, changes or drops one owner-defined agent in the registry (설정 › 에이전트 켜기/끄기).
func applyAgentToggle(reg *registry, aid string, req map[string]any) error {
	if !agentIDRx.MatchString(aid) {
		return runtimeError{"에이전트 이름이 올바르지 않아요"}
	}
	if truthy(req["enabled"]) {
		reg.added.Set(aid, host.Obj{{Key: "name", Value: firstOr(req["name"], aid)}, {Key: "skills_dir", Value: firstOr(req["skills_dir"], "")}, {Key: "chats_dir", Value: firstOr(req["chats_dir"], "")}})
		reg.removed = removeStr(reg.removed, aid)
		if folder := str(req["skills_dir"]); folder != "" {
			if err := os.MkdirAll(expand(folder), 0o777); err != nil {
				return err
			}
		}
		return nil
	}
	reg.added.Del(aid)
	if containsStr(defaultOn, aid) && !containsStr(reg.removed, aid) {
		reg.removed = append(reg.removed, aid)
	}
	return nil
}

func removeStr(list []string, s string) []string {
	out := []string{}
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func applyAgent(req map[string]any) error {
	reg := loadRegistry()
	if req["kind"] == "shared" {
		folder := pyStr(firstOr(req["name"], ""))
		reg.shared = removeStr(reg.shared, folder)
		if truthy(req["enabled"]) {
			reg.shared = append(reg.shared, folder)
		}
	} else if err := applyAgentToggle(&reg, pyStr(firstOr(req["agent"], "")), req); err != nil {
		return err
	}
	return saveRegistry(reg)
}

// ---------------------------------------------------------------- memory use (기억: 들어감 · 쓰임)

const (
	memoryUseEvery = 30 * 60
	memoryUseDays  = 7
)

// memoryUse is injections, receipts and estimated use per memory over the last week and the last 30 days,
// recomputed at most every 30 minutes.
func memoryUse(now float64) (any, any, error) {
	path := filepath.Join(agentsState, "memory-use.json")
	if cached := readOrdered(path); cached != nil && now-numFloat(getAt(cached)) < memoryUseEvery {
		use, ok := cached.Get("use")
		use30, ok30 := cached.Get("use_30")
		if ok && ok30 {
			return use, use30, nil
		}
	}
	use, err := memoryUseOver(now, memoryUseDays)
	if err != nil {
		return nil, nil, err
	}
	use30, err := memoryUseOver(now, 30)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(agentsState, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(path, []byte(host.MustDumps(host.Obj{{Key: "at", Value: now}, {Key: "use", Value: use}, {Key: "use_30", Value: use30}}, host.DumpOpts{})), 0o666); err != nil {
		return nil, nil, err
	}
	return use, use30, nil
}

func memoryUseOver(now float64, days int) (host.Obj, error) {
	since := time.Unix(int64(now), 0).UTC().AddDate(0, 0, -days).Format("2006-01-02T15:04:05Z")
	counts, sessions, err := host.UtilityCounts(since)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { // most injected first; ties by id, since Go maps keep no order
		if counts[ids[i]].Injected != counts[ids[j]].Injected {
			return counts[ids[i]].Injected > counts[ids[j]].Injected
		}
		return ids[i] < ids[j]
	})
	// used is record_memory_usage receipts; estimated is answers restating the memory, shown apart;
	// unused marks a memory returned unused_after times since its last receipt for a person to review.
	after := host.UnusedAfter()
	items, injections, used, estimated := []any{}, 0, 0, 0
	for _, id := range ids[:min(len(ids), 5000)] {
		n := counts[id]
		items = append(items, host.Obj{{Key: "id", Value: id}, {Key: "injected", Value: n.Injected}, {Key: "used", Value: n.Receipts},
			{Key: "estimated", Value: n.Overlap}, {Key: "unreceipted", Value: n.Unreceipted}, {Key: "unused", Value: n.Unreceipted >= after}})
		injections += n.Injected
		used += n.Receipts
		estimated += n.Overlap
	}
	use := host.Obj{{Key: "days", Value: days}, {Key: "sessions", Value: sessions}, {Key: "injections", Value: injections}, {Key: "used", Value: used},
		{Key: "estimated", Value: estimated}, {Key: "unused_after", Value: after}, {Key: "items", Value: items}}
	// The hooks' own text size (SessionStart index + prompt recall); a host that has not logged it yet sends nothing.
	if n, chars, err := host.InjectedChars(since); err == nil && n > 0 {
		use.Set("inject_sessions", n)
		use.Set("inject_chars", chars)
	}
	return use, nil
}

// ---------------------------------------------------------------- switching

func toCodexArgs(name string, d map[string]any) ([]string, error) {
	if isStdio(d) {
		args := []string{"mcp", "add", name}
		env := orMap(d["env"])
		for _, k := range sortedKeys(env) {
			args = append(args, "--env", k+"="+pyStr(env[k]))
		}
		args = append(args, "--", pyStr(d["command"]))
		for _, a := range asList(d["args"]) {
			args = append(args, pyStr(a))
		}
		return args, nil
	}
	if truthy(d["headers"]) {
		return nil, runtimeError{"헤더가 필요한 서버는 Codex에 직접 추가해야 해요"}
	}
	args := []string{"mcp", "add", name, "--url", pyStr(d["url"])}
	if truthy(d["bearer_token_env_var"]) {
		args = append(args, "--bearer-token-env-var", pyStr(d["bearer_token_env_var"]))
	}
	return args, nil
}

func cleanDef(d map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range d {
		if !strings.HasPrefix(k, "_") && k != "bearer_token_env_var" {
			out[k] = v
		}
	}
	return out
}

func stashFor(stash map[string]any, agent string) map[string]any {
	m, ok := stash[agent].(map[string]any)
	if !ok {
		m = map[string]any{}
		stash[agent] = m
	}
	return m
}

func switchClaude(name string, enabled bool, stash map[string]any) error {
	live := claudeServers()
	_, has := live[name]
	if !enabled {
		if has {
			stashFor(stash, "claude_code")[name] = live[name]
			if err := saveStash(stash); err != nil {
				return err
			}
			_, err := agentRun(60*time.Second, claudeBin, "mcp", "remove", name, "-s", "user")
			return err
		}
		return nil
	}
	if has {
		return nil
	}
	d := asMap(orMap(stash["claude_code"])[name])
	if !truthy(d) {
		d = otherDef(name, "claude_code", stash)
	}
	if len(d) == 0 {
		return runtimeError{"이 서버의 설정을 찾지 못했어요"}
	}
	if _, err := agentRun(60*time.Second, claudeBin, "mcp", "add-json", name, host.MustDumps(d, host.DumpOpts{}), "-s", "user"); err != nil {
		return err
	}
	delete(orMap(stash["claude_code"]), name)
	return saveStash(stash)
}

func switchCodex(name string, enabled bool, stash map[string]any) error {
	live := codexServers()
	_, has := live[name]
	if !enabled {
		if has {
			stashFor(stash, "codex")[name] = live[name]
			if err := saveStash(stash); err != nil {
				return err
			}
			_, err := agentRun(60*time.Second, codexBin, "mcp", "remove", name)
			return err
		}
		return nil
	}
	if has {
		return nil
	}
	d := asMap(orMap(stash["codex"])[name])
	if !truthy(d) {
		d = otherDef(name, "codex", stash)
	}
	if len(d) == 0 {
		return runtimeError{"이 서버의 설정을 찾지 못했어요"}
	}
	args, err := toCodexArgs(name, d)
	if err != nil {
		return err
	}
	if _, err := agentRun(60*time.Second, append([]string{codexBin}, args...)...); err != nil {
		return err
	}
	delete(orMap(stash["codex"]), name)
	return saveStash(stash)
}

var switches = map[string]func(string, bool, map[string]any) error{"claude_code": switchClaude, "codex": switchCodex,
	"gemini": mcpFileAgents[0].switchMCP, "zcode": mcpFileAgents[1].switchMCP, "grok": mcpFileAgents[2].switchMCP}

// addServer adds a new MCP server for one agent with that agent's own CLI. No keys: an address or a program only.
func addServer(req map[string]any) error {
	name, agent := str(req["name"]), str(req["agent"])
	if !agentNameRx.MatchString(name) {
		return runtimeError{"이름이 올바르지 않아요"}
	}
	var d host.Obj
	if truthy(req["url"]) {
		d = host.Obj{{Key: "type", Value: "http"}, {Key: "url", Value: req["url"]}}
	} else if cmd := asList(req["command"]); len(cmd) > 0 {
		d = host.Obj{{Key: "type", Value: "stdio"}, {Key: "command", Value: cmd[0]}, {Key: "args", Value: append([]any{}, cmd[1:]...)}}
	} else {
		return runtimeError{"실행할 프로그램이나 주소가 필요해요"}
	}
	return addServerDef(agent, name, d)
}

// addServerDef writes one server definition (Claude's shape) with the agent's own CLI or config file.
func addServerDef(agent, name string, d host.Obj) error {
	switch agent {
	case "claude_code":
		if _, ok := claudeServers()[name]; ok {
			return runtimeError{"같은 이름의 서버가 이미 있어요"}
		}
		_, err := agentRun(60*time.Second, claudeBin, "mcp", "add-json", name, host.MustDumps(d, host.DumpOpts{}), "-s", "user")
		return err
	case "codex":
		if _, ok := codexServers()[name]; ok {
			return runtimeError{"같은 이름의 서버가 이미 있어요"}
		}
		m := map[string]any{}
		for _, kv := range d {
			m[kv.Key] = kv.Value
		}
		args, err := toCodexArgs(name, m)
		if err != nil {
			return err
		}
		_, err = agentRun(60*time.Second, append([]string{codexBin}, args...)...)
		return err
	}
	for _, a := range mcpFileAgents {
		if a.id == agent {
			m := map[string]any{}
			for _, kv := range d {
				m[kv.Key] = kv.Value
			}
			return a.addNew(name, m)
		}
	}
	return runtimeError{"이 에이전트는 아직 서버 추가를 지원하지 않아요"}
}

func applyRequest(req, stash map[string]any) error {
	agent, kind, name, enabled := str(req["agent"]), str(req["kind"]), str(req["name"]), truthy(req["enabled"])
	switch kind {
	case "agent", "shared":
		return applyAgent(req)
	case "skill_update":
		return updateRepo(name)
	case "skill_check":
		return recheck(name)
	case "mcp_add":
		return addServer(req)
	case "plugin_remove":
		if agent != "claude_code" || !agentNameRx.MatchString(name) {
			return runtimeError{"플러그인은 Claude Code만 지원해요"}
		}
		_, err := agentRun(60*time.Second, claudeBin, "plugin", "uninstall", name)
		return err
	}
	if !agentNameRx.MatchString(name) {
		return runtimeError{"이름이 올바르지 않아요"}
	}
	if kind == "plugin" {
		if agent != "claude_code" {
			return runtimeError{"플러그인은 Claude Code만 지원해요"}
		}
		verb := "disable"
		if enabled {
			verb = "enable"
		}
		_, err := agentRun(60*time.Second, claudeBin, "plugin", verb, name)
		return err
	}
	sw, ok := switches[agent]
	if !ok {
		return runtimeError{"이 에이전트는 아직 켜고 끄기를 지원하지 않아요"}
	}
	return sw(name, enabled, stash)
}

func syncAgents() error {
	if err := os.MkdirAll(agentsState, 0o700); err != nil {
		return err
	}
	unlock, ok, err := tryLock(filepath.Join(agentsState, "agents.lock"))
	if err != nil || !ok {
		return err
	}
	defer unlock()
	res, err := host.Get("/v1/agent_tools?"+scopeQuery(host.Scope()), 30*time.Second)
	if err != nil {
		return err
	}
	doc := orMap(res["agent_tools"])
	results, done, failed, err := syncAgentsApply(doc)
	if err != nil {
		return err
	}
	servers, plugins, err := inventory()
	if err != nil {
		return err
	}
	reg := loadRegistry()
	agents := agentsInventory(reg)
	body := host.Obj{{Key: "scope", Value: host.Scope()}, {Key: "servers", Value: servers}, {Key: "plugins", Value: plugins}, {Key: "results", Value: results},
		{Key: "agents", Value: agents}, {Key: "shared_dirs", Value: stringsAny(reg.shared[:min(len(reg.shared), 8)])}}
	if use, use30, err := memoryUse(nowFloat()); err != nil { // stats are extra: never block the tool switches
		_ = host.Metric(map[string]any{"event": "memory_use_failed", "error": pyErrName(err)})
	} else {
		body.Set("memory_use", use)
		body.Set("memory_use_30", use30)
	}
	var dirs []string
	for _, a := range agents {
		for _, d := range asList(objGet(a.(host.Obj), "skills_dirs")) {
			dirs = append(dirs, pyStr(d))
		}
	}
	dirs = append(append(dirs, reg.shared...), host.SkillsRoot)
	updates, err := skillUpdates(dirs, 0) // checked every six hours; between checks the cached list is sent
	if err != nil {
		return err
	}
	body.Set("skill_updates", updates)
	if repos := skillRepoChecks(); repos != nil { // the 스킬 화면's 업데이트 section: remote, commit, last check time per repository
		body.Set("skill_repos", repos)
	}
	body.Set("pc_links", pcReport(nowFloat())) // which agents on the PC reached this server (skill screen PC icon)
	// the last capture drain that touched a job (분석 window, #449)
	if run := readOrdered(filepath.Join(host.CaptureState, "analysis.json")); run != nil {
		body.Set("analysis_run", run)
	}
	body.Set("extractor", capture.ExtractorStatus()) // 기억 추출: which CLI runs it and whether it can (home start checklist)
	// Scores from the last skill-checks scan; left out before the first one so the server keeps its copy.
	if checks, tools := checksReport(); checks != nil {
		body.Set("skill_checks", checks)
		body.Set("skill_check_tools", tools)
	}
	if _, err := host.Post("/v1/agent_tools", body, 30*time.Second); err != nil {
		return err
	}
	if _, err := syncReviews(false); err != nil { // a GitHub outage must not stop the tool switches
		_ = host.Metric(map[string]any{"event": "code_reviews_failed", "error": pyErrName(err)})
	}
	if len(results) > 0 {
		_ = host.Metric(map[string]any{"event": "agent_tools_applied", "done": done, "failed": failed})
	}
	return nil
}

// syncAgentsApply applies the pending requests in doc; a malformed one stops the sync.
func syncAgentsApply(doc map[string]any) ([]any, int, int, error) {
	stash, results := loadStash(), []any{}
	done, failed := 0, 0
	for _, r := range asList(doc["requests"]) {
		req := asMap(r)
		if req["status"] != "pending" {
			continue
		}
		if req["kind"] == "mcp_login" {
			startLogin(req)
			continue
		}
		for _, k := range []string{"agent", "kind", "name", "enabled", "id"} { // a malformed request stops the sync, as a KeyError did
			if _, ok := req[k]; !ok {
				return nil, 0, 0, keyErr(k)
			}
		}
		id := req["id"]
		if res, ok := applyCatalogRequest(req, stash); ok { // skill_install, mcp_add: results with a detail (skillinstall.go)
			results = append(results, res)
			if objGet(res, "status") == "done" {
				done++
			} else {
				failed++
			}
		} else if err := applyRequest(req, stash); err != nil {
			results = append(results, host.Obj{{Key: "id", Value: id}, {Key: "status", Value: "failed"}, {Key: "error", Value: runeCut(err.Error(), 300)}})
			failed++
		} else {
			results = append(results, host.Obj{{Key: "id", Value: id}, {Key: "status", Value: "done"}})
			done++
		}
	}
	return results, done, failed, nil
}

// ---------------------------------------------------------------- MCP sign-in (mcp_login)

const loginMaxSeconds = 20 * 60 // longer than any sign-in (10 minutes for the browser, a minute either side)

var (
	ansiRx    = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07]*\x07")
	authURLRx = regexp.MustCompile(`https://\S+redirect_uri=\S+`)
	addressRx = regexp.MustCompile(`\S*(https?://|[?&](code|state)=)\S*`)
)

// loginBusy: a sign-in already runs for this request; a marker left by a killed one stops counting after 20 minutes.
func loginBusy(marker string) bool {
	st, err := os.Stat(marker)
	return err == nil && time.Since(st.ModTime()).Seconds() < loginMaxSeconds
}

// startLogin hands one sign-in to its own process. Under systemd it gets its own transient unit:
// this minute's oneshot sync ends and systemd would otherwise kill everything left in its cgroup.
func startLogin(req map[string]any) {
	agent, name, id := str(req["agent"]), str(req["name"]), str(req["id"])
	if _, ok := loginCLIs[agent]; !ok || !agentNameRx.MatchString(name) || !loginIDRx.MatchString(id) {
		return
	}
	if loginBusy(filepath.Join(agentsState, "login-"+id)) {
		return
	}
	argv := []string{host.Executable(), "agents", "login", id, agent, name}
	if _, err := lookPath("systemd-run"); err == nil && os.Getenv("INVOCATION_ID") != "" {
		unit := []string{"systemd-run", "--quiet", "--collect", "--unit=arkism-login-" + id, fmt.Sprintf("--property=RuntimeMaxSec=%d", loginMaxSeconds),
			"--setenv=HOME=" + agentsHome, "--setenv=PATH=" + os.Getenv("PATH")}
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "ARKISM_") {
				unit = append(unit, "--setenv="+kv)
			}
		}
		argv = append(unit, argv...)
	}
	_ = startDetached(argv)
}

func postLoginStep(reqID string, body host.Obj) error {
	_, err := host.Post("/v1/agent_tools/requests/"+reqID+"/login", append(host.Obj{{Key: "scope", Value: host.Scope()}}, body...), 30*time.Second)
	return err
}

func getLoginRequest(reqID string) (map[string]any, error) {
	res, err := host.Get("/v1/agent_tools?"+scopeQuery(host.Scope()), 15*time.Second)
	if err != nil {
		return nil, err
	}
	for _, r := range asList(orMap(res["agent_tools"])["requests"]) {
		if asMap(r)["id"] == reqID {
			return asMap(r), nil
		}
	}
	return nil, nil
}

// terminal is the CLI under a wide pseudo-terminal: both CLIs only take a pasted redirect from a terminal.
type terminal struct {
	master *os.File
	cmd    *exec.Cmd
	done   chan struct{}
	text   string
}

// poll is Popen.poll(): the exit code once the CLI ended.
func (t *terminal) poll() (int, bool) {
	select {
	case <-t.done:
		return t.cmd.ProcessState.ExitCode(), true
	default:
		return 0, false
	}
}

func (t *terminal) running() bool { _, ended := t.poll(); return !ended }

func (t *terminal) read(wait time.Duration) {
	buf := make([]byte, 65536)
	_ = t.master.SetReadDeadline(time.Now().Add(wait)) // the master is opened O_NONBLOCK, so deadlines apply
	n, err := t.master.Read(buf)
	if n > 0 {
		t.text += ansiRx.ReplaceAllString(decodeReplace(buf[:n]), "")
	} else if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		time.Sleep(wait) // the CLI closed the terminal
	}
}

func (t *terminal) write(line string) { _, _ = t.master.Write([]byte(line + "\r")) } // a closed terminal shows up in read

// lastLine is the CLI's last words for an error, without any address: the echoed redirect carries the one-time code.
func lastLine(text string) string {
	var lines []string
	for _, x := range splitLines(strings.ReplaceAll(text, "\r", "\n")) {
		if x = pyStrip(x); x != "" {
			lines = append(lines, x)
		}
	}
	last := ""
	if len(lines) > 0 {
		last = lines[len(lines)-1]
	}
	return runeCut(addressRx.ReplaceAllString(last, "(주소)"), 300)
}

func (t *terminal) close() {
	if t.running() {
		t.cmd.Process.Kill()
		<-t.done
	}
	t.master.Close()
}

func errOr(msg, fallback string) error {
	if msg == "" {
		msg = fallback
	}
	return runtimeError{msg}
}

// waitAuthURL is the sign-in address the CLI prints, and where it waits for the redirect (localhost or 127.0.0.1).
func waitAuthURL(t *terminal) (string, *url.URL, error) {
	deadline := time.Now().Add(60 * time.Second)
	for !authURLRx.MatchString(t.text) && t.running() && time.Now().Before(deadline) {
		t.read(500 * time.Millisecond)
	}
	authURL := authURLRx.FindString(t.text)
	if authURL == "" {
		return "", nil, errOr(lastLine(t.text), "로그인 주소를 받지 못했어요")
	}
	u, err := url.Parse(authURL)
	if err != nil {
		return "", nil, err
	}
	q, _ := url.ParseQuery(u.RawQuery) // like parse_qs, keep the good pairs of a partly bad query
	if len(q["redirect_uri"]) == 0 {
		return "", nil, keyErr("redirect_uri")
	}
	redirect, err := url.Parse(q["redirect_uri"][0])
	if err != nil {
		return "", nil, err
	}
	port, perr := strconv.Atoi(redirect.Port())
	if redirect.Scheme != "http" || !containsStr([]string{"localhost", "127.0.0.1"}, strings.ToLower(redirect.Hostname())) || perr != nil || port == 0 {
		return "", nil, runtimeError{"로그인 주소가 이 서버로 돌아오지 않아요"}
	}
	return authURL, redirect, nil
}

// waitCallback is the path and query the owner's browser came back to, handed over through Arkism.
func waitCallback(reqID string, t *terminal) (string, error) {
	deadline := time.Now().Add(600 * time.Second)
	for time.Now().Before(deadline) && t.running() {
		t.read(2 * time.Second)
		req, err := loginRequest(reqID)
		if err != nil {
			return "", err
		}
		if req["status"] != "waiting" {
			return "", nil // replaced, timed out or ended elsewhere
		}
		if cb := str(req["callback"]); strings.HasPrefix(cb, "/") {
			return cb, nil
		}
	}
	return "", runtimeError{"로그인 창에서 10분 안에 돌아오지 않았어요"}
}

var loopbackClient = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// replay opens the redirect on the CLI's own loopback listener, as if the browser had reached it.
func replay(redirect *url.URL, callback string) {
	res, err := loopbackClient.Get("http://" + redirect.Host + callback)
	if err != nil {
		return // the CLI may only take the pasted address; finishLogin pastes it
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
}

func finishLogin(t *terminal, pasted string) error {
	deadline, done := time.Now().Add(30*time.Second), false
	for t.running() && time.Now().Before(deadline) {
		t.read(time.Second)
		if !done && time.Now().After(deadline.Add(-20*time.Second)) { // still asking after 10 s: paste it instead
			t.write(pasted)
			done = true
		}
	}
	if code, ended := t.poll(); !ended || code != 0 {
		return errOr(lastLine(t.text), "로그인을 끝내지 못했어요")
	}
	return nil
}

func runLogin(reqID, agent, name string) error {
	t, err := openTerminal([]string{loginCLIs[agent](), "mcp", "login", name, "--no-browser"})
	if err != nil {
		return err
	}
	defer t.close()
	authURL, redirect, err := waitAuthURL(t)
	if err != nil {
		return err
	}
	if err := loginStep(reqID, host.Obj{{Key: "status", Value: "waiting"}, {Key: "auth_url", Value: authURL}}); err != nil {
		return err
	}
	callback, err := waitCallback(reqID, t)
	if err != nil || callback == "" {
		return err
	}
	replay(redirect, callback)
	if err := finishLogin(t, "http://"+redirect.Hostname()+":"+redirect.Port()+callback); err != nil { // the CLI's own loopback
		return err
	}
	return loginStep(reqID, host.Obj{{Key: "status", Value: "done"}})
}

func agentsLogin(reqID, agent, name string) error {
	if err := os.MkdirAll(agentsState, 0o700); err != nil {
		return err
	}
	marker := filepath.Join(agentsState, "login-"+reqID)
	if !loginBusy(marker) {
		os.Remove(marker) // left by a sign-in that was killed
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return err
	}
	f.Close()
	if err := runLogin(reqID, agent, name); err != nil {
		_ = loginStep(reqID, host.Obj{{Key: "status", Value: "failed"}, {Key: "error", Value: runeCut(err.Error(), 300)}}) // the request times out on its own
	}
	os.Remove(marker)
	os.Remove(filepath.Join(agentsState, "claude-auth.json")) // re-check sign-ins now, not in ten minutes
	return syncAgents()
}

func agentsMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch {
	case cmd == "sync":
		err = syncAgents()
	case cmd == "login" && len(args) == 4 && loginCLIs[args[2]] != nil:
		err = agentsLogin(args[1], args[2], args[3])
	case cmd == "inventory":
		var servers, plugins []any
		if servers, plugins, err = inventory(); err == nil {
			printJSON(stdout, host.Obj{{Key: "servers", Value: servers}, {Key: "plugins", Value: plugins}}, 1)
		}
	default:
		_, _ = io.WriteString(stdout, agentsUsage+"\n")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
