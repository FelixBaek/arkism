package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const checksUsage = `Score and security-scan this host's agent skill folders for Arkism.

  scan   check every skill folder whose content changed since the last scan and
         write the results to CACHE (run hourly by arkism-skill-checks.timer;
         agents sync sends CACHE)
  show   print CACHE

Built in (always runs, offline, no tools needed): secrets, remote execution,
destructive commands, credential-file reads, exfiltration hints and SKILL.md
prompt injection → SAFE | CAUTION | DO_NOT_INSTALL; SKILL.md structure → 0–100.

Optional scanners, used next to the built-in check when installed
(` + "`uv tool install`" + `, Apache-2.0, no API keys):
  NVIDIA SkillEvaluator  ` + "`skillevaluator quality-check DIR -r json`" + `  → 0–100 score, A–F grade
  NVIDIA SkillSpector    ` + "`skillspector scan DIR --no-llm --format json`" + ` → risk 0–100,
                         SAFE | CAUTION | DO_NOT_INSTALL
Neither paid tier runs here (no LLM judge, no agent runs). What leaves this host per
skill is the folder name and path, the two scores, and finding category · pattern ·
severity (· file:line for built-in findings): never the matched text, a code
snippet or file contents.
`

func init() { host.Register("skill-checks", checksMain) }

type agentRoot struct{ agent, root string }

var (
	checksHome   = host.Home
	checksState  = host.AgentsState
	checksCache  = filepath.Join(host.AgentsState, "skill-checks.json")
	toolsBin     = host.Env("ARKISM_SKILL_TOOLS_BIN", filepath.Join(host.Home, ".local/bin"))
	evaluatorBin = host.Env("ARKISM_SKILLEVALUATOR_BIN", filepath.Join(toolsBin, "skillevaluator"))
	spectorBin   = host.Env("ARKISM_SKILLSPECTOR_BIN", filepath.Join(toolsBin, "skillspector"))
	// Same folders the desktop app lists under 설정 › 에이전트.
	checkRoots = []agentRoot{{"claude_code", filepath.Join(host.Home, ".claude/skills")}, {"codex", filepath.Join(host.Home, ".codex/skills")},
		{"codex", filepath.Join(host.Home, ".agents/skills")}, {"zcode", filepath.Join(host.Home, ".zcode/skills")}}
	checkName    = regexp.MustCompile(`^[A-Za-z0-9@._-]{1,128}$`)
	severityRank = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "INFO": 4}
	semver       = regexp.MustCompile(`(\d+\.\d+\.\d+)`)
)

const (
	checkMaxFiles = 400
	checkMaxBytes = 8 << 20
)

// runtimeError and timeoutExpired carry the Python exception names the cache reports.
type runtimeError struct{ msg string }

func (e runtimeError) Error() string { return e.msg }

var errTimeoutExpired = errors.New("timed out")

func pyErrName(err error) string {
	var rt runtimeError
	switch {
	case errors.As(err, &rt):
		return "RuntimeError"
	case errors.Is(err, errTimeoutExpired):
		return "TimeoutExpired"
	case errors.Is(err, exec.ErrNotFound):
		return "FileNotFoundError"
	}
	return pyStr(failureCode(err))
}

// skillDirs is (agent, folder) for every direct child holding a SKILL.md; one level deep, like the agents read them.
func skillDirs() [][2]string {
	var out [][2]string
	for _, r := range checkRoots {
		entries, err := os.ReadDir(r.root)
		if err != nil {
			continue
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		sort.Strings(names)
		for _, n := range names {
			d := filepath.Join(r.root, n)
			if isDir(d) && isFile(filepath.Join(d, "SKILL.md")) && checkName.MatchString(n) {
				out = append(out, [2]string{r.agent, d})
			}
		}
	}
	return out
}

// contentHash hashes relative paths and bytes; "" when the folder is too big to scan.
func contentHash(folder string) (string, error) {
	var rels []string
	err := filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != folder {
			rel, _ := filepath.Rel(folder, p)
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sortPaths(rels)
	h, files, total := sha256.New(), 0, int64(0)
	underGit := containsStr(strings.Split(filepath.ToSlash(folder), "/"), ".git")
	for _, rel := range rels {
		p := filepath.Join(folder, filepath.FromSlash(rel))
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() || underGit || containsStr(strings.Split(rel, "/"), ".git") {
			continue
		}
		files++
		total += st.Size()
		if files > checkMaxFiles || total > checkMaxBytes {
			return "", nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		h.Write([]byte(filepath.FromSlash(rel) + "\x00"))
		h.Write(raw)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func scanTool(timeout time.Duration, argv ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = time.Second // a grandchild holding the pipe must not outlive the timeout
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "ARKISM_CAPTURE_CHILD=1")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", "", errTimeoutExpired
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return "", "", err
	}
	return out.String(), errOut.String(), nil
}

func readReport(path string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadsStrict(string(raw))
}

func isPyNumber(v any) bool {
	switch v.(type) {
	case json.Number, float64, int, int64, bool:
		return true
	}
	return false
}

// quality is (score, grade) from SkillEvaluator's quality_summary. A failed quality gate still has a score.
func quality(folder string) (float64, string, error) {
	tmp, err := os.MkdirTemp("", "arkism-se-")
	if err != nil {
		return 0, "", err
	}
	defer os.RemoveAll(tmp)
	if _, _, err := scanTool(180*time.Second, evaluatorBin, "quality-check", folder, "-r", "json", "-o", tmp); err != nil {
		return 0, "", err
	}
	doc, err := readReport(filepath.Join(tmp, "skillevaluator-quality.json"))
	if err != nil {
		return 0, "", err
	}
	list := asList(asMap(doc)["quality_summary"])
	summary := map[string]any{}
	if len(list) > 0 {
		summary = asMap(list[0])
	}
	score := summary["overall_score"]
	if !isPyNumber(score) {
		return 0, "", runtimeError{"품질 점수를 못 읽었어요"}
	}
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(numFloat(score), 'f', 1, 64), 64)
	return rounded, runeCut(pyStr(firstOr(summary["grade"], "")), 1), nil
}

func findingLabel(issue map[string]any) string {
	var parts []string
	for _, k := range []string{"category", "pattern", "severity"} {
		if p := runeCut(pyStrip(pyStr(firstOr(issue[k], ""))), 60); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

type securityResult struct {
	risk    any // int or nil
	verdict string
	n       int
	top     []string
}

// security is (risk, verdict, finding count, top three category · pattern · severity) from SkillSpector.
func security(folder string) (securityResult, error) {
	tmp, err := os.MkdirTemp("", "arkism-ss-")
	if err != nil {
		return securityResult{}, err
	}
	defer os.RemoveAll(tmp)
	report := filepath.Join(tmp, "report.json")
	if _, _, err := scanTool(180*time.Second, spectorBin, "scan", folder, "--no-llm", "--format", "json", "--output", report); err != nil {
		return securityResult{}, err
	}
	v, err := readReport(report)
	if err != nil {
		return securityResult{}, err
	}
	doc := asMap(v)
	risk := orMap(doc["risk_assessment"])
	issues := append([]any(nil), asList(doc["issues"])...)
	rank := func(i any) (int, float64) {
		r, ok := severityRank[strings.ToUpper(pyStr(asMap(i)["severity"]))]
		if !ok {
			r = 9
		}
		return r, -numFloat(firstOr(asMap(i)["confidence"], 0))
	}
	sort.SliceStable(issues, func(a, b int) bool {
		ra, ca := rank(issues[a])
		rb, cb := rank(issues[b])
		return ra < rb || ra == rb && ca < cb
	})
	res := securityResult{verdict: pyStr(firstOr(risk["recommendation"], "")), n: len(issues), top: []string{}}
	for _, i := range issues {
		if label := findingLabel(asMap(i)); label != "" && !containsStr(res.top, label) {
			res.top = append(res.top, label)
		}
		if len(res.top) == 3 {
			break
		}
	}
	if score := risk["score"]; isPyNumber(score) {
		res.risk = max(0, min(100, int(numFloat(score))))
	}
	return res, nil
}

func homePath(folder string) string {
	rel, _ := filepath.Rel(checksHome, folder)
	return "~/" + filepath.ToSlash(rel)
}

func check(agent, folder, digest string) host.Obj {
	entry := host.Obj{{Key: "agent", Value: agent}, {Key: "name", Value: filepath.Base(folder)}, {Key: "path", Value: homePath(folder)}, {Key: "findings", Value: 0},
		{Key: "scanned_at", Value: time.Now().UTC().Format("2006-01-02T15:04:05Z")}, {Key: "hash", Value: nil}}
	if digest == "" {
		entry.Set("error", "폴더가 너무 커서 건너뛰었어요")
		return entry
	}
	entry.Set("hash", digest)
	entry.Set("source", checkSource())
	entry.Set("builtin_version", builtinVersion)
	var errs []string
	// The built-in check always runs; the NVIDIA scanners add to it where they are installed.
	bScore, bIssues := builtinQuality(folder)
	bFindings := builtinSecurity(folder)
	bRisk, bVerdict := builtinVerdict(bFindings)
	var bTop []string
	for _, f := range bFindings {
		if l := f.label(); !containsStr(bTop, l) && len(bTop) < 3 {
			bTop = append(bTop, l)
		}
	}
	qScore, qGrade := bScore, builtinGrade(bScore)
	if toolInstalled(evaluatorBin) {
		if score, grade, err := quality(folder); err != nil {
			errs = append(errs, "품질: "+pyErrName(err))
		} else {
			qScore, qGrade = score, grade
		}
	}
	entry.Set("quality_score", qScore)
	entry.Set("quality_grade", qGrade)
	risk, verdict, n, top := any(bRisk), bVerdict, len(bFindings), bTop
	if toolInstalled(spectorBin) {
		if res, err := security(folder); err != nil {
			errs = append(errs, "보안: "+pyErrName(err))
			if bVerdict == "SAFE" { // a broken scanner is an error, not a pass
				verdict, risk = "", nil
			}
		} else {
			if r, ok := res.risk.(int); ok {
				risk = max(r, bRisk)
			}
			if containsStr([]string{"SAFE", "CAUTION", "DO_NOT_INSTALL"}, res.verdict) {
				verdict = worseVerdict(res.verdict, bVerdict)
			}
			n += res.n
			top = mergeTop(res.top, bTop)
		}
	}
	if risk != nil {
		entry.Set("risk_score", risk)
	}
	if verdict != "" {
		entry.Set("verdict", verdict)
	}
	entry.Set("findings", n)
	entry.Set("top_findings", stringsAny(append([]string{}, top...)))
	entry.Set("quality_issues", stringsAny(bIssues))
	entry.Set("builtin_findings", findingsAny(bFindings))
	if errs != nil {
		entry.Set("error", strings.Join(errs, " · "))
	}
	return entry
}

func toolInstalled(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// checkSource names what produced an entry; an entry from another set of scanners is rescanned.
func checkSource() string {
	if toolInstalled(evaluatorBin) || toolInstalled(spectorBin) {
		return "builtin+nvidia"
	}
	return "builtin"
}

func scannerVersions() string {
	out := []string{"arkism-builtin " + builtinVersion}
	for _, t := range [][2]string{{"skillevaluator", evaluatorBin}, {"skillspector", spectorBin}} {
		if !toolInstalled(t[1]) {
			continue
		}
		so, se, err := scanTool(30*time.Second, t[1], "--version")
		if err != nil {
			out = append(out, t[0]+" 없음")
			continue
		}
		v := "?"
		if m := semver.FindStringSubmatch(so + se); m != nil {
			v = m[1]
		}
		out = append(out, t[0]+" "+v)
	}
	return strings.Join(out, " · ")
}

func loadChecks() host.Obj {
	o := readOrdered(checksCache)
	if o == nil {
		return host.Obj{}
	}
	return o
}

func scanChecks(stderr io.Writer) int {
	if err := os.MkdirAll(checksState, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	unlock, ok, err := tryLock(filepath.Join(checksState, "skill-checks.lock"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !ok {
		return 0
	}
	defer unlock()
	old := map[[2]string]host.Obj{}
	for _, c := range asList(objGet(loadChecks(), "checks")) {
		o := asObj(c)
		old[[2]string{pyStr(objGet(o, "agent")), pyStr(objGet(o, "path"))}] = o
	}
	checks, source := []any{}, checkSource()
	for _, ad := range skillDirs() {
		digest, err := contentHash(ad[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		prev := old[[2]string{ad[0], homePath(ad[1])}]
		if prev != nil && digest != "" && objGet(prev, "hash") == digest && !truthy(objGet(prev, "error")) &&
			objGet(prev, "source") == source && objGet(prev, "builtin_version") == builtinVersion {
			checks = append(checks, prev)
		} else {
			checks = append(checks, check(ad[0], ad[1], digest))
		}
	}
	doc := host.Obj{{Key: "tools", Value: scannerVersions()}, {Key: "checks", Value: checks}}
	tmp := strings.TrimSuffix(checksCache, filepath.Ext(checksCache)) + ".tmp"
	if err := os.WriteFile(tmp, []byte(host.MustDumps(doc, host.DumpOpts{NoASCII: true})), 0o600); err == nil {
		if err = os.Chmod(tmp, 0o600); err == nil {
			err = os.Rename(tmp, checksCache)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// checksReport is what agents sync sends: nil until the first scan, so the server keeps what it has.
func checksReport() ([]any, string) {
	doc := loadChecks()
	raw, ok := doc.Get("checks")
	if !ok {
		return nil, ""
	}
	out := []any{}
	for _, c := range asList(raw) {
		row := host.Obj{}
		for _, kv := range asObj(c) {
			// Cache-only keys: the server's skill_checks schema rejects unknown fields.
			if !containsStr([]string{"hash", "source", "builtin_version", "quality_issues", "builtin_findings"}, kv.Key) {
				row = append(row, kv)
			}
		}
		out = append(out, row)
	}
	return out[:min(len(out), 2000)], runeCut(str(objGet(doc, "tools")), 120)
}

func checksMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "scan":
		return scanChecks(stderr)
	case "show":
		printJSON(stdout, loadChecks(), 1)
	default:
		io.WriteString(stdout, checksUsage+"\n")
		return 2
	}
	return 0
}
