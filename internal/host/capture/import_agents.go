package capture

// `arkism capture import-agents` brings in what the agents a person already uses remember: Claude
// Code's CLAUDE.md and auto-memory files, Codex's and Zcode's AGENTS.md, Gemini's GEMINI.md. Each file
// is split into self-contained statements that go to /v1/capture as backfill claims with basis
// "inferred", so every one lands as a candidate a person reviews; none becomes active on its own.
// The files are only read. The sections Arkism itself writes into them are left out, and so is
// anything in a folder the person excluded from capture.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

const (
	importMaxStatements = 300
	importMaxChars      = 1000
	importBatch         = 32 // the server takes at most 32 claims per event
)

// importSource is one file to read and the agent it belongs to.
type importSource struct {
	path   string
	label  string // path relative to home, with forward slashes: stable across computers
	origin string // event origin_product
	kind   string // kind of a statement without frontmatter
	repo   string
	auto   bool // a Claude auto-memory topic file (frontmatter + one memory per file)
}

// importStatement is one candidate memory.
type importStatement struct {
	ID      string
	Origin  string
	Subject string
	Text    string
	Kind    string
	Repo    string
	Label   string
	At      time.Time
}

type importReport struct {
	Files      int            `json:"files"`
	Statements int            `json:"statements"`
	ByKind     map[string]int `json:"by_kind"`
	Skipped    map[string]int `json:"skipped"`
}

type importSent struct {
	Sent       int `json:"sent"`
	Candidates int `json:"candidates"`
	Duplicates int `json:"duplicates"`
	Failed     int `json:"failed"`
}

// importKinds maps a Claude auto-memory frontmatter type to an Arkism kind.
var importKinds = map[string]string{"user": "user", "feedback": "procedural", "project": "semantic", "reference": "semantic"}

func importAgentsMain(args []string, stdout io.Writer) error {
	dry := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dry = true
		default:
			return fmt.Errorf("unknown argument %q", a)
		}
	}
	stmts, rep := importCollect()
	if dry {
		return importPrint(stdout, rep)
	}
	res := importSend(stmts, rep.Skipped["already_sent"])
	return importPrint(stdout, res)
}

func importPrint(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	return enc.Encode(v)
}

// importSources lists the files this computer has, in a stable order.
func importSources(skip map[string]int) []importSource {
	home := host.Home
	var out []importSource
	add := func(rel, origin, kind, repo string, auto bool) {
		p := filepath.Join(home, filepath.FromSlash(rel))
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return
		}
		out = append(out, importSource{path: p, label: rel, origin: origin, kind: kind, repo: repo, auto: auto})
	}
	globals := []struct{ rel, origin string }{
		{".claude/CLAUDE.md", "claude_code"}, {".codex/AGENTS.md", "codex"}, {".gemini/GEMINI.md", "gemini"}, {".zcode/AGENTS.md", "zcode"},
	}
	for _, g := range globals {
		if captureSkipped(filepath.Dir(filepath.Join(home, filepath.FromSlash(g.rel)))) {
			if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(g.rel))); err == nil {
				skip["excluded_folder"]++
			}
			continue
		}
		add(g.rel, g.origin, "procedural", "", false)
	}
	projects := filepath.Join(home, ".claude", "projects")
	dirs, _ := os.ReadDir(projects)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		mem := filepath.Join(projects, d.Name(), "memory")
		files, _ := filepath.Glob(filepath.Join(globEscape(mem), "*.md"))
		sort.Strings(files)
		if len(files) == 0 {
			continue
		}
		cwd := claudeProjectPath(d.Name())
		if captureSkipped(cwd) || captureSkipped(mem) {
			skip["excluded_folder"] += len(files)
			continue
		}
		repo := ""
		if cwd != "" && repoTagsOn() {
			repo = repoKey(cwd)
		}
		for _, f := range files {
			rel := ".claude/projects/" + d.Name() + "/memory/" + filepath.Base(f)
			if filepath.Base(f) == "MEMORY.md" {
				add(rel, "claude_code", "semantic", repo, false)
			} else {
				add(rel, "claude_code", "semantic", repo, true)
			}
		}
	}
	return out
}

// importCollect reads every source and returns the statements not yet sent, at most importMaxStatements.
func importCollect() ([]importStatement, importReport) {
	rep := importReport{ByKind: map[string]int{}, Skipped: map[string]int{}}
	sent := importLedgerRead()
	seen := map[string]bool{}
	var out []importStatement
	for _, src := range importSources(rep.Skipped) {
		raw, err := os.ReadFile(src.path)
		if err != nil {
			rep.Skipped["unreadable"]++
			continue
		}
		fi, err := os.Stat(src.path)
		if err != nil {
			continue
		}
		rep.Files++
		at := fi.ModTime().UTC()
		if now := time.Now().UTC(); at.After(now) {
			at = now
		}
		for _, s := range importSplit(src, string(raw), rep.Skipped) {
			s.At = at
			switch {
			case seen[s.ID]:
				rep.Skipped["duplicate"]++
			case sent[s.ID]:
				seen[s.ID] = true
				rep.Skipped["already_sent"]++
			case len(out) >= importMaxStatements:
				seen[s.ID] = true
				rep.Skipped["over_limit"]++
			default:
				seen[s.ID] = true
				out = append(out, s)
				rep.ByKind[s.Kind]++
			}
		}
	}
	rep.Statements = len(out)
	return out, rep
}

var (
	importHeading  = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t#]*$`)
	importBullet   = regexp.MustCompile(`^(\s*)(?:[-*+]|\d+[.)])[ \t]+(.*)$`)
	importFence    = regexp.MustCompile("^\\s*(```|~~~)")
	importRule     = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	importLink     = regexp.MustCompile(`!?\[([^\]]*)\]\(([^)\s]*)[^)]*\)`)
	importPathish  = regexp.MustCompile(`^(?:[a-zA-Z][a-zA-Z0-9+.-]*://\S+|[A-Za-z]:[\\/]\S*|[~./\\]\S*|\S*[/\\]\S*)$`)
	importBlank    = regexp.MustCompile(`\n{3,}`)
	importEmphasis = strings.NewReplacer("**", "", "__", "")
)

// importManaged reports whether a heading is one of the sections Arkism writes (its titles come
// from instSections, so a renamed section stays skipped).
func importManaged(level int, title string) bool {
	if level == 1 && title == strings.TrimPrefix(instGuideHeader, "# ") {
		return true
	}
	if level != 2 {
		return false
	}
	for _, kv := range instSections("") {
		if kv.Key == title {
			return true
		}
	}
	return false
}

// importSplit cuts one file into statements: bullets and paragraphs under their nearest heading.
// A Claude auto-memory file is one memory, so its body stays whole when it fits.
func importSplit(src importSource, text string, skip map[string]int) []importStatement {
	text = strings.ReplaceAll(strings.TrimPrefix(text, "\ufeff"), "\r\n", "\n")
	kind, name := src.kind, ""
	if fm, body, ok := importFrontmatter(text); ok {
		text = body
		name = strings.TrimSpace(fm["name"])
		if k, ok := importKinds[strings.ToLower(strings.TrimSpace(fm["type"]))]; ok {
			kind = k
		}
	}
	var (
		chunks  []string // finished statements, heading context already prefixed
		heading string
		cur     []string
		fence   string
		managed = false
		table   = false
		whole   []string // auto-memory body without code, tables, rules or Arkism sections
	)
	flush := func() {
		if len(cur) == 0 {
			return
		}
		s := strings.Join(cur, " ")
		cur = nil
		if s = importClean(s, skip); s != "" {
			if heading != "" {
				s = heading + ": " + s
			}
			chunks = append(chunks, s)
		}
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if fence != "" {
			if strings.HasPrefix(strings.TrimSpace(line), fence) {
				fence = ""
			}
			continue
		}
		if m := importFence.FindStringSubmatch(line); m != nil {
			flush()
			fence = m[1]
			skip["code_block"]++
			continue
		}
		if m := importHeading.FindStringSubmatch(line); m != nil {
			flush()
			level, title := len(m[1]), strings.TrimSpace(m[2])
			if managed && level <= 2 {
				managed = false
			}
			if importManaged(level, title) {
				skip["arkism_section"]++
				heading = ""
				managed = level == 2
				continue
			}
			if managed {
				continue
			}
			heading = importInline(title)
			if src.auto {
				whole = append(whole, heading)
			}
			continue
		}
		if managed {
			continue
		}
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "|") {
			flush()
			if !table {
				skip["table"]++
			}
			table = true
			continue
		}
		table = false
		if trim == "" || importRule.MatchString(line) || strings.HasPrefix(trim, "<!--") {
			flush()
			if src.auto && trim == "" {
				whole = append(whole, "")
			}
			continue
		}
		if src.auto {
			whole = append(whole, line)
		}
		if m := importBullet.FindStringSubmatch(line); m != nil {
			flush()
			cur = append(cur, m[2])
			continue
		}
		cur = append(cur, trim)
	}
	flush() // an unclosed fence hides the rest of the file, as a renderer would

	if src.auto {
		body := strings.TrimSpace(strings.Join(whole, "\n"))
		if body != "" && utf8.RuneCountInString(body) <= importMaxChars && importClean(body, map[string]int{}) != "" {
			chunks = []string{importTidy(body)}
		}
	}
	subject := importSubject(src, name)
	out := make([]importStatement, 0, len(chunks))
	for _, c := range chunks {
		if name != "" && src.auto {
			c = name + ": " + c
		}
		c = importTruncate(c)
		repo := src.repo
		if kind == "user" {
			repo = ""
		}
		out = append(out, importStatement{ID: importID(src.label, c), Origin: src.origin, Subject: subject, Text: c, Kind: kind, Repo: repo, Label: src.label})
	}
	return out
}

// importFrontmatter parses a leading "---" block of `key: value` lines.
func importFrontmatter(text string) (map[string]string, string, bool) {
	if !strings.HasPrefix(text, "---\n") {
		return nil, text, false
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, text, false
	}
	fm := map[string]string{}
	for _, l := range strings.Split(rest[:end], "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && !strings.HasPrefix(l, " ") {
			fm[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	body := rest[end+4:]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return fm, body, true
}

// importInline drops link targets and emphasis markers but keeps the words.
func importInline(s string) string {
	s = importLink.ReplaceAllString(s, "$1")
	return strings.TrimSpace(importEmphasis.Replace(s))
}

// importClean is the statement's text, or "" when it carries nothing to remember: only links,
// paths or URLs, or too few letters.
func importClean(s string, skip map[string]int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.TrimSpace(strings.Trim(importLink.ReplaceAllString(s, ""), " \t-–—:;,.|·")) == "" {
		skip["path_or_url"]++
		return ""
	}
	s = importInline(s)
	only := true
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, "`'\"()<>[],;:")
		if f != "" && !importPathish.MatchString(f) {
			only = false
			break
		}
	}
	if only {
		skip["path_or_url"]++
		return ""
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 4 {
		skip["too_short"]++
		return ""
	}
	return importTidy(s)
}

// importTidy keeps the text but makes its spacing stable, so the same statement hashes the same.
func importTidy(s string) string {
	lines := strings.Split(importInline(s), "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.TrimSpace(importBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func importTruncate(s string) string {
	if utf8.RuneCountInString(s) <= importMaxChars {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:importMaxChars-1])) + "…"
}

// importSubject is the frontmatter name or the file, within the server's 128-byte label limit.
func importSubject(src importSource, name string) string {
	s := name
	if s == "" {
		s = strings.TrimSuffix(filepath.Base(src.path), ".md")
		if !src.auto && s != "MEMORY" {
			s = src.origin + " " + s
		} else if s == "MEMORY" {
			s = "claude memory index"
		}
	}
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	for len(s) > 128 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		s = "agent memory"
	}
	return s
}

// importID is stable for the same statement in the same file, on any computer.
func importID(label, text string) string {
	sum := sha256.Sum256([]byte(label + "\n" + strings.ToLower(strings.Join(strings.Fields(text), " "))))
	return hex.EncodeToString(sum[:])[:24]
}

// claudeProjectPath turns a ~/.claude/projects folder name back into the folder it stands for.
// Claude Code writes the path with every character other than a letter or digit as "-", which is
// ambiguous, so the walk only follows folders that exist. "" when none matches.
func claudeProjectPath(name string) string {
	var root, rest string
	switch {
	case len(name) >= 3 && isASCIILetter(name[0]) && name[1:3] == "--":
		root, rest = name[:1]+`:\`, name[3:]
	case strings.HasPrefix(name, "-"):
		root, rest = string(filepath.Separator), name[1:]
		if filepath.Separator == '\\' {
			return ""
		}
	default:
		return ""
	}
	return importWalk(root, rest, 0)
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func importEncode(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return '-'
	}, s)
}

func importWalk(dir, rest string, depth int) string {
	if rest == "" {
		return dir
	}
	if depth > 64 {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		enc := importEncode(e.Name())
		if rest == enc {
			return filepath.Join(dir, e.Name())
		}
		if strings.HasPrefix(rest, enc+"-") {
			if p := importWalk(filepath.Join(dir, e.Name()), rest[len(enc)+1:], depth+1); p != "" {
				return p
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------- sending

func importLedgerPath() string { return filepath.Join(capState, "import-agents.json") }

// importLedgerRead is the statement ids an earlier run already delivered.
func importLedgerRead() map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(importLedgerPath())
	if err != nil {
		return out
	}
	var l struct {
		Sent []string `json:"sent"`
	}
	if json.Unmarshal(raw, &l) == nil {
		for _, id := range l.Sent {
			out[id] = true
		}
	}
	return out
}

func importLedgerAdd(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	all := importLedgerRead()
	for _, id := range ids {
		all[id] = true
	}
	list := make([]string, 0, len(all))
	for id := range all {
		list = append(list, id)
	}
	sort.Strings(list)
	raw, err := json.Marshal(map[string]any{"version": 1, "sent": list})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(capState, 0o700); err != nil {
		return err
	}
	return atomicWrite(importLedgerPath(), string(raw))
}

// importBatches groups statements by agent, at most importBatch per event, in reading order.
func importBatches(stmts []importStatement) [][]importStatement {
	var order []string
	by := map[string][]importStatement{}
	for _, s := range stmts {
		if _, ok := by[s.Origin]; !ok {
			order = append(order, s.Origin)
		}
		by[s.Origin] = append(by[s.Origin], s)
	}
	var out [][]importStatement
	for _, o := range order {
		list := by[o]
		for len(list) > 0 {
			n := min(importBatch, len(list))
			out = append(out, list[:n])
			list = list[n:]
		}
	}
	return out
}

// importEventID is stable for the same batch, so a resend after a lost reply is a duplicate event.
func importEventID(batch []importStatement) string {
	h := sha256.New()
	for _, s := range batch {
		h.Write([]byte(s.ID + "\n"))
	}
	return "agent-import:" + batch[0].Origin + ":" + hex.EncodeToString(h.Sum(nil))[:32]
}

func importSend(stmts []importStatement, alreadySent int) importSent {
	res := importSent{Duplicates: alreadySent}
	trace := "agent-import-" + time.Now().UTC().Format("20060102T150405Z")
	gen := host.Obj{{Key: "model", Value: "rule-based"}, {Key: "prompt_version", Value: "agent-import-split-v1"},
		{Key: "pipeline_version", Value: "agent-memory-import-v1"}}
	for _, batch := range importBatches(stmts) {
		claims := make([]any, 0, len(batch))
		at := batch[0].At
		for _, s := range batch {
			c := host.Obj{{Key: "subject", Value: s.Subject}, {Key: "text", Value: s.Text}, {Key: "kind", Value: s.Kind},
				{Key: "basis", Value: "inferred"}, {Key: "reason", Value: "source=agent_import path=" + importReasonPath(s.Label) + " id=" + s.ID}}
			if s.Repo != "" {
				c.Set("repo", s.Repo)
			}
			claims = append(claims, c)
			if s.At.After(at) {
				at = s.At
			}
		}
		payload := host.Obj{{Key: "scope", Value: capScope()}, {Key: "generator", Value: gen}, {Key: "claims", Value: claims},
			{Key: "event", Value: host.Obj{{Key: "id", Value: importEventID(batch)}, {Key: "type", Value: "backfill"},
				{Key: "origin_product", Value: batch[0].Origin}, {Key: "trace_id", Value: trace},
				{Key: "occurred_at", Value: at.Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")}}}}
		reply, err := capPost("/v1/capture", payload, 60*time.Second)
		items := listOf(reply["items"])
		if err != nil || len(items) != len(batch) {
			res.Failed += len(batch)
			metric(kv("event", "agent_import_failure"), kv("origin", batch[0].Origin), kv("error", fmt.Sprint(err)))
			continue
		}
		res.Sent += len(batch)
		var done []string
		for i, v := range items {
			item, _ := v.(map[string]any)
			status, _ := item["status"].(string)
			metric(kv("event", "agent_import"), kv("status", status), kv("reason", item["reason"]), kv("candidate_id", item["candidate_id"]))
			switch status {
			case "candidate", "accepted":
				res.Candidates++
				done = append(done, batch[i].ID)
			case "merged":
				res.Duplicates++
				done = append(done, batch[i].ID)
			default: // rejected: a transient one is retried by the next run
				res.Failed++
			}
		}
		if err := importLedgerAdd(done); err != nil {
			metric(kv("event", "agent_import_failure"), kv("error", "ledger_write"))
		}
	}
	return res
}

// importReasonPath keeps the reason under the server's 500-byte limit.
func importReasonPath(label string) string {
	for len(label) > 400 {
		_, n := utf8.DecodeRuneInString(label)
		label = label[n:]
	}
	return label
}
