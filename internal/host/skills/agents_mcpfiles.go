package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/FelixBaek/arkism/internal/host"
)

// MCP on/off for agents without a CLI for it (Gemini CLI, ZCode, Grok): their config file is
// edited in place. Only the one server entry changes; every other byte stays as it was, and a
// layout that cannot be edited that way is refused with the file left untouched.

var errMCPLayout = runtimeError{"설정 파일을 안전하게 고칠 수 없어서 그대로 두었어요"}

type mcpFileDef struct {
	canon map[string]any // Claude's shape, as the other agents and the server list read it
	raw   string         // the entry exactly as written in the file
}

type mcpFileAgent struct {
	id, name   string
	home, file string   // relative to agentsHome
	path       []string // JSON key path of the servers object; nil for TOML
	toNative   func(d map[string]any) host.Obj
	fromNative func(v map[string]any) map[string]any
}

var mcpFileAgents = []mcpFileAgent{
	{id: "gemini", name: "Gemini CLI", home: ".gemini", file: ".gemini/settings.json", path: []string{"mcpServers"}, toNative: toGemini, fromNative: fromGemini},
	{id: "zcode", name: "ZCode", home: ".zcode", file: ".zcode/cli/config.json", path: []string{"mcp", "servers"}, toNative: toZcode, fromNative: fromZcode},
	{id: "grok", name: "Grok", home: ".grok", file: ".grok/config.toml"},
	{id: "opencode", name: "OpenCode", home: ".config/opencode", file: ".config/opencode/opencode.json", path: []string{"mcp"}, toNative: toOpenCode, fromNative: fromOpenCode},
	{id: "cursor", name: "Cursor", home: ".cursor", file: ".cursor/mcp.json", path: []string{"mcpServers"}, toNative: toCursor, fromNative: fromCursor},
}

func (a mcpFileAgent) homePath() string { return filepath.Join(agentsHome, a.home) }
func (a mcpFileAgent) filePath() string { return filepath.Join(agentsHome, a.file) }

// load reads the config; a missing file is an empty one.
func (a mcpFileAgent) load() (string, bool, map[string]mcpFileDef, error) {
	raw, err := os.ReadFile(a.filePath())
	if errors.Is(err, os.ErrNotExist) {
		return "", false, map[string]mcpFileDef{}, nil
	}
	if err != nil {
		return "", true, nil, err
	}
	text := string(raw)
	if a.path == nil {
		servers, err := tomlServers(text)
		if err != nil {
			return "", true, nil, err
		}
		out := map[string]mcpFileDef{}
		for name, s := range servers {
			out[name] = mcpFileDef{canon: fromCodexShape(s.def), raw: s.raw(text)}
		}
		return text, true, out, nil
	}
	raws, err := jsonServers(text, a.path)
	if err != nil {
		return "", true, nil, err
	}
	out := map[string]mcpFileDef{}
	for name, r := range raws {
		v, _ := host.Loads(r)
		m, ok := v.(map[string]any)
		if !ok {
			return "", true, nil, errMCPLayout
		}
		out[name] = mcpFileDef{canon: a.fromNative(m), raw: r}
	}
	return text, true, out, nil
}

func (a mcpFileAgent) servers() map[string]any {
	out := map[string]any{}
	if !isDir(a.homePath()) {
		return out
	}
	if _, _, defs, err := a.load(); err == nil {
		for k, d := range defs {
			out[k] = d.canon
		}
	}
	return out
}

func (a mcpFileAgent) remove(text, name string) (string, error) {
	if a.path == nil {
		return tomlRemove(text, name)
	}
	return jsonRemoveServer(text, a.path, name)
}

// add writes name's entry: own is this agent's stashed entry (restored as written), else d is
// another agent's definition translated into this agent's format.
func (a mcpFileAgent) add(text, name string, d map[string]any, own bool) (string, error) {
	raw := ""
	if own {
		raw = str(d["_raw"])
	}
	if a.path == nil {
		if raw == "" {
			stanza, err := tomlStanza(name, cleanDef(d))
			if err != nil {
				return "", err
			}
			raw = stanza
		}
		return tomlAdd(text, name, raw)
	}
	var v any
	if raw != "" {
		parsed, err := loadsOrdered(raw)
		if err != nil {
			return "", errMCPLayout
		}
		v = parsed
	} else {
		v = a.toNative(cleanDef(d))
	}
	return jsonAddServer(text, a.path, name, v)
}

// addNew writes a new server (Claude's shape: type, command/args or url) into this agent's config.
func (a mcpFileAgent) addNew(name string, d map[string]any) error {
	if !isDir(a.homePath()) {
		return runtimeError{a.name + "가 이 컴퓨터에 없어요"}
	}
	text, found, defs, err := a.load()
	if err != nil {
		return errMCPLayout
	}
	if _, has := defs[name]; has {
		return runtimeError{"같은 이름의 서버가 이미 있어요"}
	}
	if !found {
		text = "{}\n"
		if a.path == nil {
			text = ""
		}
		if err := os.MkdirAll(filepath.Dir(a.filePath()), 0o700); err != nil {
			return err
		}
	}
	out, err := a.add(text, name, d, false)
	if err != nil {
		return err
	}
	return writeAtomic(a.filePath(), out)
}

// switchMCP turns name off (stash, then remove) or on (restore) in this agent's config file.
func (a mcpFileAgent) switchMCP(name string, enabled bool, stash map[string]any) error {
	if !isDir(a.homePath()) {
		if !enabled {
			return nil
		}
		return runtimeError{a.name + "가 이 컴퓨터에 없어요"}
	}
	text, found, defs, err := a.load()
	if err != nil {
		return errMCPLayout
	}
	cur, has := defs[name]
	if !enabled {
		if !has {
			return nil
		}
		out, err := a.remove(text, name)
		if err != nil {
			return err
		}
		entry := map[string]any{}
		for k, v := range cur.canon {
			entry[k] = v
		}
		entry["_raw"] = cur.raw
		stashFor(stash, a.id)[name] = entry
		if err := saveStash(stash); err != nil {
			return err
		}
		return writeAtomic(a.filePath(), out)
	}
	if has {
		return nil
	}
	d, own := asMap(orMap(stash[a.id])[name]), true
	if !truthy(d) {
		d, own = otherDef(name, a.id, stash), false
	}
	if len(d) == 0 {
		return runtimeError{"이 서버의 설정을 찾지 못했어요"}
	}
	if !found {
		text = "{}\n"
		if a.path == nil {
			text = ""
		}
	}
	out, err := a.add(text, name, d, own)
	if err != nil {
		return err
	}
	if !found {
		if err := os.MkdirAll(filepath.Dir(a.filePath()), 0o700); err != nil {
			return err
		}
	}
	if err := writeAtomic(a.filePath(), out); err != nil {
		return err
	}
	delete(orMap(stash[a.id]), name)
	return saveStash(stash)
}

// fileAgentServers is each file-edited agent's servers in Claude's shape.
var fileAgentServers = func() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, a := range mcpFileAgents {
		out[a.id] = a.servers()
	}
	return out
}

// otherDef is name's definition from any agent but except, live first then stashed, without agent state.
func otherDef(name, except string, stash map[string]any) map[string]any {
	sources := []func() map[string]any{claudeServers, func() map[string]any { return orMap(stash["claude_code"]) }}
	for _, a := range mcpFileAgents {
		a := a
		sources = append(sources, a.servers, func() map[string]any { return orMap(stash[a.id]) })
	}
	sources = append(sources, codexServers, func() map[string]any { return orMap(stash["codex"]) })
	ids := []string{"claude_code", "claude_code"}
	for _, a := range mcpFileAgents {
		ids = append(ids, a.id, a.id)
	}
	ids = append(ids, "codex", "codex")
	for i, src := range sources {
		if ids[i] == except {
			continue
		}
		if d := asMap(src()[name]); len(d) > 0 {
			return cleanDef(d)
		}
	}
	return nil
}

// writeAtomic replaces path (through a symlink) with data, keeping its mode.
func writeAtomic(path string, data string) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".arkism-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.WriteString(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// ---------------------------------------------------------------- formats

func remoteType(d map[string]any) string {
	if d["type"] == "sse" {
		return "sse"
	}
	return "http"
}

func copyExcept(v map[string]any, skip ...string) map[string]any {
	out := map[string]any{}
	for k, x := range v {
		if !containsStr(skip, k) {
			out[k] = x
		}
	}
	return out
}

func setIf(o *host.Obj, d map[string]any, keys ...string) {
	for _, k := range keys {
		if truthy(d[k]) {
			o.Set(k, d[k])
		}
	}
}

// Gemini CLI: command/args/env/cwd, or httpUrl (streamable HTTP) / url (SSE) with headers.
func fromGemini(g map[string]any) map[string]any {
	d := copyExcept(g, "httpUrl", "url", "type")
	switch {
	case truthy(g["httpUrl"]):
		d["type"], d["url"] = "http", g["httpUrl"]
	case truthy(g["url"]):
		d["type"], d["url"] = "sse", g["url"]
		if g["type"] == "http" {
			d["type"] = "http"
		}
	default:
		d["type"] = "stdio"
	}
	return d
}

func toGemini(d map[string]any) host.Obj {
	o := host.Obj{}
	if isStdio(d) {
		o.Set("command", d["command"])
		setIf(&o, d, "args", "env", "cwd")
		return o
	}
	if remoteType(d) == "sse" {
		o.Set("url", d["url"])
	} else {
		o.Set("httpUrl", d["url"])
	}
	setIf(&o, d, "headers")
	return o
}

// ZCode: {"type": "stdio", "command", "args", "env"} or {"type": "http"|"sse", "url", "headers"}.
func fromZcode(z map[string]any) map[string]any {
	d := copyExcept(z)
	if !truthy(d["type"]) {
		d["type"] = "stdio"
		if !truthy(d["command"]) && truthy(d["url"]) {
			d["type"] = "http"
		}
	}
	return d
}

func toZcode(d map[string]any) host.Obj {
	if isStdio(d) {
		o := host.Obj{{Key: "type", Value: "stdio"}, {Key: "command", Value: d["command"]}, {Key: "args", Value: firstOr(d["args"], []any{})}}
		setIf(&o, d, "env", "cwd")
		return o
	}
	o := host.Obj{{Key: "type", Value: remoteType(d)}, {Key: "url", Value: d["url"]}}
	setIf(&o, d, "headers")
	return o
}

// fromCodexShape is a [mcp_servers.<name>] table (Codex/Grok keys) in Claude's shape, like readCodexServers.
func fromCodexShape(t map[string]any) map[string]any {
	var d map[string]any
	if truthy(t["command"]) {
		d = map[string]any{"type": "stdio", "command": t["command"], "args": firstOr(t["args"], []any{}), "env": firstOr(t["env"], map[string]any{})}
		if truthy(t["cwd"]) {
			d["cwd"] = t["cwd"]
		}
	} else {
		d = map[string]any{"type": "http", "url": t["url"]}
		if truthy(t["bearer_token_env_var"]) {
			d["bearer_token_env_var"] = t["bearer_token_env_var"]
		}
		if truthy(t["http_headers"]) {
			d["headers"] = t["http_headers"]
		}
	}
	d["_enabled"] = getOr(t, "enabled", true)
	return d
}

// ---------------------------------------------------------------- JSON, edited as text

func jsWS(s string, i int) int {
	for i < len(s) && strings.IndexByte(" \t\r\n", s[i]) >= 0 {
		i++
	}
	return i
}

// jsEnd is the end of the JSON value starting at s[i] (s is already known to be valid JSON).
func jsEnd(s string, i int) (int, error) {
	if i >= len(s) {
		return i, errMCPLayout
	}
	switch s[i] {
	case '"':
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
			} else if s[j] == '"' {
				return j + 1, nil
			}
		}
		return len(s), errMCPLayout
	case '{', '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"':
				e, err := jsEnd(s, j)
				if err != nil {
					return e, err
				}
				j = e - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1, nil
				}
			}
		}
		return len(s), errMCPLayout
	}
	j := i
	for j < len(s) && strings.IndexByte(",}] \t\r\n", s[j]) < 0 {
		j++
	}
	return j, nil
}

type jsMember struct {
	key                                string
	keyStart, keyEnd, valStart, valEnd int
}

// jsMembers lists the members of the object opening at s[open], and where it closes.
func jsMembers(s string, open int) ([]jsMember, int, error) {
	var out []jsMember
	i := jsWS(s, open+1)
	if i < len(s) && s[i] == '}' {
		return nil, i, nil
	}
	for i < len(s) {
		ke, err := jsEnd(s, i)
		if err != nil || s[i] != '"' {
			return nil, 0, errMCPLayout
		}
		var key string
		if json.Unmarshal([]byte(s[i:ke]), &key) != nil {
			return nil, 0, errMCPLayout
		}
		c := jsWS(s, ke)
		if c >= len(s) || s[c] != ':' {
			return nil, 0, errMCPLayout
		}
		vs := jsWS(s, c+1)
		ve, err := jsEnd(s, vs)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, jsMember{key, i, ke, vs, ve})
		i = jsWS(s, ve)
		if i < len(s) && s[i] == ',' {
			i = jsWS(s, i+1)
			continue
		}
		if i < len(s) && s[i] == '}' {
			return out, i, nil
		}
		break
	}
	return nil, 0, errMCPLayout
}

// jsFind is the one member named key (a repeated key is refused: which one counts is unclear).
func jsFind(ms []jsMember, key string) (int, error) {
	at := -1
	for n, m := range ms {
		if m.key == key {
			if at >= 0 {
				return -1, errMCPLayout
			}
			at = n
		}
	}
	return at, nil
}

// jsObject is the object at path, or the deepest existing one and the keys still missing.
func jsObject(s string, path []string) (int, []string, error) {
	if !json.Valid([]byte(s)) {
		return 0, nil, errMCPLayout
	}
	open := jsWS(s, 0)
	if open >= len(s) || s[open] != '{' {
		return 0, nil, errMCPLayout
	}
	for n, k := range path {
		ms, _, err := jsMembers(s, open)
		if err != nil {
			return 0, nil, err
		}
		at, err := jsFind(ms, k)
		if err != nil {
			return 0, nil, err
		}
		if at < 0 {
			return open, path[n:], nil
		}
		if s[ms[at].valStart] != '{' {
			return 0, nil, errMCPLayout
		}
		open = ms[at].valStart
	}
	return open, nil, nil
}

// jsonServers is each server's entry text under path.
func jsonServers(s string, path []string) (map[string]string, error) {
	open, missing, err := jsObject(s, path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(missing) > 0 {
		return out, nil
	}
	ms, _, err := jsMembers(s, open)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if _, dup := out[m.key]; dup {
			return nil, errMCPLayout
		}
		out[m.key] = s[m.valStart:m.valEnd]
	}
	return out, nil
}

func jsonRemoveServer(s string, path []string, name string) (string, error) {
	open, missing, err := jsObject(s, path)
	if err != nil {
		return "", err
	}
	if len(missing) > 0 {
		return "", errMCPLayout
	}
	ms, end, err := jsMembers(s, open)
	if err != nil {
		return "", err
	}
	at, err := jsFind(ms, name)
	if err != nil || at < 0 {
		return "", errMCPLayout
	}
	var out string
	switch {
	case len(ms) == 1:
		out = s[:open+1] + s[end:]
	case at < len(ms)-1:
		out = s[:ms[at].keyStart] + s[ms[at+1].keyStart:]
	default:
		out = s[:ms[at-1].valEnd] + s[ms[at].valEnd:]
	}
	return out, jsonCheck(out, path, name, false)
}

func jsonAddServer(s string, path []string, name string, v any) (string, error) {
	open, missing, err := jsObject(s, path)
	if err != nil {
		return "", err
	}
	key, val := name, v
	if len(missing) > 0 { // build the missing objects around the server
		key = missing[0]
		inner := host.Obj{{Key: name, Value: v}}
		for i := len(missing) - 1; i >= 1; i-- {
			inner = host.Obj{{Key: missing[i], Value: inner}}
		}
		val = inner
	}
	ms, end, err := jsMembers(s, open)
	if err != nil {
		return "", err
	}
	if at, err := jsFind(ms, key); err != nil || at >= 0 {
		return "", errMCPLayout
	}
	nl := "\n"
	if strings.Contains(s, "\r\n") {
		nl = "\r\n"
	}
	render := func(indent, unit string) (string, error) {
		if unit == "" { // a one-line file stays on one line
			return host.Dumps(val, host.DumpOpts{NoASCII: true})
		}
		compact, err := host.Dumps(val, host.DumpOpts{Compact: true, NoASCII: true})
		if err != nil {
			return "", err
		}
		var b bytes.Buffer
		if err := json.Indent(&b, []byte(compact), indent, unit); err != nil {
			return "", err
		}
		return strings.ReplaceAll(b.String(), "\n", nl), nil
	}
	quoted := host.MustDumps(key, host.DumpOpts{NoASCII: true})
	var out string
	if len(ms) > 0 {
		last := ms[len(ms)-1]
		before := last.keyStart
		for before > 0 && strings.IndexByte(" \t\r\n", s[before-1]) >= 0 {
			before--
		}
		ws := s[before:last.keyStart]
		indent, unit := "", ""
		if i := strings.LastIndexByte(ws, '\n'); i >= 0 {
			indent = ws[i+1:]
			unit = jsUnit(s)
			if p := lineIndent(s, open); strings.HasPrefix(indent, p) && len(indent) > len(p) {
				unit = indent[len(p):]
			}
		}
		text, err := render(indent, unit)
		if err != nil {
			return "", err
		}
		out = s[:last.valEnd] + "," + ws + quoted + s[last.keyEnd:last.valStart] + text + s[last.valEnd:]
	} else {
		parent, unit := lineIndent(s, open), jsUnit(s)
		text, err := render(parent+unit, unit)
		if err != nil {
			return "", err
		}
		out = s[:open+1] + nl + parent + unit + quoted + ": " + text + nl + parent + s[end:]
	}
	return out, jsonCheck(out, path, name, true)
}

// jsonCheck re-reads an edit: still valid, and the server is there or gone as asked.
func jsonCheck(s string, path []string, name string, want bool) error {
	servers, err := jsonServers(s, path)
	if err != nil {
		return errMCPLayout
	}
	if _, ok := servers[name]; ok != want {
		return errMCPLayout
	}
	return nil
}

func lineIndent(s string, at int) string {
	start := strings.LastIndexByte(s[:at], '\n') + 1
	end := start
	for end < len(s) && (s[end] == ' ' || s[end] == '\t') {
		end++
	}
	return s[start:end]
}

// jsUnit is the file's indent step, from its first indented line.
func jsUnit(s string) string {
	for _, line := range strings.Split(s, "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && trimmed != "\r" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// ---------------------------------------------------------------- TOML, edited by whole tables

var errTOMLLayout = errMCPLayout

type tomlItem struct {
	start, end, val int
	header, array   bool
	path            []string
}

var tomlBare = regexp.MustCompile(`^[A-Za-z0-9_-]+`)

// tomlKeyPath parses a dotted key at s[i:], stopping before stop.
func tomlKeyPath(s string, i int, stop string) ([]string, int, error) {
	var path []string
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return nil, i, errTOMLLayout
		}
		switch s[i] {
		case '"':
			j := i + 1
			for j < len(s) && s[j] != '"' && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) || s[j] != '"' {
				return nil, i, errTOMLLayout
			}
			part, err := strconv.Unquote(s[i : j+1])
			if err != nil {
				return nil, i, errTOMLLayout
			}
			path, i = append(path, part), j+1
		case '\'':
			j := strings.IndexAny(s[i+1:], "'\n")
			if j < 0 || s[i+1+j] != '\'' {
				return nil, i, errTOMLLayout
			}
			path, i = append(path, s[i+1:i+1+j]), i+2+j
		default:
			m := tomlBare.FindString(s[i:])
			if m == "" {
				return nil, i, errTOMLLayout
			}
			path, i = append(path, m), i+len(m)
		}
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if strings.HasPrefix(s[i:], stop) {
			return path, i + len(stop), nil
		}
		if i >= len(s) || s[i] != '.' {
			return nil, i, errTOMLLayout
		}
		i++
	}
}

// tomlLineEnd skips trailing blanks and a comment; it must reach a newline.
func tomlLineEnd(s string, i int) (int, error) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	if i < len(s) && s[i] == '#' {
		for i < len(s) && s[i] != '\n' {
			i++
		}
	}
	if i < len(s) && s[i] != '\n' {
		return i, errTOMLLayout
	}
	return min(i+1, len(s)), nil
}

// tomlValueEnd scans a key's value (strings, comments, nested arrays and tables) to its line end.
func tomlValueEnd(s string, i int) (int, error) {
	depth := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], `"""`), strings.HasPrefix(s[i:], `'''`):
			q := s[i : i+3]
			j := i + 3
			for {
				if j >= len(s) {
					return j, errTOMLLayout
				}
				if q == `"""` && s[j] == '\\' {
					j += 2
					continue
				}
				if strings.HasPrefix(s[j:], q) {
					j += 3
					for k := 0; k < 2 && j < len(s) && s[j] == q[0]; k++ {
						j++
					}
					break
				}
				j++
			}
			i = j
		case s[i] == '"' || s[i] == '\'':
			j := i + 1
			for j < len(s) && s[j] != s[i] && s[j] != '\n' {
				if s[i] == '"' && s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) || s[j] != s[i] {
				return j, errTOMLLayout
			}
			i = j + 1
		case s[i] == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case s[i] == '[' || s[i] == '{':
			depth++
			i++
		case s[i] == ']' || s[i] == '}':
			depth--
			i++
		case s[i] == '\n':
			i++
			if depth <= 0 {
				return i, nil
			}
		default:
			i++
		}
	}
	return i, nil
}

var tomlSectionLine = regexp.MustCompile(`(?m)^[ \t]*\[`)

// tomlItems splits text into top-level statements; strays are "[" lines inside values.
func tomlItems(text string) (items []tomlItem, strays []int, err error) {
	headers := map[int]bool{}
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\r') {
			j++
		}
		switch {
		case j >= len(text):
			i = j
		case text[j] == '\n' || text[j] == '#':
			i, _ = tomlLineEnd(text, j)
		case text[j] == '[':
			item := tomlItem{start: i, header: true, array: strings.HasPrefix(text[j:], "[[")}
			open, stop := j+1, "]"
			if item.array {
				open, stop = j+2, "]]"
			}
			path, k, err := tomlKeyPath(text, open, stop)
			if err != nil {
				return nil, nil, err
			}
			if item.end, err = tomlLineEnd(text, k); err != nil {
				return nil, nil, err
			}
			item.path, i = path, item.end
			headers[item.start] = true
			items = append(items, item)
		default:
			path, k, err := tomlKeyPath(text, j, "=")
			if err != nil {
				return nil, nil, err
			}
			end, err := tomlValueEnd(text, k)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, tomlItem{start: i, end: end, val: k, path: path})
			i = end
		}
	}
	for _, loc := range tomlSectionLine.FindAllStringIndex(text, -1) {
		if !headers[loc[0]] {
			strays = append(strays, loc[0])
		}
	}
	return items, strays, nil
}

type tomlServer struct {
	spans    [][2]int // its [mcp_servers.<name>] and [mcp_servers.<name>.*] tables
	editable bool
	def      map[string]any
}

func (t *tomlServer) raw(text string) string {
	var b strings.Builder
	for _, sp := range t.spans {
		b.WriteString(text[sp[0]:sp[1]])
	}
	return b.String()
}

type tomlDoc struct {
	servers map[string]*tomlServer
	inline  bool // mcp_servers is built with keys outside its own tables: a header cannot be added
}

func tomlParse(text string) (*tomlDoc, error) {
	items, strays, err := tomlItems(text)
	if err != nil {
		return nil, err
	}
	doc := &tomlDoc{servers: map[string]*tomlServer{}}
	srv := func(name string) *tomlServer {
		s, ok := doc.servers[name]
		if !ok {
			s = &tomlServer{editable: true, def: map[string]any{}}
			doc.servers[name] = s
		}
		return s
	}
	var table []string
	for n, it := range items {
		if it.header {
			table = it.path
			if it.path[0] != "mcp_servers" {
				continue
			}
			if it.array {
				return nil, errTOMLLayout
			}
			if len(it.path) < 2 {
				continue
			}
			s := srv(it.path[1])
			if !tomlEnsure(s.def, it.path[2:]) {
				return nil, errTOMLLayout
			}
			end := len(text)
			for _, next := range items[n+1:] {
				if next.header {
					end = next.start
					break
				}
			}
			// Comments and blank lines right before the next table stay with it.
			last := it.end
			for _, k := range items[n+1:] {
				if k.header {
					break
				}
				last = k.end
			}
			end = max(last, min(end, tomlTrimTail(text, last, end)))
			s.spans = append(s.spans, [2]int{it.start, end})
			continue
		}
		full := append(append([]string(nil), table...), it.path...)
		if full[0] != "mcp_servers" {
			continue
		}
		v, _, verr := tomlValue(text, it.val)
		if len(table) < 2 || table[0] != "mcp_servers" { // dotted keys or inline tables
			doc.inline = true
			if len(full) == 1 {
				for name, x := range asMap(v) {
					s := srv(name)
					s.editable = false
					for k, y := range asMap(x) {
						s.def[k] = y
					}
				}
				continue
			}
			srv(full[1]).editable = false
		}
		if verr == nil && len(full) > 2 {
			s := srv(full[1])
			if !tomlEnsure(s.def, full[2:len(full)-1]) {
				return nil, errTOMLLayout
			}
			tomlSet(s.def, full[2:], v)
		} else if verr == nil && len(full) == 2 {
			for k, y := range asMap(v) {
				srv(full[1]).def[k] = y
			}
		}
	}
	for _, s := range doc.servers {
		for _, at := range strays {
			for _, sp := range s.spans {
				if at > sp[0] && at < sp[1] {
					s.editable = false
				}
			}
		}
	}
	return doc, nil
}

// tomlTrimTail is where the comment lines (and blank lines among them) that end [from, to) begin:
// they introduce the next table and stay with it.
func tomlTrimTail(text string, from, to int) int {
	cut := to
	for cut > from {
		start := strings.LastIndexByte(text[:cut-1], '\n') + 1
		if start < from {
			break
		}
		line := strings.TrimSpace(text[start:cut])
		if line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		cut = start
	}
	for cut < to { // blank lines before the first comment still belong to this table
		end := strings.IndexByte(text[cut:to], '\n')
		if end < 0 || strings.TrimSpace(text[cut:cut+end]) != "" {
			break
		}
		cut += end + 1
	}
	return cut
}

func tomlEnsure(m map[string]any, path []string) bool {
	for _, k := range path {
		next, ok := m[k]
		if !ok {
			n := map[string]any{}
			m[k] = n
			m = n
			continue
		}
		if m, ok = next.(map[string]any); !ok {
			return false
		}
	}
	return true
}

func tomlSet(m map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		m = asMap(m[k])
	}
	m[path[len(path)-1]] = v
}

func tomlServers(text string) (map[string]*tomlServer, error) {
	doc, err := tomlParse(text)
	if err != nil {
		return nil, err
	}
	return doc.servers, nil
}

func tomlRemove(text, name string) (string, error) {
	doc, err := tomlParse(text)
	if err != nil {
		return "", err
	}
	s, ok := doc.servers[name]
	if !ok || !s.editable || len(s.spans) == 0 {
		return "", errTOMLLayout
	}
	out := text
	for i := len(s.spans) - 1; i >= 0; i-- {
		sp := s.spans[i]
		out = out[:sp[0]] + out[sp[1]:]
	}
	return out, tomlCheck(doc, out, name, false)
}

func tomlAdd(text, name, stanza string) (string, error) {
	doc, err := tomlParse(text)
	if err != nil {
		return "", err
	}
	if _, ok := doc.servers[name]; ok || doc.inline {
		return "", errTOMLLayout
	}
	if !strings.HasSuffix(stanza, "\n") {
		stanza += "\n"
	}
	out := text
	if out != "" {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if !strings.HasSuffix(out, "\n\n") {
			out += "\n"
		}
	}
	out += stanza
	return out, tomlCheck(doc, out, name, true)
}

// tomlCheck re-reads an edit: it parses, the server is there or gone, the others are untouched.
func tomlCheck(before *tomlDoc, out, name string, want bool) error {
	after, err := tomlParse(out)
	if err != nil {
		return errTOMLLayout
	}
	if s, ok := after.servers[name]; ok != want || want && !truthy(s.def["command"]) && !truthy(s.def["url"]) {
		return errTOMLLayout
	}
	for k, s := range before.servers {
		if k == name {
			continue
		}
		a, ok := after.servers[k]
		if !ok || host.MustDumps(a.def, host.DumpOpts{}) != host.MustDumps(s.def, host.DumpOpts{}) {
			return errTOMLLayout
		}
	}
	return nil
}

func tomlKey(k string) string {
	if tomlBare.FindString(k) == k {
		return k
	}
	return host.MustDumps(k, host.DumpOpts{NoASCII: true})
}

func tomlLiteral(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return host.MustDumps(x, host.DumpOpts{NoASCII: true}), nil
	case bool, json.Number, int, int64, float64:
		return host.MustDumps(x, host.DumpOpts{}), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := tomlLiteral(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		if len(x) == 0 {
			return "{}", nil
		}
		parts := []string{}
		for _, k := range sortedKeys(x) {
			s, err := tomlLiteral(x[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, tomlKey(k)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	}
	return "", runtimeError{"이 서버 설정은 TOML로 옮길 수 없어요"}
}

// tomlStanza is a server in Claude's shape as a [mcp_servers.<name>] table (Codex/Grok keys).
func tomlStanza(name string, d map[string]any) (string, error) {
	var keys []host.KV
	if isStdio(d) {
		keys = []host.KV{{Key: "command", Value: d["command"]}, {Key: "args", Value: firstOr(d["args"], []any{})}}
		for _, k := range []string{"env", "cwd"} {
			if truthy(d[k]) {
				keys = append(keys, host.KV{Key: k, Value: d[k]})
			}
		}
	} else {
		keys = []host.KV{{Key: "url", Value: d["url"]}}
		if truthy(d["bearer_token_env_var"]) {
			keys = append(keys, host.KV{Key: "bearer_token_env_var", Value: d["bearer_token_env_var"]})
		}
		if truthy(d["headers"]) {
			keys = append(keys, host.KV{Key: "http_headers", Value: d["headers"]})
		}
	}
	var b strings.Builder
	b.WriteString("[mcp_servers." + tomlKey(name) + "]\n")
	for _, kv := range keys {
		s, err := tomlLiteral(kv.Value)
		if err != nil {
			return "", err
		}
		b.WriteString(kv.Key + " = " + s + "\n")
	}
	return b.String(), nil
}

// tomlValue reads one value at s[i:] (strings, numbers, booleans, arrays, inline tables);
// other kinds (dates, multi-line strings) are an error and are only skipped when reading.
func tomlValue(s string, i int) (any, int, error) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) {
		return nil, i, errTOMLLayout
	}
	switch c := s[i]; {
	case strings.HasPrefix(s[i:], `"""`), strings.HasPrefix(s[i:], `'''`):
		return nil, i, errTOMLLayout
	case c == '"':
		j := i + 1
		for j < len(s) && s[j] != '"' && s[j] != '\n' {
			if s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(s) || s[j] != '"' {
			return nil, i, errTOMLLayout
		}
		var out string
		if json.Unmarshal([]byte(tomlToJSONEscapes(s[i:j+1])), &out) != nil {
			return nil, i, errTOMLLayout
		}
		return out, j + 1, nil
	case c == '\'':
		j := strings.IndexAny(s[i+1:], "'\n")
		if j < 0 || s[i+1+j] != '\'' {
			return nil, i, errTOMLLayout
		}
		return s[i+1 : i+1+j], i + 2 + j, nil
	case c == '[':
		out := []any{}
		i++
		for {
			i = tomlSkip(s, i)
			if i < len(s) && s[i] == ']' {
				return out, i + 1, nil
			}
			v, j, err := tomlValue(s, i)
			if err != nil {
				return nil, j, err
			}
			out = append(out, v)
			i = tomlSkip(s, j)
			if i < len(s) && s[i] == ',' {
				i++
				continue
			}
			if i < len(s) && s[i] == ']' {
				return out, i + 1, nil
			}
			return nil, i, errTOMLLayout
		}
	case c == '{':
		out := map[string]any{}
		i++
		for {
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			if i < len(s) && s[i] == '}' {
				return out, i + 1, nil
			}
			path, k, err := tomlKeyPath(s, i, "=")
			if err != nil {
				return nil, k, err
			}
			v, j, err := tomlValue(s, k)
			if err != nil {
				return nil, j, err
			}
			if !tomlEnsure(out, path[:len(path)-1]) {
				return nil, j, errTOMLLayout
			}
			tomlSet(out, path, v)
			for i = j; i < len(s) && (s[i] == ' ' || s[i] == '\t'); i++ {
			}
			if i < len(s) && s[i] == ',' {
				i++
				continue
			}
			if i < len(s) && s[i] == '}' {
				return out, i + 1, nil
			}
			return nil, i, errTOMLLayout
		}
	}
	j := i
	for j < len(s) && strings.IndexByte(",]} \t\r\n#", s[j]) < 0 {
		j++
	}
	word := s[i:j]
	switch word {
	case "true":
		return true, j, nil
	case "false":
		return false, j, nil
	}
	num := strings.ReplaceAll(word, "_", "")
	if _, err := strconv.ParseFloat(num, 64); err == nil {
		return json.Number(num), j, nil
	}
	return nil, j, errTOMLLayout
}

// tomlSkip passes blanks, newlines and comments inside an array.
func tomlSkip(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		default:
			return i
		}
	}
	return i
}

// tomlToJSONEscapes turns TOML's \UXXXXXXXX and \e escapes into JSON ones.
func tomlToJSONEscapes(q string) string {
	if !strings.Contains(q, `\U`) && !strings.Contains(q, `\e`) {
		return q
	}
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		if q[i] == '\\' && i+1 < len(q) {
			switch q[i+1] {
			case 'U':
				if i+10 <= len(q) {
					if r, err := strconv.ParseUint(q[i+2:i+10], 16, 32); err == nil && r <= unicode.MaxRune {
						b.WriteString(strings.Trim(host.MustDumps(string(rune(r)), host.DumpOpts{}), `"`))
						i += 9
						continue
					}
				}
			case 'e':
				b.WriteString(`\u001b`)
				i++
				continue
			}
			b.WriteByte(q[i])
			b.WriteByte(q[i+1])
			i++
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}
