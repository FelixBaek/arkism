package memory

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestRecallCandidateWidthSettings(t *testing.T) {
	f := newFixture(t)
	for _, size := range []int{50, 100, 0, -1, 1, 49, 51, 99, 101} {
		cfg, err := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{
			ExpectedRevision: cfg.Revision, RecallQuality: &SpaceRecallQuality{CandidatePool: size},
		}, false)
		valid := size == 0 || size == 50 || size == 100
		if (err == nil) != valid {
			t.Fatalf("size=%d: %v", size, err)
		}
		if valid && out.Config.recallQuality().CandidatePool != size {
			t.Fatalf("size=%d did not survive settings update: %+v", size, out.Config.RecallQuality)
		}
	}
}

func TestRecallCandidateWidthIndependentOfExpansion(t *testing.T) {
	for _, size := range []int{50, 100} {
		for _, limit := range []int{1, 10, 50} {
			in := SearchInput{Query: "original", retrievalQuery: "derived", Limit: limit,
				space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{CandidatePool: size}}}
			got := in.retrieval(colMemories)
			if got.Limit != size || got.Query != "derived" || in.Limit != limit || localExpandOn(in, colMemories) {
				t.Fatalf("size=%d limit=%d: independent width changed query or expansion", size, limit)
			}
			for _, kind := range []string{"history", "related", "frozen", "skills"} {
				q, col := in, colMemories
				switch kind {
				case "history":
					q.IncludeInactive = true
				case "related":
					q.RelatedTo = &Ref{Collection: colMemories, ID: "anchor", Revision: 1}
				case "frozen":
					q.contextView = &ContextInvocationView{}
				case "skills":
					col = colSkillVersions
				}
				legacy := q
				legacy.space = nil
				if q.retrieval(col).Limit != legacy.retrieval(col).Limit {
					t.Fatalf("size=%d changed excluded %s search", size, kind)
				}
			}
		}
	}
}

func TestRecallCandidateWidthRecoversFactOutsideFifty(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	old := now.Add(-3650 * 24 * time.Hour)
	lane := make([]Hit, 100)
	for i := range lane {
		lane[i] = Hit{ID: fmt.Sprint(i), Collection: colMemories, Kind: "episodic", UpdatedAt: old, Reasons: []string{reasonVector}}
	}
	// The current observation is initially 60th. Only the wider pool lets
	// the existing recency policy consider it; no new scoring is introduced.
	lane[59].UpdatedAt = now
	for _, size := range []int{0, 50, 100} {
		in := SearchInput{Scope: scopeA, Limit: 1, QueryTime: &now,
			space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{CandidatePool: size}}}
		got, err := f.svc.fuseRecallHits(testCtx, f.agent, [][]Hit{lane}, colMemories, in, Decision{}, &recallTrace{})
		want := "0"
		if size == 100 {
			want = "59"
		}
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Fatalf("size=%d: wanted one result %s, got %v / %v", size, want, got, err)
		}
	}
}

type candidateWidthRepo struct {
	Repository
	limits map[string]int
}

func (r *candidateWidthRepo) SearchCollection(_ context.Context, col string, in SearchInput, _ []float64, _ string) ([][]Hit, error) {
	r.limits[col] = in.Limit
	lane := make([]Hit, in.Limit)
	for i := range lane {
		lane[i] = Hit{ID: fmt.Sprint(i), Collection: col, Reasons: []string{reasonVector}}
	}
	return [][]Hit{lane}, nil
}

func TestRecallCandidateWidthSourcePoolLeavesProvisionalBounded(t *testing.T) {
	r := &candidateWidthRepo{limits: map[string]int{}}
	s := Service{Repo: r}
	for _, size := range []int{0, 50, 100} {
		in := SearchInput{Scope: scopeA, Query: "original", Limit: 10,
			space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{CandidatePool: size}}}
		for _, col := range []string{"sources", "memory_candidates"} {
			rows, err := s.sideLane(testCtx, col, in, SearchCriteria{})
			want := provisionalLaneLimit
			if col == "sources" && size > 0 {
				want = size
			}
			if err != nil || len(rows) != want || r.limits[col] != want {
				t.Fatalf("size=%d collection=%s: count=%d limit=%d err=%v", size, col, len(rows), r.limits[col], err)
			}
		}
	}
}

func TestRecallCandidateWidthPreservesPartialRerankOrder(t *testing.T) {
	f := newFixture(t)
	f.svc.Reranker = NewLocalReranker()
	lane := make([]Hit, 100)
	for i := range lane {
		lane[i] = Hit{ID: fmt.Sprint(i), Collection: colMemories, Text: "An unrelated documented fact.", Reasons: []string{reasonVector}}
	}
	var legacy []string
	for _, size := range []int{0, 50, 100} {
		in := SearchInput{Scope: scopeA, Query: "copper pump gasket", Limit: 10,
			space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{CandidatePool: size}}}
		trace := &recallTrace{rerank: map[string]float64{}}
		rows, err := f.svc.fuseRecallHits(testCtx, f.agent, [][]Hit{lane}, colMemories, in, Decision{}, trace)
		if err != nil || !trace.reranked || len(rows) != 10 {
			t.Fatalf("size=%d: partial rerank failed: %v", size, err)
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		if size == 0 {
			legacy = ids
		} else if !reflect.DeepEqual(ids, legacy) {
			t.Fatalf("size=%d unscored tail displaced head: %v vs %v", size, ids, legacy)
		}
	}
}
