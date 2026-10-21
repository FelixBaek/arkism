package memory

import (
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

// recencyFactor keeps at least half of a hit's score, so an old memory that is the
// only real match still surfaces; it only loses ties to fresher ones.
func recencyFactor(h Hit, now time.Time) float64 {
	half, ok := recencyHalfLife[h.Kind]
	if !ok {
		return 1
	}
	// Age from content time: UpdatedAt also moves on metadata-only edits such as reach.
	at := h.UpdatedAt
	if h.CreatedAt != nil && !h.CreatedAt.IsZero() {
		at = *h.CreatedAt
	}
	if h.EffectiveAt != nil && !h.EffectiveAt.IsZero() {
		at = *h.EffectiveAt
	}
	if at.IsZero() || !at.Before(now) {
		return 1
	}
	return 0.5 + 0.5*math.Exp2(-float64(now.Sub(at))/float64(half))
}

// decayByRecency rescales fused scores by age and keeps the best limit rows.
func decayByRecency(rows []ScoredHit, now time.Time, limit int) []ScoredHit {
	for i := range rows {
		rows[i].Score *= recencyFactor(rows[i].Hit, now)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	return rows[:min(limit, len(rows))]
}

func fuseRecallHits(lists [][]Hit, collection string, includeInactive bool, limit int) []ScoredHit {
	if collection == colMemories && !includeInactive {
		return decayByRecency(FuseScored(lists, limit*3), time.Now(), limit)
	}
	return FuseScored(lists, limit)
}
