package capture

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// conflictServer serves one conflict r1 between old memory o1 and candidate k1 and records
// the posted actions in order.
func conflictServer(t *testing.T, waiting string) *[]string {
	t.Helper()
	var posts []string
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/memory_candidates?"):
			if strings.Contains(path, "status=conflicted") {
				return mustLoad(t, `{"items": `+waiting+`}`), nil
			}
			return map[string]any{"items": []any{}}, nil
		case strings.HasPrefix(path, "/v1/memory_relations/r1?"):
			return mustLoad(t, `{"id": "r1", "revision": 4, "relations": [{"collection": "memory_candidates", "id": "k1"}, {"collection": "memories", "id": "o1"}]}`), nil
		case strings.HasPrefix(path, "/v1/memory_relations/"):
			return mustLoad(t, `{"id": "r2", "revision": 1, "relations": [{"collection": "memory_candidates", "id": "k2"}]}`), nil
		case strings.HasPrefix(path, "/v1/memories/o1?"):
			return mustLoad(t, `{"id": "o1", "revision": 2, "text": "라이트 테마를 쓴다"}`), nil
		case strings.HasPrefix(path, "/v1/memories/k1?"):
			return mustLoad(t, `{"id": "k1", "revision": 1}`), nil
		}
		return mustLoad(t, `{"revision": 7, "status": "candidate"}`), nil
	}
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		posts = append(posts, path+" "+str(field(body, "action"))+" "+str(field(body, "resolution")))
		if path == "/v1/memories/conflicts" && field(body, "action") == "mark" {
			return mustLoad(t, `{"memories": [{"id": "o1", "revision": 3}, {"id": "k1", "revision": 2}], "relation": {"id": "r9", "revision": 1}}`), nil
		}
		return map[string]any{}, nil
	}
	return &posts
}

func TestCaptureAIReviewResolvesConflictsByChoice(t *testing.T) {
	for _, c := range []struct {
		action string
		posts  []string
		trial  []string
	}{
		{"keep_old", []string{"/v1/memories/conflicts resolve keep_first"}, nil},
		{"reject", []string{"/v1/memories/conflicts resolve keep_first"}, nil},
		{"keep_both", []string{"/v1/memories/conflicts resolve keep_both", "/v1/memory_candidates/k1/review approve None"}, []string{"k1"}},
		{"approve", []string{"/v1/memories/conflicts resolve keep_both", "/v1/memory_candidates/k1/review approve None"}, []string{"k1"}},
		{"replace_old", []string{"/v1/memories/conflicts resolve keep_both", "/v1/memory_candidates/k1/review approve None",
			"/v1/memories/conflicts mark None", "/v1/memories/conflicts resolve second_supersedes_first"}, []string{"k1"}},
	} {
		t.Run(c.action, func(t *testing.T) {
			capFake(t)
			posts := conflictServer(t, `[{"id": "k1", "kind": "preference", "conflict_id": "r1", "text": "다크 테마를 쓴다", "tags": ["basis:explicit_user"]}]`)
			search = func(string, int, float64) ([]any, error) { return []any{}, nil }
			var prompts []string
			llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) {
				prompts = append(prompts, user)
				return []any{map[string]any{"id": "k1", "action": c.action, "content": "", "reason": "이유"}}, nil
			}
			if err := aiReview(false, nil); err != nil {
				t.Fatal(err)
			}
			if len(prompts) != 1 || !strings.Contains(prompts[0], "CONFLICT OLD: 라이트 테마를 쓴다") {
				t.Fatal(prompts)
			}
			if !reflect.DeepEqual(*posts, c.posts) {
				t.Fatal(*posts)
			}
			if got := trialSet(); !reflect.DeepEqual(got, c.trial) {
				t.Fatal(got)
			}
		})
	}
}

func TestCaptureAIReviewLeavesAConflictWithoutAnOldSideForLater(t *testing.T) {
	capFake(t)
	conflictServer(t, `[{"id": "k2", "kind": "preference", "conflict_id": "r2", "text": "x", "tags": ["basis:explicit_user"]}]`)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("asked the model without the old side")
		return nil, nil
	}
	if err := aiReview(false, nil); err != nil {
		t.Fatal(err)
	}
	// replace_old with no old memory among the relations stops instead of approving blindly into a supersede
	c := map[string]any{"id": "k2", "conflict_id": "r2"}
	if _, err := resolveCandidateConflict(c, "replace_old"); pyType(err) != "KeyError" {
		t.Fatal(err)
	}
}

func TestCaptureAIReviewDryRunPrintsDecisionsWithoutWriting(t *testing.T) {
	capFake(t)
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "status=candidate") {
			return mustLoad(t, `{"items": [{"id": "ep", "kind": "episodic", "text": "오늘 배포함"}, {"id": "p", "kind": "preference", "text": "짧게 쓴다", "tags": ["basis:explicit_user"]}]}`), nil
		}
		return map[string]any{"items": []any{}}, nil
	}
	search = func(string, int, float64) ([]any, error) {
		return []any{map[string]any{"id": "p", "text": "자기 자신"}, map[string]any{"id": "q", "text": "비슷한 것"}}, nil
	}
	var prompts []string
	llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) {
		prompts = append(prompts, user)
		return []any{map[string]any{"id": "p", "action": "edit_approve", "content": "  ", "reason": "좋다"}}, nil
	}
	var out bytes.Buffer
	if err := aiReview(true, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"id": "ep", "action": "reject"`) || !strings.Contains(lines[1], `"id": "p", "action": "approve"`) {
		t.Fatal(out.String())
	}
	if len(prompts) != 1 || strings.Contains(prompts[0], "자기 자신") || !strings.Contains(prompts[0], "RELATED: 비슷한 것") {
		t.Fatal(prompts)
	}
	if len(trialState()) != 0 {
		t.Fatal(trialState())
	}
}

func TestCaptureAIReviewSurvivesAModelOutageAndStopsOnServerErrors(t *testing.T) {
	capFake(t)
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "status=candidate") {
			return mustLoad(t, `{"items": [{"id": "p", "kind": "preference", "text": "짧게 쓴다"}]}`), nil
		}
		return map[string]any{"items": []any{}}, nil
	}
	search = func(string, int, float64) ([]any, error) { return []any{}, nil }
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return nil, runtimeErr("extractor_exit_1")
	}
	if err := aiReview(false, nil); err != nil {
		t.Fatal(err)
	}
	capGet = func(string, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 500} }
	if err := aiReview(false, nil); host.HTTPCode(err) != 500 {
		t.Fatal(err)
	}
}

func TestCaptureApplyReviewOnAPlainItem(t *testing.T) {
	capFake(t)
	status := "candidate"
	var bodies []any
	capGet = func(string, time.Duration) (map[string]any, error) {
		if status == "norev" {
			return map[string]any{"status": "candidate"}, nil
		}
		return map[string]any{"revision": 1, "status": status}, nil
	}
	capPost = func(_ string, body any, _ time.Duration) (map[string]any, error) {
		bodies = append(bodies, body)
		return map[string]any{}, nil
	}
	c := map[string]any{"id": "p"}
	// a conflict action answered for a plain item maps to approve/reject
	if got, err := applyReview(c, "keep_both", "", "", false, nil); err != nil || got != "p" || field(bodies[0], "action") != "approve" {
		t.Fatal(got, err, bodies)
	}
	if got, err := applyReview(c, "keep_old", "", "중복", false, nil); err != nil || got != nil || field(bodies[1], "reason") != "AI 검토: 중복" {
		t.Fatal(got, err, bodies)
	}
	status = "approved"
	if got, err := applyReview(c, "approve", "", "", false, nil); err != nil || got != nil || len(bodies) != 2 {
		t.Fatal(got, err)
	}
	status = "norev"
	if _, err := applyReview(c, "approve", "", "", false, nil); pyType(err) != "KeyError" {
		t.Fatal(err)
	}
}
