package memory

import (
	"context"
	"slices"
	"time"
)

type StatGroup struct {
	Total    int64            `json:"total"`
	ByKind   map[string]int64 `json:"by_kind,omitempty"`
	ByStatus map[string]int64 `json:"by_status"`
}

type ScopeStats struct {
	Scope             Scope     `json:"scope"`
	Memories          StatGroup `json:"memories"`
	PendingCandidates int64     `json:"pending_candidates"`
	Sources           StatGroup `json:"sources"`
	// Collections holds optional collections (observations, entities) when this build stores them.
	Collections map[string]StatGroup `json:"collections,omitempty"`
}

// StatsTimeseriesInput asks for ingestion counts of one collection per bucket.
type StatsTimeseriesInput struct {
	Scope      Scope
	Collection string
	Bucket     string
	By         string
	From, To   time.Time
}

type StatsTimeseries struct {
	Collection string     `json:"collection"`
	Bucket     string     `json:"bucket"`
	By         string     `json:"by"`
	From       time.Time  `json:"from"`
	To         time.Time  `json:"to"`
	Points     []OpsCount `json:"points"`
}

// optionalStatCollections are counted only when Collections lists them.
var optionalStatCollections = []string{colObservations, colMentalModels, "entities"}

var timeseriesCollections = []string{colMemories, "memory_candidates", "sources"}

func (s *Service) statGroup(ctx context.Context, st opsStore, scope Scope, collection string, byKind bool) (StatGroup, error) {
	rows, err := st.OpsCounts(ctx, opsCountQuery{Scope: scope, Collection: collection, By: "status"})
	if err != nil {
		return StatGroup{}, err
	}
	g := StatGroup{}
	g.ByStatus, g.Total = opsCountMap(rows)
	if byKind {
		if rows, err = st.OpsCounts(ctx, opsCountQuery{Scope: scope, Collection: collection, By: "kind"}); err != nil {
			return StatGroup{}, err
		}
		g.ByKind, _ = opsCountMap(rows)
	}
	return g, nil
}

// Stats counts a scope's memories, waiting candidates and sources.
func (s *Service) Stats(ctx context.Context, token string, scope Scope) (ScopeStats, error) {
	out := ScopeStats{Scope: scope}
	// An agent reads the counts of a space it may read (its MCP space stats); people and services also need ops:metrics.
	d, err := s.opsAuthorize(ctx, token, "read:memories", scope, "", roleHuman, roleService, roleAgent)
	if err == nil && d.Role != roleAgent {
		_, err = s.opsAuthorize(ctx, token, "ops:metrics", scope, "", roleHuman, roleService)
	}
	if err != nil {
		return out, err
	}
	st, err := s.opsStore()
	if err != nil {
		return out, err
	}
	if out.Memories, err = s.statGroup(ctx, st, scope, colMemories, true); err != nil {
		return out, err
	}
	candidates, err := s.statGroup(ctx, st, scope, "memory_candidates", false)
	if err != nil {
		return out, err
	}
	out.PendingCandidates = candidates.ByStatus["candidate"]
	if out.Sources, err = s.statGroup(ctx, st, scope, "sources", true); err != nil {
		return out, err
	}
	for _, c := range optionalStatCollections {
		if !slices.Contains(Collections, c) {
			continue
		}
		g, err := s.statGroup(ctx, st, scope, c, true)
		if err != nil {
			return out, err
		}
		if out.Collections == nil {
			out.Collections = map[string]StatGroup{}
		}
		out.Collections[c] = g
	}
	return out, nil
}

// StatsTimeseries counts rows created per day or week, split by kind, status or source.
func (s *Service) StatsTimeseries(ctx context.Context, token string, in StatsTimeseriesInput) (StatsTimeseries, error) {
	if in.Collection == "" {
		in.Collection = colMemories
	}
	if in.Bucket == "" {
		in.Bucket = "day"
	}
	if in.By == "" {
		in.By = "kind"
	}
	out := StatsTimeseries{Collection: in.Collection, Bucket: in.Bucket, By: in.By, From: in.From, To: in.To, Points: []OpsCount{}}
	if !slices.Contains(timeseriesCollections, in.Collection) || in.Bucket != "day" && in.Bucket != "week" ||
		!slices.Contains([]string{"kind", "status", "source"}, in.By) || in.From.IsZero() || in.To.IsZero() {
		return out, ErrInvalid
	}
	if _, err := s.opsAuthorize(ctx, token, "ops:metrics", in.Scope, "", roleHuman, roleService); err != nil {
		return out, err
	}
	st, err := s.opsStore()
	if err != nil {
		return out, err
	}
	rows, err := st.OpsCounts(ctx, opsCountQuery{Scope: in.Scope, Collection: in.Collection, By: in.By, Bucket: in.Bucket, From: in.From, To: in.To})
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		r.Key = opsText(r.Key)
		out.Points = append(out.Points, r)
	}
	return out, nil
}
