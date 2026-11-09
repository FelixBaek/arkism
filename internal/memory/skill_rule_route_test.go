package memory

import "testing"

func TestRuleRouteNeedsMeaningMatch(t *testing.T) {
	c := []SkillCandidate{{ID: "a", Kind: "skill"}, {ID: "b", Kind: "skill"}}
	embedded := map[string]Record{"a": {Embedding: &Embedding{}}}
	keyword := ContextBundle{Items: []Hit{{ID: "a", Lanes: []string{"keyword"}}, {ID: "b", Lanes: []string{"vector"}}}}
	both := ContextBundle{Items: []Hit{{ID: "a", Lanes: []string{"keyword", "vector"}}}}
	noVector := ContextBundle{Items: keyword.Items, Degraded: []string{"memory_vector_unavailable"}}
	for _, tc := range []struct {
		name   string
		bundle ContextBundle
		modes  []string
		recs   map[string]Record
		want   string
	}{
		{"keyword-only top hit is offered, not applied", keyword, nil, embedded, ""},
		{"meaning lane agrees", both, nil, embedded, "a"},
		{"meaning lane down keeps the keyword pick", noVector, nil, embedded, "a"},
		{"meaning lane not asked keeps the keyword pick", keyword, []string{"exact", "fts"}, embedded, "a"},
		{"skill not embedded yet keeps the keyword pick", keyword, nil, nil, "a"},
	} {
		out := ruleRoute(c, "jev_unavailable", ruleRouteConfident(c, tc.recs, tc.bundle, tc.modes))
		if out.SelectedID != tc.want || len(out.Judgments) != 2 {
			t.Fatalf("%s: selected %q judgments %d", tc.name, out.SelectedID, len(out.Judgments))
		}
	}
	if ruleRouteConfident(nil, embedded, both, nil) {
		t.Fatal("no candidates cannot be confident")
	}
}
