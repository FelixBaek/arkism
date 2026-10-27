package crew

import (
	"strings"
	"testing"
	"time"
)

func TestWorkflowMentionsSkipQuotedCodeAndEmail(t *testing.T) {
	for _, prompt := range []string{
		"인용: \"@딥리서치\" 이후 @실행",
		"'@딥리서치' 이후 @실행",
		"“@딥리서치” ‘$인용’ 「@자료」 『@문서』 @실행",
		"`@딥리서치` ``code ` @딥리서치`` @실행",
		"```go\n@딥리서치\n```\n@실행",
		"~~~text\n$딥리서치\n~~~\n@실행",
		"````text\n```\n@딥리서치\n````\n@실행",
		"> @딥리서치\n> $인용\n@실행",
		"email@example.com @README.md @src/file.go \\@딥리서치 @실행",
	} {
		got := handles(prompt, 5)
		if len(got) != 1 || got[0] != "@실행" {
			t.Fatal(prompt, got)
		}
	}
	if got := handles("don't run quoted commands: \"@딥리서치", 5); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestWorkflowAliasHookAndQuotedMentionDoNotResolveTheSame(t *testing.T) {
	lists, resolves := 0, 0
	list := func() ([]map[string]any, error) {
		lists++
		return []map[string]any{{"handle": "@딥리서치"}}, nil
	}
	resolve := func(h string) (map[string]any, error) {
		resolves++
		if h != "@딥리서치" {
			t.Fatal(h)
		}
		return atFixture("workflow", "딥리서치", nil), nil
	}
	text, err := hookContext("@딥리서치 최근 자료 찾아줘", "", time.Second, resolve, list)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[@딥리서치 — 워크플로우]", "`run_workflow`", "request_id", "run_id·run_kind", "`wait_workflow`", "`get_workflow`", "`cancel_workflow`", "자동 푸시는 지원하지 않으며"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	text, err = hookContext("다음 문구를 번역해줘: `@딥리서치 최근 자료 찾아줘`", "", time.Second, resolve, list)
	if err != nil || text != "" || lists != 1 || resolves != 1 {
		t.Fatal(text, err, lists, resolves)
	}
}
