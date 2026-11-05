package memory

import (
	"context"
	"math"
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
	rows := fuseWeighted(lists, n, trace.weights)
	trace.noteFused(rows)
	rows, err := s.rerankRows(ctx, in, d, rows, trace)
	if err != nil {
		return nil, err
	}
	rows = s.modelRerankRows(ctx, token, in, rows, trace)
	if recall {
		return decayByRecency(rows, s.recallClock(in), s.outcomeWeights(ctx, in.Scope, rows), in.Limit), nil
	}
	return rows[:min(in.Limit, len(rows))], nil
}
