package crew

import (
	"strings"
	"testing"
	"time"
)

func atFixture(typ, handle string, extra map[string]any) map[string]any {
	c := map[string]any{"type": typ, "handle": handle, "name": handle + " 이름", "role": "역할"}
	for k, v := range extra {
		c[k] = v
	}
	return map[string]any{"item": map[string]any{"id": "id-" + handle, "revision": 2, "crew": c}}
}

func TestAtHandlesKeepTheirSigilAndSkipMailAndFiles(t *testing.T) {
	eq(t, handles("@리뷰어 봐 줘, $리서치 도, me@x.com, @src/a.ts, @README.md, @백엔드팀에게.", 5),
		[]string{"@리뷰어", "리서치", "@백엔드팀에게"})
	eq(t, handles("검토해줘.@리뷰어 this.$refs ~/.@cache", 5), []string{"@리뷰어"})
	eq(t, matchKnown([]string{"@백엔드팀에게", "리서치", "@리서치"}, []string{"리서치", "@백엔드팀"}), []string{"@백엔드팀", "리서치"})
}

func TestAtContextNamesTheExpertAndTeamAndHowToCallThem(t *testing.T) {
	data := map[string]map[string]any{
		"@리뷰어":  atFixture("agent", "리뷰어", nil),
		"@백엔드팀": atFixture("team", "백엔드팀", nil),
		"@리서치":  atFixture("workflow", "리서치", nil), // a workflow is never an @ call
	}
	lister := func() ([]map[string]any, error) {
		return []map[string]any{{"handle": "@리뷰어"}, {"handle": "@백엔드팀"}, {"handle": "@리서치"}}, nil
	}
	text, err := hookContext("@리뷰어 랑 @백엔드팀에게 물어봐, @리서치", "", time.Second, func(h string) (map[string]any, error) { return data[h], nil }, lister)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[@리뷰어 — 전문가] 리뷰어 이름: 역할", "[@백엔드팀 — 팀] 백엔드팀 이름: 역할",
		"`run_workflow`(handle \"@리뷰어\", task)로 맡겨요", "팀 회의를 열어 결론"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q not in %s", want, text)
		}
	}
	if strings.Contains(text, "[@리서치") {
		t.Fatal(text)
	}
}

func TestAtExpertAsksTheRunnerAndWaitsForItsAnswer(t *testing.T) {
	posts := []post{}
	patch(t, &apiPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		posts = append(posts, post{path, body})
		return map[string]any{"id": "assist-1", "crew_assist": map[string]any{"status": "pending"}}, nil
	})
	gets := []string{}
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		gets = append(gets, path)
		return map[string]any{"id": "assist-1", "crew_assist": map[string]any{"status": "done", "output": "검토 끝"}}, nil
	})
	patch(t, &runSleep, func(float64) {})
	view, err := startWorkflow("@리뷰어", "PR 봐 줘", 30, "/w", func(h string) (map[string]any, error) {
		if h != "@리뷰어" {
			t.Fatal(h)
		}
		return atFixture("agent", "리뷰어", nil), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, view, map[string]any{"run_id": "assist-1", "status": "succeeded", "summary": "검토 끝", "waiting": nil})
	a := obj(plain(t, posts[0].body).(map[string]any)["assist"])
	if posts[0].path != "/v1/crew/assists" || a["kind"] != "try" || a["expert"] != "@리뷰어" || a["prompt"] != "PR 봐 줘\n\n작업 폴더: /w" {
		t.Fatal(posts)
	}
	if !strings.HasPrefix(gets[0], "/v1/crew/assists/assist-1?") {
		t.Fatal(gets)
	}
}

func TestAtTeamHoldsAMeetingAndReportsItsConclusion(t *testing.T) {
	posts := []post{}
	patch(t, &apiPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		posts = append(posts, post{path, body})
		return map[string]any{"id": "meet-1", "crew_meeting": map[string]any{"status": "pending"}}, nil
	})
	patch(t, &apiGet, func(string, time.Duration) (map[string]any, error) {
		return map[string]any{"id": "meet-1", "crew_meeting": map[string]any{"status": "concluded", "conclusion": map[string]any{
			"summary": "A로 가요", "decisions": []any{"A 채택"}, "todos": []any{"문서 고치기"}, "open_questions": []any{}}}}, nil
	})
	patch(t, &runSleep, func(float64) {})
	view, err := startWorkflow("@백엔드팀", "캐시를 어떻게 할까\n자세히", 30, "", func(string) (map[string]any, error) {
		return atFixture("team", "백엔드팀", nil), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, view, map[string]any{"run_id": "meet-1", "status": "succeeded", "summary": "A로 가요\n결정: A 채택\n할 일: 문서 고치기", "waiting": nil})
	m := obj(plain(t, posts[0].body).(map[string]any)["meeting"])
	eq(t, []any{posts[0].path, m["team_id"], m["title"], m["trigger"]}, []any{"/v1/crew/meetings", "id-백엔드팀", "캐시를 어떻게 할까", "call"})
	_, err = startWorkflow("@없음", "x", 0, "", func(string) (map[string]any, error) { return nil, nil })
	mustCrewErr(t, err, "unknown")
	_, err = startWorkflow("@리서치", "x", 0, "", func(string) (map[string]any, error) { return atFixture("workflow", "리서치", nil), nil })
	mustCrewErr(t, err, "workflow by @")
}
