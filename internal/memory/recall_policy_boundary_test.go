package memory

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// ST-815 / TC-FUNC-037: retain the legacy fallback for servers without verified
// vector evidence at the fusion/JSON/host boundary. This is not a relevance-quality acceptance test:
// the self-query baseline contains no labeled vector-only paraphrases, and
// cannot establish that dropping every semantic-only match is desirable.
func TestRecallLegacyPolicyBoundaryVectorOnlyRequiresExplicitFloorOverride(t *testing.T) {
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	t.Setenv("ARKISM_RECALL_BUDGET", "")
	lane := make([]Hit, 50)
	for i := range lane {
		lane[i] = Hit{ID: fmt.Sprintf("semantic-%02d", i), Origin: "memory", Collection: colMemories, Reasons: []string{reasonVector}}
	}
	scored := FuseScored([][]Hit{nil, nil, lane}, len(lane))
	hits := make([]Hit, len(scored))
	for i, h := range scored {
		h.Hit.Score, h.Hit.Lanes = h.Score, hitLanes(h.Reasons)
		hits[i] = h.Hit
	}
	raw, err := json.Marshal(hits)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	b := host.LoadRecallBudget()
	if kept, dropped := b.Floor(decoded, true); len(kept) != 0 || dropped != len(hits) {
		t.Fatalf("legacy fallback changed: kept=%d dropped=%d", len(kept), dropped)
	}
	t.Logf("legacy floor %.6f exceeds best vector-only RRF %.6f; all %d ranks excluded without server evidence", b.MinScore, hits[0].Score, len(hits))
	// The existing explicit opt-out remains usable without changing the defaults
	// or treating this synthetic fixture as a person's quality-policy approval.
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0")
	if kept, dropped := host.LoadRecallBudget().Floor(decoded, true); len(kept) != len(hits) || dropped != 0 {
		t.Fatalf("explicit override did not restore semantic results: kept=%d dropped=%d", len(kept), dropped)
	}
}
