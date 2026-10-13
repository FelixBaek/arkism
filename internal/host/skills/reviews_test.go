package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const reviewComment = "<!-- arkism-ai-review -->\n\n## AI 리뷰 — blocking 1건 · 권고 1건\n\n" +
	"- **[blocking]** `internal/crewrun/daemon.go:129` — 다른 프로세스의 실행을 가져가요.\n" +
	"- **[권고]** `desktop/ui/crew.js:67-70` — 끝 노드 형식이 무시돼요.\n\n" +
	"<sub>e086e2e · codex / gpt-6.1-sol · 파일 5/5</sub>"

func TestParseTheCIComment(t *testing.T) {
	findings, model, sha := parseReview(reviewComment)
	var got []string
	for _, f := range findings {
		got = append(got, pyStr(objGet(asObj(f), "severity"))+" "+pyStr(objGet(asObj(f), "path"))+" "+pyStr(objGet(asObj(f), "line")))
	}
	if strings.Join(got, ",") != "blocking internal/crewrun/daemon.go 129,advisory desktop/ui/crew.js 67" || model != "codex / gpt-6.1-sol" || sha != "e086e2e" {
		t.Fatal(got, model, sha)
	}
	if f, _, _ := parseReview("<!-- arkism-ai-review -->\n## AI 리뷰 — 문제 없음"); len(f) != 0 {
		t.Fatal(f)
	}
}

func TestSettingsFileIsWrittenOnlyWhenItChanges(t *testing.T) {
	old := reviewSettings
	reviewSettings = filepath.Join(t.TempDir(), "review-settings.json")
	t.Cleanup(func() { reviewSettings = old })
	if !applySettings(map[string]any{"provider": "claude", "on_blocking": "block", "repos_off": []any{"demo-org/b", "demo-org/a"}}) {
		t.Fatal("not written")
	}
	raw, _ := os.ReadFile(reviewSettings)
	if canon(t, j(t, string(raw))) != `{"on_blocking":"block","provider":"claude","repos_off":["demo-org/a","demo-org/b"]}` {
		t.Fatal(string(raw))
	}
	if applySettings(map[string]any{"provider": "claude", "on_blocking": "block", "repos_off": []any{"demo-org/a", "demo-org/b"}}) {
		t.Fatal("rewritten")
	}
}

func TestUnfinishedReviewIsFailed(t *testing.T) {
	if prState(false, map[string]any{"conclusion": "cancelled"}, "") != "failed" || prState(false, map[string]any{"conclusion": "success"}, "") != "done" ||
		prState(false, map[string]any{}, "cancelled") != "failed" {
		t.Fatal("state")
	}
}

func TestPullItemFromFakeGH(t *testing.T) {
	old := gh
	t.Cleanup(func() { gh = old })
	gh = func(_ time.Duration, args ...string) (string, error) {
		path := args[len(args)-1]
		switch {
		case strings.Contains(path, "/actions/runs?"):
			return `{"workflow_runs": [{"name": "quality", "id": 7, "status": "completed", "conclusion": "success", "created_at": "c", "updated_at": "u"}]}`, nil
		case strings.HasSuffix(path, "/jobs"):
			return `{"jobs": [{"name": "ai-review", "conclusion": "success", "started_at": "s", "completed_at": "f"}, {"name": "pr-quality", "conclusion": "success"}]}`, nil
		case strings.Contains(path, "/comments"):
			return `{"body": "old"}` + "\n" + `{"body": ` + strings.ReplaceAll(canon(t, reviewComment), "\\u003c", "<") + "}\n", nil
		}
		t.Fatal(args)
		return "", nil
	}
	p := map[string]any{"head": map[string]any{"sha": "e086e2e1", "ref": "feat"}, "base": map[string]any{"ref": "main"}, "number": 3,
		"title": "t", "html_url": "u", "state": "open"}
	item, err := pullItem("o/r", p)
	if err != nil || objGet(item, "state") != "done" || len(objGet(item, "findings").([]any)) != 2 || len(objGet(item, "steps").([]any)) != 4 {
		t.Fatal(item, err)
	}
	delete(p, "title")
	if _, err := pullItem("o/r", p); err == nil || err.Error() != "'title'" {
		t.Fatal(err)
	}
}
