package crew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func convertStore() map[string]any {
	rec := func(id string, c map[string]any) any { return map[string]any{"id": id, "revision": 1, "crew": c} }
	return map[string]any{"items": []any{
		rec("a1", map[string]any{"type": "agent", "name": "리뷰어", "handle": "리뷰어", "role": "코드 리뷰", "category": "개발"}),
		rec("t1", map[string]any{"type": "team", "name": "리뷰팀", "handle": "리뷰팀"}),
		rec("a2", map[string]any{"type": "agent", "name": "이름 없음"}),                                   // a part only: no wrapper
		rec("a3", map[string]any{"type": "agent", "name": "배포", "handle": "배포", "workflow": "$배포흐름"}), // runs as its workflow
		rec("w1", map[string]any{"type": "workflow", "name": "배포흐름", "handle": "배포흐름"}),
		rec("a4", map[string]any{"type": "agent", "name": "요약", "handle": "요약"}),
		rec("w2", map[string]any{"type": "workflow", "name": "요약", "handle": "요약"}), // clash
		rec("a5", map[string]any{"type": "agent", "name": "번역", "handle": "번역"}),
		rec("w3", map[string]any{"type": "workflow", "name": "번역", "handle": "번역", "graph": map[string]any{"nodes": []any{
			map[string]any{"id": "work", "type": "expert", "config": map[string]any{"expert": "a5"}}}}}), // converted before
		rec("p1", map[string]any{"type": "persona", "name": "도우미"}),
	}}
}

func TestConvertPlanWrapsNamedAgentsAndTeamsAndReportsTheRest(t *testing.T) {
	plan := planConvert(list(convertStore()["items"]))
	got := []string{}
	for _, ch := range plan {
		got = append(got, pyStr(ch["id"])+" "+pyStr(ch["action"])+" "+pyStr(getOr(ch, "reason", "")))
	}
	eq(t, got, []string{"a1 wrap ", "t1 wrap ", "a3 skip works_by_workflow", "a4 skip rename", "a5 skip already"})
	agent, team := obj(plan[0]["crew"]), obj(plan[1]["crew"])
	eq(t, []any{agent["type"], agent["name"], agent["handle"], agent["role"], agent["category"]}, []any{"workflow", "리뷰어", "리뷰어", "코드 리뷰", "개발"})
	// The same one step the server runs an agent or team as (memory flowGraphFor).
	work := obj(list(obj(agent["graph"])["nodes"])[1])
	eq(t, []any{work["type"], work["config"]}, []any{"expert", map[string]any{"expert": "a1", "access": "read", "goal": "{{input}}"}})
	work = obj(list(obj(team["graph"])["nodes"])[1])
	eq(t, []any{work["type"], work["config"]}, []any{"team", map[string]any{"team": "t1", "access": "read", "topic": "{{input}}"}})
	if _, ok := team["role"]; ok {
		t.Fatal("empty fields copied", team)
	}
}

func TestConvertWrapsAnAgentWhoseLinkedWorkflowIsArchived(t *testing.T) {
	// The list holds live records only; an archived workflow leaves the agent's link dangling.
	items := []any{map[string]any{"id": "a1", "crew": map[string]any{"type": "agent", "name": "배포", "handle": "배포", "workflow": "$없어진흐름"}}}
	plan := planConvert(items)
	if len(plan) != 1 || plan[0]["action"] != "wrap" || obj(plan[0]["crew"])["handle"] != "배포" {
		t.Fatal(plan)
	}
}

func TestConvertDryRunWritesNothing(t *testing.T) {
	patch(t, &apiGet, func(string, time.Duration) (map[string]any, error) { return convertStore(), nil })
	patch(t, &apiPost, func(path string, _ any, _ time.Duration) (map[string]any, error) {
		t.Fatal("dry run wrote", path)
		return nil, nil
	})
	out, err := runConvert(false, filepath.Join(t.TempDir(), "never.json"))
	if err != nil || out["apply"] != false || out["backup"] != nil || len(out["changes"].([]map[string]any)) != 5 {
		t.Fatal(out, err)
	}
}

func TestConvertApplyBacksUpFirstThenCreatesOnlyTheWrappers(t *testing.T) {
	backup := filepath.Join(t.TempDir(), "b", "crew.json")
	patch(t, &apiGet, func(string, time.Duration) (map[string]any, error) { return convertStore(), nil })
	posts := []post{}
	patch(t, &apiPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		if _, err := os.Stat(backup); err != nil {
			t.Fatal("wrote before the backup", err)
		}
		posts = append(posts, post{path, body})
		return map[string]any{"id": "new-" + string(rune('0'+len(posts)))}, nil
	})
	out, err := runConvert(true, backup)
	if err != nil || out["backup"] != backup {
		t.Fatal(out, err)
	}
	raw, err := os.ReadFile(backup)
	if err != nil || !strings.Contains(string(raw), `"배포흐름"`) || !strings.Contains(string(raw), `"도우미"`) {
		t.Fatal("backup is not the whole store", err)
	}
	if fi, _ := os.Stat(backup); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	if len(posts) != 2 || posts[0].path != "/v1/crew" || obj(obj(plain(t, posts[1].body))["crew"])["handle"] != "리뷰팀" {
		t.Fatal(posts)
	}
	eq(t, out["changes"].([]map[string]any)[0]["created"], "new-1")
}

func TestLegacyAtCallUsesThePartOfAConvertedWorkflow(t *testing.T) {
	patch(t, &apiPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		return map[string]any{"id": "meet-1", "crew_meeting": map[string]any{"status": "concluded"}}, nil
	})
	view, err := callAt("@리뷰팀", "봐 줘", 0, "", func(string) (map[string]any, error) {
		res := atFixture("workflow", "리뷰팀", nil)
		res["part"] = atFixture("team", "리뷰팀", nil)["item"]
		return res, nil
	})
	if err != nil || view["run_kind"] != "meeting" {
		t.Fatal(view, err)
	}
}
