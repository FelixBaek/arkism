package memory

import (
	"context"
	"math"
	"sync"
	"time"
)

const reviewScoreCacheLimit = 4096
const reviewScoreCacheTTL = 10 * time.Minute

type reviewScoreKey struct {
	Scope                                            Scope
	Principal, Role, Model, Space, ID, Kind, Content string
	Revision                                         int64
}
type reviewCachedScore struct {
	Score   float64
	Expires time.Time
}
type reviewScoreFlight struct {
	Done  chan struct{}
	Score float64
	OK    bool
}

// Only identity metadata, hashes and scalar advice are retained; no candidate text or vectors are cached.
// Authorization and transfer checks still run before every cache lookup.
type reviewScoreCache struct {
	mu      sync.Mutex
	values  map[reviewScoreKey]reviewCachedScore
	pending map[reviewScoreKey]*reviewScoreFlight
}
type reviewScorePlan struct {
	keys    []reviewScoreKey
	scores  map[reviewScoreKey]float64
	owned   map[reviewScoreKey]*reviewScoreFlight
	waiting map[reviewScoreKey]*reviewScoreFlight
}

func (c *reviewScoreCache) plan(keys []reviewScoreKey, now time.Time) *reviewScorePlan {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[reviewScoreKey]reviewCachedScore{}
		c.pending = map[reviewScoreKey]*reviewScoreFlight{}
	}
	for key, value := range c.values {
		if !value.Expires.After(now) {
			delete(c.values, key)
		}
	}
	p := &reviewScorePlan{keys: keys, scores: map[reviewScoreKey]float64{}, owned: map[reviewScoreKey]*reviewScoreFlight{}, waiting: map[reviewScoreKey]*reviewScoreFlight{}}
	for _, key := range keys {
		if value, ok := c.values[key]; ok {
			p.scores[key] = value.Score
			continue
		}
		if flight := c.pending[key]; flight != nil {
			p.waiting[key] = flight
			continue
		}
		if len(c.pending) >= reviewScoreCacheLimit {
			c.completeLocked(p.owned, nil, now)
			return nil
		}
		flight := &reviewScoreFlight{Done: make(chan struct{})}
		c.pending[key] = flight
		p.owned[key] = flight
	}
	return p
}
func (c *reviewScoreCache) complete(owned map[reviewScoreKey]*reviewScoreFlight, scores map[reviewScoreKey]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completeLocked(owned, scores, time.Now())
}
func (c *reviewScoreCache) completeLocked(owned map[reviewScoreKey]*reviewScoreFlight, scores map[reviewScoreKey]float64, now time.Time) {
	for key, flight := range owned {
		if c.pending[key] != flight {
			continue
		}
		score, ok := scores[key]
		flight.Score, flight.OK = score, ok && !math.IsNaN(score) && !math.IsInf(score, 0)
		if flight.OK {
			c.evictOldestLocked()
			c.values[key] = reviewCachedScore{Score: score, Expires: now.Add(reviewScoreCacheTTL)}
		}
		delete(c.pending, key)
		close(flight.Done)
	}
}
func (c *reviewScoreCache) evictOldestLocked() {
	if len(c.values) < reviewScoreCacheLimit {
		return
	}
	var oldest reviewScoreKey
	var expires time.Time
	for key, value := range c.values {
		if expires.IsZero() || value.Expires.Before(expires) {
			oldest, expires = key, value.Expires
		}
	}
	delete(c.values, oldest)
}
func (p *reviewScorePlan) results(ctx context.Context, computed map[reviewScoreKey]float64) ([]float64, bool) {
	for key, flight := range p.waiting {
		select {
		case <-ctx.Done():
			return nil, false
		case <-flight.Done:
		}
		if !flight.OK {
			return nil, false
		}
		p.scores[key] = flight.Score
	}
	for key, score := range computed {
		p.scores[key] = score
	}
	out := make([]float64, len(p.keys))
	for i, key := range p.keys {
		score, ok := p.scores[key]
		if !ok {
			return nil, false
		}
		out[i] = score
	}
	return out, true
}
