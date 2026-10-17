package host

import (
	"encoding/json"
	"strings"
	"testing"
)

func hit(id, kind string, score any, text string) map[string]any {
	m := map[string]any{"id": id, "kind": kind, "text": text}
	if score != nil {
		m["score"] = score
	}
	return m
}

func ids(items []any) string {
	var out []string
	for _, i := range items {
		out = append(out, i.(map[string]any)["id"].(string))
	}
	return strings.Join(out, ",")
}

// ST-815: score floor, per-kind count and char caps, in rank order.
func TestRecallBudgetApply(t *testing.T) {
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	t.Setenv("ARKISM_RECALL_BUDGET", "")
	b := LoadRecallBudget() // malformed env keeps the defaults
	if b.MinScore != defaultRecallMinScore || b.Kinds["procedural"] != (KindBudget{3, 1500}) || b.Kinds["user"] != (KindBudget{2, 1500}) {
		t.Fatal(b)
	}
	items := []any{
		hit("p1", "procedural", json.Number("0.065574"), "a"),
		hit("low", "procedural", json.Number("0.016393"), "b"), // below the floor
		hit("p2", "procedural", 0.048, "c"),
		hit("p3", "procedural", 0.04, "d"),
		hit("p4", "procedural", 0.035, "e"),                // fourth procedural: over count
		hit("u1", "user", 0.04, strings.Repeat("가", 2000)), // the kind's best hit goes in however long
		hit("u2", "user", 0.04, "짧음"),                      // then the char cap is full
		hit("old", "lesson", nil, "x"),                     // no score from an older server: not judged
		hit("k", "", 0.04, "y"),                            // unlisted kind: floor only
	}
	kept, low, over := b.Apply(items)
	if ids(kept) != "p1,p2,p3,u1,old,k" || low != 1 || over != 2 {
		t.Fatal(ids(kept), low, over)
	}
	for _, bad := range []string{"+Inf", "NaN", "-1"} {
		t.Setenv("ARKISM_RECALL_MIN_SCORE", bad)
		if got := LoadRecallBudget().MinScore; got != defaultRecallMinScore {
			t.Fatal(bad, got)
		}
	}
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0")
	t.Setenv("ARKISM_RECALL_BUDGET", "procedural=1:10, bad, user=x:1")
	b = LoadRecallBudget()
	if b.MinScore != 0 || b.Kinds["procedural"] != (KindBudget{1, 10}) || b.Kinds["user"] != (KindBudget{2, 1500}) {
		t.Fatal(b)
	}
	if kept, low, _ := b.Apply(items[:2]); ids(kept) != "p1" || low != 0 {
		t.Fatal(ids(kept), low)
	}
}
