package memory

import (
	"context"
	"sort"
	"time"
	"unicode/utf8"
)

// Recall lanes beyond exact/keyword/vector/relation, and the rerank stage.
//
// Blend: a reranked row's score is rerankBlend*rerank + (1-rerankBlend)*fused/bestFused,
// both in [0,1]; a row the reranker did not score (past the head, or text unsafe to
// send) keeps fused/bestFused. Recency and outcome weights then multiply that score
// as before, so they still only break near-ties.
const (
	reasonTemporal  = "temporal"
	reasonGraph     = "graph"
	graphLaneWeight = 0.5 // RRF weight: a neighbour is weaker evidence than a direct match.
	graphSeedCount  = 5
	graphFanOut     = 20
	rerankMaxRows   = 100
	rerankTextMax   = 2000
	rerankBlend     = 0.7
)

// HitExplain is one hit's debug trace: its 1-based rank in each lane that found it.
type HitExplain struct {
	LaneRanks   map[string]int `json:"lane_ranks"`
	FusedScore  float64        `json:"fused_score"`
	RerankScore *float64       `json:"rerank_score,omitempty"`
}

// RecallExplain is the search-level debug trace returned with explain=true.
type RecallExplain struct {
	Lanes      []string          `json:"lanes"`
	TimeWindow *RecallTimeWindow `json:"time_window,omitempty"`
	Reranked   bool              `json:"reranked"`
}

type RecallTimeWindow struct {
	Phrase string    `json:"phrase"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
}

// recallTrace follows one search through fusion; the maps are filled only for explain.
type recallTrace struct {
	explain  bool
	lanes    []string
	window   *timeWindow
	fused    map[string]float64
	rerank   map[string]float64
	reranked bool
}

func (s *Service) recallNow() time.Time {
	if s.RecallNow != nil {
		return s.RecallNow()
	}
	return time.Now()
}

func hitKey(h Hit) string { return h.Origin + ":" + h.Collection + ":" + h.ID }

// recallLanes appends the temporal and graph lanes to plain memory recall. Lanes
// chosen explicitly (in.Modes) and history searches keep exactly the lanes asked for.
func (s *Service) recallLanes(ctx context.Context, in SearchInput, collection string, lists [][]Hit, clean func([]Hit) ([]Hit, error)) ([][]Hit, *recallTrace, error) {
	trace := &recallTrace{explain: in.Explain, fused: map[string]float64{}, rerank: map[string]float64{}}
	for i := range lists {
		trace.lanes = append(trace.lanes, []string{"exact", "keyword", "vector", "relation"}[min(i, 3)])
	}
	if collection != colMemories || in.IncludeInactive || len(in.Modes) > 0 {
		return lists, trace, nil
	}
	if w, ok := parseTimeIntent(in.Query, s.recallNow().In(recallLocation(in.Timezone))); ok {
		hits, err := s.Repo.SearchMemoriesInWindow(ctx, in, w.From, w.To)
		if err == nil {
			hits, err = clean(hits)
		}
		if err != nil {
			return nil, nil, err
		}
		trace.window = &w
		lists, trace.lanes = append(lists, hits), append(trace.lanes, "temporal")
	}
	if !s.RecallGraph {
		return lists, trace, nil
	}
	hits, err := s.graphLane(ctx, in, FuseScored(lists, graphSeedCount))
	if err == nil {
		hits, err = clean(hits)
	}
	if err != nil {
		return nil, nil, err
	}
	trace.lanes = append(trace.lanes, "graph")
	return append(lists, hits), trace, nil
}

// recallItems adds the recall lanes, fuses and fills out.Items (with the explain trace when asked).
func (s *Service) recallItems(ctx context.Context, in SearchInput, d Decision, collection string, lists [][]Hit, clean func([]Hit) ([]Hit, error), out *ContextBundle) error {
	lists, trace, err := s.recallLanes(ctx, in, collection, lists, clean)
	if err != nil {
		return err
	}
	scored := s.fuseRecallHits(ctx, lists, collection, in, d, trace)
	out.Items = make([]Hit, len(scored))
	for i, r := range scored {
		r.Hit.Score, r.Hit.Lanes = r.Score, hitLanes(r.Hit.Reasons)
		out.Items[i] = r.Hit
	}
	trace.annotate(out, lists)
	return nil
}

// graphLane reads the approved memories one relations step from the top fused hits.
func (s *Service) graphLane(ctx context.Context, in SearchInput, top []ScoredHit) ([]Hit, error) {
	seeds := make([]string, 0, len(top))
	for _, r := range top {
		seeds = append(seeds, r.ID)
	}
	if len(seeds) == 0 {
		return []Hit{}, nil
	}
	ids, err := s.Repo.MemoryNeighbors(ctx, in.Scope, seeds, min(graphFanOut, 2*in.Limit))
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(ids))
	for _, id := range ids {
		// A neighbour that is gone or unreadable simply drops out; cleaning rechecks the rest.
		if r, err := s.Repo.Get(ctx, colMemories, in.Scope, id); err == nil {
			hits = append(hits, searchHit(r, colMemories, reasonGraph))
		}
	}
	return hits, nil
}

// rerankRows reorders the fused head by the reranker (blend above). Any failure keeps
// the fused order; text is sent only when the vector lane could send the query too.
func (s *Service) rerankRows(ctx context.Context, in SearchInput, d Decision, rows []ScoredHit, trace *recallTrace) []ScoredHit {
	if s.Reranker == nil || len(rows) < 2 || rows[0].Score <= 0 || !(d.ExternalTransfer && d.ContentSafe && safeTransfer(in.Query)) {
		return rows
	}
	texts, at := []string{}, []int{}
	for i := range rows[:min(len(rows), in.Limit*3, rerankMaxRows)] {
		text := rows[i].Text
		if utf8.RuneCountInString(text) > rerankTextMax {
			text = string([]rune(text)[:rerankTextMax])
		}
		if safeTransfer(text) && text != "" {
			texts, at = append(texts, text), append(at, i)
		}
	}
	if len(texts) == 0 {
		return rows
	}
	scores, err := s.Reranker.Rerank(ctx, in.Query, texts)
	if err != nil {
		s.Reranker.fellBack(err)
		return rows
	}
	best := rows[0].Score
	for i := range rows {
		rows[i].Score /= best
	}
	for k, i := range at {
		rows[i].Score = rerankBlend*scores[k] + (1-rerankBlend)*rows[i].Score
		trace.rerank[hitKey(rows[i].Hit)] = scores[k]
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	trace.reranked = true
	return rows
}

func (t *recallTrace) noteFused(rows []ScoredHit) {
	if t.explain {
		for _, r := range rows {
			t.fused[hitKey(r.Hit)] = r.Score
		}
	}
}

// annotate attaches the explain trace to out when the caller asked for it.
func (t *recallTrace) annotate(out *ContextBundle, lists [][]Hit) {
	if !t.explain {
		return
	}
	ranks := map[string]map[string]int{}
	for lane, list := range lists {
		for pos, h := range list {
			k := hitKey(h)
			if ranks[k] == nil {
				ranks[k] = map[string]int{}
			}
			if _, seen := ranks[k][t.lanes[lane]]; !seen {
				ranks[k][t.lanes[lane]] = pos + 1
			}
		}
	}
	for i := range out.Items {
		k := hitKey(out.Items[i])
		e := &HitExplain{LaneRanks: ranks[k], FusedScore: t.fused[k]}
		if v, ok := t.rerank[k]; ok {
			e.RerankScore = &v
		}
		out.Items[i].Explain = e
	}
	out.Explain = &RecallExplain{Lanes: t.lanes, Reranked: t.reranked}
	if t.window != nil {
		out.Explain.TimeWindow = &RecallTimeWindow{Phrase: t.window.Phrase, From: t.window.From, To: t.window.To}
	}
}
