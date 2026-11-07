package memory

import (
	"context"
	"slices"
	"sort"
	"sync"
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
	reasonSemantic  = "semantic"
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
	// ModelRank is the hit's 1-based place in the model rerank order.
	ModelRank int `json:"model_rank,omitempty"`
	// GraphHops is how many graph or causal steps from a top hit the hit was first reached.
	GraphHops int `json:"graph_hops,omitempty"`
	// Recency and Outcome are the memory-recall factors the score was multiplied by after
	// fusion and rerank (recency.go): age decay (0.5-1) and the outcome weight.
	Recency *float64 `json:"recency,omitempty"`
	Outcome *float64 `json:"outcome,omitempty"`
}

// RecallExplain is the search-level debug trace returned with explain=true.
type RecallExplain struct {
	Lanes      []string          `json:"lanes"`
	TimeWindow *RecallTimeWindow `json:"time_window,omitempty"`
	Reranked   bool              `json:"reranked"`
	// ModelRerank is set when the model rerank was asked for: applied, or why the fused order was kept.
	ModelRerank string `json:"model_rerank,omitempty"`
	// Expand is set when query expansion was on: applied, or why plain recall ran (recall_expand.go);
	// ExpandQueries are the model's sub-queries that were searched.
	Expand        string   `json:"expand,omitempty"`
	ExpandQueries []string `json:"expand_queries,omitempty"`
	// MinScoreDropped counts hits the request's min_score removed.
	MinScoreDropped int `json:"min_score_dropped,omitempty"`
	// Dropped counts hits each stage removed: the semantic, keyword and reranker floors
	// (min_scores) and the model rerank's prune and dedup (rerank_options).
	Dropped map[string]int `json:"dropped,omitempty"`
	// LaneWeights are the fusion weights other than 1 that applied (request over space).
	LaneWeights map[string]float64 `json:"lane_weights,omitempty"`
	// LaneMS is each lane's wall time in whole milliseconds (all collections summed), plus rerank when it ran.
	LaneMS map[string]int64 `json:"lane_ms,omitempty"`
	// RerankChain is each configured reranker tried, in order, with its outcome:
	// ok, failed, or not_allowed (the request may not send text to it).
	RerankChain []RerankStage `json:"rerank_chain,omitempty"`
	// LaneHits is each lane's own list before fusion, in lane order (at most laneHitsMax per lane),
	// including hits fusion or a floor left out; ids only, a reader opens each with its own read.
	LaneHits map[string][]Ref `json:"lane_hits,omitempty"`
}

// RerankStage is one reranker of the fallback chain and what it did for this search.
type RerankStage struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Outcome  string `json:"outcome"`
}

const laneHitsMax = 20

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
	chain    []RerankStage
	// modelRerank is the model rerank outcome (recall_model_rerank.go); modelRank its 1-based order.
	modelRerank string
	modelRank   map[string]int
	// expand and expandQueries are the query expansion outcome (recall_expand.go).
	expand        string
	expandQueries []string
	dropped       int
	dropStages    map[string]int
	weights       map[string]float64
	clock         *laneClock
	// boosts are each memory hit's recency factor and outcome weight (noteBoosts).
	boosts map[string][2]float64
}

// laneClock sums each lane's wall time for explain output; without explain the
// context carries none and every call is a no-op.
type laneClock struct {
	mu sync.Mutex
	d  map[string]time.Duration
}

type laneClockKey struct{}

func withLaneClock(ctx context.Context, explain bool) context.Context {
	if !explain {
		return ctx
	}
	return context.WithValue(ctx, laneClockKey{}, &laneClock{d: map[string]time.Duration{}})
}

func laneClockOf(ctx context.Context) *laneClock {
	c, _ := ctx.Value(laneClockKey{}).(*laneClock)
	return c
}

// since adds the time from start to lane: defer laneClockOf(ctx).since(lane, time.Now()).
// Every run also lands in the recall lane histogram, with or without explain.
func (c *laneClock) since(lane string, start time.Time) {
	elapsed := time.Since(start)
	observeStage(stageRecallLane, lane, elapsed)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.d[lane] += elapsed
	c.mu.Unlock()
}

func (c *laneClock) millis() map[string]int64 {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.d))
	for lane, d := range c.d {
		out[lane] = d.Milliseconds()
	}
	return out
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
	return in.GraphDepth >= 1 && in.GraphDepth <= graphDepthMax && in.MinScore >= 0 && in.MinScore <= 1 && validInclude(in.Include, in.IncludeOptions) &&
		validLaneWeights(in.LaneWeights) && in.TemporalWindow.valid() && in.MinScores.valid()
}

func hitKey(h Hit) string { return h.Origin + ":" + h.Collection + ":" + h.ID }

// recallLanes appends the temporal and graph lanes to plain memory recall. Lanes
// chosen explicitly (in.Modes) and history searches keep exactly the lanes asked for.
func (s *Service) recallLanes(ctx context.Context, in SearchInput, collection string, lists [][]Hit, clean func([]Hit) ([]Hit, error)) ([][]Hit, *recallTrace, error) {
	trace := &recallTrace{explain: in.Explain, fused: map[string]float64{}, rerank: map[string]float64{}, clock: laneClockOf(ctx), weights: in.laneWeights(), expand: in.expandStatus, expandQueries: in.expandQueries}
	for i := range lists {
		trace.lanes = append(trace.lanes, []string{"exact", "keyword", "vector", "relation"}[min(i, 3)])
	}
	lists = laneFloors(in, lists, trace)
	if collection == colMemories {
		lists = weighKeywordCoverage(in, relevanceGate(in, lists, trace))
		for _, hits := range expandedLanes(in, trace) {
			lists, trace.lanes = append(lists, hits), append(trace.lanes, "expanded")
		}
	}
	if collection != colMemories || in.IncludeInactive || len(in.Modes) > 0 && !in.spaceModes {
		return lists, trace, nil
	}
	if w, ok := in.timeWindow(s.recallClock(in)); ok && spaceLaneOn(in.space, "temporal", true) {
		start := time.Now()
		hits, err := s.Repo.SearchMemoriesInWindow(ctx, in, w.From, w.To)
		if err == nil {
			hits, err = clean(hits)
		}
		trace.clock.since("temporal", start)
		if err != nil {
			return nil, nil, err
		}
		trace.window = &w
		lists, trace.lanes = append(lists, hits), append(trace.lanes, "temporal")
	}
	sources := []neighborSource{}
	if spaceLaneOn(in.space, "graph", s.RecallGraph) {
		sources = append(sources, neighborSource{reasonGraph, s.Repo.MemoryNeighbors})
	}
	if in.ExpandCausal {
		sources = append(sources, neighborSource{reasonCausal, s.causalNeighbors})
	}
	if spaceLaneOn(in.space, "semantic", false) {
		sources = append(sources, neighborSource{reasonSemantic, s.semanticNeighbors})
	}
	if len(sources) == 0 {
		return lists, trace, nil
	}
	lanes, err := s.neighborLanes(ctx, in, fuseWeighted(lists, graphSeedCount, trace.weights), sources, clean)
	if err != nil {
		return nil, nil, err
	}
	for i, hits := range lanes {
		lists, trace.lanes = append(lists, hits), append(trace.lanes, sources[i].reason)
	}
	return lists, trace, nil
}

// recallItems adds the recall lanes, fuses and fills out.Items (with the explain trace when asked).
func (s *Service) recallItems(ctx context.Context, token string, in SearchInput, d Decision, collection string, lists [][]Hit, clean func([]Hit) ([]Hit, error), out *ContextBundle) error {
	lists, trace, err := s.recallLanes(ctx, in, collection, lists, clean)
	if err != nil {
		return err
	}
	scored, err := s.fuseRecallHits(ctx, token, lists, collection, in, d, trace)
	if err != nil {
		return err
	}
	out.Items = make([]Hit, 0, len(scored))
	final := max(in.MinScore, in.floor().Final)
	for _, r := range scored {
		if r.Score < final {
			trace.dropped++
			continue
		}
		r.Hit.Score, r.Hit.Lanes = r.Score, hitLanes(r.Hit.Reasons)
		out.Items = append(out.Items, r.Hit)
	}
	if in.PreferObservations != nil && *in.PreferObservations {
		out.Items = preferObservations(out.Items)
	}
	if len(out.Items) == 0 && trace.dropStages["relevance"] > 0 {
		out.EmptyReason = emptyBelowRelevance
	}
	trace.annotate(out, lists)
	if err := s.pendingRecall(ctx, token, in, collection, out); err != nil {
		return err
	}
	s.conversationTurns(ctx, token, in, collection, out)
	return s.collectSourceFacts(ctx, in, out, clean)
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

// semanticNeighbors is the semantic link step: each seed's nearest stored vectors at
// cosine similarity >= memoryGraphSimilarMin, through the same per-memory nearest-neighbour
// query the memory map draws its lines from (same scope, embedding space and reach), nearest
// first. No embedding call is made.
func (s *Service) semanticNeighbors(ctx context.Context, scope Scope, seeds []string, limit int) ([]string, error) {
	pairs, err := s.Repo.MemoryGraphSimilar(ctx, MemoryGraphQuery{Scope: scope, Mode: MemoryGraphModeAll}, seeds, 1-memoryGraphSimilarMin, min(limit, memoryGraphMaxNodes))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Distance < pairs[j].Distance })
	out := []string{}
	for _, p := range pairs {
		if !slices.Contains(out, p.To) && !slices.Contains(seeds, p.To) {
			out = append(out, p.To)
		}
	}
	return out[:min(limit, len(out))], nil
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
	defer laneClockOf(ctx).since(src.reason, time.Now())
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

// rerankRows reorders the fused head by the first reranker of the chain that
// answers (blend above); when every one fails the fused order stays. Only a
// usage-ledger failure is returned, as for the query embedding. Text is sent to a
// remote reranker only when the vector lane could send the query too, and only for
// hits the embedding worker would send (not restricted, safe to transfer); a local
// reranker sends nothing, so it skips the transfer decision but keeps the text
// filters (call traces may keep the text).
func (s *Service) rerankRows(ctx context.Context, in SearchInput, d Decision, rows []ScoredHit, trace *recallTrace) ([]ScoredHit, error) {
	if s.Reranker == nil || in.Rerank == rerankOff || !spaceRerankOn(in.space) || len(rows) < 2 || rows[0].Score <= 0 || !safeTransfer(in.Query) {
		return rows, nil
	}
	texts, at := rerankTexts(rows[:min(len(rows), in.Limit*3, rerankMaxRows)])
	if len(texts) == 0 {
		return rows, nil
	}
	start := time.Now()
	var scores []float64
	for r := s.Reranker; r != nil && scores == nil; r = r.next {
		u, outcome := r.usage(), "ok"
		if !(r.local || d.ExternalTransfer && d.ContentSafe) {
			trace.chain = append(trace.chain, RerankStage{Provider: u.Provider, Model: u.Model, Outcome: "not_allowed"})
			continue
		}
		got, err, e := s.meteredRerank(ctx, r, in.Scope, in.Query, texts)
		if e != nil {
			return nil, e
		}
		if err != nil {
			r.fellBack(err)
			outcome = "failed"
		} else {
			scores = got
		}
		trace.chain = append(trace.chain, RerankStage{Provider: u.Provider, Model: u.Model, Outcome: outcome})
	}
	trace.clock.since("rerank", start)
	if scores == nil {
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
	return rerankFloor(in, rows, trace), nil
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

// noteBoosts keeps the factors decayByRecency applies, for explain.
func (t *recallTrace) noteBoosts(rows []ScoredHit, now time.Time, weights map[string]float64) {
	if !t.explain {
		return
	}
	t.boosts = map[string][2]float64{}
	for _, r := range rows {
		w, ok := weights[r.ID]
		if !ok {
			w = 1
		}
		t.boosts[hitKey(r.Hit)] = [2]float64{recencyFactor(r.Hit, now), w}
	}
}

// laneHits lists each lane's hits in lane order, at most laneHitsMax each, once per lane.
func (t *recallTrace) laneHits(lists [][]Hit) map[string][]Ref {
	out, seen := map[string][]Ref{}, map[string]bool{}
	for lane, list := range lists {
		name := t.lanes[lane]
		for _, h := range list {
			k := name + "\x00" + hitKey(h)
			if len(out[name]) >= laneHitsMax || seen[k] {
				continue
			}
			seen[k] = true
			out[name] = append(out[name], Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision})
		}
	}
	return out
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
		e := &HitExplain{LaneRanks: ranks[k], FusedScore: t.fused[k], GraphHops: hops[k], ModelRank: t.modelRank[k]}
		if v, ok := t.rerank[k]; ok {
			e.RerankScore = &v
		}
		if b, ok := t.boosts[k]; ok {
			e.Recency, e.Outcome = &b[0], &b[1]
		}
		out.Items[i].Explain = e
	}
	out.Explain = &RecallExplain{Lanes: t.lanes, Reranked: t.reranked, ModelRerank: t.modelRerank, Expand: t.expand, ExpandQueries: t.expandQueries, MinScoreDropped: t.dropped,
		Dropped: t.dropStages, LaneWeights: t.weights, LaneMS: t.clock.millis(), RerankChain: t.chain, LaneHits: t.laneHits(lists)}
	if t.window != nil {
		out.Explain.TimeWindow = &RecallTimeWindow{Phrase: t.window.Phrase, From: t.window.From, To: t.window.To}
	}
}
