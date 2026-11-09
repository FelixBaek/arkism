//go:build linux

package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
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
	// A row that only shares the topic (similarity 0.8, below the near-duplicate band) keeps its
	// place ahead of a less similar but lower scored row.
	topic := []ScoredHit{diverseRow("a", 1, 1, 0), diverseRow("same-topic", 0.9, 0.8, 0.6), diverseRow("far", 0.85, 0, 1)}
	if got := ids(selectDiverse(topic, 2)); !slices.Equal(got, []string{"a", "same-topic"}) {
		t.Fatal("a same-topic row lost its place", got)
	}
	if nearDuplicateShare(0.5) != 0 || nearDuplicateShare(1) < 0.999 {
		t.Fatal(nearDuplicateShare(0.5), nearDuplicateShare(1))
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
	for _, text := range []string{"melanie camping trip with the kids in the forest", "melanie camping trip with the kids in the forest today", "melanie camping trip at the beach"} {
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

// A single-fact question keeps its best match first: the penalty only reorders the rows
// after it, so diversity cannot move the one fact a single-step answer needs off the top.
func TestRecallDiversityKeepsTheSingleFactFirst(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	f.svc.Repo = nearestFirstRepo{f.repo}
	for _, text := range []string{"caroline went to the support group on 7 may", "caroline went to the support group on 20 may",
		"caroline went to the support group with melanie", "caroline painted a sunrise", "caroline moved from sweden"} {
		f.embedText(f.activeMemory(text))
	}
	query := SearchInput{Scope: scopeA, Query: "when did caroline go to the support group on 7 may", Modes: []string{"vector"}, Limit: 3}
	off := false
	setRecallQuality(t, f, SpaceRecallQuality{Diversity: &off})
	plain := f.search(f.human, query)
	setRecallQuality(t, f, SpaceRecallQuality{})
	diverse := f.search(f.human, query)
	if len(plain.Items) != 3 || len(diverse.Items) != 3 || plain.Items[0].ID != diverse.Items[0].ID || diverse.Items[0].Text != "caroline went to the support group on 7 may" {
		t.Fatal(plain.Items, diverse.Items)
	}
	// Any pool: the best row is the first pick and nothing beyond the limit comes back.
	rows := []ScoredHit{diverseRow("a", 0.9, 1, 0), diverseRow("b", 0.89, 1, 0), diverseRow("c", 0.2, 0, 1), diverseRow("d", 0.1)}
	for limit := 1; limit <= 4; limit++ {
		if got := selectDiverse(slices.Clone(rows), limit); len(got) != limit || got[0].ID != "a" {
			t.Fatal(limit, got)
		}
	}
}

func TestSelectDiverseSourceSpan(t *testing.T) {
	base := Hit{vector: []float64{1, 0}, ID: "first", Scope: scopeA, Sources: []SourceRef{{SourceID: "source-a"}}, evidenceSpan: "one two three four five six seven eight nine ten"}
	for _, tc := range []struct {
		name   string
		span   string
		source string
		scope  Scope
		want   bool
	}{
		{"contained", "one two three four five", "source-a", scopeA, true},
		{"ninety percent", "one two three four five six seven eight nine other", "source-a", scopeA, true},
		{"below threshold", "one two three four five six seven eight other words", "source-a", scopeA, false},
		{"one token", "one", "source-a", scopeA, false},
		{"two tokens", "one two", "source-a", scopeA, false},
		{"three tokens", "one two three", "source-a", scopeA, false},
		{"four tokens", "one two three four", "source-a", scopeA, true},
		{"repeated short tokens", "one one two two three three", "source-a", scopeA, false},
		{"missing span", "", "source-a", scopeA, false},
		{"separate capture", base.evidenceSpan, "source-b", scopeA, true},
		{"different scope", base.evidenceSpan, "source-a", Scope{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retold := Hit{vector: []float64{.95, .31225}, ID: "copy", Scope: tc.scope, Sources: []SourceRef{{SourceID: tc.source}}, evidenceSpan: tc.span}
			rows := []ScoredHit{{Hit: base, Score: 1}, {Hit: retold, Score: .95}, diverseRow("distinct", .7)}
			got := selectDiverse(rows, 2)
			if (got[1].ID == "distinct") != tc.want {
				t.Fatal(got)
			}
		})
	}
}

func TestRecallDiversitySourceSpanStaysPrivate(t *testing.T) {
	f := newFixture(t)
	span := "private provenance of one repaired valve"
	for i, id := range []string{"span-a", "span-b", "span-c"} {
		r := f.activeMemory("repair note " + id)
		delete(f.repo.store[colMemories], recKey(scopeA, r.ID))
		r.ID = id
		r.Classification = &CandidateClassification{EvidenceSpan: span}
		if i == 2 {
			r.Classification.EvidenceSpan = "independent evidence about replacing the copper coupling"
		}
		f.repo.store[colMemories][recKey(scopeA, id)] = r
	}
	query := SearchInput{Scope: scopeA, Query: "repair", Modes: []string{"fts"}, Limit: 2}
	off := false
	setRecallQuality(t, f, SpaceRecallQuality{Diversity: &off})
	plain := f.search(f.human, query)
	if len(plain.Items) != 2 || plain.Items[0].ID != "span-a" || plain.Items[1].ID != "span-b" {
		t.Fatal("plain ranking did not keep the repeated fact", plain.Items)
	}
	setRecallQuality(t, f, SpaceRecallQuality{})
	diverse := f.search(f.human, query)
	if len(diverse.Items) != 2 || diverse.Items[0].ID != "span-a" || diverse.Items[1].ID != "span-b" {
		t.Fatal("missing vectors must preserve the distinct-fact possibility", diverse.Items)
	}
	encoded, err := json.Marshal(diverse)
	if err != nil || strings.Contains(string(encoded), span) || strings.Contains(string(encoded), "evidenceSpan") {
		t.Fatal("private provenance leaked into the response", err)
	}
}

// The same broad quotation can support different facts from a single message.
func TestSelectDiverseSharedEvidenceKeepsDistinctFacts(t *testing.T) {
	span := "the valve was repaired monday and the copper coupling arrived friday"
	for _, tc := range []struct {
		name          string
		first, second []float64
	}{
		{"different facts", []float64{1, 0}, []float64{.8, .6}},
		{"no vectors", nil, nil},
		{"missing first vector", nil, []float64{1, 0}},
		{"missing second vector", []float64{1, 0}, nil},
		{"incomparable vectors", []float64{1, 0}, []float64{1}},
		{"zero vectors", []float64{0, 0}, []float64{0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []ScoredHit{diverseRow("repair date", 1, tc.first...), diverseRow("arrival date", .95, tc.second...), diverseRow("unrelated", .8)}
			for i := 0; i < 2; i++ {
				rows[i].Scope = scopeA
				rows[i].Sources = []SourceRef{{SourceID: "one-message"}}
				rows[i].evidenceSpan = span
			}
			got := selectDiverse(rows, 2)
			if got[0].ID != "repair date" || got[1].ID != "arrival date" {
				t.Fatal("shared quote displaced a distinct fact", got)
			}
		})
	}
}

func BenchmarkSelectDiverseSourceSpan(b *testing.B) {
	for _, identical := range []bool{false, true} {
		b.Run(fmt.Sprintf("identical=%t", identical), func(b *testing.B) {
			rows := make([]ScoredHit, 100)
			for i := range rows {
				vector := make([]float64, 101)
				vector[0], vector[i+1] = 1, .2
				rows[i] = diverseRow(fmt.Sprint(i), 1-float64(i)/1000, vector...)
				rows[i].Scope = scopeA
				var quote strings.Builder
				for j := 0; j < 200; j++ {
					n := j
					if !identical {
						n += i * 200
					}
					fmt.Fprintf(&quote, "word%05d ", n)
				}
				rows[i].evidenceSpan = quote.String()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = selectDiverse(rows, 50)
			}
		})
	}
}
