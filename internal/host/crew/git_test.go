//go:build unix

package crew

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func sh(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// gitFixture: a local bare repository stands in for GitHub; gh calls are faked and recorded.
type gitFixture struct {
	upstream, work, mainSHA, featureSHA string
	calls                               [][]string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	f := &gitFixture{upstream: filepath.Join(root, "remote", "ORG", "demo.git"), work: filepath.Join(root, "work")}
	sh(t, "", "git", "init", "--bare", "-q", "-b", "main", f.upstream)
	sh(t, "", "git", "clone", "-q", f.upstream, f.work)
	g := func(args ...string) string {
		return sh(t, f.work, append([]string{"git", "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	}
	write := func(name, text string) { _ = os.WriteFile(filepath.Join(f.work, name), []byte(text), 0o644) }
	write("a.txt", "one\n")
	g("add", ".")
	g("commit", "-qm", "first")
	write("a.txt", "one\ntwo\n")
	g("commit", "-qam", "second")
	g("push", "-q", "origin", "main")
	g("checkout", "-qb", "feature")
	g("mv", "a.txt", "b.txt")
	write("c.txt", "feature\n")
	g("add", ".")
	g("commit", "-qm", "feature work")
	g("push", "-q", "origin", "feature")
	f.mainSHA, f.featureSHA = g("rev-parse", "main"), g("rev-parse", "feature")
	patch(t, &gitState, filepath.Join(root, "state"))
	patch(t, &gitRemote, filepath.Join(root, "remote"))
	patch(t, &gitOrg, "ORG")
	patch(t, &reposFn, func() ([]any, error) {
		return []any{map[string]any{"name": "demo", "default_branch": "main", "archived": false, "url": "", "description": "", "pushed_at": ""}}, nil
	})
	patch(t, &viewerFn, func() (map[string]any, error) { return map[string]any{"login": "owner", "name": "Owner", "id": 7}, nil })
	patch(t, &ghRun, func(_ time.Duration, args ...string) (string, error) {
		f.calls = append(f.calls, args)
		switch {
		case len(args) > 1 && args[0] == "pr" && args[1] == "create":
			return "https://github.com/ORG/demo/pull/9\n", nil
		case args[0] == "api" && strings.HasSuffix(args[1], "/git/refs"):
			sh(t, "", "git", "--git-dir="+f.upstream, "update-ref", args[3][4:], args[5][4:])
		case len(args) > 2 && args[0] == "api" && args[1] == "-X" && args[2] == "DELETE":
			sh(t, "", "git", "--git-dir="+f.upstream, "update-ref", "-d", "refs/heads/"+strings.SplitN(args[3], "/git/refs/heads/", 2)[1])
		}
		return "", nil
	})
	patch(t, &ghJSON, func(args ...string) (any, error) {
		f.calls = append(f.calls, args)
		if args[0] == "pr" && args[1] == "list" {
			return []any{}, nil
		}
		return map[string]any{}, nil
	})
	return f
}

func handleGit(t *testing.T, req map[string]any) (map[string]any, error) {
	t.Helper()
	out, err := gitHandle(req)
	if err != nil {
		return nil, err
	}
	v, lerr := host.Loads(host.MustDumps(out, host.DumpOpts{}))
	if lerr != nil {
		t.Fatal(lerr)
	}
	return obj(v), nil
}

func mustGit(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	out, err := handleGit(t, req)
	if err != nil {
		t.Fatalf("%v: %v", req, err)
	}
	return out
}

func mustGitErr(t *testing.T, req map[string]any, code string) {
	t.Helper()
	_, err := handleGit(t, req)
	var ge gitError
	if !errors.As(err, &ge) || string(ge) != code {
		t.Fatalf("%v: want %s, got %v", req, code, err)
	}
}

func (f *gitFixture) up(t *testing.T, args ...string) string {
	return sh(t, "", append([]string{"git", "--git-dir=" + f.upstream}, args...)...)
}

func TestGraphCommitAndRenameDiff(t *testing.T) {
	f := newGitFixture(t)
	got := mustGit(t, map[string]any{"action": "graph", "repo": "demo"})
	subjects := []any{}
	for _, c := range list(got["commits"]) {
		subjects = append(subjects, obj(c)["subject"])
	}
	eq(t, subjects, []any{"feature work", "second", "first"})
	refs := []string{}
	for _, r := range list(got["refs"]) {
		refs = append(refs, pyStr(obj(r)["kind"])+" "+pyStr(obj(r)["name"]))
	}
	sort.Strings(refs)
	eq(t, refs, []string{"branch feature", "branch main"})
	detail := mustGit(t, map[string]any{"action": "commit", "repo": "demo", "sha": f.featureSHA})
	found := false
	for _, x := range list(detail["files"]) {
		found = found || same(x, map[string]any{"path": "b.txt", "old_path": "a.txt", "added": 0, "deleted": 0})
	}
	if !found {
		t.Fatalf("no rename in %v", detail["files"])
	}
	eq(t, detail["branches"], []any{"feature"})
	diff := mustGit(t, map[string]any{"action": "diff", "repo": "demo", "sha": f.featureSHA, "path": "c.txt"})
	if !strings.Contains(pyStr(diff["patch"]), "+feature") {
		t.Fatalf("%v", diff["patch"])
	}
	// a pull request's whole change: from the merge-base with its base branch
	whole := mustGit(t, map[string]any{"action": "diff", "repo": "demo", "sha": f.featureSHA, "base": "main", "path": "c.txt"})
	if !strings.Contains(pyStr(whole["patch"]), "+feature") {
		t.Fatalf("%v", whole["patch"])
	}
	mustGitErr(t, map[string]any{"action": "diff", "repo": "demo", "sha": f.featureSHA, "base": "-x", "path": "c.txt"}, "git_invalid_branch")
	mustGitErr(t, map[string]any{"action": "graph", "repo": "../demo"}, "git_invalid_repo")
}

func TestRevertOnFeaturePushesFastForwardOnly(t *testing.T) {
	f := newGitFixture(t)
	mustGitErr(t, map[string]any{"action": "revert", "repo": "demo", "sha": f.featureSHA, "branch": "feature", "expected_sha": f.mainSHA}, "git_state_changed")
	out := mustGit(t, map[string]any{"action": "revert", "repo": "demo", "sha": f.featureSHA, "branch": "feature", "expected_sha": f.featureSHA})
	eq(t, out["branch"], "feature")
	eq(t, f.up(t, "rev-parse", "feature"), out["sha"])
	eq(t, f.up(t, "rev-parse", "feature^"), f.featureSHA)
	eq(t, f.up(t, "log", "-1", "--format=%an", "feature"), "Owner")
}

func TestDefaultBranchGetsANewBranchAndPR(t *testing.T) {
	f := newGitFixture(t)
	out := mustGit(t, map[string]any{"action": "cherry_pick", "repo": "demo", "sha": f.featureSHA, "branch": "main", "expected_sha": f.mainSHA})
	if !strings.HasPrefix(pyStr(out["branch"]), "arkism/cherry-pick-") {
		t.Fatalf("%v", out["branch"])
	}
	eq(t, f.up(t, "rev-parse", "main"), f.mainSHA)
	eq(t, out["url"], "https://github.com/ORG/demo/pull/9")
	if !slices.ContainsFunc(f.calls, func(c []string) bool { return c[0] == "pr" && c[1] == "create" && slices.Contains(c, "main") }) {
		t.Fatalf("no PR: %v", f.calls)
	}
}

func TestConflictAbortsWithoutPush(t *testing.T) {
	f := newGitFixture(t)
	first := sh(t, f.work, "git", "rev-parse", "main^")
	// Reverting "first" on feature conflicts (a.txt was renamed and edited since).
	mustGitErr(t, map[string]any{"action": "revert", "repo": "demo", "sha": first, "branch": "feature", "expected_sha": f.featureSHA}, "git_conflict")
	eq(t, f.up(t, "rev-parse", "feature"), f.featureSHA)
}

func TestBranchCreateAndGuardedDelete(t *testing.T) {
	f := newGitFixture(t)
	mustGit(t, map[string]any{"action": "branch_create", "repo": "demo", "name": "topic", "sha": f.mainSHA})
	mustGitErr(t, map[string]any{"action": "branch_create", "repo": "demo", "name": "topic", "sha": f.mainSHA}, "git_branch_exists")
	for _, c := range [][3]string{{"main", f.mainSHA, "git_default_branch"}, {"feature", f.featureSHA, "git_branch_not_merged"},
		{"topic", f.featureSHA, "git_state_changed"}, {"bad..name", f.mainSHA, "git_invalid_branch"}} {
		mustGitErr(t, map[string]any{"action": "branch_delete", "repo": "demo", "name": c[0], "expected_sha": c[1]}, c[2])
	}
	mustGit(t, map[string]any{"action": "branch_delete", "repo": "demo", "name": "topic", "expected_sha": f.mainSHA})
	if strings.Contains(f.up(t, "branch"), "topic") {
		t.Fatal("topic still there")
	}
}

func TestMergeRequiresSeenHeadAndMergeable(t *testing.T) {
	f := newGitFixture(t)
	states := []any{map[string]any{"headRefOid": strings.Repeat("0", 40), "state": "OPEN", "isDraft": false, "mergeable": "MERGEABLE"},
		map[string]any{"headRefOid": f.featureSHA, "state": "OPEN", "isDraft": false, "mergeable": "CONFLICTING"},
		map[string]any{"headRefOid": f.featureSHA, "state": "OPEN", "isDraft": false, "mergeable": "MERGEABLE"}}
	patch(t, &ghJSON, func(...string) (any, error) { s := states[0]; states = states[1:]; return s, nil })
	for _, code := range []string{"git_state_changed", "git_not_mergeable"} {
		mustGitErr(t, map[string]any{"action": "pr_merge", "repo": "demo", "number": 3, "expected_head_sha": f.featureSHA}, code)
	}
	mustGit(t, map[string]any{"action": "pr_merge", "repo": "demo", "number": 3, "expected_head_sha": f.featureSHA, "method": "squash"})
	want := []string{"pr", "merge", "3", "--repo", "ORG/demo", "--squash", "--match-head-commit", f.featureSHA}
	if !slices.ContainsFunc(f.calls, func(c []string) bool { return slices.Equal(c, want) }) {
		t.Fatalf("%v", f.calls)
	}
}

func TestConcurrentCacheMissesPublishCompleteJSON(t *testing.T) {
	root := t.TempDir()
	patch(t, &gitState, root)
	var ready sync.WaitGroup
	ready.Add(2)
	patch(t, &gitReplace, func(src, dst string) error { ready.Done(); ready.Wait(); return os.Rename(src, dst) })
	got := make([]any, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = cached("repos", 0, func() (any, error) { return map[string]any{"writer": i}, nil })
		}()
	}
	wg.Wait()
	eq(t, got, []any{map[string]any{"writer": 0}, map[string]any{"writer": 1}})
	raw, _ := os.ReadFile(filepath.Join(root, ".repos.json"))
	v, err := host.Loads(string(raw))
	if err != nil || (!same(v, map[string]any{"writer": 0}) && !same(v, map[string]any{"writer": 1})) {
		t.Fatalf("%s", raw)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("%d files left", len(entries))
	}
}

// The desktop's SSH call: JSON in, one JSON line out, fixed codes on failure.
func TestGitCommandRepliesWithOneJSONLine(t *testing.T) {
	newGitFixture(t)
	for _, c := range []struct{ in, want string }{
		{`{"action":"repos"}`, `{"repos":[{"archived":false,"default_branch":"main","description":"","name":"demo","pushed_at":"","url":""}],"authority":"data_only_untrusted"}`},
		{`{"action":"nope"}`, `{"error": "git_invalid_request"}`},
		{`not json`, `{"error": "git_unavailable"}`},
	} {
		var out strings.Builder
		code := gitMain(nil, strings.NewReader(c.in), &out, &out)
		if strings.TrimSpace(out.String()) != c.want || (code == 0) != strings.HasPrefix(c.want, `{"repos"`) {
			t.Fatalf("%s -> %d %s", c.in, code, out.String())
		}
	}
}

// Without ARKISM_GIT_ORG the screen must name the missing setting, not a generic failure.
func TestGitReposWithoutOrgNamesTheSetting(t *testing.T) {
	patch(t, &gitOrg, "")
	var out strings.Builder
	if code := gitMain(nil, strings.NewReader(`{"action":"repos"}`), &out, &out); code == 0 || strings.TrimSpace(out.String()) != `{"error": "git_org_unset"}` {
		t.Fatalf("%d %s", code, out.String())
	}
}
