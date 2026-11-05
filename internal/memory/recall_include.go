package memory

import (
	"context"
	"errors"
	"slices"
)

// Bounds for what include adds to each memory hit; they sit outside max_chars and the token budget.
const (
	includeSourcesMax  = 3
	includeExcerptMax  = 300
	includeEntitiesMax = 10
)

// HitEntity is a named entity an included hit mentions.
type HitEntity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// attachIncludes adds what in.Include asks for to the memory hits of out. Sources come
// from the same checked trace as GET /v1/memories/{id}/sources (per-source authorization,
// audit row); entities from the same view as the entity reads (agents see confirmed ones).
// A hit or a part the caller may not read is left without it, never an error.
func (s *Service) attachIncludes(ctx context.Context, token string, in SearchInput, out *ContextBundle) error {
	if slices.Contains(in.Include, "sources") {
		for i := range out.Items {
			if err := s.attachSources(ctx, token, in.Scope, &out.Items[i]); err != nil {
				return err
			}
		}
	}
	if slices.Contains(in.Include, "entities") {
		return s.attachEntities(ctx, token, in.Scope, out.Items)
	}
	return nil
}

func validInclude(include []string) bool {
	for i, inc := range include {
		if inc != "sources" && inc != "entities" || slices.Contains(include[:i], inc) {
			return false
		}
	}
	return len(include) <= 2
}

// withIncludes is attachIncludes for the end of a search: a failure returns no bundle.
func (s *Service) withIncludes(ctx context.Context, token string, in SearchInput, out ContextBundle, d Decision) (ContextBundle, Decision, error) {
	if err := s.attachIncludes(ctx, token, in, &out); err != nil {
		return ContextBundle{}, Decision{}, err
	}
	return out, d, nil
}

// skippable: the part is unreadable or absent for this caller, not a failure.
func skippable(err error) bool {
	return errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnavailable)
}

func (s *Service) attachSources(ctx context.Context, token string, scope Scope, h *Hit) error {
	if h.Collection != colMemories || h.Origin != "memory" {
		return nil
	}
	trace, err := s.traceRecordSources(ctx, token, scope, h.ID, colMemories)
	if skippable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	h.SourceTraces = trace.Sources[:min(len(trace.Sources), includeSourcesMax)]
	for i := range h.SourceTraces {
		t := &h.SourceTraces[i]
		t.ArchiveMessages = nil
		t.Excerpt = runeCutString(redactSessionPrivate(t.Excerpt), includeExcerptMax)
		// A restricted memory's evidence text stays out, as does anything that looks secret.
		if h.Sensitivity == "restricted" || !safeTransfer(t.Excerpt) {
			t.Excerpt = ""
		}
	}
	return nil
}

func (s *Service) attachEntities(ctx context.Context, token string, scope Scope, items []Hit) error {
	v, err := s.entityReader(ctx, token, scope, "")
	if skippable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	at := map[string]int{}
	for i, h := range items {
		if h.Collection == colMemories && h.Origin == "memory" {
			at[h.ID] = i
		}
	}
	for _, m := range v.mentions {
		i, ok := at[m.MemoryID]
		e := v.entity(m.EntityID)
		if !ok || e == nil || len(items[i].Entities) >= includeEntitiesMax || slices.ContainsFunc(items[i].Entities, func(x HitEntity) bool { return x.ID == e.ID }) {
			continue
		}
		items[i].Entities = append(items[i].Entities, HitEntity{ID: e.ID, Name: e.Name, Type: e.Type})
	}
	return nil
}
