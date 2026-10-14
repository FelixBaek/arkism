package crew

// Git screen backend: read one GitHub owner's repositories (ARKISM_GIT_ORG) and make bounded GitHub changes.
//
// The desktop app runs `arkism git` over the owner's SSH (one JSON request on stdin, one JSON reply on stdout).
// Each repository is an Arkism-only bare mirror under gitState; the owner's working checkouts are never touched.
// GitHub calls use the server's `gh` login. Writes take the state the user saw (branch head, PR head) and refuse
// when it moved; nothing force-pushes, deletes an unmerged branch, or commits to the default branch directly
// (reverts and cherry-picks onto it go through a new branch and PR).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

var (
	gitOrg    = os.Getenv("ARKISM_GIT_ORG") // GitHub user or organization the Git screen shows
	gitState  = host.Env("ARKISM_GIT_STATE", filepath.Join(host.Home, ".local/share/arkism-git"))
	gitRemote = strings.TrimRight(host.Env("ARKISM_GIT_REMOTE", "https://github.com"), "/") // tests point this at a local directory
)

const (
	fetchEvery = 60 * time.Second
	replyLimit = 240_000
	diffLimit  = 120_000
	gitSep     = "\x1f"
	emptyTree  = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

var (
	repoName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	fullSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shaPrefix = regexp.MustCompile(`^[0-9a-f]{4,40}$`)
)

// gitError is a fixed git_* code shown to the user; never carries command output.
type gitError string

func (e gitError) Error() string { return string(e) }

func isSHA(v any) bool { s, ok := v.(string); return ok && fullSHA.MatchString(s) }

// gitRun runs one command; a timeout or a missing program is an error, a non-zero exit only when check.
func gitRun(args []string, cwd string, timeout time.Duration, check bool, env map[string]string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	all := osEnv()
	all["GIT_TERMINAL_PROMPT"] = "0"
	for k, v := range env {
		all[k] = v
	}
	var out bytes.Buffer
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = cwd, envList(all), &out, io.Discard
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || ctx.Err() != nil {
			return "", -1, err
		}
		code = exitCode(exit.ProcessState)
	}
	if check && code != 0 {
		return "", code, gitError("git_command_failed")
	}
	return strings.ToValidUTF8(out.String(), "�"), code, nil
}

func ghCall(timeout time.Duration, args ...string) (string, error) {
	out, _, err := gitRun(append([]string{"gh"}, args...), "", timeout, true, nil)
	return out, err
}

func ghJSONCall(args ...string) (any, error) {
	out, err := ghRun(60*time.Second, args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		out = "null"
	}
	return host.Loads(out)
}

// GitHub, the repository list and the signed-in user (fakes in tests); gitReplace publishes a cache file.
var (
	ghRun      = ghCall
	ghJSON     = ghJSONCall
	reposFn    = gitRepos
	viewerFn   = gitViewer
	gitReplace = os.Rename
)

// cached is produce() kept in gitState/.name.json for seconds; concurrent misses each publish a whole file.
func cached(name string, seconds float64, produce func() (any, error)) (any, error) {
	path := filepath.Join(gitState, "."+name+".json")
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()).Seconds() < seconds {
		if raw, err := os.ReadFile(path); err == nil {
			if v, err := host.Loads(string(raw)); err == nil {
				return v, nil
			}
		}
	}
	value, err := produce()
	if err != nil {
		return nil, err
	}
	text := host.MustDumps(value, host.DumpOpts{})
	if err := os.MkdirAll(gitState, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(gitState, "."+name+"-*.tmp")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, werr := f.WriteString(text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, werr
	}
	if err := gitReplace(tmp, path); err != nil {
		return nil, err
	}
	return host.Loads(text)
}

func sOrDefault(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func gitRepos() ([]any, error) {
	if gitOrg == "" {
		return nil, errors.New("ARKISM_GIT_ORG is not set")
	}
	v, err := cached("repos", 300, func() (any, error) {
		rows, err := ghJSON("repo", "list", gitOrg, "--limit", "100", "--json", "name,description,defaultBranchRef,isArchived,pushedAt,url")
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, r := range list(rows) {
			r := obj(r)
			out = append(out, host.Obj{{Key: "name", Value: r["name"]}, {Key: "description", Value: sOrDefault(r["description"], "")},
				{Key: "default_branch", Value: sOrDefault(obj(r["defaultBranchRef"])["name"], "main")}, {Key: "archived", Value: r["isArchived"]},
				{Key: "pushed_at", Value: r["pushedAt"]}, {Key: "url", Value: r["url"]}})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	rows := append([]any{}, list(v)...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := truthy(obj(rows[i])["archived"]), truthy(obj(rows[j])["archived"])
		if a != b {
			return !a
		}
		return pyStr(obj(rows[i])["name"]) < pyStr(obj(rows[j])["name"])
	})
	return rows, nil
}

func repoInfo(name any) (map[string]any, error) {
	s, ok := name.(string)
	if !ok || !repoName.MatchString(s) {
		return nil, gitError("git_invalid_repo")
	}
	rows, err := reposFn()
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if obj(r)["name"] == s {
			return obj(r), nil
		}
	}
	return nil, gitError("git_unknown_repo")
}

func gitViewer() (map[string]any, error) {
	v, err := cached("viewer", 3600, func() (any, error) {
		return ghJSON("api", "user", "--jq", "{login: .login, name: .name, id: .id}")
	})
	return obj(v), err
}

// gitLocks are released when the request ends.
var (
	gitLocksMu sync.Mutex
	gitLocks   []*os.File
)

// mirror is a bare mirror of one repository, locked for the whole request.
type mirror struct {
	info       map[string]any
	name, path string
}

func newMirror(name any, refresh bool) (*mirror, error) {
	info, err := repoInfo(name)
	if err != nil {
		return nil, err
	}
	m := &mirror{info: info, name: name.(string), path: filepath.Join(gitState, name.(string)+".git")}
	if err := os.MkdirAll(gitState, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(gitState, "."+m.name+".lock"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, err
	}
	gitLocksMu.Lock()
	gitLocks = append(gitLocks, lock)
	gitLocksMu.Unlock()
	if _, err := flock(lock, true); err != nil {
		return nil, err
	}
	if !exists(filepath.Join(m.path, "HEAD")) {
		if _, _, err := gitRun([]string{"git", "clone", "--bare", "--quiet", gitRemote + "/" + gitOrg + "/" + m.name + ".git", m.path}, "", 600*time.Second, true, nil); err != nil {
			return nil, err
		}
		if _, err := m.git(60, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*"); err != nil {
			return nil, err
		}
		if _, err := m.git(60, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*"); err != nil {
			return nil, err
		}
		refresh = true
	}
	stamp := filepath.Join(m.path, "arkism-fetched")
	info2, serr := os.Stat(stamp)
	if refresh || serr != nil || time.Since(info2.ModTime()) > fetchEvery {
		if err := m.fetch(); err != nil {
			return nil, err
		}
		if f, err := os.OpenFile(stamp, os.O_CREATE|os.O_WRONLY, 0o666); err == nil {
			_ = f.Close()
		}
		now := time.Now()
		if err := os.Chtimes(stamp, now, now); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func releaseGitLocks() {
	gitLocksMu.Lock()
	defer gitLocksMu.Unlock()
	for _, f := range gitLocks {
		_ = f.Close()
	}
	gitLocks = nil
}

func (m *mirror) git(timeoutSec int, args ...string) (string, error) {
	out, _, err := gitRun(append([]string{"git", "--git-dir=" + m.path}, args...), "", time.Duration(timeoutSec)*time.Second, true, nil)
	return out, err
}

func (m *mirror) fetch() error {
	_, err := m.git(300, "fetch", "--prune", "--quiet", "origin")
	return err
}

func (m *mirror) headOf(branch string) string {
	out, _, _ := gitRun([]string{"git", "--git-dir=" + m.path, "rev-parse", "--verify", "--quiet", "refs/heads/" + branch + "^{commit}"}, "", 60*time.Second, false, nil)
	out = strings.TrimSpace(out)
	if fullSHA.MatchString(out) {
		return out
	}
	return ""
}

func gitRefs(m *mirror) ([]any, error) {
	text, err := m.git(60, "for-each-ref", "--format=%(refname)"+gitSep+"%(objectname)"+gitSep+"%(*objectname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, line := range pySplitlines(text) {
		parts := strings.Split(line, gitSep)
		if len(parts) != 3 {
			return nil, errors.New("bad ref line")
		}
		kind, name := "tag", strings.TrimPrefix(parts[0], "refs/tags/")
		if strings.HasPrefix(parts[0], "refs/heads/") {
			kind, name = "branch", parts[0][11:]
		}
		sha := parts[2]
		if sha == "" {
			sha = parts[1]
		}
		out = append(out, host.Obj{{Key: "kind", Value: kind}, {Key: "name", Value: name}, {Key: "sha", Value: sha}})
	}
	return out, nil
}

// intOr is int(v or def) for a request field.
func intOr(v any, def int64) (int64, error) {
	if !truthy(v) {
		return def, nil
	}
	n, ok := pyIntArg(v)
	if !ok {
		return 0, errors.New("invalid literal for int()")
	}
	return n, nil
}

func gitGraph(req map[string]any) (host.Obj, error) {
	m, err := newMirror(req["repo"], truthy(req["refresh"]))
	if err != nil {
		return nil, err
	}
	limit, err := intOr(req["limit"], 300)
	if err != nil {
		return nil, err
	}
	limit = min(max(limit, 1), 600)
	skip, err := intOr(req["skip"], 0)
	if err != nil {
		return nil, err
	}
	skip = max(skip, 0)
	args := []string{"log", "--branches", "--tags", "--date-order", fmt.Sprintf("--max-count=%d", limit), fmt.Sprintf("--skip=%d", skip),
		"--format=%H" + gitSep + "%P" + gitSep + "%an" + gitSep + "%at" + gitSep + "%s"}
	query := ""
	if truthy(req["query"]) {
		s, ok := req["query"].(string)
		if !ok {
			return nil, gitError("git_invalid_query")
		}
		query = strings.TrimSpace(s)
	}
	byPrefix := shaPrefix.MatchString(query)
	if query != "" {
		if utf8.RuneCountInString(query) > 200 {
			return nil, gitError("git_invalid_query")
		}
		if !byPrefix {
			args = append(args, "--regexp-ignore-case", "--fixed-strings", "--grep="+query)
		}
	}
	text, err := m.git(120, args...)
	if err != nil {
		return nil, err
	}
	rows := []any{}
	for _, line := range pySplitlines(text) {
		f := strings.SplitN(line, gitSep, 5)
		if len(f) != 5 {
			return nil, errors.New("bad log line")
		}
		at, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return nil, err
		}
		if byPrefix && !strings.HasPrefix(f[0], query) {
			continue
		}
		parents := []any{}
		for _, p := range strings.Fields(f[1]) {
			parents = append(parents, p)
		}
		rows = append(rows, host.Obj{{Key: "sha", Value: f[0]}, {Key: "parents", Value: parents}, {Key: "author", Value: f[2]}, {Key: "time", Value: at}, {Key: "subject", Value: f[4]}})
	}
	refs, err := gitRefs(m)
	if err != nil {
		return nil, err
	}
	av, err := avatars(m.name)
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "repo", Value: m.info}, {Key: "refs", Value: refs}, {Key: "commits", Value: rows},
		{Key: "more", Value: int64(len(rows)) == limit && query == ""}, {Key: "avatars", Value: av}}, nil
}

// avatars is author name -> GitHub avatar URL from the latest commits; missing ones fall back to initials.
func avatars(name string) (any, error) {
	return cached("avatars-"+name, 600, func() (any, error) {
		rows, err := ghJSON("api", "repos/"+gitOrg+"/"+name+"/commits?per_page=100", "--jq", `[.[] | {n: .commit.author.name, a: (.author.avatar_url // "")}]`)
		var ge gitError
		if errors.As(err, &ge) {
			return map[string]any{}, nil
		}
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		for _, r := range list(rows) {
			if a, ok := obj(r)["a"].(string); ok && strings.HasPrefix(a, "https://avatars.githubusercontent.com/") {
				out[pyStr(obj(r)["n"])] = a
			}
		}
		return out, nil
	})
}

func parentsOf(m *mirror, sha string) ([]string, error) {
	out, err := m.git(60, "show", "--no-patch", "--format=%P", sha)
	return strings.Fields(out), err
}

func gitCommit(req map[string]any) (host.Obj, error) {
	m, err := newMirror(req["repo"], false)
	if err != nil {
		return nil, err
	}
	if !isSHA(req["sha"]) {
		return nil, gitError("git_invalid_sha")
	}
	sha := req["sha"].(string)
	text, err := m.git(60, "show", "--no-patch", "--format=%H"+gitSep+"%P"+gitSep+"%an"+gitSep+"%ae"+gitSep+"%at"+gitSep+"%cn"+gitSep+"%ct"+gitSep+"%B", sha)
	if err != nil {
		return nil, err
	}
	meta := strings.SplitN(text, gitSep, 8)
	if len(meta) != 8 {
		return nil, errors.New("bad commit")
	}
	parents := strings.Fields(meta[1])
	base := emptyTree
	if len(parents) > 0 {
		base = parents[0]
	}
	numstat, err := m.git(120, "diff", "--numstat", "-z", "-M", base, sha)
	if err != nil {
		return nil, err
	}
	files := []any{}
	tokens := strings.Split(numstat, "\x00")
	for i := 0; i < len(tokens); i++ {
		parts := strings.Split(tokens[i], "\t")
		if len(parts) != 3 {
			continue
		}
		path, old := parts[2], any(nil)
		if path == "" { // rename/copy: the old and new paths follow as their own fields
			next := func() string {
				if i+1 < len(tokens) {
					i++
					return tokens[i]
				}
				return ""
			}
			old = next()
			path = next()
		}
		count := func(s string) (any, error) {
			if s == "-" {
				return nil, nil
			}
			return strconv.ParseInt(s, 10, 64)
		}
		added, err := count(parts[0])
		if err != nil {
			return nil, err
		}
		deleted, err := count(parts[1])
		if err != nil {
			return nil, err
		}
		files = append(files, host.Obj{{Key: "path", Value: path}, {Key: "old_path", Value: old}, {Key: "added", Value: added}, {Key: "deleted", Value: deleted}})
	}
	branchText, err := m.git(60, "for-each-ref", "--contains", sha, "--format=%(refname:short)", "--count=20", "refs/heads")
	if err != nil {
		return nil, err
	}
	branches := []any{}
	for _, b := range strings.Fields(branchText) {
		branches = append(branches, b)
	}
	at, err1 := strconv.ParseInt(meta[4], 10, 64)
	ct, err2 := strconv.ParseInt(meta[6], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return nil, err
	}
	ps := []any{}
	for _, p := range parents {
		ps = append(ps, p)
	}
	return host.Obj{{Key: "sha", Value: meta[0]}, {Key: "parents", Value: ps}, {Key: "author", Value: meta[2]}, {Key: "author_email", Value: meta[3]},
		{Key: "time", Value: at}, {Key: "committer", Value: meta[5]}, {Key: "commit_time", Value: ct}, {Key: "message", Value: strings.TrimSpace(meta[7])},
		{Key: "branches", Value: branches}, {Key: "files", Value: files[:min(len(files), 500)]}, {Key: "more_files", Value: max(len(files)-500, 0)}}, nil
}

func gitDiff(req map[string]any) (host.Obj, error) {
	m, err := newMirror(req["repo"], false)
	if err != nil {
		return nil, err
	}
	path, ok := req["path"].(string)
	if !isSHA(req["sha"]) || !ok || path == "" || utf8.RuneCountInString(path) > 1000 || strings.Contains(path, "\x00") {
		return nil, gitError("git_invalid_request")
	}
	sha := req["sha"].(string)
	base := emptyTree
	if req["base"] != nil {
		// a pull request's whole change: from where it left its base branch (merge-base) to its head
		name, err := branchName(req["base"])
		if err != nil {
			return nil, err
		}
		out, err := m.git(60, "merge-base", "refs/heads/"+name, sha)
		if err != nil {
			return nil, err
		}
		base = strings.TrimSpace(out)
	} else if parents, err := parentsOf(m, sha); err != nil {
		return nil, err
	} else if len(parents) > 0 {
		base = parents[0]
	}
	text, err := m.git(120, "diff", "-M", "--no-color", base, sha, "--", path)
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "sha", Value: sha}, {Key: "path", Value: path}, {Key: "patch", Value: clipRunes(text, diffLimit)},
		{Key: "truncated", Value: utf8.RuneCountInString(text) > diffLimit}}, nil
}

func checkState(c map[string]any) string {
	for _, k := range []string{"conclusion", "state", "status"} {
		if truthy(c[k]) {
			return pyStr(c[k])
		}
	}
	return ""
}

func checkSummary(rollup any) string {
	states := []string{}
	for _, c := range list(rollup) {
		states = append(states, strings.ToUpper(checkState(obj(c))))
	}
	if len(states) == 0 {
		return "none"
	}
	has := func(set ...string) bool {
		for _, s := range states {
			for _, x := range set {
				if s == x {
					return true
				}
			}
		}
		return false
	}
	if has("FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED") {
		return "failure"
	}
	if has("PENDING", "IN_PROGRESS", "QUEUED", "EXPECTED", "WAITING", "") {
		return "pending"
	}
	return "success"
}

const prFields = "number,title,state,isDraft,author,headRefName,headRefOid,baseRefName,updatedAt,reviewDecision,additions,deletions,url,reviewRequests,assignees"

func login(v any) string { return sOrDefault(obj(v)["login"], "") }

func anyLogin(v, me any) bool {
	for _, a := range list(v) {
		if same(obj(a)["login"], me) {
			return true
		}
	}
	return false
}

func lower(v any) string { return strings.ToLower(sOrDefault(v, "")) }

func cacheFor(req map[string]any) float64 {
	if truthy(req["refresh"]) {
		return 0
	}
	return 60
}

// gitPRs is open PRs with their checks plus recent closed ones without (checks on all 60 took 7s).
func gitPRs(req map[string]any) (host.Obj, error) {
	info, err := repoInfo(req["repo"])
	if err != nil {
		return nil, err
	}
	viewer, err := viewerFn()
	if err != nil {
		return nil, err
	}
	me := viewer["login"]
	repo := gitOrg + "/" + pyStr(info["name"])
	v, err := cached("prs-"+pyStr(info["name"]), cacheFor(req), func() (any, error) {
		var open, closed any
		var oerr, cerr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			open, oerr = ghJSON("pr", "list", "--repo", repo, "--state", "open", "--limit", "60", "--json", prFields+",statusCheckRollup")
		}()
		go func() {
			defer wg.Done()
			closed, cerr = ghJSON("pr", "list", "--repo", repo, "--state", "all", "--limit", "40", "--json", prFields)
		}()
		wg.Wait()
		if err := errors.Join(oerr, cerr); err != nil {
			return nil, err
		}
		rows, seen := append([]any{}, list(open)...), map[string]bool{}
		for _, r := range rows {
			seen[pyStr(obj(r)["number"])] = true
		}
		for _, r := range list(closed) {
			if !seen[pyStr(obj(r)["number"])] {
				rows = append(rows, r)
			}
		}
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	rows := append([]any{}, list(v)...)
	sort.SliceStable(rows, func(i, j int) bool { return pyStr(obj(rows[i])["updatedAt"]) > pyStr(obj(rows[j])["updatedAt"]) })
	out := []any{}
	for _, r := range rows {
		r := obj(r)
		checks := "none"
		if _, ok := r["statusCheckRollup"]; ok {
			checks = checkSummary(r["statusCheckRollup"])
		}
		out = append(out, host.Obj{{Key: "number", Value: r["number"]}, {Key: "title", Value: r["title"]}, {Key: "state", Value: strings.ToLower(pyStr(r["state"]))},
			{Key: "draft", Value: r["isDraft"]}, {Key: "author", Value: login(r["author"])}, {Key: "head", Value: r["headRefName"]}, {Key: "head_sha", Value: r["headRefOid"]},
			{Key: "base", Value: r["baseRefName"]}, {Key: "updated_at", Value: r["updatedAt"]}, {Key: "review", Value: lower(r["reviewDecision"])},
			{Key: "additions", Value: r["additions"]}, {Key: "deletions", Value: r["deletions"]}, {Key: "url", Value: r["url"]}, {Key: "checks", Value: checks},
			{Key: "mine", Value: same(obj(r["author"])["login"], me)}, {Key: "assigned", Value: anyLogin(r["assignees"], me)},
			{Key: "review_requested", Value: anyLogin(r["reviewRequests"], me)}})
	}
	return host.Obj{{Key: "repo", Value: info}, {Key: "viewer", Value: me}, {Key: "pulls", Value: out}}, nil
}

func validNumber(v any) (int64, bool) {
	n, ok := host.Int(v)
	return n, ok && n >= 1
}

func gitPR(req map[string]any) (host.Obj, error) {
	info, err := repoInfo(req["repo"])
	if err != nil {
		return nil, err
	}
	number, ok := validNumber(req["number"])
	if !ok {
		return nil, gitError("git_invalid_request")
	}
	v, err := ghJSON("pr", "view", strconv.FormatInt(number, 10), "--repo", gitOrg+"/"+pyStr(info["name"]), "--json",
		"number,title,body,state,isDraft,author,headRefName,headRefOid,baseRefName,createdAt,updatedAt,mergedAt,mergeable,mergeStateStatus,reviewDecision,additions,deletions,changedFiles,commits,files,reviews,statusCheckRollup,url")
	if err != nil {
		return nil, err
	}
	r := obj(v)
	checks := []any{}
	for _, c := range list(r["statusCheckRollup"]) {
		c := obj(c)
		checks = append(checks, host.Obj{{Key: "name", Value: sOrDefault(c["name"], sOrDefault(c["context"], ""))},
			{Key: "state", Value: strings.ToLower(checkState(c))}, {Key: "url", Value: sOrDefault(c["detailsUrl"], sOrDefault(c["targetUrl"], ""))}})
	}
	files := []any{}
	for _, f := range list(r["files"])[:min(len(list(r["files"])), 300)] {
		f := obj(f)
		files = append(files, host.Obj{{Key: "path", Value: f["path"]}, {Key: "added", Value: f["additions"]}, {Key: "deleted", Value: f["deletions"]}})
	}
	reviews := []any{}
	all := list(r["reviews"])
	for _, rv := range all[max(0, len(all)-30):] {
		rv := obj(rv)
		reviews = append(reviews, host.Obj{{Key: "author", Value: login(rv["author"])}, {Key: "state", Value: lower(rv["state"])},
			{Key: "body", Value: clipRunes(sOrDefault(rv["body"], ""), 2000)}, {Key: "at", Value: rv["submittedAt"]}})
	}
	return host.Obj{{Key: "repo", Value: info}, {Key: "number", Value: r["number"]}, {Key: "title", Value: r["title"]},
		{Key: "body", Value: clipRunes(sOrDefault(r["body"], ""), 20000)}, {Key: "state", Value: strings.ToLower(pyStr(r["state"]))}, {Key: "draft", Value: r["isDraft"]},
		{Key: "author", Value: login(r["author"])}, {Key: "head", Value: r["headRefName"]}, {Key: "head_sha", Value: r["headRefOid"]}, {Key: "base", Value: r["baseRefName"]},
		{Key: "created_at", Value: r["createdAt"]}, {Key: "updated_at", Value: r["updatedAt"]}, {Key: "merged_at", Value: r["mergedAt"]},
		{Key: "mergeable", Value: lower(r["mergeable"])}, {Key: "merge_state", Value: lower(r["mergeStateStatus"])}, {Key: "review", Value: lower(r["reviewDecision"])},
		{Key: "additions", Value: r["additions"]}, {Key: "deletions", Value: r["deletions"]}, {Key: "changed_files", Value: r["changedFiles"]},
		{Key: "commits", Value: len(list(r["commits"]))}, {Key: "files", Value: files}, {Key: "reviews", Value: reviews}, {Key: "checks", Value: checks},
		{Key: "check_summary", Value: checkSummary(r["statusCheckRollup"])}, {Key: "url", Value: r["url"]}}, nil
}

func gitRuns(req map[string]any) (host.Obj, error) {
	info, err := repoInfo(req["repo"])
	if err != nil {
		return nil, err
	}
	v, err := cached("runs-"+pyStr(info["name"]), cacheFor(req), func() (any, error) {
		return ghJSON("run", "list", "--repo", gitOrg+"/"+pyStr(info["name"]), "--limit", "40", "--json",
			"databaseId,workflowName,displayTitle,status,conclusion,headBranch,headSha,event,createdAt,updatedAt,url")
	})
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, r := range list(v) {
		r := obj(r)
		out = append(out, host.Obj{{Key: "id", Value: r["databaseId"]}, {Key: "workflow", Value: r["workflowName"]}, {Key: "title", Value: r["displayTitle"]},
			{Key: "status", Value: r["status"]}, {Key: "conclusion", Value: sOrDefault(r["conclusion"], "")}, {Key: "branch", Value: r["headBranch"]},
			{Key: "sha", Value: r["headSha"]}, {Key: "event", Value: r["event"]}, {Key: "created_at", Value: r["createdAt"]}, {Key: "updated_at", Value: r["updatedAt"]},
			{Key: "url", Value: r["url"]}})
	}
	return host.Obj{{Key: "repo", Value: info}, {Key: "runs", Value: out}}, nil
}

// ---------------------------------------------------------------- bounded writes

func branchName(v any) (string, error) {
	s, ok := v.(string)
	if !ok || utf8.RuneCountInString(s) < 1 || utf8.RuneCountInString(s) > 200 || strings.HasPrefix(s, "-") {
		return "", gitError("git_invalid_branch")
	}
	if _, code, err := gitRun([]string{"git", "check-ref-format", "--branch", s}, "", 60*time.Second, false, nil); err != nil {
		return "", err
	} else if code != 0 {
		return "", gitError("git_invalid_branch")
	}
	return s, nil
}

func expectHead(m *mirror, branch string, expected any) error {
	if !isSHA(expected) || m.headOf(branch) != expected.(string) {
		return gitError("git_state_changed")
	}
	return nil
}

func branchCreate(req map[string]any) (host.Obj, error) {
	m, err := newMirror(req["repo"], true)
	if err != nil {
		return nil, err
	}
	name, err := branchName(req["name"])
	if err != nil {
		return nil, err
	}
	if !isSHA(req["sha"]) {
		return nil, gitError("git_invalid_sha")
	}
	sha := req["sha"].(string)
	if kind, _, err := gitRun([]string{"git", "--git-dir=" + m.path, "cat-file", "-t", sha}, "", 60*time.Second, false, nil); err != nil {
		return nil, err
	} else if strings.TrimSpace(kind) != "commit" {
		return nil, gitError("git_invalid_sha")
	}
	if m.headOf(name) != "" {
		return nil, gitError("git_branch_exists")
	}
	if _, err := ghRun(60*time.Second, "api", "repos/"+gitOrg+"/"+m.name+"/git/refs", "-f", "ref=refs/heads/"+name, "-f", "sha="+sha); err != nil {
		return nil, err
	}
	if err := m.fetch(); err != nil {
		return nil, err
	}
	return host.Obj{{Key: "created", Value: name}, {Key: "sha", Value: sha}}, nil
}

func branchDelete(req map[string]any) (host.Obj, error) {
	m, err := newMirror(req["repo"], true)
	if err != nil {
		return nil, err
	}
	name, err := branchName(req["name"])
	if err != nil {
		return nil, err
	}
	def := pyStr(m.info["default_branch"])
	if name == def {
		return nil, gitError("git_default_branch")
	}
	if err := expectHead(m, name, req["expected_sha"]); err != nil {
		return nil, err
	}
	// Like `git branch -d`: only a branch fully contained in the default branch.
	if _, code, err := gitRun([]string{"git", "--git-dir=" + m.path, "merge-base", "--is-ancestor", req["expected_sha"].(string), "refs/heads/" + def}, "", 60*time.Second, false, nil); err != nil {
		return nil, err
	} else if code != 0 {
		return nil, gitError("git_branch_not_merged")
	}
	open, err := ghJSON("pr", "list", "--repo", gitOrg+"/"+m.name, "--head", name, "--state", "open", "--json", "number")
	if err != nil {
		return nil, err
	}
	if truthy(open) {
		return nil, gitError("git_branch_has_open_pr")
	}
	if _, err := ghRun(60*time.Second, "api", "-X", "DELETE", "repos/"+gitOrg+"/"+m.name+"/git/refs/heads/"+name); err != nil {
		return nil, err
	}
	if err := m.fetch(); err != nil {
		return nil, err
	}
	return host.Obj{{Key: "deleted", Value: name}}, nil
}

func prCreate(req map[string]any) (host.Obj, error) {
	m, err := newMirror(req["repo"], true)
	if err != nil {
		return nil, err
	}
	head, err := branchName(req["head"])
	if err != nil {
		return nil, err
	}
	baseArg := req["base"]
	if !truthy(baseArg) {
		baseArg = m.info["default_branch"]
	}
	base, err := branchName(baseArg)
	if err != nil {
		return nil, err
	}
	title, ok := req["title"].(string)
	bodyArg := req["body"]
	if !truthy(bodyArg) {
		bodyArg = ""
	}
	body, bok := bodyArg.(string)
	if !ok || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 256 || !bok || utf8.RuneCountInString(body) > 60000 || head == base {
		return nil, gitError("git_invalid_request")
	}
	if m.headOf(head) == "" || m.headOf(base) == "" {
		return nil, gitError("git_unknown_branch")
	}
	args := []string{"pr", "create", "--repo", gitOrg + "/" + m.name, "--head", head, "--base", base, "--title", strings.TrimSpace(title), "--body", body}
	if truthy(req["draft"]) {
		args = append(args, "--draft")
	}
	url, err := ghRun(60*time.Second, args...)
	if err != nil {
		return nil, err
	}
	return host.Obj{{Key: "url", Value: strings.TrimSpace(url)}}, nil
}

func prMerge(req map[string]any) (host.Obj, error) {
	info, err := repoInfo(req["repo"])
	if err != nil {
		return nil, err
	}
	number, isInt := host.Int(req["number"])
	method := req["method"]
	if !truthy(method) {
		method = "merge"
	}
	if !isInt || (method != "merge" && method != "squash") || !isSHA(req["expected_head_sha"]) {
		return nil, gitError("git_invalid_request")
	}
	expected, repo, n := req["expected_head_sha"].(string), gitOrg+"/"+pyStr(info["name"]), strconv.FormatInt(number, 10)
	v, err := ghJSON("pr", "view", n, "--repo", repo, "--json", "state,mergeable,headRefOid,isDraft")
	if err != nil {
		return nil, err
	}
	state := obj(v)
	for _, k := range []string{"headRefOid", "state", "isDraft", "mergeable"} {
		if _, ok := state[k]; !ok {
			return nil, fmt.Errorf("KeyError: %s", k)
		}
	}
	if state["headRefOid"] != expected {
		return nil, gitError("git_state_changed")
	}
	if state["state"] != "OPEN" || truthy(state["isDraft"]) || state["mergeable"] != "MERGEABLE" {
		return nil, gitError("git_not_mergeable")
	}
	if _, err := ghRun(120*time.Second, "pr", "merge", n, "--repo", repo, "--"+method.(string), "--match-head-commit", expected); err != nil {
		return nil, err
	}
	return host.Obj{{Key: "merged", Value: number}, {Key: "method", Value: method}}, nil
}

// applyCommit reverts or cherry-picks one commit onto a branch in a throwaway worktree, then pushes fast-forward only.
func applyCommit(req map[string]any, kind string) (host.Obj, error) {
	m, err := newMirror(req["repo"], true)
	if err != nil {
		return nil, err
	}
	branch, err := branchName(req["branch"])
	if err != nil {
		return nil, err
	}
	if !isSHA(req["sha"]) {
		return nil, gitError("git_invalid_sha")
	}
	sha := req["sha"].(string)
	if err := expectHead(m, branch, req["expected_sha"]); err != nil {
		return nil, err
	}
	me, err := viewerFn()
	if err != nil {
		return nil, err
	}
	target := branch
	if branch == pyStr(m.info["default_branch"]) {
		// Never commit to the default branch directly: a new branch plus a PR.
		if target, err = branchName(fmt.Sprintf("arkism/%s-%s-%d", kind, sha[:7], time.Now().Unix())); err != nil {
			return nil, err
		}
	}
	work, err := os.MkdirTemp("", "arkism-git-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	gitDir := "--git-dir=" + m.path
	if _, _, err := gitRun([]string{"git", gitDir, "worktree", "add", "--detach", "--quiet", work, req["expected_sha"].(string)}, "", 60*time.Second, true, nil); err != nil {
		return nil, err
	}
	newSHA, err := func() (string, error) {
		defer func() {
			_, _, _ = gitRun([]string{"git", gitDir, "worktree", "remove", "--force", work}, "", 60*time.Second, false, nil)
			_, _, _ = gitRun([]string{"git", gitDir, "worktree", "prune"}, "", 60*time.Second, false, nil)
		}()
		if _, ok := me["login"]; !ok {
			return "", errors.New("KeyError: login")
		}
		name := sOrDefault(me["name"], pyStr(me["login"]))
		email := pyStr(me["id"]) + "+" + pyStr(me["login"]) + "@users.noreply.github.com"
		identity := map[string]string{"GIT_AUTHOR_NAME": name, "GIT_AUTHOR_EMAIL": email, "GIT_COMMITTER_NAME": name, "GIT_COMMITTER_EMAIL": email}
		parents, err := parentsOf(m, sha)
		if err != nil {
			return "", err
		}
		verb := "cherry-pick"
		if kind == "revert" {
			verb = "revert"
		}
		command := []string{"git", "-c", "commit.gpgsign=false", verb, "--no-edit"}
		if len(parents) > 1 {
			command = append(command, "-m", "1")
		}
		if kind == "cherry-pick" {
			command = append(command, "-x")
		}
		command = append(command, sha)
		if _, code, err := gitRun(command, work, 120*time.Second, false, identity); err != nil {
			return "", err
		} else if code != 0 {
			_, _, _ = gitRun([]string{"git", verb, "--abort"}, work, 60*time.Second, false, nil)
			return "", gitError("git_conflict")
		}
		out, _, err := gitRun([]string{"git", "rev-parse", "HEAD"}, work, 60*time.Second, true, nil)
		if err != nil {
			return "", err
		}
		out = strings.TrimSpace(out)
		// Fast-forward only: the remote refuses if the branch moved since expected_sha.
		_, _, err = gitRun([]string{"git", "push", "--quiet", gitRemote + "/" + gitOrg + "/" + m.name + ".git", out + ":refs/heads/" + target}, work, 180*time.Second, true, nil)
		return out, err
	}()
	if err != nil {
		return nil, err
	}
	if err := m.fetch(); err != nil {
		return nil, err
	}
	out := host.Obj{{Key: "branch", Value: target}, {Key: "sha", Value: newSHA}}
	if target != branch {
		title := "Cherry-pick " + sha[:7]
		if kind == "revert" {
			title = "Revert " + sha[:7]
		}
		url, err := ghRun(60*time.Second, "pr", "create", "--repo", gitOrg+"/"+m.name, "--head", target, "--base", branch, "--title", title,
			"--body", fmt.Sprintf("Arkism Git 화면에서 만든 %s 변경입니다. 원본 커밋 %s.", kind, sha))
		if err != nil {
			return nil, err
		}
		out = append(out, host.KV{Key: "url", Value: strings.TrimSpace(url)})
	}
	return out, nil
}

func gitRerun(req map[string]any) (host.Obj, error) {
	info, err := repoInfo(req["repo"])
	if err != nil {
		return nil, err
	}
	id, ok := validNumber(req["run_id"])
	if !ok {
		return nil, gitError("git_invalid_request")
	}
	if _, err := ghRun(60*time.Second, "run", "rerun", strconv.FormatInt(id, 10), "--failed", "--repo", gitOrg+"/"+pyStr(info["name"])); err != nil {
		return nil, err
	}
	return host.Obj{{Key: "rerun", Value: id}}, nil
}

var gitWriteActions = map[string]bool{"branch_create": true, "branch_delete": true, "pr_create": true, "pr_merge": true, "revert": true, "cherry_pick": true, "rerun": true}

var gitActions map[string]func(map[string]any) (host.Obj, error)

func init() {
	gitActions = map[string]func(map[string]any) (host.Obj, error){
		"repos": func(map[string]any) (host.Obj, error) {
			rows, err := reposFn()
			return host.Obj{{Key: "repos", Value: rows}}, err
		},
		"graph": gitGraph, "commit": gitCommit, "diff": gitDiff, "prs": gitPRs, "pr": gitPR, "runs": gitRuns,
		"branch_create": branchCreate, "branch_delete": branchDelete, "pr_create": prCreate, "pr_merge": prMerge,
		"revert":      func(r map[string]any) (host.Obj, error) { return applyCommit(r, "revert") },
		"cherry_pick": func(r map[string]any) (host.Obj, error) { return applyCommit(r, "cherry-pick") },
		"rerun":       gitRerun,
	}
	host.Register("git", gitMain)
}

func gitHandle(v any) (host.Obj, error) {
	req, ok := v.(map[string]any)
	action, _ := req["action"].(string)
	if !ok || gitActions[action] == nil {
		return nil, gitError("git_invalid_request")
	}
	if repo, ok := req["repo"].(string); gitWriteActions[action] && ok && repoName.MatchString(repo) {
		for _, kind := range []string{"prs", "runs"} { // a change must show on the next read, not after the cache expires
			_ = os.Remove(filepath.Join(gitState, "."+kind+"-"+repo+".json"))
		}
	}
	defer releaseGitLocks()
	out, err := gitActions[action](req)
	if err != nil {
		return nil, err
	}
	return append(out, host.KV{Key: "authority", Value: "data_only_untrusted"}), nil
}

func gitMain(_ []string, stdin io.Reader, stdout, _ io.Writer) int {
	reply := func() string {
		raw, err := io.ReadAll(io.LimitReader(stdin, 65536))
		if err != nil {
			return ""
		}
		req, err := host.Loads(string(raw))
		if err != nil {
			return ""
		}
		out, err := gitHandle(req)
		if err != nil {
			var ge gitError
			if errors.As(err, &ge) {
				return host.MustDumps(host.Obj{{Key: "error", Value: string(ge)}}, host.DumpOpts{})
			}
			return ""
		}
		text, err := host.Dumps(out, host.DumpOpts{Compact: true, NoASCII: true})
		if err != nil {
			return ""
		}
		if len(text) > replyLimit {
			return host.MustDumps(host.Obj{{Key: "error", Value: "git_reply_too_large"}}, host.DumpOpts{})
		}
		fmt.Fprintln(stdout, text)
		return "ok"
	}()
	switch reply {
	case "ok":
		return 0
	case "":
		reply = host.MustDumps(host.Obj{{Key: "error", Value: "git_unavailable"}}, host.DumpOpts{})
	}
	fmt.Fprintln(stdout, reply)
	return 1
}
