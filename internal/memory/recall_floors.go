package memory

import (
	"slices"
	"time"
)

// Request-side recall tuning: a caller-given temporal window, per-stage score floors
// and the model rerank's prune/dedup switches. Each stage's removals are counted in
// explain.dropped under the stage name.

// TemporalWindow is a caller-given [from, to) for the temporal lane. Like a window read
// from the query's time words it only adds a lane: memories inside rank higher, the
// rest still match on the other lanes.
type TemporalWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func (w *TemporalWindow) valid() bool {
	return w == nil || w.From.Before(w.To) && w.From.Year() >= 1970 && w.To.Year() <= 9999
}

// MinScores are floors in [0, 1] per stage; 0 is off.
//   - semantic: the vector lane keeps hits with 1 - cosine distance at least this.
//   - keyword: the keyword lane keeps hits whose text holds at least this share of the query's terms.
//   - reranker: after a rerank, hits the reranker scored lower (or could not score) are dropped.
//   - final: like min_score; the higher of the two applies.
//
// The lane floors act inside their own lane, before fusion: a hit another lane found stays.
type MinScores struct {
	Semantic float64 `json:"semantic,omitempty"`
	Keyword  float64 `json:"keyword,omitempty"`
	Reranker float64 `json:"reranker,omitempty"`
	Final    float64 `json:"final,omitempty"`
}

func (m *MinScores) valid() bool {
	in01 := func(v float64) bool { return v >= 0 && v <= 1 }
	return m == nil || in01(m.Semantic) && in01(m.Keyword) && in01(m.Reranker) && in01(m.Final)
}

// RerankOptions widen what the model rerank (rerank: "model") may do in its one call:
// prune drops snippets it marks off-topic, dedup drops snippets it marks as repeating
// a higher one. Neither runs without the model rerank, so neither spends extra budget.
type RerankOptions struct {
	Prune bool `json:"prune,omitempty"`
	Dedup bool `json:"dedup,omitempty"`
}

func (in SearchInput) floor() MinScores {
	if in.MinScores == nil {
		return MinScores{}
	}
	return *in.MinScores
}

// laneFloors applies the semantic and keyword floors to the lane lists (copied, not in place).
func laneFloors(in SearchInput, lists [][]Hit, trace *recallTrace) [][]Hit {
	f := in.floor()
	if f.Semantic == 0 && f.Keyword == 0 {
		return lists
	}
	terms := searchQueryTerms(in.Query)
	out := make([][]Hit, len(lists))
	for i, list := range lists {
		out[i] = slices.DeleteFunc(slices.Clone(list), func(h Hit) bool {
			switch {
			case f.Semantic > 0 && slices.Contains(h.Reasons, reasonVector) && semanticScore(h) < f.Semantic:
				trace.drop("semantic")
			case f.Keyword > 0 && slices.Contains(h.Reasons, reasonTextSearch) && keywordScore(terms, h) < f.Keyword:
				trace.drop("keyword")
			default:
				return false
			}
			return true
		})
	}
	return out
}

// semanticScore is 1 - the checked cosine distance; a hit without one scores 0.
func semanticScore(h Hit) float64 {
	if h.VectorMatch == nil {
		return 0
	}
	return 1 - h.VectorMatch.Distance
}

// keywordScore is the share of the query's topic terms found in the hit's text and summary.
func keywordScore(terms []string, h Hit) float64 {
	if len(terms) == 0 {
		return 1
	}
	have := SearchTokens(h.Text + " " + h.Summary)
	found := 0
	for _, t := range terms {
		if slices.Contains(have, t) {
			found++
		}
	}
	return float64(found) / float64(len(terms))
}

// rerankFloor drops reranked rows the reranker scored under the floor or did not score.
func rerankFloor(in SearchInput, rows []ScoredHit, trace *recallTrace) []ScoredHit {
	f := in.floor().Reranker
	if f == 0 || !trace.reranked {
		return rows
	}
	return slices.DeleteFunc(rows, func(r ScoredHit) bool {
		v, ok := trace.rerank[hitKey(r.Hit)]
		if !ok || v < f {
			trace.drop("reranker")
			return true
		}
		return false
	})
}

func (t *recallTrace) drop(stage string) {
	if t.dropStages == nil {
		t.dropStages = map[string]int{}
	}
	t.dropStages[stage]++
}

// timeWindow is the temporal lane's window: the request's temporal_window, else the
// query's time words read at the search clock in the request's timezone.
func (in SearchInput) timeWindow(now time.Time) (timeWindow, bool) {
	if w := in.TemporalWindow; w != nil {
		return timeWindow{From: w.From, To: w.To, Phrase: "temporal_window"}, true
	}
	return parseTimeIntent(in.Query, now.In(recallLocation(in.Timezone)))
}
