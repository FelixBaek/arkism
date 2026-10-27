package capture

import (
	"bytes"
	"errors"
	"os"
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
		case strings.HasPrefix(path, "/v1/memory_relations/r3?"):
			return mustLoad(t, `{"id": "r3", "revision": 1, "relations": [{"collection": "memory_candidates", "id": "k3"}, {"collection": "memories", "id": "u1"}]}`), nil
		case strings.HasPrefix(path, "/v1/memory_relations/r1?"):
			return mustLoad(t, `{"id": "r1", "revision": 4, "relations": [{"collection": "memory_candidates", "id": "k1"}, {"collection": "memories", "id": "o1"}]}`), nil
		case strings.HasPrefix(path, "/v1/memory_relations/"):
			return mustLoad(t, `{"id": "r2", "revision": 1, "relations": [{"collection": "memory_candidates", "id": "k2"}]}`), nil
		case strings.HasPrefix(path, "/v1/memories/o1?"):
			return mustLoad(t, `{"id": "o1", "revision": 2, "kind": "decision", "text": "라이트 테마를 쓴다"}`), nil
		case strings.HasPrefix(path, "/v1/memories/u1?"):
			return mustLoad(t, `{"id": "u1", "revision": 1, "kind": "user", "text": "개인 정보"}`), nil
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
		rev, _ := num(field(body, "expected_revision"))
		id := strings.Split(path, "/")[3]
		return map[string]any{"id": id, "revision": rev + 1, "status": "candidate", "classification": map[string]any{"risk": field(body, "risk")}}, nil
	}
	reviewSearch = func(text string) ([]any, error) {
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
	if err := os.RemoveAll(reviewOutboxDir()); err != nil { // the 403 write would stop this run first
		t.Fatal(err)
	}
	capGet = func(string, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 500} }
	if err := aiReview(false, nil); host.HTTPCode(err) != 500 {
		t.Fatal(err)
	}
}

func TestCaptureAIReviewKeepsUserMemoryTextFromTheModel(t *testing.T) {
	capFake(t)
	got := reviewServer(t, `[]`, `[{"id": "k3", "revision": 2, "kind": "user", "conflict_id": "r3", "text": "y", "tags": ["basis:explicit_user"]}]`)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("a user memory's text reached the model")
		return nil, nil
	}
	if err := aiReview(false, nil); err != nil || len(*got) != 1 {
		t.Fatal(err, *got)
	}
	if c := (*got)[0].(map[string]any); c["risk"] != "high" || c["dup"] != "u1" {
		t.Fatal(c)
	}
}

func TestCaptureReviewSearchAsksOnlyReviewKinds(t *testing.T) {
	capFake(t)
	var kinds any
	capPost = func(string, any, time.Duration) (map[string]any, error) {
		t.Fatal("the review searches under the reviewer's credential, not the harness's")
		return nil, nil
	}
	revPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		kinds = field(body, "kinds")
		return map[string]any{"items": []any{}}, nil
	}
	if _, err := reviewSearch("x"); err != nil || host.MustDumps(kinds, host.DumpOpts{}) != `["lesson", "decision", "procedural"]` {
		t.Fatal(err, kinds)
	}
}

// A classify write lost to the network is spooled and replayed on the next run without asking the model again.
func TestCaptureAIReviewReplaysTheOutbox(t *testing.T) {
	capFake(t)
	got := reviewServer(t, `[{"id": "p", "revision": 3, "kind": "decision", "text": "추측", "tags": ["basis:inferred"], "created_by": "owner"}]`, `[]`)
	revPost = func(string, any, time.Duration) (map[string]any, error) { return nil, errors.New("connection refused") }
	calls := 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		calls++
		return []any{map[string]any{"id": "p", "risk": "medium", "risk_reasons": []any{"추측"}, "dup_hint": ""}}, nil
	}
	if err := aiReview(false, nil); err != nil || len(*got) != 0 {
		t.Fatal(err, *got)
	}
	// the server now shows the replayed call; the model is not asked again
	got = reviewServer(t, `[{"id": "p", "revision": 4, "kind": "decision", "text": "추측", "created_by": "owner", "classification": {"risk": "medium", "by": "arkism-memory-reviewer"}}]`, `[]`)
	timeNow = func() float64 { return float64(time.Now().Add(2*time.Hour).UnixNano()) / 1e9 } // past the backoff
	if err := aiReview(false, nil); err != nil || calls != 1 || len(*got) != 1 || host.MustDumps((*got)[0].(map[string]any)["rev"], host.DumpOpts{}) != "3" {
		t.Fatal(err, calls, *got)
	}
	if left, _ := filepath.Glob(filepath.Join(reviewOutboxDir(), "batch-*.json")); len(left) != 0 {
		t.Fatal(left)
	}
}

// A 403 stops every run with no backoff: the next run replays the write and stops again.
func TestCaptureAIReviewOutboxFailsClosedOnForbidden(t *testing.T) {
	capFake(t)
	reviewServer(t, `[{"id": "p", "revision": 3, "kind": "decision", "text": "추측", "tags": ["basis:inferred"], "created_by": "owner"}]`, `[]`)
	sent := 0
	revPost = func(string, any, time.Duration) (map[string]any, error) {
		sent++
		return nil, &host.HTTPError{Code: 403}
	}
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return []any{map[string]any{"id": "p", "risk": "medium", "risk_reasons": []any{"추측"}, "dup_hint": ""}}, nil
	}
	for run := 1; run <= 2; run++ {
		if err := aiReview(false, nil); host.HTTPCode(err) != 403 || sent != run {
			t.Fatal(run, err, sent)
		}
	}
}

func TestWithJudgementKeepsAdviceOnly(t *testing.T) {
	req := withJudgement(classifyRequest(map[string]any{"id": "p", "revision": 1}, "medium", nil, ""), map[string]any{"verdict": "fix", "verdict_text": "고친 문장", "verdict_reason": "빠진 말",
		"semantic": []any{map[string]any{"check": "no_additions", "ok": false, "note": "더함"}}})
	b := req["body"].(map[string]any)
	if v := b["verdict"].(map[string]any); v["action"] != "fix" || v["text"] != "고친 문장" || len(b["semantic"].([]any)) != 1 {
		t.Fatal(b)
	}
	if b := withJudgement(classifyRequest(map[string]any{"id": "p", "revision": 1}, "low", nil, ""), map[string]any{})["body"].(map[string]any); b["verdict"] != nil || b["semantic"] != nil {
		t.Fatal(b)
	}
	// Over the server's limits: an overlong fix is dropped (never cut), notes are clipped, at most 3 checks.
	long := strings.Repeat("가", 1000)
	var many []any
	for range 5 {
		many = append(many, map[string]any{"check": "same_meaning", "ok": true, "note": long})
	}
	b = withJudgement(classifyRequest(map[string]any{"id": "p", "revision": 1}, "low", nil, ""), map[string]any{"verdict": "fix", "verdict_text": long, "verdict_reason": long, "semantic": many})["body"].(map[string]any)
	if sem := b["semantic"].([]any); b["verdict"] != nil || len(sem) != 3 || len(sem[0].(map[string]any)["note"].(string)) > 1000 {
		t.Fatal(b["verdict"], len(sem))
	}
}

func TestWithJudgementSendsRelationOnlyWithItsMemory(t *testing.T) {
	c := map[string]any{"id": "p", "revision": 1}
	for _, rel := range []string{"duplicate", "contradiction", "newer_version"} {
		if b := withJudgement(classifyRequest(c, "high", nil, "m1"), map[string]any{"relation": rel})["body"].(map[string]any); b["relation"] != rel {
			t.Fatal(rel, b)
		}
	}
	// no memory named, an empty answer, or a value the server does not know: nothing is sent
	for _, tc := range []struct{ dup, rel string }{{"", "duplicate"}, {"m1", ""}, {"m1", "merge"}} {
		if b := withJudgement(classifyRequest(c, "high", nil, tc.dup), map[string]any{"relation": tc.rel})["body"].(map[string]any); b["relation"] != nil {
			t.Fatal(tc, b)
		}
	}
}

// The session processor runs as the harness, which holds no review grant: the waiting list is read as the reviewer.
func TestCaptureAIReviewListsWaitingAsTheReviewer(t *testing.T) {
	capFake(t)
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memory_candidates") {
			t.Fatal("waiting list read with the harness token")
		}
		return map[string]any{"items": []any{}}, nil
	}
	var listed []string
	revGet = func(path string, _ time.Duration) (map[string]any, error) {
		listed = append(listed, path)
		return map[string]any{"items": []any{}}, nil
	}
	if err := aiReview(true, &bytes.Buffer{}); err != nil || len(listed) != 2 {
		t.Fatal(err, listed)
	}
}
