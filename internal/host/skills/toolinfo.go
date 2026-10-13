package skills

// What 설정 › MCP·플러그인 and 할 일 show beyond the on/off switches, read on the server.
//
//	toolCalls()       today's MCP tool calls per server, from Claude Code transcripts (tool name, ok, time)
//	serverTools()     the tools an MCP server lists (stdio: one short-lived tools/list), cached a day
//	pluginContents()  a Claude Code plugin's version, description and bundled skills, MCP servers and commands
//	skillUpdates()    skills whose git repository has newer commits upstream; updateRepo() pulls one forward
//
// agents sync calls these every minute and reports names and counts only: never arguments,
// results, environment or tokens.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/FelixBaek/arkism/internal/host"
)

var (
	tiState        = host.AgentsState
	claudeProjects = host.Env("ARKISM_CLAUDE_PROJECTS", filepath.Join(host.Home, ".claude/projects"))
	toolsCache     = filepath.Join(host.AgentsState, "mcp-tools.json")
	updatesCache   = filepath.Join(host.AgentsState, "skill-updates.json")
)

const (
	toolsTTL   = 24 * 3600
	updatesTTL = 6 * 3600
)

var writeVerbs = []string{"create", "update", "delete", "remove", "merge", "send", "write", "post", "put", "push", "publish", "close", "transfer",
	"pay", "exec", "run", "deploy", "move", "rename", "upload", "cancel", "approve", "archive", "set"}

func writeState(path string, v any) error {
	if err := os.MkdirAll(tiState, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(host.MustDumps(v, host.DumpOpts{NoASCII: true})), 0o666)
}

func readOrdered(path string) host.Obj {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, _ := loadsOrdered(string(raw))
	o, _ := v.(host.Obj)
	return o
}

var notServerChar = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// normServer is Claude Code's form of a server name inside mcp__<server>__<tool>.
func normServer(name string) string { return notServerChar.ReplaceAllString(name, "_") }

// ---------------------------------------------------------------- recent calls

// parseAware is datetime.fromisoformat after "Z" became "+00:00"; a time without an offset is refused.
func parseAware(s string) (time.Time, bool) {
	s = strings.ReplaceAll(s, "Z", "+00:00")
	for _, layout := range []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type mcpCall struct {
	server, tool string
	ok           bool
	at           time.Time
}

func transcriptCalls(path string, since time.Time) []mcpCall {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	type pend struct {
		name string
		at   time.Time
	}
	pending, out := map[string]pend{}, []mcpCall(nil)
	for _, line := range splitLines(decodeReplace(raw)) {
		if !strings.Contains(line, `"tool_use"`) && !strings.Contains(line, `"tool_result"`) {
			continue
		}
		v, err := host.Loads(line)
		row, isMap := v.(map[string]any)
		ts, isStr := row["timestamp"].(string)
		if err != nil || !isMap || !isStr {
			continue
		}
		at, ok := parseAware(ts)
		if !ok {
			continue
		}
		for _, p := range asList(orMap(row["message"])["content"]) {
			part := asMap(p)
			name := pyStr(getOr(part, "name", ""))
			if part["type"] == "tool_use" && strings.HasPrefix(name, "mcp__") && !at.Before(since) {
				pending[pyStr(part["id"])] = pend{name, at}
			} else if id := pyStr(part["tool_use_id"]); part["type"] == "tool_result" && pendingHas(pending, id) {
				c := pending[id]
				delete(pending, id)
				pieces := append(strings.SplitN(c.name, "__", 3), "", "")
				out = append(out, mcpCall{pieces[1], pieces[2], !truthy(part["is_error"]), c.at})
			}
		}
	}
	return out
}

func pendingHas[V any](m map[string]V, k string) bool { _, ok := m[k]; return ok }

// toolCalls is {server: {"today": n, "recent": [{tool, ok, at}, …5]}} for today's MCP calls (since local midnight).
func toolCalls(now time.Time) host.Obj {
	if now.IsZero() {
		now = time.Now()
	}
	local := now.Local()
	since := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	paths, _ := filepath.Glob(filepath.Join(claudeProjects, "*", "*.jsonl"))
	var calls []mcpCall
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.ModTime().Before(since) {
			continue
		}
		calls = append(calls, transcriptCalls(p, since)...)
	}
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].at.After(calls[j].at) })
	out := host.Obj{}
	for _, c := range calls {
		v, ok := out.Get(c.server)
		if !ok {
			v = host.Obj{{Key: "today", Value: 0}, {Key: "recent", Value: []any{}}}
		}
		row := v.(host.Obj)
		row.Set("today", objGet(row, "today").(int)+1)
		if recent := objGet(row, "recent").([]any); len(recent) < 5 && c.tool != "" {
			row.Set("recent", append(recent, host.Obj{{Key: "tool", Value: runeCut(c.tool, 128)}, {Key: "ok", Value: c.ok}, {Key: "at", Value: c.at.Format("2006-01-02T15:04:05Z")}}))
		}
		out.Set(c.server, row)
	}
	return out
}

// ---------------------------------------------------------------- tool lists

var nonWord = func(r rune) bool { return r == '_' || !(unicode.IsLetter(r) || unicode.IsDigit(r)) }

func risky(tool map[string]any) bool {
	hints := orMap(tool["annotations"])
	if hints["readOnlyHint"] == true {
		return false
	}
	if d, ok := hints["destructiveHint"]; ok {
		return truthy(d)
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(str(tool["name"])), nonWord) {
		if containsStr(writeVerbs, w) {
			return true
		}
	}
	return false
}

func rpcID(v any) (float64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case nil, string, []any, map[string]any:
		return 0, false
	}
	return numFloat(v), true
}

// rpcWait is the JSON-RPC response with want from a stdio server, or nil by the deadline.
func rpcWait(lines <-chan []byte, want int, deadline time.Time) map[string]any {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			v, err := host.Loads(string(line))
			msg, isMap := v.(map[string]any)
			if id, ok := rpcID(msg["id"]); err == nil && isMap && ok && id == float64(want) {
				return msg
			}
		}
	}
}

func listTools(stdin interface{ Write([]byte) (int, error) }, lines <-chan []byte, deadline time.Time) ([]any, error) {
	send := func(msg host.Obj) error {
		_, err := stdin.Write([]byte(host.MustDumps(msg, host.DumpOpts{}) + "\n"))
		return err
	}
	if err := send(host.Obj{{Key: "jsonrpc", Value: "2.0"}, {Key: "id", Value: 1}, {Key: "method", Value: "initialize"}, {Key: "params", Value: host.Obj{
		{Key: "protocolVersion", Value: "2025-06-18"}, {Key: "capabilities", Value: host.Obj{}}, {Key: "clientInfo", Value: host.Obj{{Key: "name", Value: "arkism"}, {Key: "version", Value: "1"}}}}}}); err != nil {
		return nil, err
	}
	if rpcWait(lines, 1, deadline) == nil {
		return nil, nil
	}
	if err := send(host.Obj{{Key: "jsonrpc", Value: "2.0"}, {Key: "method", Value: "notifications/initialized"}}); err != nil {
		return nil, err
	}
	tools, cursor := []any{}, any(nil)
	for page := 0; page < 10; page++ {
		params := host.Obj{}
		if truthy(cursor) {
			params = host.Obj{{Key: "cursor", Value: cursor}}
		}
		if err := send(host.Obj{{Key: "jsonrpc", Value: "2.0"}, {Key: "id", Value: 2 + page}, {Key: "method", Value: "tools/list"}, {Key: "params", Value: params}}); err != nil {
			return nil, err
		}
		msg := rpcWait(lines, 2+page, deadline)
		if _, ok := msg["result"]; !ok {
			if len(tools) == 0 {
				return nil, nil
			}
			return tools, nil
		}
		result := orMap(msg["result"])
		tools = append(tools, asList(result["tools"])...)
		if cursor = result["nextCursor"]; !truthy(cursor) {
			break
		}
	}
	return tools, nil
}

// stdioTools is tools/list from a stdio server definition (Claude's shape); nil when it does not answer.
func stdioTools(d map[string]any) []any {
	argv := []string{pyStr(d["command"])}
	for _, a := range asList(d["args"]) {
		argv = append(argv, pyStr(a))
	}
	env := os.Environ()
	for k, v := range orMap(d["env"]) {
		env = append(env, k+"="+pyStr(v))
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	newSession(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	defer func() {
		killGroup(cmd)
		cmd.Wait()
	}()
	lines := make(chan []byte, 64)
	go func() {
		defer close(lines)
		r := bufio.NewReaderSize(stdout, 65536)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return // a partial last line is not a message
			}
			lines <- line
		}
	}()
	tools, err := listTools(stdin, lines, time.Now().Add(20*time.Second))
	if err != nil {
		return nil
	}
	return tools
}

func defDigest(d map[string]any) string {
	kept := map[string]any{}
	for k, v := range d {
		if !strings.HasPrefix(k, "_") {
			kept[k] = v
		}
	}
	return digest([]byte(host.MustDumps(kept, host.DumpOpts{SortKeys: true})))[:16]
}

// serverTools is {server: [{name, desc, risky}]} from the day-old cache; it probes at most budget stale
// stdio servers per call, so one slow server never holds up the minute sync.
func serverTools(defs map[string]any, budget int, prober func(map[string]any) []any) (host.Obj, error) {
	if prober == nil {
		prober = stdioTools
	}
	cache := readOrdered(toolsCache)
	if cache == nil {
		cache = host.Obj{}
	}
	now := nowFloat()
	for _, name := range sortedKeys(defs) {
		if budget <= 0 {
			break
		}
		d := asMap(defs[name])
		key := defDigest(d)
		hit := asObj(objGet(cache, name))
		if objGet(hit, "key") == key && now-numFloat(getAt(hit)) < toolsTTL {
			continue
		}
		if !truthy(d["command"]) || truthy(d["url"]) {
			continue // http servers need their own login; only stdio servers are asked
		}
		budget--
		raw := prober(d)
		tools := []any{}
		for _, t := range raw {
			tm := asMap(t)
			if !truthy(tm["name"]) {
				continue
			}
			tools = append(tools, host.Obj{{Key: "name", Value: pyStrip(runeCut(pyStr(tm["name"]), 128))},
				{Key: "desc", Value: runeCut(strings.Join(strings.Fields(pyStr(orStr(tm["description"]))), " "), 300)}, {Key: "risky", Value: risky(tm)}})
		}
		cache.Set(name, host.Obj{{Key: "key", Value: key}, {Key: "at", Value: now}, {Key: "tools", Value: tools[:min(len(tools), 300)]}, {Key: "ok", Value: raw != nil}})
	}
	if err := writeState(toolsCache, cache); err != nil {
		return nil, err
	}
	out := host.Obj{}
	for _, kv := range cache {
		if _, ok := defs[kv.Key]; ok && truthy(objGet(asObj(kv.Value), "tools")) {
			out = append(out, host.KV{Key: kv.Key, Value: objGet(asObj(kv.Value), "tools")})
		}
	}
	return out, nil
}

func getAt(o host.Obj) any {
	if v, ok := o.Get("at"); ok {
		return v
	}
	return 0
}

// orStr is `v or ""`.
func orStr(v any) any {
	if !truthy(v) {
		return ""
	}
	return v
}

// ---------------------------------------------------------------- plugin contents

// front is name and description from a SKILL.md or command file's front matter.
func front(text string) map[string]string {
	out := map[string]string{}
	if !strings.HasPrefix(text, "---") {
		return out
	}
	for _, line := range splitLines(strings.SplitN(text, "---", 3)[1]) {
		k, v, _ := strings.Cut(line, ":")
		k, v = pyStrip(k), pyStrip(v)
		if (k == "name" || k == "description") && v != "" {
			out[k] = strings.Trim(v, `"'`)
		}
	}
	return out
}

func readHead(path string, n int) (string, error) {
	raw, err := os.ReadFile(path)
	return runeCut(decodeReplace(raw), n), err
}

func pluginMeta(root string) host.Obj {
	for _, f := range []string{filepath.Join(root, ".claude-plugin/plugin.json"), filepath.Join(root, "plugin.json")} {
		raw, err := os.ReadFile(f)
		if err != nil || !utf8Valid(raw) {
			continue
		}
		if v, err := loadsOrdered(string(raw)); err == nil {
			o, _ := v.(host.Obj)
			return o
		}
	}
	return host.Obj{}
}

func sortedGlob(patterns ...string) []string {
	var out []string
	for _, p := range patterns {
		m, _ := filepath.Glob(p)
		out = append(out, m...)
	}
	sortPaths(out)
	return out
}

type pluginItem struct{ kind, name, desc string }

func pluginServerNames(v any) []string {
	var out []string
	switch x := v.(type) {
	case host.Obj:
		for _, kv := range x {
			out = append(out, pyStrip(runeCut(kv.Key, 128)))
		}
	case []any:
		for _, n := range x {
			out = append(out, pyStrip(runeCut(pyStr(n), 128)))
		}
	}
	return out
}

func pluginItems(root string, meta host.Obj) []pluginItem {
	var items []pluginItem
	for _, skill := range sortedGlob(filepath.Join(root, "skills", "*", "SKILL.md")) {
		text, err := readHead(skill, 4000)
		if err != nil {
			continue
		}
		fm := front(text)
		name := fm["name"]
		if name == "" {
			name = filepath.Base(filepath.Dir(skill))
		}
		items = append(items, pluginItem{"skill", pyStrip(runeCut(name, 128)), runeCut(fm["description"], 300)})
	}
	servers := objGet(meta, "mcpServers")
	if o, ok := servers.(host.Obj); ok {
		for _, n := range pluginServerNames(o) {
			items = append(items, pluginItem{kind: "mcp", name: n})
		}
	}
	files := []string{filepath.Join(root, ".mcp.json")}
	if s, ok := servers.(string); ok {
		if filepath.IsAbs(s) {
			files = append(files, s)
		} else {
			files = append(files, filepath.Join(root, s))
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil || !utf8Valid(raw) {
			continue
		}
		v, err := loadsOrdered(string(raw))
		if err != nil {
			continue
		}
		if o, ok := v.(host.Obj); ok {
			if inner := objGet(o, "mcpServers"); truthy(inner) {
				v = inner
			}
		}
		for _, n := range pluginServerNames(v) {
			items = append(items, pluginItem{kind: "mcp", name: n})
		}
	}
	for _, c := range sortedGlob(filepath.Join(root, "commands", "*.md"), filepath.Join(root, "commands", "*.toml")) {
		desc := ""
		if strings.HasSuffix(c, ".md") {
			text, err := readHead(c, 2000)
			if err != nil {
				continue
			}
			desc = runeCut(front(text)["description"], 300)
		}
		stem := strings.TrimSuffix(filepath.Base(c), filepath.Ext(c))
		items = append(items, pluginItem{"command", "/" + runeCut(stem, 120), desc})
	}
	return items
}

// pluginContents is {version, desc, contents} of one installed plugin folder; empty when it cannot be read.
func pluginContents(installPath string) host.Obj {
	root := installPath
	if root == "" {
		root = "."
	}
	if !isDir(root) {
		return host.Obj{}
	}
	meta := pluginMeta(root)
	seen := map[[2]string]bool{}
	unique := []any{}
	for _, it := range pluginItems(root, meta) {
		if seen[[2]string{it.kind, it.name}] {
			continue
		}
		seen[[2]string{it.kind, it.name}] = true
		row := host.Obj{{Key: "kind", Value: it.kind}}
		if it.name != "" {
			row.Set("name", it.name)
		}
		if it.desc != "" {
			row.Set("desc", it.desc)
		}
		unique = append(unique, row)
	}
	return host.Obj{{Key: "version", Value: runeCut(pyStr(orStr(objGet(meta, "version"))), 40)},
		{Key: "desc", Value: runeCut(strings.Join(strings.Fields(pyStr(orStr(objGet(meta, "description")))), " "), 300)},
		{Key: "contents", Value: unique[:min(len(unique), 200)]}}
}

// ---------------------------------------------------------------- skill updates

func git(repo string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.WaitDelay = time.Second // a grandchild holding the pipe must not outlive the timeout
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", err
		}
		msg := errOut.String()
		if msg == "" {
			msg = "git 실패"
		}
		return "", errors.New(runeCut(pyStrip(msg), 200))
	}
	return pyStrip(out.String()), nil
}

var versionRx = regexp.MustCompile(`(?m)^version:\s*['"]?([^'"\n]+)`)

func skillVersionAt(repo, rev, path string) string {
	fm, err := git(repo, 20*time.Second, "show", rev+":"+path)
	if err != nil {
		return ""
	}
	head := ""
	if strings.HasPrefix(fm, "---") {
		head = strings.SplitN(fm, "---", 3)[1]
	}
	m := versionRx.FindStringSubmatch(head)
	if m == nil {
		return ""
	}
	return runeCut(pyStrip(m[1]), 40)
}

func tilde(p string) string {
	if strings.HasPrefix(p, host.Home+"/") {
		return "~" + p[len(host.Home):]
	}
	return p
}

func behindCount(repo string) (int, error) {
	s, err := git(repo, 60*time.Second, "rev-list", "--count", "HEAD..@{u}")
	if err != nil || s == "" {
		return 0, err
	}
	return strconv.Atoi(s)
}

// repoUpdates are the skills changed upstream in one repository (after a fetch).
func repoUpdates(repo string) ([]any, error) {
	if _, err := git(repo, 120*time.Second, "fetch", "--quiet"); err != nil {
		return nil, err
	}
	behind, err := behindCount(repo)
	if err != nil || behind == 0 {
		return nil, err
	}
	diff, err := git(repo, 60*time.Second, "diff", "--name-only", "HEAD", "@{u}")
	if err != nil {
		return nil, err
	}
	var out []any
	for _, path := range splitLines(diff) {
		if !strings.HasSuffix(path, "SKILL.md") {
			continue
		}
		name := filepath.Base(filepath.Dir(path))
		from, to := skillVersionAt(repo, "HEAD", path), skillVersionAt(repo, "@{u}", path)
		if from == "" {
			if from, err = git(repo, 60*time.Second, "rev-parse", "--short", "HEAD"); err != nil {
				return nil, err
			}
		}
		if to == "" {
			if to, err = git(repo, 60*time.Second, "rev-parse", "--short", "@{u}"); err != nil {
				return nil, err
			}
		}
		out = append(out, host.Obj{{Key: "name", Value: runeCut(name, 128)}, {Key: "repo", Value: tilde(repo)}, {Key: "from", Value: from}, {Key: "to", Value: to}, {Key: "commits", Value: behind}})
	}
	return out, nil
}

func expandHome(p string) string {
	if p == "~" {
		return host.Home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(host.Home, p[2:])
	}
	return p
}

// skillRepos are the git repositories (with an upstream) holding the given skill folders.
func skillRepos(dirs []string) []string {
	repos := map[string]bool{}
	for _, d := range dirs {
		p := expandHome(d)
		if !isDir(p) {
			continue
		}
		// The folder's own repository, and skills installed as their own clones one level down.
		cands := []string{p}
		entries, _ := os.ReadDir(p)
		for _, e := range entries {
			if c := filepath.Join(p, e.Name()); exists(filepath.Join(c, ".git")) {
				cands = append(cands, c)
			}
		}
		for _, c := range cands {
			if top := upstreamRepo(c); top != "" {
				repos[top] = true
			}
		}
	}
	return sortedKeys(repos)
}

func upstreamRepo(path string) string {
	top, err := git(path, 10*time.Second, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	if _, err := git(top, 10*time.Second, "rev-parse", "--abbrev-ref", "@{u}"); err != nil {
		return ""
	}
	return top
}

var (
	schemeRx = regexp.MustCompile(`^[a-z+]+://([^/@]*@)?`)
	scpRx    = regexp.MustCompile(`^[^@/\s]+@([^:/\s]+):`)
)

// publicRemote is a remote address a person can read: no scheme, no user or token, no .git.
func publicRemote(u string) string {
	u = schemeRx.ReplaceAllString(pyStrip(u), "")
	u = scpRx.ReplaceAllString(u, "$1/")
	return strings.TrimSuffix(u, ".git")
}

// cutBytes keeps at most limit UTF-8 bytes, never half a character (the server limits bytes).
func cutBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}

// repoStatus is one repository as the skill screen's 업데이트 section shows it (after repoUpdates fetched).
// The address is the remote of the branch's own upstream, the one git compared against.
func repoStatus(repo string, now float64) (host.Obj, error) {
	branch, err := git(repo, 60*time.Second, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return nil, err
	}
	name, err := git(repo, 60*time.Second, "config", "--get", "branch."+branch+".remote") // remote names may contain "/", so ask git
	if err != nil {
		return nil, err
	}
	remote := ""
	if name != "" && name != "." { // "." = a local branch is the upstream
		raw, err := git(repo, 60*time.Second, "remote", "get-url", name)
		if err != nil {
			return nil, err
		}
		remote = publicRemote(raw)
	}
	if strings.ContainsAny(remote, " \t\r\n@") { // the server rejects these: show the folder instead of failing the whole report
		remote = ""
	}
	head, err := git(repo, 60*time.Second, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, err
	}
	behind, err := behindCount(repo)
	if err != nil {
		return nil, err
	}
	at := time.Unix(int64(now), 0).UTC().Format("2006-01-02T15:04:05Z")
	return host.Obj{{Key: "repo", Value: tilde(repo)}, {Key: "remote", Value: cutBytes(remote, 200)}, {Key: "head", Value: runeCut(head, 40)},
		{Key: "behind", Value: behind}, {Key: "checked_at", Value: at}}, nil
}

// skillUpdates asks git every six hours and sends the last answer in between; the same pass records
// each repository's status for skillRepoChecks.
func skillUpdates(dirs []string, now float64) ([]any, error) {
	if now == 0 {
		now = nowFloat()
	}
	cache := readOrdered(updatesCache)
	items, itemsOK := objGet(cache, "items").([]any)
	_, reposOK := objGet(cache, "repos").([]any)
	if now-numFloat(getAt(cache)) < updatesTTL && itemsOK && reposOK {
		return items, nil
	}
	out, repos := []any{}, []any{}
	for _, repo := range skillRepos(dirs) {
		ups, err := repoUpdates(repo)
		if err != nil {
			continue
		}
		out = append(out, ups...)
		status, err := repoStatus(repo, now)
		if err != nil {
			continue
		}
		if r := str(objGet(status, "repo")); len(r) <= 200 && !strings.Contains(r, "..") { // one bad entry must not reject the whole report
			repos = append(repos, status)
		}
	}
	out, repos = out[:min(len(out), 500)], repos[:min(len(repos), 200)]
	if err := writeState(updatesCache, host.Obj{{Key: "at", Value: now}, {Key: "items", Value: out}, {Key: "repos", Value: repos}}); err != nil {
		return nil, err
	}
	return out, nil
}

// skillRepoChecks is every tracked repository with its last check time (nil before the first check).
func skillRepoChecks() []any {
	repos, _ := objGet(readOrdered(updatesCache), "repos").([]any)
	return repos
}

func skillRepo(repo string) (string, error) {
	p := expandHome(repo)
	if strings.Contains(repo, "..") || !exists(filepath.Join(p, ".git")) {
		return "", errors.New("스킬 저장소가 아니에요")
	}
	return p, nil
}

// recheck forgets the cached answer so the next sync asks the upstream again (the 업데이트 확인 button).
func recheck(repo string) error {
	if _, err := skillRepo(repo); err != nil {
		return err
	}
	os.Remove(updatesCache)
	return nil
}

// updateRepo pulls one skill repository forward without merging; the next sync re-checks it.
func updateRepo(repo string) error {
	p, err := skillRepo(repo)
	if err != nil {
		return err
	}
	if _, err := git(p, 120*time.Second, "pull", "--ff-only", "--quiet"); err != nil {
		return err
	}
	os.Remove(updatesCache)
	return nil
}
