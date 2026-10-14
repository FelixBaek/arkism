package skills

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Agent kit: what the owner puts in one place for every agent. Skills in host.SkillsRoot and in
// each plugin's skills/ reach all agents through the hub (syncRoots). MCP servers (one folder per
// server in host.McpRoot with an arkism-mcp.json, and each plugin's .mcp.json) go into every
// installed agent's MCP config. Plugin hooks go only to Claude Code and Codex, the agents that read
// Claude-format hooks. sync-skills plans this and applies it only with --execute.

const kitManifest = "arkism-mcp.json" // {"name", "command", "args", "env", "description"}

// kitMCPAgents are the agents whose MCP config Arkism writes (the 연결 › MCP switches).
var kitMCPAgents = []string{"claude_code", "codex", "gemini", "zcode", "grok", "opencode", "cursor"}

// kitHookFiles: the agents that take plugin hooks, and the file holding them.
var kitHookFiles = []struct{ agent, file string }{{"claude_code", ".claude/settings.json"}, {"codex", ".codex/hooks.json"}}

type kitServer struct {
	name, desc, from string
	def              host.Obj
}

type kitPlugin struct {
	name, desc, dir string
	skills          bool
	servers         []kitServer
	hooks           host.Obj
}

// kitText reads a kit file with ${CLAUDE_PLUGIN_ROOT} (and ${MCP_DIR}) pointing at its folder.
func kitText(path, dir string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	quoted := host.MustDumps(dir, host.DumpOpts{})
	esc := quoted[1 : len(quoted)-1]
	return strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", esc, "${MCP_DIR}", esc).Replace(string(raw)), nil
}

// kitDef turns one server entry (Claude's shape) into the definition addServerDef writes.
func kitDef(m map[string]any) host.Obj {
	if u := str(m["url"]); u != "" {
		return host.Obj{{Key: "type", Value: "http"}, {Key: "url", Value: u}}
	}
	if str(m["command"]) == "" {
		return nil
	}
	d := host.Obj{{Key: "type", Value: "stdio"}, {Key: "command", Value: m["command"]}, {Key: "args", Value: firstOr(m["args"], []any{})}}
	if env := orMap(m["env"]); len(env) > 0 {
		d = append(d, host.KV{Key: "env", Value: env})
	}
	return d
}

func kitServers() []kitServer {
	var out []kitServer
	for _, f := range sortedGlob(filepath.Join(host.McpRoot, "*", kitManifest)) {
		text, err := kitText(f, filepath.Dir(f))
		if err != nil {
			continue
		}
		v, err := host.Loads(text)
		m := asMap(v)
		if err != nil || !agentNameRx.MatchString(str(m["name"])) || kitDef(m) == nil {
			continue
		}
		out = append(out, kitServer{name: str(m["name"]), desc: runeCut(str(m["description"]), 300), from: f, def: kitDef(m)})
	}
	return out
}

func kitPlugins() []kitPlugin {
	var out []kitPlugin
	for _, dir := range sortedGlob(filepath.Join(host.PluginsRoot, "*")) {
		if !isDir(dir) {
			continue
		}
		meta := pluginMeta(dir)
		p := kitPlugin{name: str(objGet(meta, "name")), desc: runeCut(str(objGet(meta, "description")), 300), dir: dir, skills: isDir(filepath.Join(dir, "skills"))}
		if p.name == "" {
			p.name = filepath.Base(dir)
		}
		if !agentNameRx.MatchString(p.name) {
			continue
		}
		f := filepath.Join(dir, ".mcp.json")
		if text, err := kitText(f, dir); err == nil {
			v, _ := host.Loads(text)
			servers := asMap(v)
			if inner := asMap(servers["mcpServers"]); inner != nil {
				servers = inner
			}
			for _, name := range sortedKeys(servers) {
				if d := kitDef(asMap(servers[name])); d != nil && agentNameRx.MatchString(name) {
					p.servers = append(p.servers, kitServer{name: name, desc: p.desc, from: f, def: d})
				}
			}
		}
		if text, err := kitText(filepath.Join(dir, "hooks", "hooks.json"), dir); err == nil {
			if v, err := loadsOrdered(text); err == nil {
				if o, ok := v.(host.Obj); ok {
					p.hooks, _ = objGet(o, "hooks").(host.Obj)
				}
			}
		}
		out = append(out, p)
	}
	return out
}

// kitAgents are the MCP agents installed here and not taken off in 설정 › 에이전트.
func kitAgents() []string {
	var out []string
	for _, a := range knownAgents {
		if containsStr(kitMCPAgents, a.id) && isDir(expand(a.home)) && !host.AgentOff(a.id) {
			out = append(out, a.id)
		}
	}
	return out
}

// agentMCPNames is every server an agent has, live or switched off (stashed).
func agentMCPNames(agent string, stash map[string]any) map[string]any {
	out := map[string]any{}
	switch agent {
	case "claude_code":
		out = claudeServers()
	case "codex":
		out = codexConfigServers()
	default:
		for _, a := range mcpFileAgents {
			if a.id == agent {
				out = a.servers()
			}
		}
	}
	for name, d := range orMap(stash[agent]) {
		if _, has := out[name]; !has {
			m := map[string]any{}
			for k, v := range asMap(d) {
				m[k] = v
			}
			m["_enabled"] = false
			out[name] = m
		}
	}
	return out
}

// codexConfigServers reads ~/.codex/config.toml directly: the codex CLI is too slow for the router.
func codexConfigServers() map[string]any {
	out := map[string]any{}
	raw, err := os.ReadFile(filepath.Join(agentsHome, ".codex", "config.toml"))
	if err != nil {
		return out
	}
	servers, err := tomlServers(string(raw))
	if err != nil {
		return out
	}
	for name, s := range servers {
		out[name] = fromCodexShape(s.def)
	}
	return out
}

// mergeHooks adds each hook group not already in the file's "hooks"; it writes only when asked.
func mergeHooks(path string, add host.Obj, write bool) (bool, error) {
	text := "{}"
	if raw, err := os.ReadFile(path); err == nil {
		text = string(raw)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	v, err := loadsOrdered(text)
	doc, ok := v.(host.Obj)
	if err != nil || !ok {
		return false, errMCPLayout
	}
	hooks, _ := objGet(doc, "hooks").(host.Obj)
	changed := false
	for _, kv := range add {
		groups := asList(objGet(hooks, kv.Key))
		for _, g := range asList(kv.Value) {
			if !slicesContainsJSON(groups, g) {
				groups, changed = append(groups, g), true
			}
		}
		hooks.Set(kv.Key, groups)
	}
	if !changed || !write {
		return changed, nil
	}
	doc.Set("hooks", hooks)
	out, err := dumpIndent(doc, 2, false)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	return true, writeAtomic(path, out+"\n")
}

func slicesContainsJSON(list []any, v any) bool {
	for _, x := range list {
		if sameJSON(x, v) {
			return true
		}
	}
	return false
}

// kitRun plans (and with execute applies) the kit: one row per action with its state:
// planned, applied, exists (already there or switched off by the owner), via_hub, or failed: <why>.
func kitRun(execute bool) []any {
	servers, plugins := kitServers(), kitPlugins()
	if len(servers) == 0 && len(plugins) == 0 {
		return nil
	}
	agents, stash := kitAgents(), loadStash()
	have := map[string]map[string]any{}
	rows := []any{}
	row := func(kind, name, agent, from, state string) {
		rows = append(rows, host.Obj{{Key: "kind", Value: kind}, {Key: "name", Value: name}, {Key: "agent", Value: agent}, {Key: "from", Value: from}, {Key: "state", Value: state}})
	}
	all := servers
	for _, p := range plugins {
		if p.skills {
			row("skills", p.name, "all", filepath.Join(p.dir, "skills"), "via_hub")
		}
		all = append(all, p.servers...)
	}
	for _, s := range all {
		for _, agent := range agents {
			if have[agent] == nil {
				have[agent] = agentMCPNames(agent, stash)
			}
			state := "planned"
			if _, ok := have[agent][s.name]; ok {
				state = "exists" // a server the owner switched off stays off
			} else if execute {
				state = "applied"
				if err := addServerDef(agent, s.name, s.def); err != nil {
					state = "failed: " + err.Error()
				} else {
					have[agent][s.name] = map[string]any{}
				}
			}
			row("mcp", s.name, agent, s.from, state)
		}
	}
	for _, p := range plugins {
		if len(p.hooks) == 0 {
			continue
		}
		for _, h := range kitHookFiles {
			if !containsStr(agents, h.agent) {
				continue
			}
			changed, err := mergeHooks(filepath.Join(agentsHome, h.file), p.hooks, execute)
			state := map[bool]string{true: "planned", false: "exists"}[changed]
			if err != nil {
				state = "failed: " + err.Error()
			} else if changed && execute {
				state = "applied"
			}
			row("hooks", p.name, h.agent, filepath.Join(p.dir, "hooks", "hooks.json"), state)
		}
	}
	return rows
}

// ---------------------------------------------------------------- router: MCP servers and plugins

var kitIDRx = regexp.MustCompile(`^[A-Za-z0-9@._-]{1,128}$`)

type localTool struct{ kind, name, desc, state string }

func (t localTool) id() string { return t.kind + "-" + t.name }

// localTools is every MCP server and plugin the router can offer beside skills, with its state:
// enabled (live in some agent), disabled (only switched off) or not_installed (kit only).
func localTools() []localTool {
	stash := loadStash()
	state := map[string]string{} // mcp name -> state
	for _, agent := range kitMCPAgents {
		for name, d := range agentMCPNames(agent, stash) {
			if enabledOf(asMap(d)) {
				state[name] = "enabled"
			} else if state[name] == "" {
				state[name] = "disabled"
			}
		}
	}
	or := func(name string) string {
		if s := state[name]; s != "" {
			return s
		}
		return "not_installed"
	}
	var out []localTool
	seen := map[string]bool{}
	add := func(t localTool) {
		if kitIDRx.MatchString(t.name) && !seen[t.id()] {
			seen[t.id()] = true
			out = append(out, t)
		}
	}
	for _, s := range kitServers() {
		add(localTool{"mcp", s.name, s.desc, or(s.name)})
	}
	for _, p := range kitPlugins() {
		// shortcut: a kit plugin's state follows its MCP servers; one with hooks or skills only reads not_installed until hooks are tracked too.
		st := "not_installed"
		for _, s := range p.servers {
			if x := or(s.name); x == "enabled" || x == "disabled" && st != "enabled" {
				st = x
			}
		}
		add(localTool{"plugin", p.name, p.desc, st})
	}
	for _, name := range sortedKeys(state) {
		add(localTool{"mcp", name, "", state[name]})
	}
	settings, _ := readJSONMap(filepath.Join(agentsHome, ".claude", "settings.json"))
	plugins := orMap(settings["enabledPlugins"])
	for _, id := range sortedKeys(plugins) {
		add(localTool{"plugin", id, "", map[bool]string{true: "enabled", false: "disabled"}[plugins[id] == true]})
	}
	return out
}

var wordRx = regexp.MustCompile(`[\p{L}\p{N}]{2,}`)

// matchTools picks up to n local tools whose name or description shares a word with the query.
func matchTools(query string, tools []localTool, n int) []localTool {
	words := wordRx.FindAllString(strings.ToLower(query), -1)
	var out []localTool
	for _, t := range tools {
		hay := strings.ToLower(t.name + " " + t.desc)
		name := strings.ToLower(t.name)
		for _, w := range words {
			if strings.Contains(hay, w) || len(name) > 2 && strings.Contains(w, name) {
				out = append(out, t)
				break
			}
		}
		if len(out) == n {
			break
		}
	}
	return out
}

func (t localTool) item() host.Obj {
	o := host.Obj{{Key: "id", Value: t.id()}, {Key: "kind", Value: t.kind}, {Key: "name", Value: t.name}, {Key: "description", Value: t.desc}, {Key: "state", Value: t.state}}
	switch t.state {
	case "disabled":
		o.Set("note", "Switched off. Call enable_tool with this kind and name: it is enabled and usable from the next agent session.")
	case "not_installed":
		o.Set("note", "Not installed in any agent yet. The owner runs `arkism sync-skills --execute` to distribute it; it is usable from the next agent session.")
	}
	return o
}

// enableTool turns a switched-off MCP server (or a kit plugin's servers, or a Claude Code plugin)
// back on through the same path as the 연결 › MCP/플러그인 switches.
func enableTool(kind, name string) (host.Obj, error) {
	if !kitIDRx.MatchString(name) {
		return nil, runtimeError{"이름이 올바르지 않아요"}
	}
	stash := loadStash()
	names := []string{name}
	if kind == "plugin" {
		names = nil
		for _, p := range kitPlugins() {
			if p.name == name {
				for _, s := range p.servers {
					names = append(names, s.name)
				}
			}
		}
		if names == nil {
			if err := applyRequest(map[string]any{"agent": "claude_code", "kind": "plugin", "name": name, "enabled": true}, stash); err != nil {
				return nil, err
			}
			return host.Obj{{Key: "enabled", Value: []any{"claude_code"}}, {Key: "usable", Value: "next_session"}}, nil
		}
	} else if kind != "mcp" {
		return nil, runtimeError{"kind는 mcp 또는 plugin이어야 해요"}
	}
	done := []any{}
	for _, agent := range kitMCPAgents {
		for _, n := range names {
			if _, off := orMap(stash[agent])[n]; !off {
				continue
			}
			if err := applyRequest(map[string]any{"agent": agent, "kind": "mcp", "name": n, "enabled": true}, stash); err != nil {
				return nil, err
			}
			done = append(done, agent)
		}
	}
	if len(done) == 0 {
		return nil, runtimeError{"꺼 둔 곳이 없어요: 설치가 안 됐다면 sync-skills --execute로 먼저 배포하세요"}
	}
	return host.Obj{{Key: "enabled", Value: done}, {Key: "usable", Value: "next_session"}}, nil
}
