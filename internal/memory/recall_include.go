package memory

import (
	"context"
	"errors"
	"slices"
)

// Bounds for what include adds to each memory hit. The extras sit outside max_chars and
// the token budget (documented on the API). Without include_options: at most 3 traced
// sources (one checked read transaction per hit, limit <= 50) of 300 runes, 10 entity
// names per hit, and source_facts within 4096 tokens. include_options.<part>.max_tokens
// spends a cl100k token budget over all hits instead (sources: excerpts up to 4000 runes;
// entities: also each entity's approved summary and approved observations).
const (
	includeSourcesMax     = 3
	includeExcerptMax     = 300
	includeEntitiesMax    = 10
	includeBudgetRunesMax = 4000 // one excerpt, summary or fact under a token budget
	includeTokensMax      = 16384
	includeFactsDefault   = 4096
	includeFactsPerHit    = 8 // source memories read per observation hit
	includeEntityFactsMax = 5 // approved observations per entity
)

var includeParts = []string{"sources", "entities", "source_facts"}

// IncludeOptions give an include part a token budget; a part named here must be in include.
type IncludeOptions struct {
	Sources     *IncludeBudget `json:"sources,omitempty"`
	Entities    *IncludeBudget `json:"entities,omitempty"`
	SourceFacts *IncludeBudget `json:"source_facts,omitempty"`
}

// IncludeBudget is a part's token budget over all hits; MaxTokensPerObservation
// (source_facts only) also caps what one observation hit gets.
type IncludeBudget struct {
	MaxTokens               int `json:"max_tokens"`
	MaxTokensPerObservation int `json:"max_tokens_per_observation,omitempty"`
}

// HitEntity is a named entity an included hit mentions; Summary and Observations are
// filled only under an entities token budget.
type HitEntity struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Summary      string         `json:"summary,omitempty"`
	Observations []IncludedFact `json:"observations,omitempty"`
}

// IncludedFact is a bounded text an include part attached: an observation hit's source
// memory (source_facts) or an approved observation about an entity.
type IncludedFact struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func validInclude(include []string, opts *IncludeOptions) bool {
	for i, inc := range include {
		if !slices.Contains(includeParts, inc) || slices.Contains(include[:i], inc) {
			return false
		}
	}
	if opts == nil {
		return true
	}
	part := func(b *IncludeBudget, name string) bool {
		return b == nil || slices.Contains(include, name) && b.MaxTokens >= 1 && b.MaxTokens <= includeTokensMax &&
			(b.MaxTokensPerObservation == 0 || name == "source_facts" && b.MaxTokensPerObservation >= 1 && b.MaxTokensPerObservation <= b.MaxTokens)
	}
	return part(opts.Sources, "sources") && part(opts.Entities, "entities") && part(opts.SourceFacts, "source_facts")
}

// budget is the part's token packer, nil when the part has no budget (and no default).
func (in SearchInput) includeBudget(part string) (*tokenPacker, int, error) {
	var b *IncludeBudget
	if o := in.IncludeOptions; o != nil {
		b = map[string]*IncludeBudget{"sources": o.Sources, "entities": o.Entities, "source_facts": o.SourceFacts}[part]
	}
	if b == nil && part == "source_facts" {
		b = &IncludeBudget{MaxTokens: includeFactsDefault}
	}
	if b == nil {
		return nil, 0, nil
	}
	p, err := newTokenPacker(b.MaxTokens, 0)
	return p, b.MaxTokensPerObservation, err
}

// attachIncludes adds what in.Include asks for to the memory hits of out. Sources come
// from the same checked trace as GET /v1/memories/{id}/sources (per-source authorization,
// audit row); entities from the same view as the entity reads (agents see confirmed ones),
// their observations from the same read as GET /v1/observations; source facts were read
// and cleaned like any search hit before the search's checks (collectSourceFacts).
// A hit or a part the caller may not read is left without it, never an error.
func (s *Service) attachIncludes(ctx context.Context, token string, in SearchInput, out *ContextBundle) error {
	if slices.Contains(in.Include, "sources") {
		budget, _, err := in.includeBudget("sources")
		if err != nil {
			return err
		}
		for i := range out.Items {
			if err := s.attachSources(ctx, token, in.Scope, &out.Items[i], budget); err != nil {
				return err
			}
		}
	}
	if slices.Contains(in.Include, "source_facts") {
		if err := attachSourceFacts(in, out); err != nil {
			return err
		}
	}
	if slices.Contains(in.Include, "entities") {
		return s.attachEntities(ctx, token, in, out.Items)
	}
	return nil
}

// withIncludes is attachIncludes for the end of a search: a failure returns no bundle.
func (s *Service) withIncludes(ctx context.Context, token string, in SearchInput, out ContextBundle, d Decision) (ContextBundle, Decision, error) {
	if err := s.attachIncludes(ctx, token, in, &out); err != nil {
		return ContextBundle{}, Decision{}, err
	}
	return out, d, nil
}

// collectSourceFacts reads the source memories of each observation hit and passes them
// through the search's own clean step (liveness, approval, filters, per-hit authorization),
// so a source fact is exactly a memory this search could have returned.
func (s *Service) collectSourceFacts(ctx context.Context, in SearchInput, out *ContextBundle, clean func([]Hit) ([]Hit, error)) error {
	if !slices.Contains(in.Include, "source_facts") {
		return nil
	}
	out.sourceFacts = map[string][]Hit{}
	for _, h := range out.Items {
		if !h.Derived {
			continue
		}
		hits := []Hit{}
		for _, id := range h.SourceIDs[:min(len(h.SourceIDs), includeFactsPerHit)] {
			if r, err := s.Repo.Get(ctx, colMemories, in.Scope, id); err == nil {
				hits = append(hits, searchHit(r, colMemories, "source_fact"))
			}
		}
		cleaned, err := clean(hits)
		if err != nil {
			return err
		}
		out.sourceFacts[hitKey(h)] = cleaned
	}
	return nil
}

// attachSourceFacts packs the collected facts in hit order under the total budget and,
// when set, the per-observation one; a fact that does not fit is left out.
func attachSourceFacts(in SearchInput, out *ContextBundle) error {
	total, perObs, err := in.includeBudget("source_facts")
	if err != nil {
		return err
	}
	for i := range out.Items {
		h := &out.Items[i]
		var own *tokenPacker
		if perObs > 0 {
			own = &tokenPacker{codec: total.codec, cap: perObs}
		}
		for _, f := range out.sourceFacts[hitKey(*h)] {
			fact := IncludedFact{ID: f.ID, Text: runeCutString(redactSessionPrivate(f.Text), includeBudgetRunesMax)}
			if own != nil && own.count(fact)+own.used > own.cap {
				continue
			}
			if !total.fits(fact) {
				continue
			}
			if own != nil {
				own.fits(fact)
			}
			h.SourceFacts = append(h.SourceFacts, fact)
		}
	}
	return nil
}

// skippable: the part is unreadable or absent for this caller, not a failure.
func skippable(err error) bool {
	return errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnavailable)
}

func (s *Service) attachSources(ctx context.Context, token string, scope Scope, h *Hit, budget *tokenPacker) error {
	if h.Collection != colMemories || h.Origin != "memory" {
		return nil
	}
	trace, err := s.traceRecordSources(ctx, token, scope, h.ID, colMemories, includeSourcesMax)
	if skippable(err) {
		return nil
	}
	if err != nil {
		return err
	}
	h.SourceTraces = trace.Sources
	for i := range h.SourceTraces {
		t := &h.SourceTraces[i]
		t.ArchiveMessages = nil
		limit := includeExcerptMax
		if budget != nil {
			limit = includeBudgetRunesMax
		}
		t.Excerpt = runeCutString(redactSessionPrivate(t.Excerpt), limit)
		// A restricted memory's evidence text stays out, as does anything that looks secret
		// or past the token budget.
		if h.Sensitivity == "restricted" || !safeTransfer(t.Excerpt) || budget != nil && !budget.fits(t.Excerpt) {
			t.Excerpt = ""
		}
	}
	return nil
}

func (s *Service) attachEntities(ctx context.Context, token string, in SearchInput, items []Hit) error {
	v, err := s.entityReader(ctx, token, in.Scope, "")
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
	budget, _, err := in.includeBudget("entities")
	if budget == nil || err != nil {
		return err
	}
	return s.attachEntityFacts(ctx, token, in.Scope, v, items, budget)
}

// attachEntityFacts fills, in hit order and under the budget, each attached entity's
// approved summary and up to includeEntityFactsMax approved observations written from
// memories that mention it. Observations need the caller's observation read grant.
func (s *Service) attachEntityFacts(ctx context.Context, token string, scope Scope, v *entityView, items []Hit, budget *tokenPacker) error {
	byEntity, err := s.entityObservations(ctx, token, scope, v)
	if err != nil {
		return err
	}
	for i := range items {
		for j := range items[i].Entities {
			x := &items[i].Entities[j]
			if e := v.entity(x.ID); e != nil && e.Summary != nil && safeTransfer(e.Summary.Text) {
				if text := runeCutString(e.Summary.Text, includeBudgetRunesMax); budget.fits(text) {
					x.Summary = text
				}
			}
			for _, f := range byEntity[x.ID] {
				if safeTransfer(f.Text) && budget.fits(f) {
					x.Observations = append(x.Observations, f)
				}
			}
		}
	}
	return nil
}

// entityObservations maps entity ids to the approved, recallable observations written from
// memories that mention them (up to includeEntityFactsMax each); none without read:observations.
func (s *Service) entityObservations(ctx context.Context, token string, scope Scope, v *entityView) (map[string][]IncludedFact, error) {
	byEntity := map[string][]IncludedFact{}
	if _, err := s.authorize(ctx, token, "read:"+colObservations, scope, "", ""); err != nil {
		if skippable(err) {
			return byEntity, nil
		}
		return nil, err
	}
	rows, err := s.Repo.ListRecordsByStatus(ctx, scope, colObservations, obsActive, obsListLimit)
	if err != nil {
		return nil, err
	}
	mentioned := map[string][]string{} // memory id -> entity ids
	for _, m := range v.mentions {
		mentioned[m.MemoryID] = append(mentioned[m.MemoryID], m.EntityID)
	}
	for _, r := range rows {
		if r.Scope != scope || r.Observation == nil || r.PrivacyQuarantined || r.Sensitivity == "restricted" || !observationRecallable(ctx, s.Repo, r) {
			continue
		}
		fact := IncludedFact{ID: r.ID, Text: runeCutString(redactSessionPrivate(r.Text), includeBudgetRunesMax)}
		for _, src := range r.Observation.SourceMemories {
			for _, id := range mentioned[src.ID] {
				if len(byEntity[id]) < includeEntityFactsMax && !slices.Contains(byEntity[id], fact) {
					byEntity[id] = append(byEntity[id], fact)
				}
			}
		}
	}
	return byEntity, nil
}
