package memory

import (
	"context"
	"slices"
	"sort"
	"time"
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
	reasonCausal    = "causal"
	graphLaneWeight = 0.5 // RRF weight: a neighbour is weaker evidence than a direct match.
	graphSeedCount  = 5
	graphFanOut     = 20
	graphDepthMax   = 3
	graphStepMax    = 100 // ids one step may read: the repository's neighbour limit
	graphHopDecay   = 0.5 // each further step halves a neighbour's lane weight
	rerankMaxRows   = 100
	rerankTextMax   = 2000
	rerankBlend     = 0.7
)

// HitExplain is one hit's debug trace: its 1-based rank in each lane that found it.
type HitExplain struct {
	LaneRanks   map[string]int `json:"lane_ranks"`
	FusedScore  float64        `json:"fused_score"`
	RerankScore *float64       `json:"rerank_score,omitempty"`
	// GraphHops is how many graph or causal steps from a top hit the hit was first reached.
	GraphHops int `json:"graph_hops,omitempty"`
}

// RecallExplain is the search-level debug trace returned with explain=true.
type RecallExplain struct {
	Lanes      []string          `json:"lanes"`
	TimeWindow *RecallTimeWindow `json:"time_window,omitempty"`
	Reranked   bool              `json:"reranked"`
	// MinScoreDropped counts hits the request's min_score removed.
	MinScoreDropped int `json:"min_score_dropped,omitempty"`
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
	dropped  int
}

func (s *Service) recallNow() time.Time {
	if s.RecallNow != nil {
		return s.RecallNow()
	}
	return time.Now()
}

// recallClock is the request's query_time, else the server clock.
func (s *Service) recallClock(in SearchInput) time.Time {
	if in.QueryTime != nil {
		return *in.QueryTime
	}
	return s.recallNow()
}

// boundRecallOptions checks the timezone and recall-only options and fills the graph depth default.
func boundRecallOptions(in *SearchInput) bool {
	if !validTimezone(in.Timezone) || in.QueryTime != nil && (in.QueryTime.Year() < 1970 || in.QueryTime.Year() > 9999) {
		return false
	}
	if in.GraphDepth == 0 {
		in.GraphDepth = 2
	}
	return in.GraphDepth >= 1 && in.GraphDepth <= graphDepthMax && in.MinScore >= 0 && in.MinScore <= 1 && validInclude(in.Include)
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
	if w, ok := parseTimeIntent(in.Query, s.recallClock(in).In(recallLocation(in.Timezone))); ok {
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
	sources := []neighborSource{}
	if s.RecallGraph {
		sources = append(sources, neighborSource{reasonGraph, s.Repo.MemoryNeighbors})
	}
	if in.ExpandCausal {
		sources = append(sources, neighborSource{reasonCausal, s.causalNeighbors})
	}
	if len(sources) == 0 {
		return lists, trace, nil
	}
	lanes, err := s.neighborLanes(ctx, in, FuseScored(lists, graphSeedCount), sources, clean)
	if err != nil {
		return nil, nil, err
	}
	for i, hits := range lanes {
		lists, trace.lanes = append(lists, hits), append(trace.lanes, sources[i].reason)
	}
	return lists, trace, nil
}

// recallItems adds the recall lanes, fuses and fills out.Items (with the explain trace when asked).
func (s *Service) recallItems(ctx context.Context, in SearchInput, d Decision, collection string, lists [][]Hit, clean func([]Hit) ([]Hit, error), out *ContextBundle) error {
	lists, trace, err := s.recallLanes(ctx, in, collection, lists, clean)
	if err != nil {
		return err
	}
	scored, err := s.fuseRecallHits(ctx, lists, collection, in, d, trace)
	if err != nil {
		return err
	}
	out.Items = make([]Hit, 0, len(scored))
	for _, r := range scored {
		if r.Score < in.MinScore {
			trace.dropped++
			continue
		}
		r.Hit.Score, r.Hit.Lanes = r.Score, hitLanes(r.Hit.Reasons)
		out.Items = append(out.Items, r.Hit)
	}
	if in.PreferObservations {
		out.Items = preferObservations(out.Items)
	}
	trace.annotate(out, lists)
	return nil
}

// preferObservations drops the memories an approved observation among items was written from.
func preferObservations(items []Hit) []Hit {
	covered := map[string]bool{}
	for _, h := range items {
		for _, id := range h.SourceIDs {
			covered[id] = covered[id] || h.Derived
		}
	}
	return slices.DeleteFunc(items, func(h Hit) bool { return !h.Derived && h.Collection == colMemories && covered[h.ID] })
}

// neighborSource is one way to step from memories to their neighbours (relations
// and entity co-mentions, or person-approved causal links).
type neighborSource struct {
	reason string
	next   func(context.Context, Scope, []string, int) ([]string, error)
}

// neighborLanes walks up to in.GraphDepth steps from the top fused hits over all
// sources together, so a path may mix relation and causal steps. Each source fills
// its own lane in hop order, at most min(graphFanOut, 2*limit) hits, and a step only
// continues from neighbours that passed clean: the walk never crosses a memory the
// caller could not read. A hit's graphHops makes farther neighbours weigh less.
func (s *Service) neighborLanes(ctx context.Context, in SearchInput, top []ScoredHit, sources []neighborSource, clean func([]Hit) ([]Hit, error)) ([][]Hit, error) {
	seen, frontier := map[string]bool{}, []string{}
	for _, r := range top {
		seen[r.ID], frontier = true, append(frontier, r.ID)
	}
	lanes, budget := make([][]Hit, len(sources)), min(graphFanOut, 2*in.Limit)
	for hop := 1; hop <= in.GraphDepth && len(frontier) > 0; hop++ {
		var next []string
		for i, src := range sources {
			if len(lanes[i]) >= budget {
				continue
			}
			hits, err := s.neighborStep(ctx, in, frontier, src, budget, seen, clean)
			if err != nil {
				return nil, err
			}
			for _, h := range hits[:min(len(hits), budget-len(lanes[i]))] {
				h.graphHops = hop
				lanes[i], next = append(lanes[i], h), append(next, h.ID)
			}
		}
		frontier = next
	}
	return lanes, nil
}

// neighborStep reads and cleans the unseen memories one src step from frontier.
func (s *Service) neighborStep(ctx context.Context, in SearchInput, frontier []string, src neighborSource, limit int, seen map[string]bool, clean func([]Hit) ([]Hit, error)) ([]Hit, error) {
	// Ask past the ids already seen (back-edges to earlier steps), so they cannot fill the step.
	ids, err := src.next(ctx, in.Scope, frontier, min(limit+len(seen), graphStepMax))
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(ids))
	read := 0 // at most limit reads per step, however many ids came back
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if read == limit {
			break
		}
		seen[id], read = true, read+1
		// A neighbour that is gone or unreadable simply drops out; cleaning rechecks the rest.
		if r, err := s.Repo.Get(ctx, colMemories, in.Scope, id); err == nil {
			hits = append(hits, searchHit(r, colMemories, src.reason))
		}
	}
	return clean(hits)
}

// rerankRows reorders the fused head by the reranker (blend above). A reranker failure
// keeps the fused order; only a usage-ledger failure is returned, as for the query
// embedding. Text is sent only when the vector lane could send the query too, and
// only for hits the embedding worker would send (not restricted, safe to transfer).
func (s *Service) rerankRows(ctx context.Context, in SearchInput, d Decision, rows []ScoredHit, trace *recallTrace) ([]ScoredHit, error) {
	if s.Reranker == nil || in.Rerank != nil && !*in.Rerank || len(rows) < 2 || rows[0].Score <= 0 || !(d.ExternalTransfer && d.ContentSafe && safeTransfer(in.Query)) {
		return rows, nil
	}
	texts, at := rerankTexts(rows[:min(len(rows), in.Limit*3, rerankMaxRows)])
	if len(texts) == 0 {
		return rows, nil
	}
	scores, err, e := s.meteredRerank(ctx, in.Scope, in.Query, texts)
	if e != nil {
		return nil, e
	}
	if err != nil {
		s.Reranker.fellBack(err)
		return rows, nil
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
	return rows, nil
}

// rerankTexts picks the head rows whose text may leave the host, cut to rerankTextMax runes.
func rerankTexts(head []ScoredHit) ([]string, []int) {
	texts, at := []string{}, []int{}
	for i, r := range head {
		text := runeCutString(r.Text, rerankTextMax)
		if r.Sensitivity != "restricted" && text != "" && safeTransfer(text) {
			texts, at = append(texts, text), append(at, i)
		}
	}
	return texts, at
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
	ranks, hops := map[string]map[string]int{}, map[string]int{}
	for lane, list := range lists {
		for pos, h := range list {
			k := hitKey(h)
			if h.graphHops > 0 && (hops[k] == 0 || h.graphHops < hops[k]) {
				hops[k] = h.graphHops
			}
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
		e := &HitExplain{LaneRanks: ranks[k], FusedScore: t.fused[k], GraphHops: hops[k]}
		if v, ok := t.rerank[k]; ok {
			e.RerankScore = &v
		}
		out.Items[i].Explain = e
	}
	out.Explain = &RecallExplain{Lanes: t.lanes, Reranked: t.reranked, MinScoreDropped: t.dropped}
	if t.window != nil {
		out.Explain.TimeWindow = &RecallTimeWindow{Phrase: t.window.Phrase, From: t.window.From, To: t.window.To}
	}
}
