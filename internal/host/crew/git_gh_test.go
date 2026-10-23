//go:build unix

package crew

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPRListMergesOpenAndClosedAndMarksTheViewer(t *testing.T) {
	newGitFixture(t)
	var asked [][]string
	patch(t, &ghJSON, func(args ...string) (any, error) {
		asked = append(asked, args)
		if slices.Contains(args, "open") {
			return []any{
				map[string]any{"number": 2, "title": "mine", "state": "OPEN", "author": map[string]any{"login": "owner"}, "updatedAt": "2026-10-02",
					"reviewDecision": "APPROVED", "assignees": []any{map[string]any{"login": "owner"}},
					"statusCheckRollup": []any{map[string]any{"conclusion": "SUCCESS"}, map[string]any{"status": "IN_PROGRESS"}}},
				map[string]any{"number": 3, "title": "theirs", "state": "OPEN", "author": map[string]any{"login": "bob"}, "updatedAt": "2026-10-03",
					"reviewRequests": []any{map[string]any{"login": "owner"}}, "statusCheckRollup": []any{map[string]any{"state": "FAILURE"}}},
			}, nil
		}
		return []any{map[string]any{"number": 2, "title": "dup"}, map[string]any{"number": 1, "title": "old", "state": "MERGED", "updatedAt": "2026-09-01"}}, nil
	})
	out := mustGit(t, map[string]any{"action": "prs", "repo": "demo"})
	if out["viewer"] != "owner" {
		t.Fatal(out)
	}
	pulls := list(out["pulls"])
	if len(pulls) != 3 {
		t.Fatalf("pulls = %v", pulls)
	}
	first, second, third := obj(pulls[0]), obj(pulls[1]), obj(pulls[2])
	eq(t, []any{first["number"], second["number"], third["number"]}, []any{3, 2, 1})
	eq(t, []any{first["checks"], second["checks"], third["checks"]}, []any{"failure", "pending", "none"})
	eq(t, []any{second["mine"], second["assigned"], second["review"], first["review_requested"], first["mine"]}, []any{true, true, "approved", true, false})
	eq(t, third["state"], "merged")

	// The second read inside a minute comes from the cache; refresh asks GitHub again.
	n := len(asked)
	mustGit(t, map[string]any{"action": "prs", "repo": "demo"})
	if len(asked) != n {
		t.Fatal("prs not cached")
	}
	mustGit(t, map[string]any{"action": "prs", "repo": "demo", "refresh": true})
	if len(asked) != n+2 {
		t.Fatal("refresh did not ask GitHub")
	}
	patch(t, &ghJSON, func(...string) (any, error) { return nil, errors.New("gh down") })
	if _, err := handleGit(t, map[string]any{"action": "prs", "repo": "demo", "refresh": true}); err == nil {
		t.Fatal("gh failure must surface")
	}
	mustGitErr(t, map[string]any{"action": "prs", "repo": "../x"}, "git_invalid_repo")
	mustGitErr(t, map[string]any{"action": "prs", "repo": "other"}, "git_unknown_repo")
}

func TestPRDetailShapesChecksFilesAndReviews(t *testing.T) {
	newGitFixture(t)
	reviews := []any{}
	for i := range 35 {
		reviews = append(reviews, map[string]any{"author": map[string]any{"login": "r"}, "state": "COMMENTED", "body": strings.Repeat("x", i)})
	}
	var asked []string
	patch(t, &ghJSON, func(args ...string) (any, error) {
		asked = args
		return map[string]any{"number": 5, "title": "t", "body": nil, "state": "OPEN", "author": map[string]any{"login": "bob"},
			"mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "commits": []any{1, 2},
			"files":             []any{map[string]any{"path": "a.go", "additions": 3, "deletions": 1}},
			"reviews":           reviews,
			"statusCheckRollup": []any{map[string]any{"name": "ci", "conclusion": "SUCCESS", "detailsUrl": "d"}, map[string]any{"context": "legacy", "state": "SUCCESS", "targetUrl": "u"}}}, nil
	})
	out := mustGit(t, map[string]any{"action": "pr", "repo": "demo", "number": 5})
	eq(t, asked[:5], []string{"pr", "view", "5", "--repo", "ORG/demo"})
	eq(t, []any{out["body"], out["mergeable"], out["merge_state"], out["commits"], out["check_summary"]}, []any{"", "mergeable", "clean", 2, "success"})
	checks := list(out["checks"])
	eq(t, obj(checks[1]), map[string]any{"name": "legacy", "state": "success", "url": "u"})
	eq(t, obj(list(out["files"])[0]), map[string]any{"path": "a.go", "added": 3, "deleted": 1})
	if rv := list(out["reviews"]); len(rv) != 30 || len(obj(rv[29])["body"].(string)) != 34 {
		t.Fatalf("reviews = %d", len(rv))
	}
	for _, bad := range []any{0, -1, "5", nil} {
		mustGitErr(t, map[string]any{"action": "pr", "repo": "demo", "number": bad}, "git_invalid_request")
	}
}

func TestRunsListAndRerun(t *testing.T) {
	f := newGitFixture(t)
	patch(t, &ghJSON, func(args ...string) (any, error) {
		return []any{map[string]any{"databaseId": 99, "workflowName": "quality", "status": "completed", "conclusion": nil, "headBranch": "main"}}, nil
	})
	out := mustGit(t, map[string]any{"action": "runs", "repo": "demo"})
	run := obj(list(out["runs"])[0])
	eq(t, []any{run["id"], run["workflow"], run["conclusion"], run["branch"]}, []any{99, "quality", "", "main"})
	mustGit(t, map[string]any{"action": "rerun", "repo": "demo", "run_id": 99})
	want := []string{"run", "rerun", "99", "--failed", "--repo", "ORG/demo"}
	if !slices.ContainsFunc(f.calls, func(c []string) bool { return slices.Equal(c, want) }) {
		t.Fatalf("%v", f.calls)
	}
	mustGitErr(t, map[string]any{"action": "rerun", "repo": "demo", "run_id": "99"}, "git_invalid_request")
	patch(t, &ghRun, func(time.Duration, ...string) (string, error) { return "", gitError("git_command_failed") })
	mustGitErr(t, map[string]any{"action": "rerun", "repo": "demo", "run_id": 1}, "git_command_failed")
}

func TestPRCreateValidatesAndOpensThePR(t *testing.T) {
	f := newGitFixture(t)
	out := mustGit(t, map[string]any{"action": "pr_create", "repo": "demo", "head": "feature", "title": "  Add b  ", "draft": true})
	eq(t, out["url"], "https://github.com/ORG/demo/pull/9")
	want := []string{"pr", "create", "--repo", "ORG/demo", "--head", "feature", "--base", "main", "--title", "Add b", "--body", "", "--draft"}
	if !slices.ContainsFunc(f.calls, func(c []string) bool { return slices.Equal(c, want) }) {
		t.Fatalf("%v", f.calls)
	}
	for _, c := range []struct {
		req  map[string]any
		code string
	}{
		{map[string]any{"head": "feature", "title": " "}, "git_invalid_request"},
		{map[string]any{"head": "main", "title": "t"}, "git_invalid_request"},
		{map[string]any{"head": "feature", "title": "t", "body": 3}, "git_invalid_request"},
		{map[string]any{"head": "feature", "title": strings.Repeat("t", 257)}, "git_invalid_request"},
		{map[string]any{"head": "nope", "title": "t"}, "git_unknown_branch"},
		{map[string]any{"head": "-x", "title": "t"}, "git_invalid_branch"},
		{map[string]any{"head": "feature", "base": "bad..base", "title": "t"}, "git_invalid_branch"},
	} {
		c.req["action"], c.req["repo"] = "pr_create", "demo"
		mustGitErr(t, c.req, c.code)
	}
}

func TestRepoListAndViewerFromGH(t *testing.T) {
	root := t.TempDir()
	patch(t, &gitState, root)
	patch(t, &gitOrg, "")
	if _, err := gitRepos(); err == nil {
		t.Fatal("no org must fail")
	}
	patch(t, &gitOrg, "ORG")
	calls := 0
	patch(t, &ghJSON, func(args ...string) (any, error) {
		calls++
		if args[0] == "api" {
			return map[string]any{"login": "owner", "id": 1}, nil
		}
		return []any{
			map[string]any{"name": "zeta", "description": nil, "defaultBranchRef": nil, "isArchived": false},
			map[string]any{"name": "old", "description": "d", "defaultBranchRef": map[string]any{"name": "trunk"}, "isArchived": true},
			map[string]any{"name": "alpha", "isArchived": false},
		}, nil
	})
	rows, err := gitRepos()
	if err != nil {
		t.Fatal(err)
	}
	var names []any
	for _, r := range rows {
		names = append(names, obj(r)["name"])
	}
	eq(t, names, []any{"alpha", "zeta", "old"})
	eq(t, []any{obj(rows[1])["default_branch"], obj(rows[1])["description"], obj(rows[2])["default_branch"]}, []any{"main", "", "trunk"})
	if _, err := gitRepos(); err != nil || calls != 1 {
		t.Fatal("repo list not cached", calls, err)
	}
	v, err := gitViewer()
	if err != nil || v["login"] != "owner" {
		t.Fatal(v, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".viewer.json")); err != nil {
		t.Fatal(err)
	}
}

func TestGHCallsRunTheBinary(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\nlist) echo '[1,2]';;\nempty) ;;\nslow) sleep 5;;\n*) exit 4;;\nesac\n"
	os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if out, err := ghCall(5*time.Second, "list"); err != nil || strings.TrimSpace(out) != "[1,2]" {
		t.Fatal(out, err)
	}
	if _, err := ghCall(5*time.Second, "fail"); err == nil || err.Error() != "git_command_failed" {
		t.Fatal(err)
	}
	if _, err := ghCall(50*time.Millisecond, "slow"); err == nil {
		t.Fatal("timeout must fail")
	}
	patch(t, &ghRun, ghCall)
	if v, err := ghJSONCall("list"); err != nil || len(list(v)) != 2 {
		t.Fatal(v, err)
	}
	if v, err := ghJSONCall("empty"); err != nil || v != nil {
		t.Fatal(v, err)
	}
	if _, err := ghJSONCall("fail"); err == nil {
		t.Fatal("gh failure must surface")
	}
	eq(t, gitError("git_x").Error(), "git_x")
}
