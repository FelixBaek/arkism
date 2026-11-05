package memory

import (
	"context"
	"time"
)

type EntityExtractInput struct {
	Scope  Scope `json:"scope"`
	DryRun bool  `json:"dry_run,omitempty"`
	Limit  int   `json:"limit,omitempty"`
	// Model adds a server-model pass; its findings stay proposals until a person confirms them.
	Model bool `json:"model,omitempty"`
}

type EntityFound struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	EntityID string `json:"entity_id"`
	New      bool   `json:"new,omitempty"`
	Relation string `json:"relation"`
}

type EntityExtractItem struct {
	MemoryID string        `json:"memory_id"`
	Found    []EntityFound `json:"found"`
}

type EntityExtractResult struct {
	DryRun          bool                `json:"dry_run"`
	Memories        int                 `json:"memories"`
	NewEntities     int                 `json:"new_entities"`
	AddedMentions   int                 `json:"added_mentions"`
	RemovedMentions int                 `json:"removed_mentions"`
	ModelCalls      int                 `json:"model_calls,omitempty"`
	Truncated       bool                `json:"truncated,omitempty"`
	Items           []EntityExtractItem `json:"items,omitempty"`
}

// memoryEntities is what one pass found in one memory.
type memoryEntities struct {
	Memory Record
	Names  []entityCandidate
	Origin string
}

const entityPreviewItems = 50

// ExtractEntities runs extraction over a scope's approved memories, newest
// first, bounded by limit. dry_run reports what would change and writes nothing.
func (s *Service) ExtractEntities(ctx context.Context, token string, in EntityExtractInput) (EntityExtractResult, error) {
	if in.Limit == 0 {
		in.Limit = 100
		if in.Model {
			in.Limit = entityModelMax
		}
	}
	// Model calls run inline in the request, so the model pass takes fewer memories.
	if in.Limit < 1 || in.Limit > entityExtractMax || in.Model && in.Limit > entityModelMax {
		return EntityExtractResult{}, ErrInvalid
	}
	d, err := s.entityEditor(ctx, token, in.Scope, "", roleService)
	if err != nil {
		return EntityExtractResult{}, err
	}
	ctx = bindRequestActor(ctx, d)
	if _, err = s.authorize(ctx, token, "read:memories", in.Scope, "", ""); err != nil {
		return EntityExtractResult{}, err
	}
	cfg, err := s.spaceSettings(ctx, in.Scope)
	if err != nil {
		return EntityExtractResult{}, err
	}
	rows, err := s.Repo.ListRecordsByStatus(ctx, in.Scope, colMemories, statusActive, in.Limit)
	if err != nil {
		return EntityExtractResult{}, err
	}
	var memories []Record
	var found []memoryEntities
	for _, r := range rows {
		if entityExtractable(r) && r.Scope == in.Scope {
			memories = append(memories, r)
			found = append(found, memoryEntityFinds(r, cfg)...)
		}
	}
	calls := 0
	if in.Model {
		var more []memoryEntities
		more, calls, err = s.modelEntityFinds(ctx, token, in.Scope, d, cfg, memories)
		if err != nil {
			return EntityExtractResult{}, err
		}
		found = append(found, more...)
	}
	out, err := s.applyEntityExtraction(ctx, in.Scope, d.PrincipalID, cfg, found, in.DryRun)
	out.ModelCalls = calls
	return out, err
}

// memoryEntityFinds is what the rules and the memory's stated entities give for one
// approved memory under the space settings. Each origin is always listed, so names
// a setting now leaves out lose their edges.
func memoryEntityFinds(r Record, cfg SpaceConfig) []memoryEntities {
	rule := memoryEntities{Memory: r, Origin: originRule}
	if cfg.freeFormEntities() {
		rule.Names = extractEntityCandidates(entityText(r))
	}
	return []memoryEntities{rule, {Memory: r, Names: cfg.inputEntityCandidates(r.Entities), Origin: originInput}}
}

// applyEntityExtraction resolves found names to entities and makes each
// memory's edges of that origin match what was found, in one transaction.
func (s *Service) applyEntityExtraction(ctx context.Context, scope Scope, actor string, cfg SpaceConfig, found []memoryEntities, dryRun bool) (EntityExtractResult, error) {
	var out EntityExtractResult
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = EntityExtractResult{DryRun: dryRun}
		store, err := entityTx(tx)
		if err != nil {
			return err
		}
		if err = store.LockNamedEntities(ctx, scope); err != nil {
			return err
		}
		g, err := loadEntityGraph(ctx, store, scope)
		if err != nil {
			return err
		}
		if g.truncated {
			// Deduplication needs every entity of the scope in view.
			return ErrUnavailable
		}
		plan := newEntityPlan(g, cfg.EntityResolution)
		for _, f := range found {
			// Names found anywhere in the batch exist before weak ones are linked.
			for _, c := range f.Names {
				if !c.Weak {
					plan.entityFor(c, f.Origin)
				}
			}
		}
		for _, f := range found {
			item := plan.apply(f)
			if f.Origin == originInput { // the memory's second pass: stated entities
				if len(f.Names) == 0 {
					continue
				}
			} else {
				out.Memories++
			}
			if dryRun && len(out.Items) < entityPreviewItems {
				out.Items = append(out.Items, item)
			}
		}
		out.NewEntities, out.AddedMentions, out.RemovedMentions = len(plan.created), len(plan.add), len(plan.remove)
		if dryRun {
			return nil
		}
		return plan.write(ctx, tx, store, scope, actor)
	})
	return out, err
}

type entityPlan struct {
	g             *entityGraph
	byMemory      map[string][]EntityMention
	created       []*NamedEntity
	add, remove   []EntityMention
	now           time.Time
	createdLookup map[string]bool
	resolution    string // entity_resolution; "" is the server default, propose
	// similarLeft bounds the name comparisons of one request (entitySimilarBudget).
	similarLeft int
}

func newEntityPlan(g *entityGraph, resolution string) *entityPlan {
	p := &entityPlan{g: g, byMemory: map[string][]EntityMention{}, now: time.Now().UTC(), createdLookup: map[string]bool{}, resolution: resolution, similarLeft: entitySimilarBudget}
	for _, m := range g.mentions {
		p.byMemory[m.MemoryID] = append(p.byMemory[m.MemoryID], m)
	}
	return p
}

func hasMention(list []EntityMention, entity, relation string) bool {
	for _, m := range list {
		if m.EntityID == entity && m.Relation == relation {
			return true
		}
	}
	return false
}

func (p *entityPlan) entityFor(c entityCandidate, origin string) *NamedEntity {
	e, skip := p.g.resolve(c.Name, c.Type)
	if skip || e != nil || c.Weak {
		return e
	}
	// Stated names are taken literally; found names may match a similar existing one.
	var similar []EntitySimilar
	if origin != originInput && p.resolution != entityResolveExact && p.similarLeft >= len(p.g.entities) {
		p.similarLeft -= len(p.g.entities)
		similar = p.g.similar(c.Name, c.Type)
		if len(similar) > 0 && similar[0].Score == 1 && p.resolution == entityResolveSimilar {
			return p.g.entities[similar[0].ID] // the same words; only case, punctuation or order differ
		}
	}
	e = &NamedEntity{ID: ID(), Name: c.Name, Type: c.Type, Aliases: []string{}, Proposed: origin == originModel, Similar: similar, CreatedAt: p.now, UpdatedAt: p.now}
	e.Keys = entityKeys(*e)
	p.g.add(e)
	p.created = append(p.created, e)
	p.createdLookup[e.ID] = true
	return e
}

func (p *entityPlan) apply(f memoryEntities) EntityExtractItem {
	r := f.Memory
	existing := p.byMemory[r.ID]
	item := EntityExtractItem{MemoryID: r.ID, Found: []EntityFound{}}
	var desired []EntityMention
	for _, c := range f.Names {
		e := p.entityFor(c, f.Origin)
		if e == nil {
			continue
		}
		rel := relMentions
		if f.Origin == originModel {
			if hasMention(existing, e.ID, relMentions) || hasMention(desired, e.ID, relMentions) {
				continue
			}
			rel = relMentionsProposed
		}
		if hasMention(desired, e.ID, rel) {
			continue
		}
		desired = append(desired, EntityMention{MemoryKind: memoryEntityKind(r), MemoryID: r.ID, EntityID: e.ID, Relation: rel, Origin: f.Origin, EventAt: memoryEventAt(r)})
		item.Found = append(item.Found, EntityFound{Name: c.Name, Type: e.Type, EntityID: e.ID, New: p.createdLookup[e.ID], Relation: rel})
	}
	kept := []EntityMention{}
	for _, m := range existing {
		if m.Origin == f.Origin && !hasMention(desired, m.EntityID, m.Relation) {
			p.remove = append(p.remove, m)
			continue
		}
		kept = append(kept, m)
	}
	for _, m := range desired {
		if !hasMention(kept, m.EntityID, m.Relation) {
			p.add = append(p.add, m)
			kept = append(kept, m)
		}
	}
	p.byMemory[r.ID] = kept
	return item
}

func (p *entityPlan) write(ctx context.Context, tx Tx, store namedEntityStore, scope Scope, actor string) error {
	for _, e := range p.created {
		if err := store.PutNamedEntity(ctx, scope, *e); err != nil {
			return err
		}
	}
	if len(p.add)+len(p.remove) == 0 {
		return nil
	}
	if err := store.ReplaceEntityMentions(ctx, scope, p.remove, p.add); err != nil {
		return err
	}
	return audit(ctx, tx, scope, actor, "extract:entities", ContentHash(structuredText(p.add)))
}
