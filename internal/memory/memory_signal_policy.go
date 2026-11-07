package memory

import (
	"context"
	"slices"
	"time"
)

// SignalPolicy opts into bounded, content-specific recent observations. Historical
// outcomes remain available; missing content attribution is never guessed.
type SignalPolicy struct {
	Disabled   bool   `json:"disabled,omitempty" bson:"disabled,omitempty"`
	Dimension  string `json:"dimension" bson:"dimension"`
	WindowDays int    `json:"window_days" bson:"window_days"`
	MinSamples int    `json:"min_samples" bson:"min_samples"`
}

func (p *SignalPolicy) valid() bool {
	return p == nil || (p.Disabled && p.Dimension == "" && p.WindowDays == 0 && p.MinSamples == 0) || (!p.Disabled && (p.Dimension == "work_task" || p.Dimension == "personal_compliance") && p.WindowDays >= 1 && p.WindowDays <= 3650 && p.MinSamples >= 3 && p.MinSamples <= 1000)
}
func signalContentHash(h Hit) string {
	c := copyMemoryContext(h.MemoryContext)
	if c != nil {
		c.ObservedAt = nil
	}
	rules := []string{}
	for _, tag := range h.Tags {
		if tag == "rule:always" || tag == "rule:never" {
			rules = append(rules, tag)
		}
	}
	slices.Sort(rules)
	rules = slices.Compact(rules)
	return ContentHash(structuredText(struct {
		Text, Summary, Kind string
		Context             *MemoryContext
		Rules               []string
	}{h.Text, h.Summary, h.Kind, c, rules}))
}

// signalRecordReader is optional so other stores keep their existing behavior.
type signalRecordReader interface {
	MemorySignalRecords(context.Context, Scope, []string) ([]Record, error)
}

func (p *Postgres) MemorySignalRecords(ctx context.Context, scope Scope, ids []string) ([]Record, error) {
	if checkScope(scope) != nil || len(ids) == 0 || len(ids) > memoryOutcomeBatchLimit {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colJobs, scope, `id=ANY($6) AND kind=$7`, []any{ids, memorySignalKind}, "id ASC", len(ids))
}
func (s *Service) signalRecords(ctx context.Context, scope Scope, rows []ScoredHit) map[string]Record {
	out := map[string]Record{}
	for start := 0; start < len(rows); start += memoryOutcomeBatchLimit {
		end := min(start+memoryOutcomeBatchLimit, len(rows))
		ids := []string{}
		wanted := map[string]string{}
		for _, r := range rows[start:end] {
			id := memorySignalID(scope, r.ID)
			if _, ok := wanted[id]; !ok {
				ids = append(ids, id)
				wanted[id] = r.ID
			}
		}
		records := []Record{}
		if reader, ok := s.Repo.(signalRecordReader); ok {
			var err error
			records, err = reader.MemorySignalRecords(ctx, scope, ids)
			if err != nil {
				continue
			}
		} else {
			for _, id := range ids {
				r, err := s.Repo.Get(ctx, colJobs, scope, id)
				if err == nil {
					records = append(records, r)
				}
			}
		}
		for _, r := range records {
			id, ok := wanted[r.ID]
			if ok && r.Scope == scope && r.Kind == memorySignalKind && r.MemorySignal != nil && r.MemorySignal.Memory.ID == id {
				out[id] = r
			}
		}
	}
	return out
}
func (s *Service) policyOutcomeWeights(ctx context.Context, in SearchInput, rows []ScoredHit) map[string]float64 {
	p := in.space.recallQuality().SignalPolicy
	if p == nil || p.Disabled {
		return s.outcomeWeights(ctx, in.Scope, rows)
	}
	if s.SignalRankingOff || !p.valid() {
		return nil
	}
	out := map[string]float64{}
	// Recovery replaces only independent evaluation weighting; retain host work observations.
	for id, r := range s.signalRecords(ctx, in.Scope, rows) {
		if w := workFactor(r.MemorySignal.Work); w != 1 {
			out[id] = w
		}
	}
	to := s.recallClock(in)
	from := to.Add(-time.Duration(p.WindowDays) * 24 * time.Hour)
	for start := 0; start < len(rows); start += memoryOutcomeBatchLimit {
		query := MemoryOutcomeQuery{IndependentFirst: true, Scope: in.Scope, Dimension: p.Dimension, From: &from, To: &to, ContentHashes: map[string]string{}}
		for _, r := range rows[start:min(start+memoryOutcomeBatchLimit, len(rows))] {
			if _, ok := query.ContentHashes[r.ID]; !ok {
				query.MemoryIDs = append(query.MemoryIDs, r.ID)
				query.ContentHashes[r.ID] = signalContentHash(r.Hit)
			}
		}
		counts, err := s.Repo.MemoryOutcomeCounts(ctx, query)
		if err != nil {
			continue
		}
		for _, c := range counts {
			if _, ok := query.ContentHashes[c.MemoryID]; !ok || c.Total < p.MinSamples || c.Passed < 0 || c.Passed > c.Total {
				continue
			}
			w := 1.0
			if existing, ok := out[c.MemoryID]; ok {
				w = existing
			}
			out[c.MemoryID] = w * (0.5 + float64(c.Passed+1)/float64(c.Total+2))
		}
	}
	return out
}
