package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const reviewUsage = `Review candidate skill versions: list, diff, approve (apply) or reject.

Approval follows the hub rule: an independent evaluation first, then a
human approves and applies.

  list                       candidates waiting for review
  diff   SKILL_ID@N          unified diff against the active version
  approve SKILL_ID@N         worker token records an automated_check
                             (hash, frontmatter, size, secret scan), then the
                             owner token approves and applies it
          [--evals FILE]     behavior check first: each eval prompt runs
                             without the skill (RED) and with it (GREEN) on
                             an isolated ` + "`claude -p`" + `; a judge grades both
                             against expected_output. Any GREEN failure
                             blocks approval. Defaults to evals/evals.json
                             next to the skill's source SKILL.md, then
                             $SKILL_EVALS_DIR/<skill_id>/evals.json.
  availability SKILL_ID       owner switch on the whole skill; omitted parts
          [--status active|deprecated]   stay as they are. deprecated: routing
          [--unsupported RUNTIME ...]    and latest-approved stop serving it
                             (pins still replay); unsupported: never routed
                             to that runtime (e.g. claude_code, codex)
  approve-pins [--yes]       candidates whose SKILL.md equals the active version and only
                             add pinned companion files (first pinned sync): list them,
                             then with --yes approve each (deterministic checks, no
                             behavior eval since the instructions are unchanged)
  reject SKILL_ID@N          owner token rejects the candidate
  rollback SKILL_ID@N        owner token re-activates an earlier (superseded)
                             version, e.g. after a runtime rewrote its skill
                             back to older content

Tokens: ARKISM_TOKEN_FILE (owner, human) and ARKISM_WORKER_TOKEN_FILE
(service). Running approve/reject is the human decision.
`

func init() { host.Register("skill-review", reviewMain) }

var (
	reviewURL    = strings.TrimRight(host.Env("ARKISM_URL", host.DefaultURL), "/")
	reviewOwner  = host.Env("ARKISM_TOKEN_FILE", "/srv/arkism/config/owner.token")
	reviewWorker = host.Env("ARKISM_WORKER_TOKEN_FILE", "/srv/arkism/config/worker.token")
	evalModel    = host.Env("SKILL_EVAL_MODEL", host.HarnessCLIModel("evaluator", "sonnet"))
	// Evals for skills whose folder is owned by another tool (e.g. a runtime rewrites its system skills on update).
	evalsDir    = host.Env("SKILL_EVALS_DIR", filepath.Join(host.SkillsRoot, "evals"))
	judgeSchema = `{"type": "object", "required": ["A", "B", "reason"], "properties": {"A": {"type": "boolean"}, "B": {"type": "boolean"}, "reason": {"type": "string"}}}`
)

const reviewMaxBytes = 64000

// Test seams: the hub calls, the evaluation CLI and the judge's coin.
var (
	reviewCall       = reviewHTTP
	reviewResolve    = resolveRef
	reviewCandidates = candidates
	reviewApprove    = approveRef
	claudeRun        = claudeCLI
	coin             = rand.Float64 // math/rand: only shuffles A/B order against judge position bias, not a secret
)

func reviewHTTP(method, path string, payload any, tokenFile string) (map[string]any, error) {
	if tokenFile == "" {
		tokenFile = reviewOwner
	}
	tok, err := readToken(tokenFile)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		raw, err := host.Dumps(payload, host.DumpOpts{})
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, reviewURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := host.DoJSON(http.DefaultClient, req)
	var httpErr *host.HTTPError
	if errors.As(err, &httpErr) {
		head, _, _ := strings.Cut(path, "?")
		return nil, hubErr(fmt.Sprintf("http_%d", httpErr.Code), method+" "+head+" "+decodeReplace(httpErr.Body[:min(200, len(httpErr.Body))]))
	}
	return res, err
}

func reviewQuery() string { return "?" + scopeQueryAll() }

func resolveRef(ref string) (map[string]any, error) {
	res, err := reviewCall("POST", "/v1/skills/resolve", host.Obj{{Key: "scope", Value: host.Scope()}, {Key: "refs", Value: []any{ref}}}, "")
	if err != nil {
		return nil, err
	}
	items := asList(res["items"])
	if len(items) == 0 {
		return nil, errors.New("list index out of range")
	}
	item := asMap(items[0])
	if truthy(item["error"]) {
		return nil, hubErr(pyStr(item["error"]), ref)
	}
	return item, nil
}

func resolveApproved(skillID string) (map[string]any, error) {
	return reviewResolve(skillID + "@latest-approved")
}

type changeKey struct{ skillID, hash string }

// changeSummaries is what sync-skills wrote down about each updated version.
func changeSummaries() (map[changeKey]any, error) {
	rows, err := jsonLines(syncChanges, false)
	out := map[changeKey]any{}
	for _, r := range rows {
		c := asMap(r)
		out[changeKey{pyStr(c["skill_id"]), pyStr(c["content_hash"])}] = c["summary"]
	}
	return out, err
}

func candidates() ([]any, error) {
	changes, err := changeSummaries()
	if err != nil {
		return nil, err
	}
	out, cursor := []any{}, ""
	for {
		path := "/v1/skills" + reviewQuery() + "&limit=200"
		if cursor != "" {
			path += "&cursor=" + pyQuote(cursor)
		}
		page, err := reviewCall("GET", path, nil, "")
		if err != nil {
			return nil, err
		}
		withheld := asList(page["withheld"])
		for _, w := range withheld {
			fmt.Fprintf(os.Stderr, "경고: %s의 활성 버전을 제공할 수 없음(검증 기록 확인 필요)\n", pyStr(w))
		}
		skills := slices.Clone(asList(page["items"]))
		for _, w := range withheld {
			skills = append(skills, map[string]any{"id": w})
		}
		for _, s := range skills {
			id := pyStr(asMap(s)["id"])
			parent, err := reviewCall("GET", "/v1/skills/"+pyQuote(id)+reviewQuery(), nil, "")
			if err != nil {
				return nil, err
			}
			history := asList(parent["version_history"])
			if len(history) == 0 {
				history = []any{parent["active_version"]}
			}
			for n, v := range history {
				if !truthy(v) || sameJSON(v, parent["active_version"]) {
					continue
				}
				version, err := reviewCall("GET", "/v1/skill_versions/"+pyQuote(pyStr(v))+reviewQuery(), nil, "")
				if err != nil {
					return nil, err
				}
				if version["status"] == "candidate" {
					hash := pyStr(orMap(version["analysis"])["content_hash"])
					out = append(out, host.Obj{{Key: "ref", Value: fmt.Sprintf("%s@%d", id, n+1)}, {Key: "version_id", Value: v},
						{Key: "created_at", Value: version["created_at"]}, {Key: "change", Value: changes[changeKey{id, hash}]}})
				}
			}
		}
		cursor = str(page["next_cursor"])
		if cursor == "" {
			return out, nil
		}
	}
}

func bundleByPath(item map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, f := range asList(orMap(item["bundle"])["files"]) {
		out[pyStr(asMap(f)["path"])] = asMap(f)
	}
	return out
}

func diffRef(ref string) (string, error) {
	item, err := reviewResolve(ref)
	if err != nil {
		return "", err
	}
	active, err := resolveApproved(pyStr(item["skill_id"]))
	if err != nil {
		return "", err
	}
	a, b := pyStr(active["skill_id"])+"@"+pyStr(active["resolved_version"]), ref
	out := unifiedDiff(splitLinesKeep(str(active["instructions"])), splitLinesKeep(str(item["instructions"])), a, b, 3)
	// Companion files are part of the version: a body-only diff must not hide a script change.
	oldFiles, newFiles := bundleByPath(active), bundleByPath(item)
	_, activeHas := active["bundle"]
	_, itemHas := item["bundle"]
	if activeHas != itemHas {
		out += fmt.Sprintf("# 보조 파일 고정 여부가 다름: %s=%s, %s=%s\n", a, pyStr(activeHas), ref, pyStr(itemHas))
	}
	paths := map[string]bool{}
	for p := range oldFiles {
		paths[p] = true
	}
	for p := range newFiles {
		paths[p] = true
	}
	for _, path := range sortedKeys(paths) {
		x, y := oldFiles[path], newFiles[path]
		if x != nil && y != nil && sameJSON(x["sha256"], y["sha256"]) {
			continue
		}
		_, xo := x["omitted"]
		_, yo := y["omitted"]
		if xo || yo {
			out += fmt.Sprintf("# %s: 해시만 고정된 파일 변경 %s -> %s\n", path, pyStr(getOr(x, "sha256", "-")), pyStr(getOr(y, "sha256", "-")))
			continue
		}
		out += unifiedDiff(splitLinesKeep(contentOf(x)), splitLinesKeep(contentOf(y)), a+"/"+path, b+"/"+path, 3)
	}
	return out, nil
}

func contentOf(f map[string]any) string {
	if s, ok := f["content"].(string); ok {
		return s
	}
	return ""
}

// automatedCheck runs deterministic checks only; the human reads the diff for meaning.
func automatedCheck(item map[string]any, state string) []string {
	text := str(item["instructions"])
	var problems []string
	if meta := frontmatter(text); meta["name"] == "" || meta["description"] == "" {
		problems = append(problems, "frontmatter name/description missing")
	}
	if len(text) > reviewMaxBytes {
		problems = append(problems, "larger than 64000 bytes")
	}
	if secretRx.MatchString(text) {
		problems = append(problems, "credential-like string")
	}
	if item["validation_status"] != state {
		problems = append(problems, "not "+state+": "+pyStr(item["validation_status"]))
	}
	return problems
}

// claudeCLI is a text-only `claude -p` on the subscription login, without user settings, hooks, MCP or CLAUDE.md.
func claudeCLI(prompt, system, schema string) (any, error) {
	// The prompt goes on stdin: eval prompts come from skill files and one starting with "-" would be read as a flag.
	cmd := []string{"-p", "--model", evalModel, "--setting-sources", "", "--strict-mcp-config",
		"--tools", "", "--no-session-persistence", "--output-format", "json"}
	if system != "" {
		cmd = append(cmd, "--append-system-prompt", system)
	}
	if schema != "" {
		cmd = append(cmd, "--json-schema", schema)
	}
	dir, err := os.MkdirTemp("", "skill-eval-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "claude", cmd...)
	c.WaitDelay = time.Second // a grandchild holding the pipe must not outlive the timeout
	c.Dir = dir
	c.Stdin = strings.NewReader(prompt)
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil && ctx.Err() != nil {
		return nil, err
	} else if errors.Is(err, exec.ErrNotFound) {
		return nil, err
	}
	v, err := loadsStrict(stdout.String())
	out, isObj := v.(map[string]any)
	if err != nil || !isObj {
		detail := stderr.String()
		if detail == "" {
			detail = stdout.String()
		}
		return nil, hubErr("eval_run_failed", runeCut(detail, 200))
	}
	if truthy(out["is_error"]) {
		return nil, hubErr("eval_run_failed", runeCut(pyStr(out["result"]), 200))
	}
	if schema != "" {
		return out["structured_output"], nil
	}
	return out["result"], nil
}

// behaviorEval is RED/GREEN per eval; the judge sees both answers in random order as A/B.
func behaviorEval(instructions string, evals []any) ([]host.Obj, error) {
	var rows []host.Obj
	for _, raw := range evals {
		c := asMap(raw)
		prompt := str(c["prompt"])
		red, err := claudeRun(prompt, "", "")
		if err != nil {
			return nil, err
		}
		green, err := claudeRun(prompt, instructions, "")
		if err != nil {
			return nil, err
		}
		flip := coin() < 0.5
		a, b := red, green
		if flip {
			a, b = green, red
		}
		expected := str(c["expected_output"])
		for _, x := range asList(c["assertions"]) {
			expected += "\n- " + pyStr(x)
		}
		v, err := claudeRun("Grade two answers to the same request. For each, true only if it satisfies the "+
			"expected behavior below; ignore style.\n\n## Request\n"+prompt+"\n\n## Expected behavior\n"+expected+
			"\n\n## Answer A\n"+pyStr(a)+"\n\n## Answer B\n"+pyStr(b), "", judgeSchema)
		if err != nil {
			return nil, err
		}
		verdict := asMap(v)
		greenOK, redOK := verdict["B"], verdict["A"]
		if flip {
			greenOK, redOK = verdict["A"], verdict["B"]
		}
		id, ok := c["id"]
		if !ok {
			id = len(rows) + 1
		}
		rows = append(rows, host.Obj{{Key: "id", Value: id}, {Key: "red", Value: redOK}, {Key: "green", Value: greenOK}, {Key: "reason", Value: verdict["reason"]}})
	}
	return rows, nil
}

func findEvals(version map[string]any, path string) ([]any, bool, error) {
	var paths []string
	if path != "" {
		paths = []string{path}
	} else {
		if source := str(orMap(version["analysis"])["source_path"]); source != "" {
			paths = append(paths, filepath.Join(filepath.Dir(source), "evals", "evals.json"))
		}
		if id := version["skill_id"]; truthy(id) {
			paths = append(paths, filepath.Join(evalsDir, pyStr(id), "evals.json"))
		}
	}
	for _, p := range paths {
		if isFile(p) {
			raw, err := os.ReadFile(p)
			if err != nil {
				return nil, false, err
			}
			v, err := host.Loads(string(raw))
			if err != nil {
				return nil, false, err
			}
			evals, ok := asMap(v)["evals"]
			if !ok {
				return nil, false, keyErr("evals")
			}
			return asList(evals), true, nil
		}
	}
	return nil, false, nil
}

// evaluate runs the deterministic check, an optional behavior eval, then an independent
// automated_check evaluation by the worker. It returns the version and the evaluation id.
func evaluate(item map[string]any, evalsPath, state string, behavior bool) (map[string]any, any, error) {
	if problems := automatedCheck(item, state); problems != nil {
		return nil, nil, hubErr("automated_check_failed", strings.Join(problems, "; "))
	}
	version, err := reviewCall("GET", "/v1/skill_versions/"+pyQuote(pyStr(item["version_id"]))+reviewQuery(), nil, "")
	if err != nil {
		return nil, nil, err
	}
	target := host.Obj{{Key: "collection", Value: "skill_versions"}, {Key: "id", Value: version["id"]}, {Key: "revision", Value: version["revision"]}}
	var evals []any
	found := false
	if behavior {
		if evals, found, err = findEvals(version, evalsPath); err != nil {
			return nil, nil, err
		}
	}
	if evalsPath != "" && !found {
		return nil, nil, hubErr("evals_not_found", evalsPath)
	}
	if len(evals) > 0 {
		rows, err := behaviorEval(str(item["instructions"]), evals)
		if err != nil {
			return nil, nil, err
		}
		green, red, delta := 0, 0, 0
		var failures []string
		for _, r := range rows {
			g, rd := truthy(objGet(r, "green")), truthy(objGet(r, "red"))
			if g {
				green++
			} else {
				failures = append(failures, fmt.Sprintf("eval %s: %s", pyStr(objGet(r, "id")), pyStr(objGet(r, "reason"))))
			}
			if rd {
				red++
			}
			if g && !rd {
				delta++
			}
		}
		passed := green == len(rows)
		rowsAny := make([]any, len(rows))
		for i, r := range rows {
			rowsAny[i] = r
		}
		text := fmt.Sprintf("behavior_eval (%s): with skill %d/%d, without skill %d/%d, skill-only wins %d\n", evalModel, green, len(rows), red, len(rows), delta) +
			runeCut(host.MustDumps(rowsAny, host.DumpOpts{NoASCII: true}), 20000)
		if _, err := reviewCall("POST", "/v1/evaluations", host.Obj{{Key: "scope", Value: host.Scope()}, {Key: "passed", Value: passed}, {Key: "target", Value: target},
			{Key: "sources", Value: version["sources"]}, {Key: "text", Value: text},
			{Key: "evaluation", Value: host.Obj{{Key: "eval_type", Value: "automated_check"}, {Key: "passed", Value: passed},
				{Key: "score", Value: float64(green)}, {Key: "score_max", Value: float64(len(rows))}}}}, reviewWorker); err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(os.Stderr, "behavior_eval: with skill %d/%d, skill-only wins %d\n", green, len(rows), delta)
		if !passed {
			return nil, nil, hubErr("behavior_eval_failed", runeCut(strings.Join(failures, "; "), 500))
		}
	}
	evaluation, err := reviewCall("POST", "/v1/evaluations", host.Obj{{Key: "scope", Value: host.Scope()},
		{Key: "text", Value: "automated_check: frontmatter, size, secret scan, " + state + " state passed"},
		{Key: "passed", Value: true}, {Key: "evaluation", Value: host.Obj{{Key: "eval_type", Value: "automated_check"}, {Key: "passed", Value: true}}},
		{Key: "target", Value: target}, {Key: "sources", Value: version["sources"]}}, reviewWorker)
	if err != nil {
		return nil, nil, err
	}
	return version, evaluation["id"], nil
}

func approveRef(ref, evalsPath string, behavior bool) (map[string]any, error) {
	item, err := reviewResolve(ref)
	if err != nil {
		return nil, err
	}
	version, evaluationID, err := evaluate(item, evalsPath, "candidate", behavior)
	if err != nil {
		return nil, err
	}
	approved, err := reviewCall("POST", "/v1/skill_versions/"+pyQuote(pyStr(version["id"]))+"/transition", host.Obj{{Key: "scope", Value: host.Scope()},
		{Key: "expected_revision", Value: version["revision"]}, {Key: "status", Value: "approved"}, {Key: "validation_id", Value: evaluationID}}, "")
	if err != nil {
		return nil, err
	}
	skill := pyQuote(pyStr(item["skill_id"]))
	parent, err := reviewCall("GET", "/v1/skills/"+skill+reviewQuery(), nil, "")
	if err != nil {
		return nil, err
	}
	if _, err := reviewCall("POST", "/v1/skills/"+skill+"/apply", host.Obj{{Key: "scope", Value: host.Scope()},
		{Key: "expected_revision", Value: parent["revision"]}, {Key: "version_id", Value: approved["id"]}}, ""); err != nil {
		return nil, err
	}
	return resolveApproved(pyStr(item["skill_id"]))
}

// pinOnlyCandidates are candidates whose SKILL.md equals the active version and which only add
// pinned companions (the first pinned sync): approving them changes no instructions.
func pinOnlyCandidates() ([]host.Obj, []host.Obj, error) {
	list, err := reviewCandidates()
	if err != nil {
		return nil, nil, err
	}
	var pins, skipped []host.Obj
	for _, c := range list {
		ref := pyStr(field(c, "ref"))
		item, err := reviewResolve(ref)
		if err != nil {
			return nil, nil, err
		}
		active, err := resolveApproved(pyStr(item["skill_id"]))
		var hubE *host.SkillHubError
		if errors.As(err, &hubE) {
			skipped = append(skipped, host.Obj{{Key: "ref", Value: ref}, {Key: "reason", Value: hubE.Code}})
			continue
		} else if err != nil {
			return nil, nil, err
		}
		_, itemHas := item["bundle"]
		_, activeHas := active["bundle"]
		if itemHas && !activeHas && sameJSON(item["content_hash"], active["content_hash"]) {
			stored, hashOnly := 0, 0
			for _, f := range asList(asMap(item["bundle"])["files"]) {
				if _, ok := asMap(f)["content"]; ok {
					stored++
				}
				if _, ok := asMap(f)["omitted"]; ok {
					hashOnly++
				}
			}
			pins = append(pins, host.Obj{{Key: "ref", Value: ref}, {Key: "active", Value: pyStr(active["skill_id"]) + "@" + pyStr(active["resolved_version"])},
				{Key: "stored_files", Value: stored}, {Key: "hash_only_files", Value: hashOnly}})
		}
	}
	return pins, skipped, nil
}

func approvePins(yes bool, stdout, stderr io.Writer) (int, error) {
	pins, skipped, err := pinOnlyCandidates()
	if err != nil {
		return 0, err
	}
	for _, r := range pins {
		fmt.Fprintf(stdout, "%s  (현재 %s, 저장 파일 %s, 해시만 %s)\n", pyStr(objGet(r, "ref")), pyStr(objGet(r, "active")), pyStr(objGet(r, "stored_files")), pyStr(objGet(r, "hash_only_files")))
	}
	for _, r := range skipped {
		fmt.Fprintf(stderr, "건너뜀 %s: %s\n", pyStr(objGet(r, "ref")), pyStr(objGet(r, "reason")))
	}
	if !yes {
		fmt.Fprintf(stdout, "본문이 같고 보조 파일 고정만 추가된 후보 %d개. 승인하려면 --yes를 붙여 다시 실행하세요.\n", len(pins))
		return 0, nil
	}
	failed := 0
	for _, r := range pins {
		// Body unchanged: deterministic checks only, no behavior eval (it would rerun the same text).
		active, err := reviewApprove(pyStr(objGet(r, "ref")), "", false)
		if err != nil {
			failed++
			fmt.Fprintf(stderr, "실패 %s: %v\n", pyStr(objGet(r, "ref")), err)
			continue
		}
		fmt.Fprintf(stdout, "승인·적용 %s@%s\n", pyStr(active["skill_id"]), pyStr(active["resolved_version"]))
	}
	fmt.Fprintf(stdout, "완료: 승인 %d개, 실패 %d개\n", len(pins)-failed, failed)
	if failed > 0 {
		return 1, nil
	}
	return 0, nil
}

func rollbackRef(ref, evalsPath string) (map[string]any, error) {
	item, err := reviewResolve(ref)
	if err != nil {
		return nil, err
	}
	_, evaluationID, err := evaluate(item, evalsPath, "superseded", true)
	if err != nil {
		return nil, err
	}
	skill := pyQuote(pyStr(item["skill_id"]))
	parent, err := reviewCall("GET", "/v1/skills/"+skill+reviewQuery(), nil, "")
	if err != nil {
		return nil, err
	}
	if _, err := reviewCall("POST", "/v1/skills/"+skill+"/rollback", host.Obj{{Key: "scope", Value: host.Scope()},
		{Key: "expected_revision", Value: parent["revision"]}, {Key: "version_id", Value: item["version_id"]}, {Key: "validation_id", Value: evaluationID}}, ""); err != nil {
		return nil, err
	}
	return resolveApproved(pyStr(item["skill_id"]))
}

func rejectRef(ref string) error {
	item, err := reviewResolve(ref)
	if err != nil {
		return err
	}
	version, err := reviewCall("GET", "/v1/skill_versions/"+pyQuote(pyStr(item["version_id"]))+reviewQuery(), nil, "")
	if err != nil {
		return err
	}
	_, err = reviewCall("POST", "/v1/skill_versions/"+pyQuote(pyStr(version["id"]))+"/transition", host.Obj{{Key: "scope", Value: host.Scope()},
		{Key: "expected_revision", Value: version["revision"]}, {Key: "status", Value: "rejected"}}, "")
	return err
}

func availability(skillID, status string, unsupported []string, setUnsupported bool) (map[string]any, error) {
	parent, err := reviewCall("GET", "/v1/skills/"+pyQuote(skillID)+reviewQuery(), nil, "")
	if err != nil {
		return nil, err
	}
	compat := any(orMap(parent["skill_compatibility"]))
	if setUnsupported {
		c := host.Obj{}
		for _, r := range unsupported {
			c.Set(r, "unsupported")
		}
		compat = c
	}
	st := any(status)
	if status == "" {
		st = parent["status"]
	}
	return reviewCall("POST", "/v1/skills/"+pyQuote(skillID)+"/availability", host.Obj{{Key: "scope", Value: host.Scope()},
		{Key: "expected_revision", Value: parent["revision"]}, {Key: "status", Value: st}, {Key: "compatibility", Value: compat}}, "")
}

func reviewMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// --unsupported takes zero or more values, like nargs="*".
	var unsupported []string
	setUnsupported := false
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] != "--unsupported" {
			rest = append(rest, args[i])
			continue
		}
		setUnsupported, unsupported = true, []string{}
		for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			unsupported = append(unsupported, args[i])
		}
	}
	if slices.Contains(rest, "-h") || slices.Contains(rest, "--help") {
		_, _ = io.WriteString(stdout, reviewUsage)
		return 0
	}
	p := newArgs("skill-review", stderr)
	evals, status, yes := p.str("--evals", ""), p.str("--status", ""), p.flag("--yes")
	if !p.parse(rest, 1, 2) {
		return 2
	}
	command := p.pos[0]
	commands := []string{"list", "diff", "approve", "approve-pins", "reject", "rollback", "availability"}
	if !p.choice("command", command, commands) || (p.seen["--status"] && !p.choice("--status", *status, []string{"active", "deprecated"})) {
		return 2
	}
	ref := ""
	if len(p.pos) > 1 {
		ref = p.pos[1]
	}
	code, err := func() (int, error) {
		if command == "approve-pins" {
			return approvePins(*yes, stdout, stderr)
		}
		if command != "list" && ref == "" {
			p.fail("SKILL_ID@N (or SKILL_ID for availability) required")
			return 2, nil
		}
		switch command {
		case "list":
			list, err := reviewCandidates()
			if err != nil {
				return 0, err
			}
			printJSON(stdout, list, 1)
		case "diff":
			out, err := diffRef(ref)
			if err != nil {
				return 0, err
			}
			if out == "" {
				out = "(same content)"
			}
			fmt.Fprintln(stdout, out)
		case "approve":
			active, err := reviewApprove(ref, *evals, true)
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(stdout, "approved and active: %s@%s\n", pyStr(active["skill_id"]), pyStr(active["resolved_version"]))
		case "availability":
			skill, err := availability(ref, *status, unsupported, setUnsupported)
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(stdout, "%s: %s %s\n", pyStr(skill["id"]), pyStr(skill["status"]), host.MustDumps(orMap(skill["skill_compatibility"]), host.DumpOpts{}))
		case "rollback":
			active, err := rollbackRef(ref, *evals)
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(stdout, "rolled back, active: %s@%s\n", pyStr(active["skill_id"]), pyStr(active["resolved_version"]))
		default:
			if err := rejectRef(ref); err != nil {
				return 0, err
			}
			fmt.Fprintf(stdout, "rejected: %s\n", ref)
		}
		return 0, nil
	}()
	if err != nil {
		fmt.Fprintf(stderr, "failed: %v\n", err)
		return 1
	}
	return code
}
