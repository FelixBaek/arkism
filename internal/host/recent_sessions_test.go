package host

import (
	"strings"
	"testing"
	"time"
)

func recentPage(goal string) map[string]any {
	return map[string]any{"authority": "data_only_untrusted", "items": []any{map[string]any{"id": "work", "revision": 1, "authority": "data_only_untrusted", "updated_at": time.Now().UTC().Format(time.RFC3339Nano), "package": map[string]any{"repository": "/repo", "session_id": "past", "title": "title", "goal": goal, "source_refs": []any{map[string]any{"collection": "sources", "id": "src", "revision": 1}}, "next_steps": []any{"next"}}}}}
}
func TestRecentSessionBudgetAndProvenance(t *testing.T) {
	for _, size := range []int{500, 800, 2500} {
		out, err := RecentSessionsContext(func(path string, body map[string]any) (map[string]any, error) {
			if path != "/v1/work-checkpoints/recent" || body["session_id"] != "now" {
				t.Fatal(path, body)
			}
			return recentPage(strings.Repeat("긴 목표", 3000)), nil
		}, nil, "/repo", "now", 3, 7, size)
		if err != nil || Len(out) > size || !strings.Contains(out, `"id":"src"`) || strings.Contains(out, "긴 목표") || !strings.Contains(out, "data_only_untrusted") {
			t.Fatalf("budget %d: %s %v", size, out, err)
		}
	}
}
func TestRecentSessionsRejectCurrentRepositoryAndAge(t *testing.T) {
	for _, field := range []string{"repository", "session_id", "updated_at", "authority"} {
		page := recentPage("goal")
		item := page["items"].([]any)[0].(map[string]any)
		pkg := item["package"].(map[string]any)
		switch field {
		case "repository":
			pkg[field] = "/other"
		case "session_id":
			pkg[field] = "now"
		case "updated_at":
			item[field] = time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
		case "authority":
			item[field] = "trusted"
		}
		out, err := RecentSessionsContext(func(string, map[string]any) (map[string]any, error) { return page, nil }, nil, "/repo", "now", 3, 7, 2500)
		if err == nil || out != "" {
			t.Fatalf("%s leaked: %s %v", field, out, err)
		}
	}
}
