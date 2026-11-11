package memory

import "testing"

func TestRuleRouteNeedsMeaningMatch(t *testing.T) {
	c := []SkillCandidate{{ID: "a", Kind: "skill", Name: "k-ledger"}, {ID: "b", Kind: "skill"}}
	embedded := map[string]Record{"a": {Embedding: &Embedding{}}}
	keyword := ContextBundle{Items: []Hit{{ID: "a", laneRanks: map[string]int{"keyword": 1}}, {ID: "b", laneRanks: map[string]int{"vector": 1}}}}
	both := ContextBundle{Items: []Hit{{ID: "a", laneRanks: map[string]int{"keyword": 3, "vector": 1}}}}
	meaningOnly := ContextBundle{Items: []Hit{{ID: "a", laneRanks: map[string]int{"vector": 1}}}}
	second := ContextBundle{Items: []Hit{{ID: "a", laneRanks: map[string]int{"keyword": 1, "vector": 2}}}}
	closeOnly := ContextBundle{Items: []Hit{{ID: "a", VectorMatch: &VectorMatch{}, laneRanks: map[string]int{"vector": 1}}}}
	noVector := ContextBundle{Items: keyword.Items, Degraded: []string{"memory_vector_unavailable"}}
	for _, tc := range []struct {
		name   string
		query  string
		bundle ContextBundle
		modes  []string
		recs   map[string]Record
		want   string
	}{
		{"keyword-only top hit is offered, not applied", "", keyword, nil, embedded, ""},
		{"meaning-only top hit is offered, not applied", "", meaningOnly, nil, embedded, ""},
		{"meaning lane's second choice is offered, not applied", "", second, nil, embedded, ""},
		{"both lanes name it", "", both, nil, embedded, "a"},
		{"request names the skill and the meaning match is close", "ledger에서 찾아줘", closeOnly, nil, embedded, "a"},
		{"a name on a merely nearest match is offered, not applied", "ledger에서 찾아줘", meaningOnly, nil, embedded, ""},
		{"a close match the request does not name is offered, not applied", "find it", closeOnly, nil, embedded, ""},
		{"meaning lane down keeps the keyword pick", "", noVector, nil, embedded, "a"},
		{"meaning lane not asked keeps the keyword pick", "", keyword, []string{"exact", "fts"}, embedded, "a"},
		{"skill not embedded yet keeps the keyword pick", "", keyword, nil, nil, "a"},
	} {
		out := ruleRoute(c, "jev_unavailable", ruleRouteConfident(c, tc.recs, tc.bundle, tc.modes, tc.query))
		if out.SelectedID != tc.want || len(out.Judgments) != 2 {
			t.Fatalf("%s: selected %q judgments %d", tc.name, out.SelectedID, len(out.Judgments))
		}
	}
	far := ContextBundle{Items: []Hit{{ID: "a", VectorMatch: &VectorMatch{Distance: 0.45}, laneRanks: map[string]int{"vector": 1}}, {ID: "b", laneRanks: map[string]int{"keyword": 2}}}}
	if got := ruleOffer(c, far, false); len(got) != 1 || got[0].ID != "b" {
		t.Fatal("a far meaning-only skill must not be offered", got)
	}
	if len(ruleOffer(c, far, true)) != 2 || len(ruleOffer(c, closeOnly, false)) != 2 || len(ruleOffer(c[:1], meaningOnly, false)) != 0 {
		t.Fatal("the confident pick and a close match stay; a merely nearest one goes")
	}
	if _, criteria, ids := jevQuestions(c); len(criteria) != 3 || ids[2] != noneOfThem {
		t.Fatal("the judge must always be able to refuse every candidate", ids)
	}
	if ruleRouteConfident(nil, embedded, both, nil, "") {
		t.Fatal("no candidates cannot be confident")
	}
}

func TestFusionKeepsLaneRanksAndRouteOrdersByMeaning(t *testing.T) {
	hit := func(id, reason string) Hit {
		return Hit{ID: id, Origin: "memory", Collection: colSkillVersions, Reasons: []string{reason}}
	}
	fused := fuseWeighted([][]Hit{{hit("a", reasonTextSearch), hit("b", reasonTextSearch)}, {hit("c", reasonVector), hit("b", reasonVector)}}, 3, nil)
	ranks := map[string]map[string]int{}
	for _, h := range fused {
		ranks[h.ID] = h.laneRanks
	}
	if ranks["b"]["keyword"] != 2 || ranks["b"]["vector"] != 2 || ranks["c"]["vector"] != 1 || len(ranks["a"]) != 1 {
		t.Fatal(ranks)
	}
	if meaningRank(Hit{}) <= meaningRank(Hit{laneRanks: map[string]int{"vector": 8}}) {
		t.Fatal("a hit the meaning lane missed must come after one it found")
	}
}
