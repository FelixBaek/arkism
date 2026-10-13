package crew

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRun struct {
	pr       map[string]any
	changed  bool
	cloneSHA string
	calls    [][]string
}

func newFakeRun(pr map[string]any, changed bool) *fakeRun {
	head, _ := pr["head"].(map[string]any)
	sha, _ := head["sha"].(string)
	return &fakeRun{pr: pr, changed: changed, cloneSHA: sha}
}

func (f *fakeRun) run(argv []string, cwd string, timeout time.Duration, stdin string) (string, error) {
	f.calls = append(f.calls, argv)
	switch {
	case slices.Equal(argv[:2], []string{"gh", "api"}):
		raw, _ := json.Marshal(f.pr)
		return string(raw), nil
	case len(argv) >= 3 && slices.Equal(argv[:3], []string{"git", "status", "--porcelain"}):
		if f.changed {
			return " M a.go\n", nil
		}
		return "", nil
	case slices.Equal(argv, []string{"git", "rev-parse", "HEAD"}):
		return f.cloneSHA, nil
	case slices.Equal(argv[:2], []string{"git", "rev-parse"}):
		return "abc1234\n", nil
	}
	return "고쳤어요", nil
}

func (f *fakeRun) has(pred func([]string) bool) bool { return slices.ContainsFunc(f.calls, pred) }

func prFixture() map[string]any {
	return map[string]any{"state": "open", "head": map[string]any{"ref": "fix/x", "repo": map[string]any{"full_name": "octo/arkism"}},
		"base": map[string]any{"ref": "main", "repo": map[string]any{"default_branch": "main"}}}
}

func specFixture() map[string]any {
	return map[string]any{"repo": "octo/arkism", "number": 7, "path": "a.go", "line": 3, "text": "nil을 확인하지 않아요"}
}

func withHeadSHA(pr map[string]any, sha string) map[string]any {
	head := map[string]any{"sha": sha}
	for k, v := range pr["head"].(map[string]any) {
		head[k] = v
	}
	return withKey(pr, "head", head)
}

func isModel(c []string) bool { return slices.Contains(c, "--allowedTools") }
func isPush(c []string) bool  { return slices.Equal(c[:2], []string{"git", "push"}) }

func TestFixPushesToThePRBranchOnly(t *testing.T) {
	f := newFakeRun(prFixture(), true)
	ok, text, err := fixFinding(specFixture(), f.run, nil)
	if err != nil || !ok || !strings.Contains(text, "abc1234") {
		t.Fatal(ok, text, err)
	}
	if !f.has(func(c []string) bool { return slices.Equal(c, []string{"git", "push", "-q", "origin", "HEAD:fix/x"}) }) {
		t.Fatal(f.calls)
	}
	claude := f.calls[slices.IndexFunc(f.calls, isModel)]
	if strings.Contains(claude[slices.Index(claude, "--allowedTools")+1], "Bash") || claude[slices.Index(claude, "--setting-sources")+1] != "user" ||
		!slices.Contains(claude, "--strict-mcp-config") {
		t.Fatal(claude)
	}
}

func TestFixRefusesDefaultBranchClosedPRAndBadPaths(t *testing.T) {
	for _, pr := range []map[string]any{
		withKey(prFixture(), "head", map[string]any{"ref": "main", "repo": map[string]any{"full_name": "octo/arkism"}}),
		withKey(prFixture(), "state", "closed"),
		withKey(prFixture(), "head", map[string]any{"ref": "fix/x", "repo": map[string]any{"full_name": "someone/fork"}}),
	} {
		if _, _, err := fixFinding(specFixture(), newFakeRun(pr, true).run, nil); !isActionErr(err) {
			t.Error(pr, err)
		}
	}
	if _, _, err := fixFinding(withKey(specFixture(), "path", "../etc/passwd"), newFakeRun(prFixture(), true).run, nil); !isActionErr(err) {
		t.Error(err)
	}
}

func isActionErr(err error) bool { _, ok := err.(*ActionError); return ok }

func TestFixFixedHeadIsCheckedBeforeModelAndPush(t *testing.T) {
	sha := strings.Repeat("a", 40)
	pr := withHeadSHA(prFixture(), sha)
	spec := withKey(specFixture(), "expected_head_sha", sha)
	f := newFakeRun(pr, true)
	if ok, _, err := fixFinding(spec, f.run, nil); !ok || err != nil {
		t.Fatal(err)
	}
	apis := 0
	for _, c := range f.calls {
		if slices.Equal(c[:2], []string{"gh", "api"}) {
			apis++
		}
	}
	if apis != 2 {
		t.Fatal(apis)
	}
	for _, bad := range []string{"bad", strings.Repeat("b", 40)} {
		f := newFakeRun(pr, true)
		if _, _, err := fixFinding(withKey(spec, "expected_head_sha", bad), f.run, nil); !isActionErr(err) || f.has(isModel) {
			t.Error(bad, err)
		}
	}
	f = newFakeRun(pr, true)
	f.cloneSHA = strings.Repeat("b", 40)
	if _, _, err := fixFinding(spec, f.run, nil); !isActionErr(err) || f.has(isModel) {
		t.Error(err)
	}
	f = newFakeRun(pr, true)
	model := func([]string, string, time.Duration, string) (string, error) {
		f.pr = withKey(pr, "state", "closed")
		return "합성 결과", nil
	}
	if _, _, err := fixFinding(spec, f.run, model); !isActionErr(err) || f.has(isPush) {
		t.Error(err)
	}
}

func TestFixNoChangeIsReportedNotPushed(t *testing.T) {
	f := newFakeRun(prFixture(), false)
	if ok, _, err := fixFinding(specFixture(), f.run, nil); ok || err != nil || f.has(isPush) {
		t.Fatal(ok, err)
	}
}

func TestComfyQueueWaitAndSave(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch {
		case r.URL.Path == "/prompt":
			_, _ = w.Write([]byte(`{"prompt_id": "p1"}`))
		case strings.HasPrefix(r.URL.Path, "/history/"):
			if n <= 2 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			img := func(sub string) string {
				return `{"filename": "out_0001.png", "subfolder": "` + sub + `", "type": "output"}`
			}
			_, _ = w.Write([]byte(`{"p1": {"outputs": {"9": {"images": [` + img("") + "," + img("b") + "," + img("") + "," + img("") + `]}}}}`))
		default:
			_, _ = w.Write([]byte("PNG"))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	wf := `{"3": {"class_type": "KSampler", "inputs": {}}}`
	paths, err := comfyGenerate(map[string]any{"url": srv.URL, "workflow": wf}, dir, 0, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(paths[0])
	entries, _ := os.ReadDir(dir)
	if string(first) != "PNG" || len(paths) != 4 || len(entries) != 4 || paths[1] == paths[2] || paths[2] == paths[3] {
		t.Fatal(paths)
	}
}

func TestComfyUIFormatWorkflowIsRefused(t *testing.T) {
	spec := map[string]any{"url": "http://127.0.0.1:8188", "workflow": map[string]any{"nodes": []any{}, "links": []any{}}}
	if _, err := comfyGenerate(spec, t.TempDir(), 0, time.Second); !isActionErr(err) {
		t.Fatal(err)
	}
}
