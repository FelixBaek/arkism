package host

import (
	"encoding/json"
	"math"
	"testing"
)

func TestRecallSemanticGatePreservesVerifiedVectorMatches(t *testing.T) {
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	t.Setenv("ARKISM_RECALL_BUDGET", "")
	for _, tc := range []struct {
		name  string
		match any
		keep  bool
	}{
		{"near", map[string]any{"space": "test-space", "distance": 0.2, "max_distance": 0.38}, true},
		{"boundary", map[string]any{"space": "test-space", "distance": json.Number("0.38"), "max_distance": json.Number("0.38")}, true},
		{"zero-distance", map[string]any{"space": "test-space", "distance": 0.0, "max_distance": 0.38}, true},
		{"far", map[string]any{"space": "test-space", "distance": 0.5, "max_distance": 0.38}, false},
		{"older-server", nil, false},
		{"disabled-gate", map[string]any{"space": "test-space", "distance": 0.1, "max_distance": 0.0}, false},
		{"missing-distance", map[string]any{"space": "test-space", "max_distance": 0.38}, false},
		{"missing-space", map[string]any{"distance": 0.1, "max_distance": 0.38}, false},
		{"negative-distance", map[string]any{"space": "test-space", "distance": -0.1, "max_distance": 0.38}, false},
		{"nan-distance", map[string]any{"space": "test-space", "distance": math.NaN(), "max_distance": 0.38}, false},
		{"infinite-cut", map[string]any{"space": "test-space", "distance": 0.1, "max_distance": math.Inf(1)}, false},
		{"unbounded-cut", map[string]any{"space": "test-space", "distance": 0.1, "max_distance": 2.0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := hitLanes("semantic", "lesson", 1.0/61.0, "vector")
			i["vector_match"] = tc.match
			kept, dropped := LoadRecallBudget().Floor([]any{i}, true)
			if (len(kept) == 1) != tc.keep || dropped != 1-len(kept) {
				t.Fatalf("semantic eligibility: kept=%d dropped=%d wantKeep=%v", len(kept), dropped, tc.keep)
			}
		})
	}
}

func TestRecallSemanticMetadataCannotBypassOtherContracts(t *testing.T) {
	b := RecallBudget{MinScore: .025, Kinds: map[string]KindBudget{"lesson": {Count: 1, Chars: 100}}}
	match := map[string]any{"space": "test-space", "distance": .2, "max_distance": .38}
	items := []any{}
	for _, id := range []string{"first", "second"} {
		i := hitLanes(id, "lesson", 1.0/61.0, "vector")
		i["vector_match"] = match
		items = append(items, i)
	}
	if kept, score, budget := b.Apply(items, true); ids(kept) != "first" || score != 0 || budget != 1 {
		t.Fatalf("semantic results bypassed kind budget: kept=%v score=%d budget=%d", ids(kept), score, budget)
	}
	nonVector := hitLanes("relation", "lesson", 1.0/61.0, "relation")
	nonVector["vector_match"] = match
	if kept, _ := b.Floor([]any{nonVector}, true); len(kept) != 0 {
		t.Fatal("non-vector annotation bypassed floor")
	}
	for _, lane := range []string{"exact", "keyword"} {
		if kept, _ := b.Floor([]any{hitLanes(lane, "lesson", .001, lane)}, true); len(kept) != 1 {
			t.Fatal("text-lane compatibility changed", lane)
		}
	}
	if kept, _ := b.Floor([]any{nonVector}, false); len(kept) != 1 {
		t.Fatal("degraded-search compatibility changed")
	}
}
