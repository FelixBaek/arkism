package memory

import (
	"context"
	"math"
	"slices"
	"sort"
	"time"
)

// Half-lives for how fast a memory kind goes stale in recall. Reviewers mostly
// rejected candidates as outdated, and session state ages far faster than a lesson;
// preferences and procedures are durable, so other kinds never decay.
var recencyHalfLife = map[string]time.Duration{
	"episodic": 14 * 24 * time.Hour,
	"semantic": 30 * 24 * time.Hour,
	"decision": 60 * 24 * time.Hour,
	"lesson":   180 * 24 * time.Hour,
}

// contentTime is when a memory's content took effect: UpdatedAt also moves on
// metadata-only edits such as reach, so creation and effective time win.
func contentTime(updated time.Time, created, effective *time.Time) time.Time {
	at := updated
	if created != nil && !created.IsZero() {
		at = *created
	}
	if effective != nil && !effective.IsZero() {
		at = *effective
	}
	return at
}

// recencyFactor keeps at least half of a hit's score, so an old memory that is the
// only real match still surfaces; it only loses ties to fresher ones.
func recencyFactor(h Hit, now time.Time) float64 {
	half, ok := recencyHalfLife[h.Kind]
	if !ok {
		return 1
	}
	at := hitContentTime(h)
	if at.IsZero() || !at.Before(now) {
		return 1
	}
	return 0.5 + 0.5*math.Exp2(-float64(now.Sub(at))/float64(half))
}

// hitContentTime is a hit's content time: effective_at, else created_at, else updated_at.
// UpdatedAt is last because it also moves on metadata-only edits such as reach.
func hitContentTime(h Hit) time.Time { return contentTime(h.UpdatedAt, h.CreatedAt, h.EffectiveAt) }

// decayByRecency rescales fused scores by age and by each hit's outcome
// weight (1 when absent), and keeps the best limit rows.
func decayByRecency(rows []ScoredHit, now time.Time, weights map[string]float64, limit int) []ScoredHit {
	for i := range rows {
		rows[i].Score *= recencyFactor(rows[i].Hit, now)
		if w, ok := weights[rows[i].ID]; ok {
			rows[i].Score *= w
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	return rows[:min(limit, len(rows))]
}

// fuseRecallHits fuses the lanes, reranks the head when a reranker is set, and for
// memory recall applies recency and outcome weights before keeping in.Limit rows.
func (s *Service) fuseRecallHits(ctx context.Context, token string, lists [][]Hit, collection string, in SearchInput, d Decision, trace *recallTrace) ([]ScoredHit, error) {
	recall := collection == colMemories && !in.IncludeInactive
	n := in.Limit
	if recall || s.Reranker != nil {
		n = in.Limit * 3
	}
	if localExpandOn(in, collection) && !s.partialRecallEvaluationOn(in) {
		n = localRecallCandidateLimit * 3
	}
	if pool := in.candidatePool(collection); pool > 0 && !s.partialRecallEvaluationOn(in) {
		n = pool
	}
	rows := fuseWeighted(lists, n, trace.weights)
	trace.noteFused(rows)
	rows, err := s.rerankRows(ctx, in, d, rows, trace)
	if err != nil {
		return nil, err
	}
	rows = s.decisionRecall(ctx, token, in, rows, trace)
	rows = s.modelRerankRows(ctx, token, in, rows, trace)
	if recall {
		now, weights := s.recallClock(in), s.policyOutcomeWeights(ctx, in, rows)
		trace.noteBoosts(rows, now, weights)
		// No hit is before the zero time, so no age discount: for an earlier-state question the older
		// state is the answer, and with rerank off the fused scores sit so close together that the
		// discount alone would reorder them.
		if earlierStateIntent(in.Query) || in.Rerank == rerankOff {
			now = time.Time{}
		}
		rows = decayByRecency(rows, now, weights, len(rows))
		if s.evidenceSelectionStatus(in, collection) == "ready" {
			trace.evidenceCandidates = slices.Clone(rows)
		}
		kept := rows[:min(aggregateItemLimit(in), len(rows))]
		if in.space.recallQuality().diversityOn() {
			kept = selectDiverse(rows, aggregateItemLimit(in))
		}
		return keepPeriodHits(kept, rows, lists, trace, recallLocation(in.Timezone)), nil
	}
	return rows[:min(in.Limit, len(rows))], nil
}

// periodKeep is how many time-lane hits a recall that names a period keeps in its result.
const periodKeep = 2

// keepPeriodHits puts the time lane's best-ranked hits back into kept when the cut dropped them:
// rerankers score text, not dates, so the memory from the named period can fall below the limit.
// It takes the best row of each searched window first, then the next best, periodKeep at most and
// never more than half the result; they replace the tail. all is the ranked pool kept was cut from.
func keepPeriodHits(kept, all []ScoredHit, lists [][]Hit, trace *recallTrace, loc *time.Location) []ScoredHit {
	lane := slices.Index(trace.lanes, "temporal")
	room := min(periodKeep, len(kept)/2)
	if lane < 0 || lane >= len(lists) || room == 0 {
		return kept
	}
	inLane := map[string]bool{}
	for _, h := range lists[lane] {
		inLane[hitKey(h)] = true
	}
	picked := []ScoredHit{}
	pick := func(match func(Hit) bool) {
		for _, row := range all {
			if len(picked) < room && inLane[hitKey(row.Hit)] && match(row.Hit) && !slices.ContainsFunc(picked, func(p ScoredHit) bool { return hitKey(p.Hit) == hitKey(row.Hit) }) {
				picked = append(picked, row)
				return
			}
		}
	}
	for _, w := range trace.windows {
		pick(func(h Hit) bool { return hitInWindow(h, w, loc) })
	}
	for range room {
		pick(func(Hit) bool { return true })
	}
	missing := []ScoredHit{}
	for _, p := range picked {
		if !slices.ContainsFunc(kept, func(k ScoredHit) bool { return hitKey(k.Hit) == hitKey(p.Hit) }) {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return kept
	}
	out := slices.Clone(kept)
	for i := len(out) - 1; i >= 0 && len(missing) > 0; i-- {
		if slices.ContainsFunc(picked, func(p ScoredHit) bool { return hitKey(p.Hit) == hitKey(out[i].Hit) }) {
			continue // a picked row is never the one replaced
		}
		out[i], missing = missing[len(missing)-1], missing[:len(missing)-1]
	}
	return out
}

// Existing rerankers evaluate a bounded head, not the complete fused pool.
// Enlarging their unscored tail would mix incomparable scores and let that tail
// displace evaluated hits. Preserve their original fusion budget until a
// separately validated full-pool reranking contract is available.
func (s *Service) partialRecallEvaluationOn(in SearchInput) bool {
	crossEncoder := s.Reranker != nil && in.Rerank != rerankOff && spaceRerankOn(in.space)
	decision := in.RerankOptions != nil && (in.RerankOptions.Prune || in.RerankOptions.Dedup)
	return crossEncoder || modelRerankOn(in) || decision
}
