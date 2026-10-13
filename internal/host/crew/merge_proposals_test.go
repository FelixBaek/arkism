package crew

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func mergePoolAnswer() map[string]any {
	ref := func(col, id string) map[string]any { return map[string]any{"collection": col, "id": id, "revision": 2} }
	return map[string]any{"id": "mpool_1", "revision": 1,
		"memories": []any{
			map[string]any{"ref": ref("memories", "mem-ko"), "kind": "user", "text": "응답은 한국어로", "updated_at": "2026-09-20T00:00:00Z"},
			map[string]any{"ref": ref("memories", "mem-test"), "kind": "procedural", "text": "PR 전에 테스트", "updated_at": "2026-09-21T00:00:00Z"}},
		"candidates": []any{
			map[string]any{"ref": ref("memory_candidates", "cand-1"), "kind": "user", "text": "응답은 한국어로,\n쉬운 말로", "similar": []any{"mem-ko"}},
			map[string]any{"ref": ref("memory_candidates", "cand-2"), "kind": "decision", "text": "커밋은 영어로", "similar": []any{"mem-test"}}}}
}

// fakeMergeServer answers the pool request and records the submission.
func fakeMergeServer(t *testing.T, pool map[string]any) *[]postCall {
	posted := &[]postCall{}
	patch(t, &mPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		*posted = append(*posted, postCall{path, host.MustDumps(body, host.DumpOpts{})})
		if path == "/v1/merge_proposals/pool" {
			return pool, nil
		}
		return map[string]any{"proposed": 1, "rejected": 0}, nil
	})
	patch(t, &mMetric, func(map[string]any) {})
	return posted
}

func TestMergeRunSendsLabelsToTheModelAndIdsToArkism(t *testing.T) {
	posted := fakeMergeServer(t, mergePoolAnswer())
	var input string
	patch(t, &mergeJudgeFn, func(in string) ([]any, host.Obj, error) {
		input = in
		return []any{
			map[string]any{"candidate": "c1", "action": "merge", "targets": []any{"m1"}, "reason_code": "more_specific", "reason": "같은 규칙에\n추가"},
			map[string]any{"candidate": "c1", "action": "store", "targets": []any{}, "reason_code": "new_information", "reason": ""},  // second answer for c1
			map[string]any{"candidate": "c2", "action": "merge", "targets": []any{"m9"}, "reason_code": "same_meaning", "reason": ""}, // not in the pool
			map[string]any{"candidate": "c7", "action": "store", "targets": []any{}, "reason_code": "new_information", "reason": ""},  // not in the pool
		}, host.Obj{{Key: "model", Value: "claude-sonnet-5-5"}, {Key: "requests", Value: 1}, {Key: "input_tokens", Value: 900}, {Key: "output_tokens", Value: 80}}, nil
	})
	kept, sent, err := mergeRun(false, io.Discard)
	if err != nil || kept != 1 || sent != 1 {
		t.Fatalf("%d %d %v", kept, sent, err)
	}
	if strings.Contains(input, "mem-ko") || strings.Contains(input, "cand-1") || !strings.Contains(input, "[c1] (user; similar: m1) 응답은 한국어로, 쉬운 말로") {
		t.Fatalf("model input:\n%s", input)
	}
	eq(t, len(*posted), 2)
	eq(t, (*posted)[0].path, "/v1/merge_proposals/pool")
	sub := (*posted)[1]
	eq(t, sub.path, "/v1/merge_proposals")
	body, _ := host.Loads(sub.body)
	b := obj(body)
	eq(t, b["pool_id"], "mpool_1")
	vs := list(b["verdicts"])
	eq(t, len(vs), 1)
	eq(t, obj(vs[0])["candidate_id"], "cand-1")
	eq(t, obj(vs[0])["target_ids"], []any{"mem-ko"})
	eq(t, obj(vs[0])["reason"], "같은 규칙에 추가")
	eq(t, obj(b["generator"])["prompt_version"], mergePromptVersion)
	if u := obj(b["usage"]); u["model"] != "claude-sonnet-5-5" || fmt.Sprint(u["input_tokens"]) != "900" || fmt.Sprint(u["output_tokens"]) != "80" {
		t.Fatalf("judge usage not sent: %v", b["usage"])
	}
}

func TestMergeRunWithAnEmptyPoolAsksNoModel(t *testing.T) {
	posted := fakeMergeServer(t, map[string]any{"candidates": []any{}, "memories": []any{}})
	patch(t, &mergeJudgeFn, func(string) ([]any, host.Obj, error) { t.Fatal("judge called"); return nil, nil, nil })
	if kept, sent, err := mergeRun(false, io.Discard); err != nil || kept+sent != 0 {
		t.Fatal(kept, sent, err)
	}
	eq(t, len(*posted), 1)
}

func TestMergeMainWaitsNearTheUsageLimit(t *testing.T) {
	posted := fakeMergeServer(t, mergePoolAnswer())
	patch(t, &mergeBudgetFn, func(string) string { return "Claude 5시간 사용량 90% ≥ 85%: 이번 실행은 건너뜀" })
	var out bytes.Buffer
	if code := mergeMain([]string{"run"}, nil, &out, io.Discard); code != 0 || !strings.Contains(out.String(), "건너뜀") {
		t.Fatal(code, out.String())
	}
	eq(t, len(*posted), 0)
}
