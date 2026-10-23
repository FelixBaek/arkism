package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// fakeOrgGH answers the gh calls the review collector makes for one org with two repos.
func fakeOrgGH(t *testing.T, calls *[]string) func(time.Duration, ...string) (string, error) {
	return func(_ time.Duration, args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if args[0] == "repo" {
			return `[{"nameWithOwner":"o/r"},{"nameWithOwner":"o/noflow"},{"nameWithOwner":"o/other"}]`, nil
		}
		if args[0] == "run" {
			return "", nil
		}
		path := args[len(args)-1]
		switch {
		case path == "repos/o/noflow/actions/workflows":
			return "", errors.New("forbidden")
		case path == "repos/o/other/actions/workflows":
			return `{"workflows":[{"name":"lint"}]}`, nil
		case path == "repos/o/r/actions/workflows":
			return `{"workflows":[{"name":"build"},{"name":"quality"}]}`, nil
		case strings.HasPrefix(path, "repos/o/r/pulls?"):
			return `[{"number":12,"title":"feat","state":"closed","merged_at":"2026-10-01","html_url":"u12","commits":3,
				"head":{"sha":"aaa111","ref":"feat"},"base":{"ref":"main"}},
				{"number":11,"title":"old","state":"open","head":{"sha":"bbb222","ref":"x"},"base":{"ref":"main"}}]`, nil
		case path == "repos/o/r/pulls/12":
			return `{"head":{"sha":"aaa111"}}`, nil
		case path == "repos/o/r/pulls/13":
			return `{"head":{"sha":"ccc333"}}`, nil
		case strings.Contains(path, "/actions/runs?head_sha=aaa111"):
			return `{"workflow_runs":[{"name":"quality","id":70,"status":"completed","conclusion":"success","created_at":"2026-10-02T00:00:00Z","updated_at":"2026-10-02T00:05:00Z"}]}`, nil
		case strings.Contains(path, "/actions/runs?head_sha="):
			return `{"workflow_runs":[{"name":"build","id":1}]}`, nil
		case path == "repos/o/r/actions/runs/70/jobs":
			return `{"jobs":[{"name":"pr-quality","conclusion":"success"},{"name":"ai-review","conclusion":"success","started_at":"s","completed_at":"2026-10-02T00:04:00Z"}]}`, nil
		case strings.Contains(path, "/comments"):
			return "", nil
		}
		t.Errorf("unexpected gh %v", args)
		return "", errors.New("unexpected")
	}
}

type reviewsArkism struct {
	mu     sync.Mutex
	reruns []any
	posts  []map[string]any
}

func (a *reviewsArkism) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == "POST" {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		a.posts = append(a.posts, body)
		w.Write([]byte(`{"ok":true}`))
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"code_reviews": map[string]any{
		"settings": map[string]any{"provider": "claude", "repos_off": []any{"o/z", "o/a"}},
		"reruns":   a.reruns,
	}})
}

func isolateReviews(t *testing.T) (*reviewsArkism, *[]string) {
	t.Helper()
	dir := t.TempDir()
	a := &reviewsArkism{}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	tok := filepath.Join(dir, "owner.token")
	os.WriteFile(tok, []byte("t"), 0o600)
	g, org, cache, settings := gh, reviewOrg, reviewsCache, reviewSettings
	base, tf, state, capState := host.BaseURL, host.TokenFile, host.AgentsState, host.CaptureState
	t.Cleanup(func() {
		gh, reviewOrg, reviewsCache, reviewSettings = g, org, cache, settings
		host.BaseURL, host.TokenFile, host.AgentsState, host.CaptureState = base, tf, state, capState
	})
	host.BaseURL, host.TokenFile = srv.URL, tok
	host.AgentsState, host.CaptureState = filepath.Join(dir, "agents"), filepath.Join(dir, "capture")
	reviewsCache = filepath.Join(host.AgentsState, "code-reviews.json")
	reviewSettings = filepath.Join(dir, "ci", "review-settings.json")
	reviewOrg = "o"
	t.Setenv("ARKISM_REVIEW_REPOS", "")
	calls := &[]string{}
	gh = fakeOrgGH(t, calls)
	return a, calls
}

func TestReviewsSyncReportsAndThrottles(t *testing.T) {
	a, calls := isolateReviews(t)
	a.reruns = []any{
		map[string]any{"id": "r1", "status": "pending", "repo": "o/r", "number": "12"},
		map[string]any{"id": "r2", "status": "pending", "repo": "o/r", "number": "twelve"},
		map[string]any{"id": "r3", "status": "pending", "repo": "o/r", "number": 13},
		map[string]any{"id": "r4", "status": "pending", "number": 1},
		map[string]any{"id": "r5", "status": "done", "repo": "o/r", "number": 12},
	}
	var out, errb bytes.Buffer
	if code := reviewsMain([]string{"sync"}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "PR 1개 보고") {
		t.Fatal(code, out.String(), errb.String())
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "run rerun 70 --repo o/r") {
		t.Fatalf("rerun not requested: %v", *calls)
	}
	if len(a.posts) != 1 {
		t.Fatalf("posts = %v", a.posts)
	}
	post := a.posts[0]
	results := post["results"].([]any)
	want := map[string]string{"r1": "done", "r2": "failed", "r3": "failed", "r4": "failed"}
	if len(results) != len(want) {
		t.Fatalf("results = %v", results)
	}
	for _, r := range results {
		m := r.(map[string]any)
		if want[m["id"].(string)] != m["status"] {
			t.Errorf("result %v", m)
		}
	}
	items := post["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["pr_state"] != "merged" || items[0].(map[string]any)["state"] != "done" {
		t.Fatalf("items = %v", items)
	}
	if repos := post["repos"].([]any); len(repos) != 1 || repos[0].(map[string]any)["prs"] != float64(12) {
		t.Fatalf("repos = %v", repos)
	}
	raw, err := os.ReadFile(reviewSettings)
	if err != nil || !strings.Contains(string(raw), `"provider": "claude"`) || strings.Index(string(raw), "o/a") > strings.Index(string(raw), "o/z") {
		t.Fatalf("settings file = %s, %v", raw, err)
	}

	// Nothing pending and the last read is fresh: no GitHub read, no report.
	a.reruns = nil
	*calls = nil
	out.Reset()
	if code := reviewsMain([]string{"sync"}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "PR 0개 보고") || len(*calls) != 0 || len(a.posts) != 1 {
		t.Fatal(code, out.String(), *calls)
	}
	out.Reset()
	if code := reviewsMain([]string{"sync", "--force"}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "PR 1개 보고") || len(a.posts) != 2 {
		t.Fatal(code, out.String())
	}
}

func TestReviewsShowAndUsage(t *testing.T) {
	isolateReviews(t)
	var out, errb bytes.Buffer
	if code := reviewsMain([]string{"show"}, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), `"o/r"`) || !strings.Contains(out.String(), "aaa111") {
		t.Fatal(code, out.String(), errb.String())
	}
	out.Reset()
	if code := reviewsMain(nil, nil, &out, &errb); code != 2 || !strings.Contains(out.String(), "ARKISM_REVIEW_ORG") {
		t.Fatal(code, out.String())
	}
}

func TestReviewsSyncFailsWhenArkismIsDown(t *testing.T) {
	isolateReviews(t)
	host.BaseURL = "http://127.0.0.1:1"
	var out, errb bytes.Buffer
	if code := reviewsMain([]string{"sync"}, nil, &out, &errb); code != 1 || errb.Len() == 0 {
		t.Fatal(code, errb.String())
	}
}

func TestReviewReposSources(t *testing.T) {
	isolateReviews(t)
	t.Setenv("ARKISM_REVIEW_REPOS", " a/b , ,c/d")
	if got, err := reviewRepos(); err != nil || strings.Join(got, ",") != "a/b,c/d" {
		t.Fatal(got, err)
	}
	t.Setenv("ARKISM_REVIEW_REPOS", "")
	reviewOrg = ""
	if got, err := reviewRepos(); err != nil || got != nil {
		t.Fatal("no org must collect nothing", got, err)
	}
	reviewOrg = "o"
	gh = func(time.Duration, ...string) (string, error) { return `[{"name":"x"}]`, nil }
	if _, err := reviewRepos(); err == nil || err.Error() != "'nameWithOwner'" {
		t.Fatal(err)
	}
	gh = func(time.Duration, ...string) (string, error) { return "", nil }
	if got, err := reviewRepos(); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	gh = func(time.Duration, ...string) (string, error) { return "", errors.New("auth") }
	if _, _, err := collectReviews(); err == nil {
		t.Fatal("repo list failure must surface")
	}
}

func TestPyIntArgAndEnvInt(t *testing.T) {
	for in, want := range map[any]int64{int64(4): 4, json.Number("5"): 5, " 7 ": 7, "12": 12} {
		if got, err := pyIntArg(in); err != nil || got != want {
			t.Errorf("pyIntArg(%v) = %d, %v", in, got, err)
		}
	}
	if _, err := pyIntArg("x"); err == nil || !strings.Contains(err.Error(), "invalid literal") {
		t.Error(err)
	}
	t.Setenv("ARKISM_TEST_INT", " 30 ")
	if envInt("ARKISM_TEST_INT", 1) != 30 {
		t.Error("envInt did not read the value")
	}
	t.Setenv("ARKISM_TEST_INT", "abc")
	if envInt("ARKISM_TEST_INT", 1) != 1 {
		t.Error("envInt did not fall back")
	}
}

func TestGhCLIRunsTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = ok ]; then echo '{\"a\":1}'; exit 0; fi\nif [ \"$1\" = quiet ]; then exit 3; fi\necho 'bad credentials' >&2\nexit 1\n"
	os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755)
	t.Setenv("PATH", bin)
	if out, err := ghCLI(5*time.Second, "ok"); err != nil || strings.TrimSpace(out) != `{"a":1}` {
		t.Fatal(out, err)
	}
	if _, err := ghCLI(5*time.Second, "fail"); err == nil || err.Error() != "bad credentials" {
		t.Fatal(err)
	}
	if _, err := ghCLI(5*time.Second, "quiet"); err == nil || err.Error() != "gh 실패" {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := ghCLI(5*time.Second, "ok"); err == nil {
		t.Fatal("missing gh must fail")
	}
}
