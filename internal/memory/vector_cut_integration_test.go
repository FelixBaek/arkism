//go:build integration

package memory

import (
	"math"
	"testing"
	"time"
)

func TestIntegrationVectorCutDropsWeakNeighbours(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	unit := func(sim float64) []float64 { // cosine similarity sim to the query axis
		v := make([]float64, VectorDimension)
		v[0], v[1] = sim, math.Sqrt(1-sim*sim)
		return v
	}
	for id, sim := range map[string]float64{"near": 0.80, "close": 0.76, "third": 0.72, "drift": 0.70} {
		r := Record{ID: id, Scope: scopeA, Kind: "decision", Status: "active", Revision: 1, Text: "memory " + id, CreatedAt: now, UpdatedAt: now, EffectiveAt: &now,
			CreatedBy: "tester", CreatedByType: "human", ApprovedBy: "tester", TrustLevel: "user_verified", Trust: 1, VerificationStatus: VerificationVerified}
		r.Embedding = &Embedding{Provider: "test", Model: "test", Revision: "1", Dimension: VectorDimension, Space: "test:1:1024d", EmbeddedAt: now,
			ContentHash: ContentHash(recordContent(r)), Vector: unit(sim)}
		if err := p.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	query := unit(1)
	ids := func(max float64) []string {
		p.SetVectorMaxDistance(max)
		lanes, err := p.SearchCollection(testCtx, colMemories, SearchInput{Scope: scopeA, Limit: 8, Modes: []string{"vector"}}, query, "test:1:1024d")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range lanes[2] {
			out = append(out, h.ID)
		}
		return out
	}
	if got := ids(0); len(got) != 4 {
		t.Fatal("cut off must keep every neighbour", got)
	}
	// 0.70 < 0.93*0.80: the drifting fourth goes; the three nearest stay even though 0.72 is also below it.
	if got := ids(0.33); len(got) != 3 || got[0] != "near" || got[1] != "close" || got[2] != "third" {
		t.Fatal("relative cut", got)
	}
	// distance 0.24 > 0.22: an absolute ceiling also applies.
	if got := ids(0.22); len(got) != 1 || got[0] != "near" {
		t.Fatal("absolute cut", got)
	}
	// Nothing is near an unrelated question.
	if got := ids(0.15); len(got) != 0 {
		t.Fatal("far neighbours returned", got)
	}
}
