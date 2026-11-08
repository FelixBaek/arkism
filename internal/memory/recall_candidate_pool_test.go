package memory

import (
	"fmt"
	"testing"
	"time"
)

func TestLocalCandidatePoolDoesNotFollowOutputLimit(t *testing.T) {
	for _, limit := range []int{1, 10, 50} {
		in := SearchInput{Query: "original", retrievalQuery: "derived", Limit: limit,
			space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
		got := in.retrieval(colMemories)
		if got.Limit != localRecallCandidateLimit || got.Query != "derived" || in.Limit != limit {
			t.Fatalf("limit %d: got %d / %q; mutated %d", limit, got.Limit, got.Query, in.Limit)
		}
		// Frozen, historical, relationship and non-memory searches retain
		// their original retrieval contracts even when the space opts in.
		for _, context := range []string{"history", "related", "frozen", "other", "off"} {
			q, col := in, colMemories
			switch context {
			case "history":
				q.IncludeInactive = true
			case "related":
				q.RelatedTo = &Ref{Collection: colMemories, ID: "anchor", Revision: 1}
			case "frozen":
				q.contextView = &ContextInvocationView{}
			case "other":
				col = colSkillVersions
			case "off":
				q.space = nil
			}
			legacy := q
			legacy.space = nil
			if q.retrieval(col).Limit != legacy.retrieval(col).Limit {
				t.Fatalf("changed %s limit %d", context, limit)
			}
		}
	}
}

func TestLocalCandidatePoolKeepsLateUsefulFactBeforeRecencySelection(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	old := now.Add(-3650 * 24 * time.Hour)
	var lane []Hit
	for i := 0; i < 5; i++ {
		lane = append(lane, Hit{ID: fmt.Sprint(i), Collection: colMemories,
			Kind: "episodic", UpdatedAt: old, Reasons: []string{reasonVector}})
	}
	lane[4].UpdatedAt = now
	for _, enabled := range []bool{false, true} {
		in := SearchInput{Scope: scopeA, Limit: 1, QueryTime: &now,
			space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: enabled}}}
		got, err := f.svc.fuseRecallHits(testCtx, f.agent, [][]Hit{lane}, colMemories, in, Decision{}, &recallTrace{})
		if err != nil || len(got) != 1 {
			t.Fatalf("enabled=%v: %v, %v", enabled, got, err)
		}
		want := "0"
		if enabled {
			want = "4"
		}
		if got[0].ID != want {
			t.Fatalf("enabled=%v: selected %s, want %s", enabled, got[0].ID, want)
		}
	}
}

func TestLocalCandidatePoolDoesNotPromoteUnscoredRerankerTail(t *testing.T) {
	f := newFixture(t)
	f.svc.Reranker = NewLocalReranker()
	var lane []Hit
	for i := 0; i < 50; i++ {
		lane = append(lane, Hit{ID: fmt.Sprint(i), Collection: colMemories,
			Text: "An unrelated documented fact.", Reasons: []string{reasonVector}})
	}
	in := SearchInput{Scope: scopeA, Query: "copper pump gasket", Limit: 10,
		space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
	trace := &recallTrace{rerank: map[string]float64{}}
	got, err := f.svc.fuseRecallHits(testCtx, f.agent, [][]Hit{lane}, colMemories, in, Decision{}, trace)
	if err != nil || !trace.reranked || len(got) != 10 {
		t.Fatalf("rerank did not run: %v / %v / %+v", got, err, trace)
	}
	for i, row := range got {
		if row.ID != fmt.Sprint(i) {
			t.Fatalf("unevaluated tail displaced evaluated head: slot %d = %s", i, row.ID)
		}
	}
}

func TestLocalCandidatePoolRespectsOtherEvaluationBudgets(t *testing.T) {
	f := newFixture(t)
	for _, in := range []SearchInput{
		{Rerank: rerankModel},
		{RerankOptions: &RerankOptions{Prune: true}},
		{RerankOptions: &RerankOptions{Dedup: true}},
	} {
		if !f.svc.partialRecallEvaluationOn(in) {
			t.Fatalf("partial evaluation budget not preserved: %+v", in)
		}
	}
	f.svc.Reranker = NewLocalReranker()
	if f.svc.partialRecallEvaluationOn(SearchInput{Rerank: rerankOff}) {
		t.Fatal("disabled reranker blocked the independent candidate pool")
	}
}
