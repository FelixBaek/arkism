package capture

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Install central memory into Codex, Claude and ZCode (and the skill hub into Gemini CLI and
// Grok); preserve other settings. Written for Windows first; macOS and Linux use the same files
// in their own places, and run the client directly where Windows needs a .cmd wrapper.

const (
	instCodex  = ".codex"
	instGemini = ".gemini"
)

// instClient is the installed client's file name in ~/.config/arkism.
var instClient = map[bool]string{true: "arkism.exe", false: "arkism"}[runtime.GOOS == "windows"]

// instUnixClient marks a handler that runs the client installed on macOS or Linux; on its own,
// "arkism" would match far too much.
const instUnixClient = "/.config/arkism/arkism"

// instSupport are copied next to the installed client when they ship beside this binary.
var instSupport = []string{"install_arkism_export_task.ps1", "install_arkism_work_task.ps1"}

// instOldHooks name every handler this installer (or its Python predecessor) wrote.
var instOldHooks = []string{"honcho-memory-router", "arkism_windows.py", "hook-codex.cmd", "arkism_capture.py", "arkism.exe", instUnixClient}

var errInstTOML = valueErr("unsupported MCP TOML layout; configuration was not updated")

// instRead is Path.read_text(encoding="utf-8-sig"): BOM dropped, newlines universal.
func instRead(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimPrefix(string(raw), string(rune(0xFEFF)))
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), nil
}

// instReadOr reads path, or returns fallback when it does not exist.
func instReadOr(path, fallback string) (string, error) {
	if !exists(path) {
		return fallback, nil
	}
	return instRead(path)
}

// instJSON is json_load: the file's object in key order, or {} when it is missing.
func instJSON(path string) (host.Obj, error) {
	text, err := instReadOr(path, "{}")
	if err != nil {
		return nil, err
	}
	v, err := loadsOrdered([]byte(text))
	if err != nil {
		return nil, err
	}
	o, ok := pyNorm(v).(host.Obj)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not a JSON object", errType, path)
	}
	return o, nil
}

// instIndent is json.dumps(v, ensure_ascii=False, indent=2).
func instIndent(b *strings.Builder, v any, depth int) {
	pad := "\n" + strings.Repeat("  ", depth+1)
	switch x := v.(type) {
	case host.Obj:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, kv := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(pad + host.MustDumps(kv.Key, host.DumpOpts{NoASCII: true}) + ": ")
			instIndent(b, kv.Value, depth+1)
		}
		b.WriteString("\n" + strings.Repeat("  ", depth) + "}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(pad)
			instIndent(b, e, depth+1)
		}
		b.WriteString("\n" + strings.Repeat("  ", depth) + "]")
	default:
		b.WriteString(host.MustDumps(v, host.DumpOpts{NoASCII: true}))
	}
}

func instDumps(v any) string {
	var b strings.Builder
	instIndent(&b, v, 0)
	return b.String()
}

// instChild is parent.setdefault(key, {}) for an object value; set stores it back.
func instChild(parent host.Obj, key string) host.Obj {
	v, ok := parent.Get(key)
	if !ok {
		return host.Obj{}
	}
	o, isObj := v.(host.Obj)
	if !isObj {
		panic(fmt.Errorf("%w: %s is not an object", errType, key))
	}
	return o
}

func instList(v any, what string) []any {
	list, ok := v.([]any)
	if !ok {
		panic(fmt.Errorf("%w: %s is not a list", errType, what))
	}
	return list
}

func instHandler(v any) host.Obj {
	h, ok := v.(host.Obj)
	if !ok {
		panic(fmt.Errorf("%w: hook is not an object", errType))
	}
	return h
}

func instCommand(h host.Obj) string {
	v, ok := h.Get("command")
	if !ok {
		return ""
	}
	s, isStr := v.(string)
	if !isStr {
		panic(fmt.Errorf("%w: hook command is not a string", errType))
	}
	return strings.ToLower(s)
}

// instOurs reports whether a hook handler was written by this installer or a predecessor.
func instOurs(h host.Obj) bool {
	command := filepath.ToSlash(instCommand(h))
	args, _ := h.Get("args")
	list, _ := args.([]any)
	for _, name := range instOldHooks {
		if strings.Contains(command, name) {
			return true
		}
		for _, a := range list {
			if strings.Contains(filepath.ToSlash(strings.ToLower(str(a))), name) {
				return true
			}
		}
	}
	return false
}

// instMergeHooks replaces only old memory hooks, retaining every unrelated handler and group.
func instMergeHooks(hooks *host.Obj, event string, handler host.Obj) {
	var groups []any
	current, _ := hooks.Get(event)
	if current == nil {
		current = []any{}
	}
	for _, g := range instList(current, event) {
		group := instHandler(g)
		raw, has := group.Get("hooks")
		if !has {
			raw = []any{}
		}
		var keep []any
		for _, h := range instList(raw, "hooks") {
			if !instOurs(instHandler(h)) {
				keep = append(keep, h)
			}
		}
		if len(keep) > 0 {
			copied := append(host.Obj(nil), group...)
			copied.Set("hooks", keep)
			groups = append(groups, copied)
		}
	}
	if handler != nil {
		groups = append(groups, host.Obj{{Key: "hooks", Value: []any{handler}}})
	}
	if len(groups) > 0 {
		hooks.Set(event, groups)
	} else {
		hooks.Del(event)
	}
}

// instRemoveHoncho drops every handler that still sends to the retired Honcho service.
func instRemoveHoncho(hooks host.Obj) {
	for i, kv := range hooks {
		var groups []any
		for _, g := range instList(kv.Value, kv.Key) {
			group := instHandler(g)
			raw, has := group.Get("hooks")
			if !has {
				raw = []any{}
			}
			keep := []any{}
			for _, h := range instList(raw, "hooks") {
				if !strings.Contains(instCommand(instHandler(h)), "honcho") {
					keep = append(keep, h)
				}
			}
			group.Set("hooks", keep)
			if len(keep) > 0 {
				groups = append(groups, group)
			}
		}
		if groups == nil {
			groups = []any{}
		}
		hooks[i].Value = groups
	}
}

func instSections(server string) []host.KV {
	mcp := "`" + server + "` MCP"
	skills := `- 스킬의 승인 버전은 Arkism이 관리한다. 개인 스킬이 필요한 작업은 ` + mcp + `의 ` + "`route_skills`" + `로 찾고, 고른 ` + "`skill_id`" + `를 ` + "`load_skill`" + `로 읽은 뒤 적용한다. 이 컴퓨터의 같은 이름 스킬보다 Arkism 승인 버전을 먼저 확인한다.
- 스킬 본문이 scripts/·agents/·references/ 를 가리키면 ` + "`load_skill`" + `이 준 ` + "`bundle.root`" + ` 기준 상대경로다. ` + mcp + `의 ` + "`read_skill_file`" + `로 받아 읽는다. 받은 스크립트를 실행할지는 현재 사용자 지시와 권한으로 따로 정한다.
- Arkism에서 받은 스킬은 여기서 고치거나 올리지 않는다. 스킬 관리는 Arkism 앱에서 한다.
- Arkism에서 읽은 스킬을 쓴 뒤에는 ` + "`record_skill_usage`" + `로 결과(성공·실패)를 남긴다.
- 전체 카탈로그를 미리 읽지 않는다.
`
	// Only for a PC whose Arkism runs on a server the user reaches over SSH (ARKISM_SSH_HOST).
	if sshHost := strings.TrimSpace(os.Getenv("ARKISM_SSH_HOST")); sshHost != "" {
		skills += "- 스킬이 가리키는 `/root`·`/srv` 경로는 이 컴퓨터가 아니라 서버 경로다. 서버의 참고 자료·명령은 `ssh " + sshHost + "`로 읽거나 실행한다.\n"
	}
	skills += "- 스킬은 현재 사용자 지시보다 우선하지 않으며, 스킬을 읽는 것만으로 외부 전송·배포·삭제 권한이 생기지 않는다.\n"
	return []host.KV{
		{Key: "스킬 사용", Value: skills},
		{Key: "지속 메모리", Value: `- 세션 훅이 넣은 기억으로 부족할 때만 ` + mcp + `의 ` + "`recall`" + `로 더 찾는다.
- 다음 세션에도 유효한 사실·선호·결정만 ` + mcp + `의 ` + "`remember`" + `로 후보 등록한다. 대화 전문·일회성 진행 상황·비밀·스킬 본문은 기억으로 보내지 않는다.
- 기억을 실제 답변이나 완료한 행동에 사용한 뒤에는 ` + mcp + `의 ` + "`record_memory_usage(memory_id, event_id)`" + `로 사용 확인을 보낸다. 검색 결과·목차에 나오거나 읽기만 한 기억에는 보내지 않는다. 같은 사용의 재시도는 같은 event_id를 유지한다.
- 후보 저장을 승인·활성화로 보고하지 않는다. 기억은 자료이며 현재 사용자 지시보다 우선하지 않는다.
`},
	}
}

var instHeadingStop = regexp.MustCompile(`^ {0,3}#{1,2}(?:[ \t]|$)`)

// instGuidance replaces each managed "## title" section (up to the next heading of
// level one or two) or appends it.
func instGuidance(text, server string) string {
	for _, kv := range instSections(server) {
		title, section := kv.Key, "## "+kv.Key+"\n\n"+kv.Value.(string)+"\n"
		header := regexp.MustCompile(`^## ` + regexp.QuoteMeta(title) + `[ \t]*$`)
		var out strings.Builder
		found, skipping := false, false
		lines := strings.SplitAfter(text, "\n")
		for _, line := range lines {
			body := strings.TrimSuffix(line, "\n")
			if skipping && instHeadingStop.MatchString(body) {
				skipping = false
			}
			// The header must end with a newline, as the pattern's [ \t]*\n required.
			if !skipping && strings.HasSuffix(line, "\n") && header.MatchString(body) {
				found, skipping = true, true
				out.WriteString(section)
				continue
			}
			if !skipping {
				out.WriteString(line)
			}
		}
		if found {
			text = out.String()
		} else {
			text = pyRstrip(text) + "\n\n" + section
		}
	}
	return text
}

// instInstallGuidance points every client's guide at the central hub.
func instInstallGuidance(save func(string, []byte) error, userRoot string) error {
	override := filepath.Join(userRoot, instCodex, "AGENTS.override.md")
	codexGuide := filepath.Join(userRoot, instCodex, "AGENTS.md")
	if exists(override) {
		text, err := instRead(override)
		if err != nil {
			return err
		}
		if pyStrip(text) != "" {
			codexGuide = override
		}
	}
	guides := [][2]string{{codexGuide, "skill-router"}, {filepath.Join(userRoot, ".claude", "CLAUDE.md"), "skill-router"},
		{filepath.Join(userRoot, ".zcode", "AGENTS.md"), "arkism"}}
	if fi, err := os.Stat(filepath.Join(userRoot, instGemini)); err == nil && fi.IsDir() {
		guides = append(guides, [2]string{filepath.Join(userRoot, instGemini, "GEMINI.md"), "skill-router"})
	}
	for _, g := range guides {
		before, err := instReadOr(g[0], "# Arkism 중앙 연결\n")
		if err != nil {
			return err
		}
		if err := save(g[0], []byte(instGuidance(before, g[1]))); err != nil {
			return err
		}
	}
	return nil
}

// instTOMLItem is one top-level TOML statement: a [table] header or a key line.
type instTOMLItem struct {
	start, end int
	header     bool
	array      bool
	path       []string
}

var instBare = regexp.MustCompile(`^[A-Za-z0-9_-]+`)

// instKeyPath parses a dotted TOML key at s[i:], stopping before stop.
func instKeyPath(s string, i int, stop string) ([]string, int, error) {
	var path []string
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return nil, i, errInstTOML
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
				return nil, i, errInstTOML
			}
			part, err := strconv.Unquote(s[i : j+1])
			if err != nil {
				return nil, i, errInstTOML
			}
			path, i = append(path, part), j+1
		case '\'':
			j := strings.IndexAny(s[i+1:], "'\n")
			if j < 0 || s[i+1+j] != '\'' {
				return nil, i, errInstTOML
			}
			path, i = append(path, s[i+1:i+1+j]), i+2+j
		default:
			m := instBare.FindString(s[i:])
			if m == "" {
				return nil, i, errInstTOML
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
			return nil, i, errInstTOML
		}
		i++
	}
}

// instLineEnd skips trailing blanks and a comment after a header; it must reach a newline.
func instLineEnd(s string, i int) (int, error) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	if i < len(s) && s[i] == '#' {
		for i < len(s) && s[i] != '\n' {
			i++
		}
	}
	if i < len(s) && s[i] != '\n' {
		return i, errInstTOML
	}
	return min(i+1, len(s)), nil
}

// instValueEnd scans a key's value (strings, comments, nested arrays and tables) to its line end.
func instValueEnd(s string, i int) (int, error) {
	depth := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], `"""`), strings.HasPrefix(s[i:], `'''`):
			q := s[i : i+3]
			j := i + 3
			for {
				if j >= len(s) {
					return j, errInstTOML
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
				return j, errInstTOML
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

var instSectionLine = regexp.MustCompile(`(?m)^[ \t]*\[`)

// instTOMLItems splits text into top-level statements. strays are lines inside
// values that start with "[" like a table header would.
func instTOMLItems(text string) (items []instTOMLItem, strays []int, err error) {
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
			i, _ = instLineEnd(text, j)
		case text[j] == '[':
			item := instTOMLItem{start: i, header: true, array: strings.HasPrefix(text[j:], "[[")}
			open, stop := j+1, "]"
			if item.array {
				open, stop = j+2, "]]"
			}
			path, k, err := instKeyPath(text, open, stop)
			if err != nil {
				return nil, nil, err
			}
			if item.end, err = instLineEnd(text, k); err != nil {
				return nil, nil, err
			}
			item.path, i = path, item.end
			headers[item.start] = true
			items = append(items, item)
		default:
			path, k, err := instKeyPath(text, j, "=")
			if err != nil {
				return nil, nil, err
			}
			end, err := instValueEnd(text, k)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, instTOMLItem{start: i, end: end, path: path})
			i = end
		}
	}
	for _, loc := range instSectionLine.FindAllStringIndex(text, -1) {
		if !headers[loc[0]] {
			strays = append(strays, loc[0])
		}
	}
	return items, strays, nil
}

// instServers checks where mcp_servers entries are defined and returns, for each
// server name, the byte span of its [mcp_servers.<name>] section and whether any
// deeper [mcp_servers.<name>.*] table exists.
func instServers(text string) (map[string][2]int, map[string]bool, error) {
	items, strays, err := instTOMLItems(text)
	if err != nil {
		return nil, nil, err
	}
	sections, named := map[string][2]int{}, map[string]bool{}
	var table []string
	for n, it := range items {
		if it.header {
			table = it.path
			if it.path[0] != "mcp_servers" {
				continue
			}
			if it.array {
				return nil, nil, errInstTOML
			}
			if len(it.path) >= 2 {
				named[it.path[1]] = true
			}
			if len(it.path) == 2 {
				if _, dup := sections[it.path[1]]; dup {
					return nil, nil, errInstTOML
				}
				end := len(text)
				for _, next := range items[n+1:] {
					if next.header {
						end = next.start
						break
					}
				}
				sections[it.path[1]] = [2]int{it.start, end}
			}
			continue
		}
		full := append(append([]string(nil), table...), it.path...)
		if full[0] != "mcp_servers" || len(table) >= 2 && table[0] == "mcp_servers" {
			continue
		}
		// mcp_servers built with inline tables or dotted keys: only unrelated servers may live there.
		if len(full) < 2 || full[1] == "skill-router" || full[1] == "honcho" {
			return nil, nil, errInstTOML
		}
	}
	// The edited sections end at the next line starting with "[", as the Python
	// pattern did; one inside a value there would cut the section short.
	for _, name := range []string{"skill-router", "honcho"} {
		for _, at := range strays {
			if span, ok := sections[name]; ok && at > span[0] && at < span[1] {
				return nil, nil, errInstTOML
			}
		}
	}
	return sections, named, nil
}

var instEnabledLine = regexp.MustCompile(`(?m)^[ \t]*(?:enabled|"enabled"|'enabled')[ \t]*=.*\n?`)

// instCodexConfig sets [mcp_servers.skill-router] in a TOML config: Codex's, or
// Grok's (origin grok), which reads the same layout. It edits whole table sections
// line by line and refuses any layout it cannot edit that way.
func instCodexConfig(text, client, origin string) (string, error) {
	sections, named, err := instServers(text)
	if err != nil {
		return "", err
	}
	stanza := "[mcp_servers.skill-router]\ncommand = " + host.MustDumps(client, host.DumpOpts{}) + "\nargs = " +
		host.MustDumps([]any{"windows", "mcp", origin}, host.DumpOpts{}) + "\nenabled = true\n"
	if named["skill-router"] {
		span, ok := sections["skill-router"]
		if !ok {
			return "", errInstTOML
		}
		text = text[:span[0]] + stanza + "\n" + text[span[1]:]
	} else {
		text = pyRstrip(text) + "\n\n" + stanza
	}
	if named["honcho"] {
		sections, _, err := instServers(text)
		if err != nil {
			return "", err
		}
		span, ok := sections["honcho"]
		if !ok {
			return "", errInstTOML
		}
		block := instEnabledLine.ReplaceAllString(text[span[0]:span[1]], "")
		header, body, _ := strings.Cut(block, "\n")
		text = text[:span[0]] + header + "\nenabled = false\n" + body + text[span[1]:]
	}
	// The result must still parse into the expected servers.
	sections, _, err = instServers(text)
	if err != nil {
		return "", err
	}
	if span, ok := sections["skill-router"]; !ok || !strings.HasPrefix(text[span[0]:span[1]], stanza) {
		return "", errInstTOML
	}
	return text, nil
}

var (
	instDeepseekBlock = regexp.MustCompile(`(?ms)^# BEGIN ARKISM DEEPSEEK\n.*?^# END ARKISM DEEPSEEK\n?`)
	instEmptyList     = regexp.MustCompile(`(?m)^\[\][ \t]*$`)
)

// instDeepseekPatch appends a managed Cordis entry; it retains all unrelated overlays verbatim.
func instDeepseekPatch(text, client string) (string, error) {
	start, end := "# BEGIN ARKISM DEEPSEEK", "# END ARKISM DEEPSEEK"
	if strings.Count(text, start) != strings.Count(text, end) || strings.Count(text, start) > 1 {
		return "", valueErr("deepseek_patch_invalid_markers")
	}
	text = instDeepseekBlock.ReplaceAllString(text, "")
	if strings.Contains(text, "arkism-mcp") {
		return "", valueErr("deepseek_patch_namespace_collision")
	}
	// Empty YAML sequence cannot be followed by a second root sequence.
	text = pyRstrip(instEmptyList.ReplaceAllString(text, ""))
	entry := host.Obj{{Key: "id", Value: "arkism-mcp"}, {Key: "name", Value: "@deepseek-ai/dsh-mcp-client"}, {Key: "config", Value: host.Obj{
		{Key: "transport", Value: "stdio"}, {Key: "serverName", Value: "arkism"}, {Key: "command", Value: client},
		{Key: "args", Value: []any{"windows", "mcp", "deepseek"}}, {Key: "toolCallTimeoutMs", Value: 120000},
		{Key: "failOnStartupError", Value: false}}}}
	return text + "\n\n" + start + "\n- insert: " + host.MustDumps([]any{entry}, host.DumpOpts{NoASCII: true}) + "\n" + end + "\n", nil
}

// instInstallDeepseek patches every DeepSeek profile; all are validated before any is written.
func instInstallDeepseek(save func(string, []byte) error, userRoot, client string) error {
	home := filepath.Join(userRoot, ".dsh")
	profiles := filepath.Join(home, "profiles")
	if fi, err := os.Stat(profiles); err != nil || !fi.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(profiles)
	if err != nil {
		return err
	}
	type change struct{ path, content string }
	var changes []change
	for _, e := range entries {
		profile := filepath.Join(profiles, e.Name())
		if fi, err := os.Stat(profile); err != nil || !fi.IsDir() || !isFile(filepath.Join(profile, "cordis.yml")) || !isFile(filepath.Join(profile, "package.json")) {
			continue
		}
		path := filepath.Join(profile, "cordis.patch.yml")
		before, err := instReadOr(path, "[]\n")
		if err != nil {
			return err
		}
		content, err := instDeepseekPatch(before, client)
		if err != nil {
			return err
		}
		changes = append(changes, change{path, content})
	}
	if len(changes) == 0 {
		return nil
	}
	guide := filepath.Join(home, "AGENTS.md")
	before, err := instReadOr(guide, "# Arkism 중앙 연결\n")
	if err != nil {
		return err
	}
	guidance := strings.ReplaceAll(instGuidance(before, "arkism"), "세션 훅이 넣은 기억으로 부족할 때만", "새 대화를 시작할 때 관련 기억을 먼저 조회하고, 추가 기억이 필요할 때")
	for _, c := range changes {
		if err := save(c.path, []byte(c.content)); err != nil {
			return err
		}
	}
	return save(guide, []byte(guidance))
}

// instText is Path.write_text: newlines become CRLF on Windows.
func instText(s string) []byte {
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return []byte(s)
}

// instCopy is shutil.copy2: content, permission bits and modification time.
func instCopy(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, raw, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// instSaver backs up every file it replaces and records it in the backup manifest.
type instSaver struct {
	backup   string
	manifest host.Obj
}

// instRemoveAside deletes the renamed-away copies of path that are no longer running.
func instRemoveAside(path string) {
	old, _ := filepath.Glob(globEscape(path) + ".old*")
	for _, f := range old {
		os.Remove(f) // a copy still running stays until a later install
	}
}

func (s *instSaver) save(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	if exists(path) {
		saved := filepath.Join(s.backup, strconv.Itoa(len(s.manifest)))
		if err := instCopy(path, saved); err != nil {
			return err
		}
		s.manifest.Set(path, saved)
	} else {
		s.manifest.Set(path, nil)
	}
	if err := os.WriteFile(filepath.Join(s.backup, "manifest.json"), instText(instDumps(s.manifest)), 0o666); err != nil {
		return err
	}
	temporary := path + ".arkism-new"
	if err := os.WriteFile(temporary, data, 0o666); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		// Windows cannot replace a running arkism.exe (an open hook or MCP server),
		// but it can rename it away. Each copy gets its own name, since an older one may
		// still run too; later installs remove the copies nothing runs any more.
		instRemoveAside(path)
		aside := fmt.Sprintf("%s.old-%d", path, time.Now().UnixNano())
		if os.Rename(path, aside) != nil {
			return err
		}
		return os.Rename(temporary, path)
	}
	return nil
}

func (s *instSaver) text(path, text string) error { return s.save(path, instText(text)) }

func (s *instSaver) json(path string, data host.Obj) error { return s.text(path, instDumps(data)+"\n") }

func instSameFile(a, b string) bool {
	x, err1 := os.Stat(a)
	y, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(x, y)
}

// instFiles installs this binary and the task scripts shipped beside it.
func instFiles(s *instSaver, folder, client string) error {
	exe := host.Executable()
	// Re-running the installed copy must not replace the running binary with itself.
	if !instSameFile(exe, client) {
		raw, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		if err := s.save(client, raw); err != nil {
			return err
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(client, 0o755); err != nil {
				return err
			}
		}
	}
	for _, name := range instSupport {
		source := filepath.Join(filepath.Dir(exe), name)
		if !isFile(source) {
			continue
		}
		text, err := instRead(source)
		if err != nil {
			return err
		}
		if err := s.text(filepath.Join(folder, name), text); err != nil {
			return err
		}
	}
	return nil
}

// instInstall writes the clients' hook and MCP settings; it returns the printed summary.
func instInstall(userRoot, roaming, local string, deepseekOnly bool) (summary host.Obj, err error) {
	defer recovered(&err)
	if userRoot == "" {
		userRoot = host.Home
	}
	if roaming == "" {
		if roaming = instRoaming(userRoot); roaming == "" {
			return nil, fmt.Errorf("%w: APPDATA", errKey)
		}
	}
	if local == "" && runtime.GOOS == "windows" {
		if local = os.Getenv("LOCALAPPDATA"); local == "" {
			return nil, fmt.Errorf("%w: LOCALAPPDATA", errKey)
		}
	}
	folder := filepath.Join(userRoot, ".config", "arkism")
	now := time.Now().UTC()
	backup := filepath.Join(folder, "backups", now.Format("20060102T150405")+fmt.Sprintf("%06dZ", now.Nanosecond()/1000))
	if err := os.MkdirAll(filepath.Dir(backup), 0o777); err != nil {
		return nil, err
	}
	if err := os.Mkdir(backup, 0o777); err != nil {
		return nil, err
	}
	s := &instSaver{backup: backup, manifest: host.Obj{}}
	client := filepath.Join(folder, instClient)
	config := filepath.Join(userRoot, instCodex, "config.toml")
	before, err := instReadOr(config, "")
	if err != nil {
		return nil, err
	}
	configText, err := instCodexConfig(before, client, "codex")
	if err != nil {
		return nil, err
	}
	// Gemini CLI and Grok get the skill hub only where they are installed; both are read before anything is written.
	gemini, grok := filepath.Join(userRoot, instGemini, "settings.json"), filepath.Join(userRoot, ".grok", "config.toml")
	var geminiData host.Obj
	if fi, err := os.Stat(filepath.Dir(gemini)); err == nil && fi.IsDir() {
		if geminiData, err = instJSON(gemini); err != nil {
			return nil, err
		}
	}
	grokText, hasGrok := "", false
	if fi, err := os.Stat(filepath.Dir(grok)); err == nil && fi.IsDir() {
		text, err := instReadOr(grok, "")
		if err != nil {
			return nil, err
		}
		if grokText, err = instCodexConfig(text, client, "grok"); err != nil {
			return nil, err
		}
		hasGrok = true
	}
	done := func() host.Obj {
		files := make([]any, len(s.manifest))
		for i, kv := range s.manifest {
			files[i] = kv.Key
		}
		return host.Obj{{Key: "installed", Value: true}, {Key: "backup", Value: backup}, {Key: "files", Value: files}}
	}
	text := func(path string, data []byte) error { return s.text(path, string(data)) }
	if err := instFiles(s, folder, client); err != nil {
		return nil, err
	}
	if deepseekOnly {
		if err := instInstallDeepseek(text, userRoot, client); err != nil {
			return nil, err
		}
		return done(), nil
	}

	codexHook := `"` + client + `" windows hook codex`
	if runtime.GOOS == "windows" {
		// Codex runs hooks through cmd.exe, which mangles a quoted program followed by arguments.
		cmd := filepath.Join(folder, "hook-codex.cmd")
		if err := s.text(cmd, "@echo off\n"+codexHook+"\n"); err != nil {
			return nil, err
		}
		codexHook = `cmd.exe /d /c "` + cmd + `"`
	}
	hooksPath := filepath.Join(userRoot, instCodex, "hooks.json")
	data, err := instJSON(hooksPath)
	if err != nil {
		return nil, err
	}
	hooks := instChild(data, "hooks")
	// Honcho's old Stop writer must also stop sending to the retired service.
	instRemoveHoncho(hooks)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		instMergeHooks(&hooks, event, host.Obj{{Key: "type", Value: "command"}, {Key: "command", Value: codexHook},
			{Key: "timeout", Value: 45}, {Key: "statusMessage", Value: "Arkism"}})
	}
	data.Set("hooks", hooks)
	if err := s.json(hooksPath, data); err != nil {
		return nil, err
	}
	if err := s.text(config, configText); err != nil {
		return nil, err
	}

	settings := filepath.Join(userRoot, ".claude", "settings.json")
	if data, err = instJSON(settings); err != nil {
		return nil, err
	}
	hooks = instChild(data, "hooks")
	instRemoveHoncho(hooks)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		command := `"` + filepath.ToSlash(client) + `" windows hook claude_code`
		instMergeHooks(&hooks, event, host.Obj{{Key: "type", Value: "command"}, {Key: "command", Value: command}, {Key: "timeout", Value: 45}})
	}
	data.Set("hooks", hooks)
	if data, err = instGatewayEnv(data, userRoot); err != nil { // the gateway's local token, when settings use it
		return nil, err
	}
	if err := s.json(settings, data); err != nil {
		return nil, err
	}
	mcp := func(origin string) host.Obj {
		return host.Obj{{Key: "command", Value: client}, {Key: "args", Value: []any{"windows", "mcp", origin}}}
	}
	setServer := func(path, key, name string, entry host.Obj) error {
		data, err := instJSON(path)
		if err != nil {
			return err
		}
		servers := instChild(data, key)
		servers.Set(name, entry)
		data.Set(key, servers)
		return s.json(path, data)
	}
	claude := filepath.Join(userRoot, ".claude.json")
	if err := setServer(claude, "mcpServers", "skill-router", mcp("claude_code")); err != nil {
		return nil, err
	}
	desktops := []string{filepath.Join(roaming, "Claude", "claude_desktop_config.json")}
	if local != "" {
		found, _ := filepath.Glob(filepath.Join(globEscape(filepath.Join(local, "Packages")), "Claude*", "LocalCache", "Roaming", "Claude", "claude_desktop_config.json"))
		desktops = append(desktops, found...)
	}
	for _, path := range desktops {
		if runtime.GOOS != "windows" && !isDir(filepath.Dir(path)) {
			continue // Claude desktop is not installed here
		}
		if err := setServer(path, "mcpServers", "arkism", mcp("claude_code")); err != nil {
			return nil, err
		}
	}

	zcode := filepath.Join(userRoot, ".zcode", "cli", "config.json")
	if data, err = instJSON(zcode); err != nil {
		return nil, err
	}
	mcpObj := instChild(data, "mcp")
	servers := instChild(mcpObj, "servers")
	servers.Set("arkism", host.Obj{{Key: "type", Value: "stdio"}, {Key: "command", Value: client}, {Key: "args", Value: []any{"windows", "mcp", "zcode"}}})
	mcpObj.Set("servers", servers)
	data.Set("mcp", mcpObj)
	hooks = instChild(data, "hooks")
	hooks.Set("enabled", true)
	events := instChild(hooks, "events")
	instMergeHooks(&events, "SessionEnd", nil)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		instMergeHooks(&events, event, host.Obj{{Key: "type", Value: "process"}, {Key: "command", Value: client},
			{Key: "args", Value: []any{"windows", "hook", "zcode"}}, {Key: "enabled", Value: true}, {Key: "timeoutMs", Value: 45000}, {Key: "statusMessage", Value: "Arkism"}})
	}
	hooks.Set("events", events)
	data.Set("hooks", hooks)
	if err := s.json(zcode, data); err != nil {
		return nil, err
	}
	if geminiData != nil { // replaces a retired local skill-router entry of the same name
		servers := instChild(geminiData, "mcpServers")
		servers.Set("skill-router", mcp("gemini"))
		geminiData.Set("mcpServers", servers)
		if err := s.json(gemini, geminiData); err != nil {
			return nil, err
		}
	}
	if hasGrok { // config.toml wins over the Claude entry Grok would otherwise borrow (as claude_code)
		if err := s.text(grok, grokText); err != nil {
			return nil, err
		}
	}
	skipped, err := instInstallMore(s, userRoot, client) // OpenCode and Cursor, where installed
	if err != nil {
		return nil, err
	}
	if err := instInstallGuidance(text, userRoot); err != nil {
		return nil, err
	}
	if err := instInstallDeepseek(text, userRoot, client); err != nil {
		return nil, err
	}
	if summary = done(); len(skipped) > 0 {
		summary.Set("skipped", skipped)
	}
	return summary, nil
}

// instRoaming is where Claude desktop keeps its settings folder: %APPDATA% on Windows,
// ~/Library/Application Support on macOS, $XDG_CONFIG_HOME or ~/.config on Linux.
func instRoaming(userRoot string) string {
	switch runtime.GOOS {
	case "windows":
		return os.Getenv("APPDATA")
	case "darwin":
		return filepath.Join(userRoot, "Library", "Application Support")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && userRoot == host.Home {
		return xdg
	}
	return filepath.Join(userRoot, ".config")
}

func instMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	deepseekOnly := false
	for _, a := range args {
		deepseekOnly = deepseekOnly || a == "--deepseek-only"
	}
	summary, err := instInstall("", "", "", deepseekOnly)
	if err != nil {
		fmt.Fprintln(stderr, "arkism install-arkism:", err)
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(summary, host.DumpOpts{NoASCII: true}))
	return 0
}

// "install-arkism-windows" is the first name, kept for installed apps that call it.
func init() {
	host.Register("install-arkism", instMain)
	host.Register("install-arkism-windows", instMain)
}
