package skills

// Arkism's own skill check: offline, deterministic, no external tools. It runs on every
// scan, alone on a PC without the NVIDIA scanners and next to them where they are installed.
// A finding is category · pattern · severity · file · line — never the matched text.

import (
	"bytes"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	builtinVersion      = "1"
	builtinMaxFileBytes = 1 << 20
	builtinMaxFiles     = 400
	builtinKeepFindings = 50
)

type builtinFinding struct {
	Category, Pattern, Severity, File string
	Line                              int
}

func (f builtinFinding) label() string {
	loc := runeCut(f.File, 60) + ":" + strconv.Itoa(f.Line)
	return byteCut(f.Category+" · "+f.Pattern+" · "+f.Severity+" · "+loc, 160)
}

// byteCut keeps s within n bytes on a rune boundary (the server caps labels in bytes).
func byteCut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// lineRule matches one line; ok, when set, must also accept the match (Go regexp has no lookahead).
type lineRule struct {
	category, pattern, severity string
	re                          *regexp.Regexp
	ok                          func(line string, m []int) bool
	skillMDOnly                 bool
}

var (
	reTokenBody   = regexp.MustCompile(`[0-9]`)
	reDevSafe     = regexp.MustCompile(`^(null|zero|u?random|stdout|stderr|tty|fd/)`)
	reUploadCmd   = regexp.MustCompile(`(?i)\bcurl\b[^\n]*(\s-d\b|\s-d['"@$]|--data(-binary|-raw|-urlencode)?\b|\s-F\b|--form\b|\s-T\b|--upload-file\b)|\bwget\b[^\n]*--post-(data|file)\b|\b(invoke-webrequest|invoke-restmethod|iwr|irm)\b[^\n]*(-body\b|-infile\b)`)
	reSensitiveIn = regexp.MustCompile(`(?i)\$\(\s*(env|printenv|set)\b|\x60\s*(env|printenv)\b|\b(env|printenv)\s*\||\$\{?[a-z_]*(token|secret|api_?key|password|passwd)[a-z_]*\}?|\$env:[a-z_]*(token|secret|key|password)|@\s*["']?(~|\$home|/etc/passwd|/etc/shadow|[^\s"']*\.env\b|[^\s"']*\.ssh/|[^\s"']*\.aws/)`)
	reURL         = regexp.MustCompile(`https?://[^\s"'<>)\x60]+`)
	knownHosts    = []string{"localhost", "127.0.0.1", "::1", "0.0.0.0", "github.com", "api.github.com", "uploads.github.com", "api.openai.com", "api.anthropic.com", "generativelanguage.googleapis.com", "example.com"}
	reRemoveItem  = regexp.MustCompile(`(?i)\b(remove-item|ri)\b[^\n]*-recurse`)
	rePSRootHome  = regexp.MustCompile(`(?i)(^|[\s'"=,])(~|\$home|\$env:userprofile|\$env:homedrive|[a-z]:\\?|[/\\])[/\\]?(\*)?(['"]|\s|$|;)`)
)

var builtinRules = []lineRule{
	// hard-coded secrets
	{category: "secret", pattern: "private_key_block", severity: "CRITICAL", re: regexp.MustCompile(`-----BEGIN (RSA |EC |DSA |OPENSSH |ENCRYPTED |PGP )?PRIVATE KEY( BLOCK)?-----`)},
	{category: "secret", pattern: "aws_access_key", severity: "CRITICAL", re: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
		ok: func(l string, m []int) bool { return !strings.Contains(l[m[0]:m[1]], "EXAMPLE") }},
	{category: "secret", pattern: "github_token", severity: "CRITICAL", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{22,}`), ok: realToken},
	{category: "secret", pattern: "anthropic_key", severity: "CRITICAL", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`), ok: realToken},
	{category: "secret", pattern: "openai_key", severity: "CRITICAL", re: regexp.MustCompile(`\bsk-(proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`),
		ok: func(l string, m []int) bool { return !strings.HasPrefix(l[m[0]:], "sk-ant-") && realToken(l, m) }},
	// remote code execution
	{category: "remote_exec", pattern: "curl_pipe_shell", severity: "HIGH", re: regexp.MustCompile(`(?i)\bcurl\b[^\n|]*\|\s*(sudo\s+(-\S+\s+)*)?(ba|z|da|k|fi)?sh\b`)},
	{category: "remote_exec", pattern: "wget_pipe_shell", severity: "HIGH", re: regexp.MustCompile(`(?i)\bwget\b[^\n|]*\|\s*(sudo\s+(-\S+\s+)*)?(ba|z|da|k|fi)?sh\b`)},
	{category: "remote_exec", pattern: "iex_download", severity: "HIGH", re: regexp.MustCompile(`(?i)\biex\b\s*[\(\s]*\s*(iwr|irm|invoke-webrequest|invoke-restmethod|\(?\s*new-object\s+(system\.)?net\.webclient)\b`)},
	{category: "remote_exec", pattern: "invoke_expression_download", severity: "HIGH", re: regexp.MustCompile(`(?i)\binvoke-expression\b[^\n]*(downloadstring|\biwr\b|\birm\b|invoke-webrequest|invoke-restmethod)|(downloadstring|invoke-webrequest|invoke-restmethod)[^\n]*\|\s*(invoke-expression|iex)\b`)},
	{category: "remote_exec", pattern: "powershell_encoded", severity: "HIGH", re: regexp.MustCompile(`(?i)\b(powershell|pwsh)(\.exe)?\b[^\n]*\s-(e|ec|en|enc|encodedcommand)\s+[A-Za-z0-9+/=]{16,}`)},
	// destructive commands
	{category: "destructive", pattern: "rm_rf_root_or_home", severity: "CRITICAL", re: regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rR][a-zA-Z]*f[a-zA-Z]*|-[a-zA-Z]*f[a-zA-Z]*[rR][a-zA-Z]*|-[rR]\s+-f|-f\s+-[rR]|--recursive\s+--force|--force\s+--recursive)\s+(--no-preserve-root\s+)?("?(/|~|\$HOME|\$\{HOME\})"?/?\*?)(\s|$|[;&|])`)},
	{category: "destructive", pattern: "remove_item_recurse_root_or_home", severity: "CRITICAL", re: reRemoveItem,
		ok: func(l string, m []int) bool { return rePSRootHome.MatchString(l[m[0]:]) }},
	{category: "destructive", pattern: "mkfs", severity: "CRITICAL", re: regexp.MustCompile(`\bmkfs(\.[a-z0-9]+)?\s+(-\S+\s+)*/dev/`)},
	{category: "destructive", pattern: "dd_to_device", severity: "CRITICAL", re: regexp.MustCompile(`\bdd\b[^\n]*\bof=/dev/(\S*)`),
		ok: func(l string, m []int) bool { return !reDevSafe.MatchString(l[m[2]:m[3]]) }},
	// credential-file reads
	{category: "credential_access", pattern: "ssh_private_key", severity: "HIGH", re: regexp.MustCompile(`\.ssh[/\\]id_(rsa|dsa|ecdsa|ed25519)\b`),
		ok: func(l string, m []int) bool { return !strings.HasPrefix(l[m[1]:], ".pub") }},
	{category: "credential_access", pattern: "aws_credentials", severity: "HIGH", re: regexp.MustCompile(`\.aws[/\\]credentials\b`)},
	{category: "credential_access", pattern: "netrc", severity: "HIGH", re: regexp.MustCompile(`(^|[/\\\s"'~=])[._]netrc\b`)},
	{category: "credential_access", pattern: "browser_cookies", severity: "HIGH", re: regexp.MustCompile(`(?i)cookies\.sqlite\b|(google[/\\ ]chrome|chromium|microsoft[/\\ ]edge|brave-browser|bravesoftware)[^\n]*[/\\](network[/\\])?cookies\b`)},
	// exfiltration (conservative): an upload command whose payload is env vars or secret files, to a literal unknown host
	{category: "exfiltration", pattern: "secrets_to_unknown_url", severity: "HIGH", re: reUploadCmd, ok: exfilLine},
	// prompt injection, SKILL.md only
	{category: "prompt_injection", pattern: "ignore_previous_instructions", severity: "HIGH", skillMDOnly: true, re: regexp.MustCompile(`(?i)\b(ignore|disregard|forget)\s+(all\s+)?(of\s+)?(the\s+|your\s+|any\s+)?(previous|prior|above|earlier|preceding|system)\s+(instructions|rules|prompts?|directions|messages)\b|이전\s*(의\s*)?(지시|명령|지침|규칙)[^\n]{0,12}무시`)},
	{category: "prompt_injection", pattern: "hide_from_user", severity: "HIGH", skillMDOnly: true, re: regexp.MustCompile(`(?i)\b(do\s+not|don't|never)\s+(tell|inform|notify|alert|show|reveal\s+(this\s+)?to|mention\s+(this\s+)?to|let)\s+the\s+user\b|사용자에게\s*(알리지|말하지|보여\s*주지|밝히지)\s*(마|말)`)},
	{category: "prompt_injection", pattern: "override_system_prompt", severity: "MEDIUM", skillMDOnly: true, re: regexp.MustCompile(`(?i)\b(override|bypass|ignore)\s+(the\s+|your\s+)?(system\s+prompt|safety\s+(rules|guidelines)|guardrails)\b|\byou\s+are\s+now\s+(in\s+)?(developer\s+mode|dan\b|jailbroken)`)},
}

// realToken drops placeholders such as sk-xxxxxxxxxxxxxxxxxxxx or ghp_XXXX…: a real token has digits and varied characters.
func realToken(l string, m []int) bool {
	tok := l[m[0]:m[1]]
	if !reTokenBody.MatchString(tok) {
		return false
	}
	seen := map[rune]bool{}
	for _, r := range tok {
		seen[r] = true
	}
	return len(seen) >= 10
}

func exfilLine(l string, m []int) bool {
	if !reSensitiveIn.MatchString(l) {
		return false
	}
	urls := reURL.FindAllString(l, -1)
	if len(urls) == 0 {
		return false // the target is a variable: not enough to call it exfiltration
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || strings.Contains(u.Hostname(), "$") {
			continue
		}
		h := strings.ToLower(u.Hostname())
		known := false
		for _, k := range knownHosts {
			if h == k || strings.HasSuffix(h, "."+k) {
				known = true
				break
			}
		}
		if !known {
			return true
		}
	}
	return false
}

// builtinFiles lists the folder's regular files (no links, no .git), at most builtinMaxFiles, in path order.
func builtinFiles(folder string) []string {
	var rels []string
	_ = filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error { // the callback never fails
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" && p != folder {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(folder, p)
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	sortPaths(rels)
	return rels[:min(len(rels), builtinMaxFiles)]
}

// builtinReadText is the file's text, or nil for binaries and files over 1 MB.
func builtinReadText(p string) []byte {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > builtinMaxFileBytes {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil || bytes.IndexByte(raw[:min(len(raw), 8192)], 0) >= 0 {
		return nil
	}
	return raw
}

// builtinSecurity scans every text file of the folder line by line.
func builtinSecurity(folder string) []builtinFinding {
	var out []builtinFinding
	seen := map[builtinFinding]bool{}
	for _, rel := range builtinFiles(folder) {
		raw := builtinReadText(filepath.Join(folder, filepath.FromSlash(rel)))
		if raw == nil {
			continue
		}
		isSkillMD := rel == "SKILL.md"
		for i, line := range strings.Split(string(raw), "\n") {
			if len(line) > 8192 {
				line = line[:8192]
			}
			for _, r := range builtinRules {
				if r.skillMDOnly && !isSkillMD {
					continue
				}
				m := r.re.FindStringSubmatchIndex(line)
				if m == nil || r.ok != nil && !r.ok(line, m) {
					continue
				}
				f := builtinFinding{r.category, r.pattern, r.severity, rel, i + 1}
				if !seen[f] {
					seen[f] = true
					out = append(out, f)
				}
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return severityRank[out[a].Severity] < severityRank[out[b].Severity] })
	return out
}

// builtinVerdict maps findings to the SkillSpector vocabulary the app already shows.
func builtinVerdict(list []builtinFinding) (int, string) {
	weight := map[string]int{"CRITICAL": 40, "HIGH": 25, "MEDIUM": 10, "LOW": 3}
	risk, worst := 0, 9
	for _, f := range list {
		risk += weight[f.Severity]
		worst = min(worst, severityRank[f.Severity])
	}
	risk = min(risk, 100)
	switch {
	case worst == 0:
		return risk, "DO_NOT_INSTALL"
	case worst <= 2:
		return risk, "CAUTION"
	}
	return risk, "SAFE"
}

var (
	reMDLink  = regexp.MustCompile(`!?\[[^\]]*\]\(\s*<?([^)\s>]+)>?(\s+"[^"]*")?\s*\)`)
	reRefPath = regexp.MustCompile("(?:^|[\\s`'\"(\\[])((?:scripts|references|assets)/[A-Za-z0-9._/-]*[A-Za-z0-9_-]\\.[A-Za-z0-9]{1,8})\\b")
)

// skillFrontmatter returns the top-level keys of the leading --- block (folded and literal values joined) and the body.
func skillFrontmatter(text string) (map[string]string, string, bool) {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(text, "---\n") {
		return nil, text, false
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return nil, text, false
	}
	head, body := text[4:4+end], text[4+end+4:]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	keys, cur := map[string]string{}, ""
	for _, l := range strings.Split(head, "\n") {
		if l != "" && l[0] != ' ' && l[0] != '\t' && l[0] != '#' {
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				cur = ""
				continue
			}
			cur = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
				v = ""
			}
			keys[cur] = strings.Trim(v, `"'`)
		} else if cur != "" && strings.TrimSpace(l) != "" {
			keys[cur] = strings.TrimSpace(keys[cur] + " " + strings.TrimSpace(l))
		}
	}
	return keys, body, true
}

// builtinQuality is a 0–100 score for how well-formed the skill folder is, and what cost points.
func builtinQuality(folder string) (float64, []string) {
	raw := builtinReadText(filepath.Join(folder, "SKILL.md"))
	if raw == nil {
		return 0, []string{"SKILL.md 없음"}
	}
	score, issues := 20, []string{}
	keys, body, ok := skillFrontmatter(string(raw))
	name, desc := keys["name"], keys["description"]
	switch {
	case !ok:
		issues = append(issues, "frontmatter 없음")
	default:
		if name != "" {
			score += 15
		} else {
			issues = append(issues, "name 없음")
		}
		if desc != "" {
			score += 10
		} else {
			issues = append(issues, "description 없음")
		}
	}
	switch n := utf8.RuneCountInString(desc); {
	case desc == "":
	case n < 20:
		issues = append(issues, "description이 너무 짧음")
		score += 5
	case n > 1024:
		issues = append(issues, "description이 너무 김")
		score += 5
	default:
		score += 15
	}
	trimmed := strings.TrimSpace(body)
	switch lines := strings.Count(trimmed, "\n") + 1; {
	case trimmed == "":
		issues = append(issues, "본문 없음")
	case utf8.RuneCountInString(trimmed) < 80:
		issues = append(issues, "본문이 너무 짧음")
	case lines > 800 || len(trimmed) > 100<<10:
		issues = append(issues, "본문이 너무 김")
		score += 5
	default:
		score += 20
	}
	exists := func(rel string) bool {
		p := filepath.Clean(filepath.Join(folder, filepath.FromSlash(rel)))
		if r, err := filepath.Rel(folder, p); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return false
		}
		_, err := os.Stat(p)
		return err == nil
	}
	missingRefs, broken := builtinQualityLinks(body, exists)
	if missingRefs == 0 {
		score += 10
	} else {
		issues = append(issues, "언급한 파일 "+strconv.Itoa(missingRefs)+"개 없음")
	}
	if broken == 0 {
		score += 10
	} else {
		issues = append(issues, "깨진 상대 링크 "+strconv.Itoa(broken)+"개")
	}
	return float64(min(score, 100)), issues
}

// builtinQualityLinks counts mentioned files and relative markdown links that do not exist in the folder.
func builtinQualityLinks(body string, exists func(string) bool) (int, int) {
	missingRefs := 0
	for _, m := range reRefPath.FindAllStringSubmatch(body, -1) {
		if !exists(m[1]) {
			missingRefs++
		}
	}
	broken := 0
	for _, m := range reMDLink.FindAllStringSubmatch(body, -1) {
		target := m[1]
		if i := strings.IndexAny(target, "#?"); i >= 0 {
			target = target[:i]
		}
		if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "~") {
			continue
		}
		if t, err := url.PathUnescape(target); err == nil {
			target = t
		}
		if !exists(target) {
			broken++
		}
	}
	return missingRefs, broken
}

func builtinGrade(score float64) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	}
	return "F"
}

func findingsAny(list []builtinFinding) []any {
	out := []any{}
	for _, f := range list[:min(len(list), builtinKeepFindings)] {
		out = append(out, map[string]any{"category": f.Category, "pattern": f.Pattern, "severity": f.Severity, "file": f.File, "line": f.Line})
	}
	return out
}

// labelRank is the severity rank of a "category · pattern · severity[ · file:line]" label.
func labelRank(label string) int {
	for _, p := range strings.Split(label, " · ") {
		if r, ok := severityRank[strings.ToUpper(p)]; ok {
			return r
		}
	}
	return 9
}

// mergeTop keeps the three most severe distinct labels of both scanners.
func mergeTop(a, b []string) []string {
	all := append(append([]string{}, a...), b...)
	sort.SliceStable(all, func(i, j int) bool { return labelRank(all[i]) < labelRank(all[j]) })
	out := []string{}
	for _, l := range all {
		if l != "" && !containsStr(out, l) {
			out = append(out, l)
		}
		if len(out) == 3 {
			break
		}
	}
	return out
}

func worseVerdict(a, b string) string {
	rank := map[string]int{"SAFE": 0, "CAUTION": 1, "DO_NOT_INSTALL": 2}
	if ra, ok := rank[a]; !ok || rank[b] > ra {
		return b
	}
	return a
}
