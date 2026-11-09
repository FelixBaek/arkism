package host

// Hermes Agent keeps its MCP servers in config.yaml under a top-level mcp_servers map. There is no
// YAML library here, so the block is edited as text: only the one server entry changes, every other
// line (comments included) stays as it was, and a layout this cannot read safely (flow style, tabs,
// anchors, a second document) is refused with the file left untouched.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// ErrYAMLLayout is a config.yaml whose mcp_servers block cannot be edited safely.
var ErrYAMLLayout = errors.New("unsupported MCP YAML layout; configuration was not updated")

// HermesHome is Hermes Agent's home for userRoot: HERMES_HOME when set (for this user's own home
// only), else %LOCALAPPDATA%\hermes on Windows and ~/.hermes elsewhere.
func HermesHome(userRoot string) string {
	if userRoot == Home {
		if h := strings.TrimSpace(os.Getenv("HERMES_HOME")); h != "" {
			return h
		}
		if runtime.GOOS == "windows" {
			if l := os.Getenv("LOCALAPPDATA"); l != "" {
				return filepath.Join(l, "hermes")
			}
		}
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(userRoot, "AppData", "Local", "hermes")
	}
	return filepath.Join(userRoot, ".hermes")
}

// YAMLServer is one mcp_servers entry: its definition in Claude's shape (type, command/args/env/cwd
// or url/headers, _enabled) and the entry exactly as written, dedented to column 0.
type YAMLServer struct {
	Def        map[string]any
	Raw        string
	start, end int // byte span of the entry in the file
}

var (
	yamlTopKey    = regexp.MustCompile(`^mcp_servers[ \t]*:(.*)$`)
	yamlAnyTopKey = regexp.MustCompile(`^(mcp_servers|"mcp_servers"|'mcp_servers')[ \t]*:`) // quoted forms are refused
	yamlKeyLine   = regexp.MustCompile(`^( *)([A-Za-z0-9_.-]+|"[^"\\]*"|'[^']*')[ \t]*:(?:[ \t]+(.*))?$`)
	yamlEmptyVal  = regexp.MustCompile(`^(\{\}|null|~)?[ \t]*(#.*)?$`)
)

type yamlLine struct {
	start, end int // end includes the newline
	text       string
	indent     int
	blank      bool // empty or a comment
}

func yamlLines(text string) []yamlLine {
	var out []yamlLine
	for i := 0; i < len(text); {
		j := strings.IndexByte(text[i:], '\n')
		end := len(text)
		if j >= 0 {
			end = i + j + 1
		}
		t := strings.TrimRight(text[i:end], "\r\n")
		trim := strings.TrimLeft(t, " ")
		out = append(out, yamlLine{start: i, end: end, text: t, indent: len(t) - len(trim), blank: trim == "" || trim[0] == '#'})
		i = end
	}
	return out
}

// yamlBlock finds mcp_servers: the key line (-1 when absent), whether its value is empty ({}, null,
// nothing with no children) and the lines of its block.
type yamlBlock struct {
	lines      []yamlLine
	key        int
	empty      bool
	from, to   int // block lines [from, to): children of the key, trailing blank lines left out
	childInd   int
	entryStart []int // line index of each entry's key line
}

func yamlParse(text string) (*yamlBlock, error) {
	b := &yamlBlock{lines: yamlLines(text), key: -1}
	if err := b.topLines(); err != nil {
		return nil, err
	}
	if b.key < 0 {
		return b, nil
	}
	b.from, b.to = b.key+1, b.key+1
	for i := b.key + 1; i < len(b.lines); i++ {
		l := b.lines[i]
		if l.indent == 0 && strings.TrimSpace(l.text) != "" {
			break
		}
		if !l.blank {
			b.to = i + 1
		}
	}
	if b.to == b.from {
		b.empty = true
		return b, nil
	}
	if b.empty {
		return nil, ErrYAMLLayout // "mcp_servers: {}" with children
	}
	return b, b.entries()
}

// topLines refuses tabs in indentation and a second document, and records the mcp_servers key line.
func (b *yamlBlock) topLines() error {
	content := false
	for i, l := range b.lines {
		if strings.HasPrefix(l.text[l.indent:], "\t") {
			return ErrYAMLLayout // a tab in the indentation
		}
		if (l.text == "---" || l.text == "...") && content {
			return ErrYAMLLayout // a second document
		}
		if !l.blank && l.text != "---" {
			content = true
		}
		if l.indent == 0 && !l.blank {
			if err := b.keyLine(i, l.text); err != nil {
				return err
			}
		}
	}
	return nil
}

// keyLine records a top-level mcp_servers line; a second one, flow style or a scalar is refused.
func (b *yamlBlock) keyLine(i int, text string) error {
	if !yamlAnyTopKey.MatchString(text) {
		return nil
	}
	m := yamlTopKey.FindStringSubmatch(text)
	if m == nil || b.key >= 0 {
		return ErrYAMLLayout
	}
	b.key = i
	rest := strings.TrimSpace(m[1])
	if !yamlEmptyVal.MatchString(rest) {
		return ErrYAMLLayout
	}
	b.empty = strings.HasPrefix(rest, "{}") || strings.HasPrefix(rest, "null") || strings.HasPrefix(rest, "~")
	return nil
}

// entries records each entry's key line; anything at the child indent that is not "name:" is refused.
func (b *yamlBlock) entries() error {
	b.childInd = -1
	for i := b.from; i < b.to; i++ {
		l := b.lines[i]
		if l.blank {
			continue
		}
		if b.childInd < 0 {
			b.childInd = l.indent
		}
		if l.indent < b.childInd {
			return ErrYAMLLayout
		}
		if l.indent > b.childInd {
			if v := strings.TrimSpace(strings.TrimLeft(l.text, " -")); strings.HasPrefix(v, "<<") || yamlNode(l.text) {
				return ErrYAMLLayout // merge keys, anchors, aliases and tags
			}
			continue
		}
		m := yamlKeyLine.FindStringSubmatch(l.text)
		if m == nil || strings.TrimSpace(yamlStripComment(m[3])) != "" {
			return ErrYAMLLayout // a sequence, or an entry written in flow style
		}
		b.entryStart = append(b.entryStart, i)
	}
	return nil
}

// yamlNode reports a value that starts with an anchor, alias or tag.
func yamlNode(line string) bool {
	m := yamlKeyLine.FindStringSubmatch(line)
	v := ""
	if m != nil {
		v = m[3]
	} else if t := strings.TrimLeft(line, " "); strings.HasPrefix(t, "- ") {
		v = t[2:]
	}
	v = strings.TrimSpace(v)
	return v != "" && strings.ContainsRune("&*!", rune(v[0]))
}

// span is entry k's lines [start, end): comments and blank lines before the next entry go with it.
func (b *yamlBlock) span(k int) (int, int) {
	start, end := b.entryStart[k], b.to
	if k+1 < len(b.entryStart) {
		end = b.entryStart[k+1]
	}
	for end > start+1 && b.lines[end-1].blank && b.lines[end-1].indent <= b.childInd {
		end--
	}
	return start, end
}

func yamlUnkey(k string) string {
	if len(k) >= 2 && (k[0] == '"' || k[0] == '\'') {
		return k[1 : len(k)-1]
	}
	return k
}

// YAMLMCPServers reads every mcp_servers entry of a Hermes config.yaml.
func YAMLMCPServers(text string) (map[string]YAMLServer, error) {
	b, err := yamlParse(text)
	if err != nil {
		return nil, err
	}
	out := map[string]YAMLServer{}
	for k := range b.entryStart {
		s, e := b.span(k)
		name := yamlUnkey(yamlKeyLine.FindStringSubmatch(b.lines[s].text)[2])
		if _, dup := out[name]; dup {
			return nil, ErrYAMLLayout
		}
		var raw strings.Builder
		for _, l := range b.lines[s:e] {
			raw.WriteString(l.text[min(b.childInd, l.indent):] + "\n")
		}
		out[name] = YAMLServer{Def: yamlDef(b.lines[s+1 : e]), Raw: raw.String(), start: b.lines[s].start, end: b.lines[e-1].end}
	}
	return out, nil
}

// yamlDef reads an entry's properties into Claude's shape: scalars, a list of scalars (args) and
// one level of mapping (env, headers). Anything deeper is left out.
func yamlDef(lines []yamlLine) map[string]any {
	raw := map[string]any{}
	pi := -1
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if l.blank {
			continue
		}
		if pi < 0 {
			pi = l.indent
		}
		if l.indent != pi {
			continue
		}
		m := yamlKeyLine.FindStringSubmatch(l.text)
		if m == nil {
			continue
		}
		key, val := yamlUnkey(m[2]), strings.TrimSpace(yamlStripComment(m[3]))
		if strings.HasPrefix(val, "{") {
			continue // a flow mapping: not read
		}
		if val != "" {
			raw[key] = yamlScalarOrFlow(val)
			continue
		}
		raw[key], i = yamlChildren(lines, i, pi)
	}
	return yamlCanon(raw)
}

// yamlChildren reads the lines under property line i (indent pi) as a list of scalars or a
// one-level mapping, and returns the index of the last line it took.
func yamlChildren(lines []yamlLine, i, pi int) (any, int) {
	var list []any
	obj := map[string]any{}
	for i+1 < len(lines) && (lines[i+1].blank || lines[i+1].indent > pi || lines[i+1].indent == pi && strings.HasPrefix(strings.TrimLeft(lines[i+1].text, " "), "- ")) {
		i++
		c := lines[i]
		if c.blank {
			continue
		}
		t := strings.TrimLeft(c.text, " ")
		if strings.HasPrefix(t, "- ") || t == "-" {
			list = append(list, yamlScalar(strings.TrimSpace(yamlStripComment(strings.TrimPrefix(t, "-")))))
		} else if cm := yamlKeyLine.FindStringSubmatch(c.text); cm != nil {
			obj[yamlUnkey(cm[2])] = yamlScalar(strings.TrimSpace(yamlStripComment(cm[3])))
		}
	}
	if list != nil {
		return list, i
	}
	return obj, i
}

func yamlCanon(t map[string]any) map[string]any {
	d := map[string]any{"_enabled": yamlTruthy(t["enabled"])}
	if c, ok := t["command"].(string); ok && c != "" {
		d["type"], d["command"] = "stdio", c
		d["args"] = []any{}
		if a, ok := t["args"].([]any); ok {
			d["args"] = a
		}
		if e, ok := t["env"].(map[string]any); ok && len(e) > 0 {
			d["env"] = e
		}
		if c, ok := t["cwd"].(string); ok && c != "" {
			d["cwd"] = c
		}
		return d
	}
	d["type"], d["url"] = "http", t["url"]
	if t["transport"] == "sse" {
		d["type"] = "sse"
	}
	if h, ok := t["headers"].(map[string]any); ok && len(h) > 0 {
		d["headers"] = h
	}
	return d
}

// yamlTruthy is Hermes's reading of "enabled": absent, null or unreadable means on.
func yamlTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(x) {
		case "false", "no", "off", "0":
			return false
		}
	}
	return true
}

func yamlStripComment(v string) string {
	q := byte(0)
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case q != 0:
			if c == '\\' && q == '"' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t'):
			return v[:i]
		}
	}
	return v
}

func yamlScalarOrFlow(v string) any {
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		list := []any{}
		for _, p := range strings.Split(v[1:len(v)-1], ",") { // shortcut: no commas inside quoted items; enough for args lists
			if p = strings.TrimSpace(p); p != "" {
				list = append(list, yamlScalar(p))
			}
		}
		return list
	}
	return yamlScalar(v)
}

func yamlScalar(v string) any {
	switch {
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
		return v[1 : len(v)-1]
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	case v == "true" || v == "True":
		return true
	case v == "false" || v == "False":
		return false
	}
	return v
}

// YAMLMCPRemove takes name's entry out; with no entry left the map becomes "mcp_servers: {}".
func YAMLMCPRemove(text, name string) (string, error) {
	servers, err := YAMLMCPServers(text)
	if err != nil {
		return "", err
	}
	s, ok := servers[name]
	if !ok {
		return text, nil
	}
	out := text[:s.start] + text[s.end:]
	if len(servers) == 1 {
		b, err := yamlParse(out)
		if err != nil {
			return "", err
		}
		k := b.lines[b.key]
		out = out[:k.start] + "mcp_servers: {}\n" + out[k.end:]
	}
	return out, nil
}

// YAMLMCPAdd writes entry (an entry as YAMLMCPEntry renders it, or a Raw kept from an earlier
// removal) into mcp_servers, creating the map when there is none.
func YAMLMCPAdd(text, name, entry string) (string, error) {
	b, err := yamlParse(text)
	if err != nil {
		return "", err
	}
	if b.key >= 0 && !b.empty {
		if servers, err := YAMLMCPServers(text); err != nil {
			return "", err
		} else if _, has := servers[name]; has {
			return "", ErrYAMLLayout
		}
	}
	ind := 2
	if b.childInd > 0 {
		ind = b.childInd
	}
	var block strings.Builder
	for _, l := range strings.SplitAfter(strings.TrimRight(entry, "\n")+"\n", "\n") {
		if strings.TrimSpace(l) != "" {
			block.WriteString(strings.Repeat(" ", ind))
		}
		block.WriteString(l)
	}
	switch {
	case b.key < 0:
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return text + "mcp_servers:\n" + block.String(), nil
	case b.empty:
		k := b.lines[b.key]
		return text[:k.start] + "mcp_servers:\n" + block.String() + text[k.end:], nil
	}
	at := b.lines[b.to-1].end
	head := text[:at]
	if !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	return head + block.String() + text[at:], nil
}

// YAMLMCPEntry renders a definition in Claude's shape as one mcp_servers entry at column 0.
func YAMLMCPEntry(name string, d map[string]any) string {
	var b strings.Builder
	b.WriteString(yamlKey(name) + ":\n")
	line := func(k string, v any) { b.WriteString("  " + yamlKey(k) + ": " + yamlQuote(v) + "\n") }
	mapping := func(k string, v any) {
		m, _ := v.(map[string]any)
		if len(m) == 0 {
			return
		}
		b.WriteString("  " + k + ":\n")
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("    " + yamlKey(k) + ": " + yamlQuote(m[k]) + "\n")
		}
	}
	c, _ := d["command"].(string)
	if u, _ := d["url"].(string); d["type"] == "stdio" || c != "" && u == "" {
		line("command", d["command"])
		b.WriteString("  args:\n")
		for _, a := range anyList(d["args"]) {
			b.WriteString("    - " + yamlQuote(a) + "\n")
		}
		mapping("env", d["env"])
		if c, _ := d["cwd"].(string); c != "" {
			line("cwd", c)
		}
		return b.String()
	}
	line("url", d["url"])
	if d["type"] == "sse" {
		line("transport", "sse")
	}
	mapping("headers", d["headers"])
	return b.String()
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

var yamlBare = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func yamlKey(k string) string {
	if yamlBare.MatchString(k) {
		return k
	}
	return yamlQuote(k)
}

// yamlQuote writes a value as a double-quoted YAML scalar (JSON escapes are valid there).
func yamlQuote(v any) string {
	s, ok := v.(string)
	if !ok {
		if v == nil {
			return `""`
		}
		raw, _ := json.Marshal(v)
		s = string(raw)
	}
	raw, _ := json.Marshal(s)
	return string(raw)
}
