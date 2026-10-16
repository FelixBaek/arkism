package main

import (
	"encoding/json"
	"os"
	"testing"
)

func run(t *testing.T, b map[string]any, id string, rev int) (bool, bool) {
	t.Helper()
	raw, _ := os.ReadFile("testdata/rules.json") // an example table; the real one is deployment config
	var table Table
	if json.Unmarshal(raw, &table) != nil || validate(table) != nil {
		t.Fatal("testdata/rules.json invalid")
	}
	events := []any{map[string]any{"payload": map[string]any{"retrieved": []any{map[string]any{"collection": "memories", "id": id, "revision": rev}}}}}
	if b != nil {
		b["behavior"] = "agent_session"
		events = append(events, map[string]any{"payload": b})
	}
	in, _ := json.Marshal(map[string]any{"input": map[string]any{"dimension": "personal_compliance",
		"target": map[string]any{"collection": "memories", "id": id, "revision": rev}, "events": events}, "skill": ""})
	observed, passed, err := check(table, in)
	if err != nil {
		t.Fatal(err)
	}
	return observed, passed
}

const (
	prRule, commitRule, koRule, lenRule, tailRule = "rule-pr-text", "rule-commit-text",
		"rule-language", "rule-max-length", "rule-report-tail"
)

func replyOf(text string, lines int, detail bool) map[string]any {
	return map[string]any{"text": text, "lines": lines, "detail_requested": detail}
}

func TestRulesPassFailAndNotApplicable(t *testing.T) {
	type want struct{ observed, passed bool }
	ko := "판정기를 만들었고 테스트도 모두 통과했어요. 운영 반영은 승인 뒤에 합니다."
	cases := []struct {
		name string
		id   string
		rev  int
		b    map[string]any
		want want
	}{
		{"pr clean", prRule, 4, map[string]any{"pull_requests": []any{"gh pr create --body 'ST-1 fix'"}}, want{true, true}},
		{"pr violation, case and spacing changed", prRule, 4, map[string]any{"pull_requests": []any{"gh pr create --body '🤖 generated  WITH\nclaude code'"}}, want{true, false}},
		{"no pr in window", prRule, 4, map[string]any{"commits": []any{"git commit -m 'Generated with Claude Code'"}}, want{false, false}},
		{"commit violation", commitRule, 4, map[string]any{"commits": []any{"git commit -m 'x\n\nGenerated with Claude Code'"}}, want{true, false}},
		{"korean", koRule, 2, map[string]any{"replies": []any{replyOf(ko+" `internal/host/capture/session_outcome.go` https://example.com/very/long/english/path", 1, false)}}, want{true, true}},
		{"english report", koRule, 2, map[string]any{"replies": []any{replyOf(ko, 1, false), replyOf("Key: the screen work is all done; the summary box is in Korean too.", 1, false)}}, want{true, false}},
		{"translation with a Korean line", koRule, 2, map[string]any{"replies": []any{replyOf("번역했어요.\n\nThe checker judges only machine-checkable rules against the trace and never asks a model.", 3, false)}}, want{true, true}},
		{"too short to judge", koRule, 2, map[string]any{"replies": []any{replyOf("OK done", 1, false)}}, want{false, false}},
		{"short reply", lenRule, 2, map[string]any{"replies": []any{replyOf(ko, 25, false)}}, want{true, true}},
		{"long reply", lenRule, 2, map[string]any{"replies": []any{replyOf(ko, 26, false)}}, want{true, false}},
		{"detail asked", lenRule, 2, map[string]any{"replies": []any{replyOf(ko, 80, true)}}, want{false, false}},
		{"tail present", tailRule, 3, map[string]any{"replies": []any{replyOf(ko+"\n\n**핵심: 끝났어요.**", 6, false)}}, want{true, true}},
		{"one-line summary label", tailRule, 3, map[string]any{"replies": []any{replyOf(ko+"\n\n한 줄 요약: 끝났어요.", 6, false)}}, want{true, true}},
		{"tail missing", tailRule, 3, map[string]any{"replies": []any{replyOf(ko+"\n\n다음: 승인해 주세요.", 6, false)}}, want{true, false}},
		{"status line is not a report", tailRule, 3, map[string]any{"replies": []any{replyOf(ko, 1, false)}}, want{false, false}},
		{"revised memory", tailRule, 4, map[string]any{"replies": []any{replyOf(ko+"\n\n핵심: 끝", 6, false)}}, want{false, false}},
		{"no behavior event", prRule, 4, nil, want{false, false}},
		{"no rule", "unknown", 1, map[string]any{"replies": []any{replyOf(ko, 1, false)}}, want{false, false}},
	}
	for _, c := range cases {
		if o, p := run(t, c.b, c.id, c.rev); o != c.want.observed || p != c.want.passed {
			t.Errorf("%s: got observed=%v passed=%v", c.name, o, p)
		}
	}
}
