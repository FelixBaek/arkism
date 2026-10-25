package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const reviewsUsage = `Collect the CI code reviews of the operator's repositories for Arkism's 코드 리뷰 screen.

  sync     read recent PRs, their quality runs and AI review comments with ` + "`gh`" + `, apply the owner's review
           settings and rerun requests, and report to Arkism (agents sync calls this every minute;
           the GitHub read itself runs at most every ten minutes)
  show     print what would be reported

Only titles, branches, run timings and the AI review comment the CI already posted leave GitHub for Arkism.
The settings land in ARKISM_REVIEW_SETTINGS, which the CI review job reads.
Set ARKISM_REVIEW_ORG to the GitHub organization to read; without it nothing is collected.
`

func init() { host.Register("reviews", reviewsMain) }

var (
	reviewsCache   = filepath.Join(host.AgentsState, "code-reviews.json")
	reviewSettings = host.Env("ARKISM_REVIEW_SETTINGS", filepath.Join(host.AgentsState, "review-settings.json"))
	reviewOrg      = host.Env("ARKISM_REVIEW_ORG", "")
	reviewEvery    = envInt("ARKISM_REVIEW_EVERY", 600)
	reviewFinding  = regexp.MustCompile("^- \\*\\*\\[(blocking|권고)\\]\\*\\* `([^`:]+):(\\d+)(?:-\\d+)?` — (.+)$")
	reviewFoot     = regexp.MustCompile(`<sub>([0-9a-f]{7,40}) · ([^·]+?) · 파일`)
	// gh runs the GitHub CLI; tests replace it.
	gh = ghCLI
)

const (
	prsPerRepo   = 8
	reviewMarker = "<!-- arkism-ai-review -->"
)

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil {
		return n
	}
	return def
}

func ghCLI(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out, errOut strings.Builder
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.WaitDelay = time.Second // a grandchild holding the pipe must not outlive the timeout
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if ctx.Err() != nil || !errors.As(err, &exit) {
			return "", err
		}
		msg := errOut.String()
		if msg == "" {
			msg = "gh 실패"
		}
		return "", errors.New(runeCut(pyStrip(msg), 200))
	}
	return out.String(), nil
}

func ghJSON(path string) (any, error) {
	out, err := gh(60*time.Second, "api", path)
	if err != nil {
		return nil, err
	}
	if out == "" {
		out = "null"
	}
	return host.Loads(out)
}

// reviewRepos are the organization's repositories that run the quality workflow (the one with ai-review).
func reviewRepos() ([]string, error) {
	if env := os.Getenv("ARKISM_REVIEW_REPOS"); env != "" {
		var out []string
		for _, r := range strings.Split(env, ",") {
			if r = pyStrip(r); r != "" {
				out = append(out, r)
			}
		}
		return out, nil
	}
	if reviewOrg == "" {
		return nil, nil
	}
	raw, err := gh(60*time.Second, "repo", "list", reviewOrg, "--limit", "50", "--json", "nameWithOwner")
	if err != nil {
		return nil, err
	}
	if raw == "" {
		raw = "[]"
	}
	v, err := host.Loads(raw)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range asList(v) {
		name, ok := asMap(r)["nameWithOwner"]
		if !ok {
			return nil, keyErr("nameWithOwner")
		}
		flows, err := ghJSON("repos/" + pyStr(name) + "/actions/workflows")
		if err != nil {
			continue
		}
		for _, f := range asList(orMap(flows)["workflows"]) {
			if asMap(f)["name"] == "quality" {
				out = append(out, pyStr(name))
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// parseReview is (findings, model, sha) from the AI review comment the CI posted.
func parseReview(body string) ([]any, string, string) {
	findings := []any{}
	for _, line := range splitLines(body) {
		m := reviewFinding.FindStringSubmatch(pyStrip(line))
		if m == nil {
			continue
		}
		severity := "advisory"
		if m[1] == "blocking" {
			severity = "blocking"
		}
		n, _ := strconv.Atoi(m[3])
		findings = append(findings, host.Obj{{Key: "severity", Value: severity}, {Key: "path", Value: runeCut(m[2], 300)}, {Key: "line", Value: n}, {Key: "text", Value: runeCut(m[4], 2000)}})
	}
	findings = findings[:min(len(findings), 50)]
	if foot := reviewFoot.FindStringSubmatch(body); foot != nil {
		return findings, runeCut(pyStrip(foot[2]), 80), foot[1]
	}
	return findings, "", ""
}

// dropEmpty is {k: v for k, v in d.items() if v}.
func dropEmpty(o host.Obj) host.Obj {
	out := host.Obj{}
	for _, kv := range o {
		if truthy(kv.Value) {
			out = append(out, kv)
		}
	}
	return out
}

// firstOr is `a or b` for JSON values.
func firstOr(vals ...any) any {
	for _, v := range vals {
		if truthy(v) {
			return v
		}
	}
	return vals[len(vals)-1]
}

func stepsOf(run map[string]any, jobs []any) []any {
	by := map[string]map[string]any{}
	for _, j := range jobs {
		by[pyStr(asMap(j)["name"])] = asMap(j)
	}
	ai, quality := orMap(by["ai-review"]), orMap(by["pr-quality"])
	status := "waiting"
	if truthy(ai["started_at"]) {
		status = "done"
	}
	out := []host.Obj{{{Key: "name", Value: "runner"}, {Key: "status", Value: status}, {Key: "started_at", Value: run["created_at"]}, {Key: "finished_at", Value: ai["started_at"]}}}
	for _, nj := range []struct {
		name string
		j    map[string]any
	}{{"quality", quality}, {"ai", ai}} {
		if len(nj.j) > 0 {
			out = append(out, host.Obj{{Key: "name", Value: nj.name}, {Key: "status", Value: firstOr(nj.j["conclusion"], nj.j["status"], "")},
				{Key: "started_at", Value: nj.j["started_at"]}, {Key: "finished_at", Value: nj.j["completed_at"]}})
		}
	}
	if ai["conclusion"] == "success" {
		out = append(out, host.Obj{{Key: "name", Value: "comment"}, {Key: "status", Value: "success"}, {Key: "finished_at", Value: ai["completed_at"]}})
	}
	steps := make([]any, len(out))
	for i, s := range out {
		steps[i] = dropEmpty(s)
	}
	return steps
}

// qualityRun is the quality workflow run for a commit with its jobs, or nil when there is none.
func qualityRun(name, sha string) (map[string]any, []any, error) {
	v, err := ghJSON("repos/" + name + "/actions/runs?head_sha=" + sha + "&per_page=5")
	if err != nil {
		return nil, nil, err
	}
	var run map[string]any
	for _, r := range asList(orMap(v)["workflow_runs"]) {
		if asMap(r)["name"] == "quality" {
			run = asMap(r)
			break
		}
	}
	if run == nil {
		return nil, nil, nil
	}
	id, ok := run["id"]
	if !ok {
		return nil, nil, keyErr("id")
	}
	v, err = ghJSON("repos/" + name + "/actions/runs/" + pyStr(id) + "/jobs")
	if err != nil {
		return nil, nil, err
	}
	return run, asList(orMap(v)["jobs"]), nil
}

// prFindings is (findings, model) from the PR's newest AI review comment; empty when it is about an older commit.
func prFindings(name string, number any, sha string) ([]any, string, error) {
	out, err := gh(60*time.Second, "api", "--paginate", "--jq", ".[] | {body}", "repos/"+name+"/issues/"+pyStr(number)+"/comments?per_page=100")
	if err != nil {
		return nil, "", err
	}
	var bodies []string // every page, oldest first
	for _, line := range splitLines(out) {
		if pyStrip(line) == "" {
			continue
		}
		v, err := host.Loads(line)
		if err != nil {
			return nil, "", err
		}
		bodies = append(bodies, str(asMap(v)["body"]))
	}
	review := ""
	for i := len(bodies) - 1; i >= 0; i-- {
		if strings.HasPrefix(bodies[i], reviewMarker) {
			review = bodies[i]
			break
		}
	}
	findings, model, reviewed := []any{}, "", ""
	if review != "" {
		findings, model, reviewed = parseReview(review)
	}
	if reviewed != "" && !strings.HasPrefix(sha, reviewed) {
		findings = []any{} // the comment is about an older commit; this one is still under review or failed
	}
	return findings, model, nil
}

func prState(running bool, ai map[string]any, runConclusion string) string {
	if running {
		return "running"
	}
	if len(ai) == 0 { // the workflow ended before the review job existed (cancelled, failed to start)
		if runConclusion == "success" {
			return "done"
		}
		return "failed"
	}
	if containsStr([]string{"failure", "cancelled", "timed_out", "startup_failure", "action_required", "stale"}, str(ai["conclusion"])) {
		return "failed" // the review did not finish: never show it as 문제 없음
	}
	return "done"
}

// need returns m[k] and remembers the first missing key, like Python's KeyError.
type need struct{ err error }

func (n *need) get(m map[string]any, k string) any {
	v, ok := m[k]
	if !ok && n.err == nil {
		n.err = keyErr(k)
	}
	return v
}

// pullItem is one PR's report row, or nil when it has no quality run or its review was skipped (a draft).
func pullItem(name string, p map[string]any) (host.Obj, error) {
	var n need
	head, base := asMap(n.get(p, "head")), asMap(n.get(p, "base"))
	sha := str(n.get(head, "sha"))
	if n.err != nil {
		return nil, n.err
	}
	run, jobs, err := qualityRun(name, sha)
	if err != nil || run == nil {
		return nil, err
	}
	var ai, quality map[string]any
	for _, j := range jobs {
		switch asMap(j)["name"] {
		case "ai-review":
			if ai == nil {
				ai = asMap(j)
			}
		case "pr-quality":
			if quality == nil {
				quality = asMap(j)
			}
		}
	}
	if ai["conclusion"] == "skipped" {
		return nil, nil
	}
	number := n.get(p, "number")
	findings, model, err := prFindings(name, number, sha)
	if err != nil {
		return nil, err
	}
	running := run["status"] != "completed"
	prStateV := any("merged")
	if !truthy(p["merged_at"]) {
		prStateV = n.get(p, "state")
	}
	var finished any
	if !running {
		finished = firstOr(ai["completed_at"], run["updated_at"])
	}
	item := host.Obj{{Key: "repo", Value: name}, {Key: "number", Value: number}, {Key: "title", Value: runeCut(str(n.get(p, "title")), 300)},
		{Key: "branch", Value: runeCut(str(n.get(head, "ref")), 200)}, {Key: "base", Value: runeCut(str(n.get(base, "ref")), 200)},
		{Key: "head_sha", Value: runeCut(sha, 40)}, {Key: "commits", Value: firstOr(p["commits"], 0)}, {Key: "url", Value: n.get(p, "html_url")},
		{Key: "state", Value: prState(running, ai, str(firstOr(run["conclusion"], "")))}, {Key: "pr_state", Value: prStateV},
		{Key: "started_at", Value: run["created_at"]}, {Key: "finished_at", Value: finished},
		{Key: "quality", Value: firstOr(quality["conclusion"], quality["status"], "")}, {Key: "model", Value: model},
		{Key: "findings", Value: findings}, {Key: "steps", Value: stepsOf(run, jobs)}}
	return item, n.err
}

func collectRepo(name string) ([]host.Obj, host.Obj, error) {
	v, err := ghJSON(fmt.Sprintf("repos/%s/pulls?state=all&sort=updated&direction=desc&per_page=%d", name, prsPerRepo))
	if err != nil {
		return nil, nil, err
	}
	pulls := asList(v)
	var items []host.Obj
	for _, p := range pulls {
		item, err := pullItem(name, asMap(p))
		if err != nil {
			return nil, nil, err
		}
		if item != nil {
			items = append(items, item)
		}
	}
	total := any(0)
	if len(pulls) > 0 {
		var n need
		total = n.get(asMap(pulls[0]), "number")
		if n.err != nil {
			return nil, nil, n.err
		}
	}
	return items, host.Obj{{Key: "name", Value: name}, {Key: "prs", Value: total}, {Key: "last_checked", Value: time.Now().UTC().Format("2006-01-02T15:04:05Z")}}, nil
}

func collectReviews() ([]any, []any, error) {
	names, err := reviewRepos()
	if err != nil {
		return nil, nil, err
	}
	var items []host.Obj
	rows := []any{}
	for _, name := range names {
		got, row, err := collectRepo(name)
		if err != nil {
			_ = host.Metric(map[string]any{"event": "code_reviews_repo_failed", "repo": name, "error": runeCut(err.Error(), 120)})
			continue
		}
		items = append(items, got...)
		rows = append(rows, row)
	}
	for i, it := range items { // timestamps in JSON as Arkism stores them; drop empty ones
		for _, k := range []string{"started_at", "finished_at"} {
			if !truthy(objGet(it, k)) {
				it.Del(k)
			}
		}
		items[i] = it
	}
	sort.SliceStable(items, func(i, j int) bool { return str(objGet(items[i], "started_at")) > str(objGet(items[j], "started_at")) })
	out := []any{}
	for _, it := range items[:min(len(items), 300)] {
		out = append(out, it)
	}
	return out, rows, nil
}

// applySettings writes the review settings where the CI script reads them; true when the file changed.
func applySettings(settings map[string]any) bool {
	off := []string{}
	for _, r := range asList(settings["repos_off"]) {
		off = append(off, pyStr(r))
	}
	slices.Sort(off)
	want := host.Obj{{Key: "provider", Value: firstOr(settings["provider"], "codex")}, {Key: "on_blocking", Value: firstOr(settings["on_blocking"], "notify")},
		{Key: "repos_off", Value: stringsAny(off)}}
	if raw, err := os.ReadFile(reviewSettings); err == nil {
		if v, err := host.Loads(string(raw)); err == nil && sameJSON(v, want) {
			return false
		}
	}
	err := os.MkdirAll(filepath.Dir(reviewSettings), 0o777)
	if err == nil {
		tmp := strings.TrimSuffix(reviewSettings, filepath.Ext(reviewSettings)) + ".tmp" // the service umask lets the CI runner read it
		text, _ := dumpIndent(want, 1, false)
		if err = os.WriteFile(tmp, []byte(text), 0o666); err == nil {
			err = os.Rename(tmp, reviewSettings)
		}
	}
	if err != nil {
		_ = host.Metric(map[string]any{"event": "code_review_settings_failed", "error": failureCode(err)})
		return false
	}
	return true
}

// rerunReview reruns the latest quality run of a PR's head commit.
func rerunReview(req map[string]any) error {
	var n need
	name, numberV := pyStr(n.get(req, "repo")), n.get(req, "number")
	if n.err != nil {
		return n.err
	}
	number, err := pyIntArg(numberV)
	if err != nil {
		return err
	}
	pr, err := ghJSON(fmt.Sprintf("repos/%s/pulls/%d", name, number))
	if err != nil {
		return err
	}
	sha := pyStr(n.get(asMap(n.get(asMap(pr), "head")), "sha"))
	if n.err != nil {
		return n.err
	}
	runs, err := ghJSON("repos/" + name + "/actions/runs?head_sha=" + sha + "&per_page=5")
	if err != nil {
		return err
	}
	for _, r := range asList(orMap(runs)["workflow_runs"]) {
		if asMap(r)["name"] == "quality" {
			_, err := gh(60*time.Second, "run", "rerun", pyStr(asMap(r)["id"]), "--repo", name)
			return err
		}
	}
	return errors.New("이 PR의 검사 실행을 찾지 못했어요")
}

// pyIntArg is int(v) for a JSON number or a numeric string.
func pyIntArg(v any) (int64, error) {
	if n, ok := host.Int(v); ok {
		return n, nil
	}
	if s, ok := v.(string); ok {
		if n, err := strconv.ParseInt(pyStrip(s), 10, 64); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("invalid literal for int(): %s", pyStr(v))
}

func syncReviews(force bool) (int, error) {
	res, err := host.Get("/v1/code_reviews?"+scopeQuery(host.Scope()), 30*time.Second)
	if err != nil {
		return 0, err
	}
	doc := orMap(res["code_reviews"])
	applySettings(orMap(doc["settings"]))
	results := []any{}
	for _, r := range asList(doc["reruns"]) {
		req := asMap(r)
		if req["status"] != "pending" {
			continue
		}
		if err := rerunReview(req); err != nil {
			results = append(results, host.Obj{{Key: "id", Value: req["id"]}, {Key: "status", Value: "failed"}, {Key: "error", Value: runeCut(err.Error(), 300)}})
		} else {
			results = append(results, host.Obj{{Key: "id", Value: req["id"]}, {Key: "status", Value: "done"}})
		}
	}
	cached := host.ReadJSONFile(reviewsCache, 1<<20)
	at := any(0)
	if v, ok := cached["at"]; ok {
		at = v
	}
	if !force && len(results) == 0 && nowFloat()-numFloat(at) < float64(reviewEvery) {
		return 0, nil
	}
	items, rows, err := collectReviews()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(host.AgentsState, 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(reviewsCache, []byte(host.MustDumps(host.Obj{{Key: "at", Value: nowFloat()}}, host.DumpOpts{})), 0o666); err != nil {
		return 0, err
	}
	if _, err := host.Post("/v1/code_reviews", host.Obj{{Key: "scope", Value: host.Scope()}, {Key: "repos", Value: rows}, {Key: "items", Value: items}, {Key: "results", Value: results}}, 60*time.Second); err != nil {
		return 0, err
	}
	return len(items), nil
}

func reviewsMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "sync":
		n, err := syncReviews(slices.Contains(args, "--force"))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "PR %d개 보고\n", n)
	case "show":
		items, rows, err := collectReviews()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printJSON(stdout, host.Obj{{Key: "repos", Value: rows}, {Key: "items", Value: items[:min(len(items), 5)]}}, 1)
	default:
		_, _ = io.WriteString(stdout, reviewsUsage+"\n")
		return 2
	}
	return 0
}
