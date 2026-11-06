package memory

import (
	"context"
	"slices"
	"sort"
	"testing"
)

func diverseRow(id string, score float64, vector ...float64) ScoredHit {
	return ScoredHit{Hit: Hit{ID: id, vector: vector}, Score: score}
}

func TestSelectDiverseSkipsNearDuplicatesWithinTheLimit(t *testing.T) {
	rows := []ScoredHit{diverseRow("a", 1, 1, 0), diverseRow("a-again", 0.95, 0.99, 0.05), diverseRow("b", 0.8, 0, 1), diverseRow("c", 0.7)}
	ids := func(rows []ScoredHit) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids(selectDiverse(slices.Clone(rows), 2)); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatal("near-duplicate kept over a distinct fact", got)
	}
	if got := ids(selectDiverse(slices.Clone(rows), 4)); len(got) != 4 {
		t.Fatal("a pool within the limit lost rows", got)
	}
	// Rows without vectors are never penalised: plain score order.
	plain := []ScoredHit{diverseRow("x", 1), diverseRow("y", 0.9), diverseRow("z", 0.8)}
	if got := ids(selectDiverse(plain, 2)); !slices.Equal(got, []string{"x", "y"}) {
		t.Fatal(got)
	}
	for _, r := range selectDiverse(slices.Clone(rows), 4) {
		if r.ID == "a-again" && r.Score != 0.95 {
			t.Fatal("scores were rewritten", r.Score)
		}
	}
}

// Recall through the service: recall_quality.diversity false keeps the plain score order; with
// the default, a distinct trip outranks a reworded copy of the first.
func TestRecallDiversityDefaultAndSpaceSwitch(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	f.svc.Repo = nearestFirstRepo{f.repo}
	for _, text := range []string{"melanie camping trip with the kids in the forest", "melanie camping trip with the kids in the forest last summer", "melanie camping trip at the beach"} {
		f.embedText(f.activeMemory(text))
	}
	query := SearchInput{Scope: scopeA, Query: "melanie camping trip with the kids in the forest", Modes: []string{"vector"}, Limit: 2}
	beach := func(items []Hit) bool {
		return slices.ContainsFunc(items, func(h Hit) bool { return h.Text == "melanie camping trip at the beach" })
	}
	// The off case runs first: a recall's own outcome weights could lift the rows it returned.
	off := false
	setRecallQuality(t, f, SpaceRecallQuality{Diversity: &off})
	if out := f.search(f.human, query); len(out.Items) != 2 || beach(out.Items) {
		t.Fatal("diversity off did not keep the plain order", out.Items)
	}
	setRecallQuality(t, f, SpaceRecallQuality{})
	if out := f.search(f.human, query); len(out.Items) != 2 || !beach(out.Items) {
		t.Fatal("default recall returned two copies of one fact", out.Items)
	}
}

// nearestFirstRepo orders the fake store's vector lane by cosine to the query, as the store does,
// so the lane limit decides which rows recall sees.
type nearestFirstRepo struct{ *fakeRepo }

func (r nearestFirstRepo) SearchCollection(_ context.Context, col string, in SearchInput, v []float64, space string) ([][]Hit, error) {
	lists, err := r.search(in.Scope, in.Query, v, space, 50, col, in.SearchCriteria, in.Modes...)
	if err == nil {
		nearest := lists[2]
		sort.SliceStable(nearest, func(i, j int) bool {
			return cosine(r.embedding(nearest[i]), v) > cosine(r.embedding(nearest[j]), v)
		})
		lists[2] = clip(nearest, in.Limit)
	}
	return lists, err
}

func (r nearestFirstRepo) embedding(h Hit) []float64 {
	return r.store[h.Collection][recKey(h.Scope, h.ID)].Embedding.Vector
}
