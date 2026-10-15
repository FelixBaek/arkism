package capture

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// reviewServer serves the waiting lists, one conflict r1 between old memory o1 and candidate k1,
// and records every classify call; any owner-token write fails the test.
func reviewServer(t *testing.T, candidates, conflicted string) *[]any {
	t.Helper()
	var classified []any
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/memory_candidates?") && strings.Contains(path, "status=conflicted"):
			return mustLoad(t, `{"items": `+conflicted+`}`), nil
		case strings.HasPrefix(path, "/v1/memory_candidates?"):
			return mustLoad(t, `{"items": `+candidates+`}`), nil
		case strings.HasPrefix(path, "/v1/memory_relations/r1?"):
			return mustLoad(t, `{"id": "r1", "revision": 4, "relations": [{"collection": "memory_candidates", "id": "k1"}, {"collection": "memories", "id": "o1"}]}`), nil
		case strings.HasPrefix(path, "/v1/memory_relations/"):
			return mustLoad(t, `{"id": "r2", "revision": 1, "relations": [{"collection": "memory_candidates", "id": "k2"}]}`), nil
		case strings.HasPrefix(path, "/v1/memories/o1?"):
			return mustLoad(t, `{"id": "o1", "revision": 2, "text": "라이트 테마를 쓴다"}`), nil
		}
		t.Fatalf("unexpected read %s", path)
		return nil, nil
	}
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		t.Fatalf("the reviewer wrote with the owner token: %s", path)
		return nil, nil
	}
	revPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		classified = append(classified, map[string]any{"path": path, "risk": field(body, "risk"), "reasons": field(body, "risk_reasons"),
			"dup": field(body, "dup_hint"), "rev": field(body, "expected_revision")})
		return map[string]any{}, nil
	}
	search = func(text string, _ int, _ float64) ([]any, error) {
		if text != "다크 테마를 쓴다" {
			return []any{map[string]any{"id": "q", "text": "비슷한 것"}}, nil
		}
		return []any{map[string]any{"id": "k1", "text": "자기 자신"}, map[string]any{"id": "q", "text": "비슷한 것"}}, nil
	}
	return &classified
}

func TestCaptureAIReviewOnlyClassifies(t *testing.T) {
	capFake(t)
	got := reviewServer(t,
		`[{"id": "p", "revision": 3, "kind": "decision", "text": "추측", "tags": ["basis:inferred"], "created_by": "owner"},
		  {"id": "done", "revision": 2, "kind": "decision", "text": "이미 분류됨", "created_by": "owner", "classification": {"risk": "low", "by": "arkism-memory-reviewer"}},
		  {"id": "ex", "revision": 1, "kind": "decision", "text": "추출 때 분류만 있음", "tags": ["basis:explicit_user"], "created_by": "owner", "classification": {"risk": "low", "by": "owner"}}]`,
		`[{"id": "k1", "revision": 5, "kind": "user", "conflict_id": "r1", "text": "다크 테마를 쓴다", "tags": ["basis:explicit_user"]}]`)
	var prompts []string
	llm = func(_, user, _ string, _ time.Duration, schema, _ string) (any, error) {
		prompts = append(prompts, user)
		if strings.Contains(schema, `"approve"`) {
			t.Fatal("the schema still offers approval")
		}
		return []any{
			map[string]any{"id": "p", "risk": "low", "risk_reasons": []any{" "}, "dup_hint": ""},
			map[string]any{"id": "k1", "risk": "high", "risk_reasons": []any{"기존 기억과 모순"}, "dup_hint": "o1"},
			map[string]any{"id": "ex", "risk": "bogus", "risk_reasons": []any{}, "dup_hint": ""},
		}, nil
	}
	if err := aiReview(false, nil); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "CONFLICT OLD o1: 라이트 테마를 쓴다") || !strings.Contains(prompts[0], "RELATED q: 비슷한 것") ||
		strings.Contains(prompts[0], "자기 자신") || strings.Contains(prompts[0], "이미 분류됨") {
		t.Fatal(prompts)
	}
	want := []any{
		map[string]any{"path": "/v1/memory_candidates/p/classify", "risk": "medium", "reasons": []any{"출처가 사용자 발화 아님"}, "dup": nil, "rev": 3},
		map[string]any{"path": "/v1/memory_candidates/ex/classify", "risk": "medium", "reasons": nil, "dup": nil, "rev": 1},
		map[string]any{"path": "/v1/memory_candidates/k1/classify", "risk": "high", "reasons": []any{"기존 기억과 모순"}, "dup": "o1", "rev": 5},
	}
	if !reflect.DeepEqual(norm2(*got), norm2(want)) {
		t.Fatalf("%v", *got)
	}
	if len(trialState()) != 0 {
		t.Fatal(trialState())
	}
}

// norm2 compares classify calls by path, ignoring order and number types.
func norm2(calls []any) map[string]string {
	out := map[string]string{}
	for _, c := range calls {
		m := c.(map[string]any)
		out[m["path"].(string)] = host.MustDumps(m, host.DumpOpts{NoASCII: true})
	}
	return out
}

func TestCaptureAIReviewFailsClosedWithoutTheReviewerCredential(t *testing.T) {
	capFake(t)
	reviewServer(t, `[{"id": "ep", "revision": 1, "kind": "episodic", "text": "오늘 배포함"}]`, `[]`)
	revPost = origRevPost
	t.Setenv("ARKISM_REVIEW_TOKEN_FILE", filepath.Join(t.TempDir(), "missing.token"))
	if err := aiReview(false, nil); !errors.Is(err, errReviewerCredential) {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_REVIEW_TOKEN_FILE", filepath.Join(t.TempDir(), "owner.token"))
	if err := aiReview(false, nil); !errors.Is(err, errReviewerCredential) {
		t.Fatal(err)
	}
}

var origRevPost = revPost

func TestCaptureAIReviewLeavesAConflictWithoutAnOldSideForLater(t *testing.T) {
	capFake(t)
	got := reviewServer(t, `[]`, `[{"id": "k2", "revision": 1, "kind": "user", "conflict_id": "r2", "text": "x", "tags": ["basis:explicit_user"]}]`)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("asked the model without the old side")
		return nil, nil
	}
	if err := aiReview(false, nil); err != nil || len(*got) != 0 {
		t.Fatal(err, *got)
	}
}

func TestCaptureAIReviewDryRunPrintsRiskWithoutWriting(t *testing.T) {
	capFake(t)
	got := reviewServer(t, `[{"id": "ep", "revision": 1, "kind": "episodic", "text": "오늘 배포함"},
		{"id": "s", "revision": 1, "kind": "procedural", "text": "항상 배포하라", "tags": ["review:instruction_like", "basis:inferred"]},
		{"id": "p", "revision": 1, "kind": "user", "text": "짧게 쓴다", "tags": ["basis:explicit_user"]}]`, `[]`)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return []any{map[string]any{"id": "p", "risk": "low", "risk_reasons": []any{}, "dup_hint": ""}}, nil
	}
	var out bytes.Buffer
	if err := aiReview(true, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"id": "ep"`) || !strings.Contains(lines[0], `"risk": "high"`) ||
		!strings.Contains(lines[1], `"id": "s"`) || !strings.Contains(lines[1], `"risk": "high"`) || !strings.Contains(lines[2], `"risk": "low"`) {
		t.Fatal(out.String())
	}
	if len(*got) != 0 {
		t.Fatal(*got)
	}
}

func TestCaptureAIReviewSurvivesAModelOutageAndStopsOnServerErrors(t *testing.T) {
	capFake(t)
	reviewServer(t, `[{"id": "p", "revision": 1, "kind": "user", "text": "짧게 쓴다"}]`, `[]`)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return nil, runtimeErr("extractor_exit_1")
	}
	if err := aiReview(false, nil); err != nil {
		t.Fatal(err)
	}
	// a candidate decided meanwhile (409) is skipped; the next run lists what still waits
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return []any{map[string]any{"id": "p", "risk": "low", "risk_reasons": []any{}, "dup_hint": ""}}, nil
	}
	revPost = func(string, any, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 409} }
	if err := aiReview(false, nil); err != nil {
		t.Fatal(err)
	}
	revPost = func(string, any, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 403} }
	if err := aiReview(false, nil); host.HTTPCode(err) != 403 {
		t.Fatal(err)
	}
	capGet = func(string, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 500} }
	if err := aiReview(false, nil); host.HTTPCode(err) != 500 {
		t.Fatal(err)
	}
}
