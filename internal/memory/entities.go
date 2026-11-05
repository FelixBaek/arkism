package memory

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"
)

// Entity rows of kind named_entity carry their name, type and aliases; each
// memory that names one gets a relation memory -mentions-> entity. Model
// findings stay "mentions_proposed" until a person confirms the entity.
const (
	namedEntityKind     = "named_entity"
	relMentions         = "mentions"
	relMentionsProposed = "mentions_proposed"
	originRule          = "rule"
	originModel         = "model"
	originHuman         = "human"
	// originInput: names the caller stated on the candidate a person approved (EntityInput).
	originInput = "input"
	// shortcut: reads and writes load a scope's whole entity graph, bounded here; move the
	// aggregation into SQL when one scope outgrows these.
	entityScanLimit  = 5000
	mentionScanLimit = 20000
	entityExtractMax = 500
	entityApproveMax = 2 * time.Second
)

type NamedEntity struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Aliases  []string `json:"aliases"`
	Proposed bool     `json:"proposed,omitempty"`
	Deleted  bool     `json:"deleted,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	EditedBy string   `json:"edited_by,omitempty"`
	// Summary is the approved description; SummaryDraft is the model's cited draft a person reviews.
	Summary      *DerivedRevision `json:"summary,omitempty"`
	SummaryDraft *DerivedRevision `json:"summary_draft,omitempty"`
	// Similar are merge proposals: existing entities with a similar name (entities_similar.go).
	Similar   []EntitySimilar `json:"similar,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// EntityMention is one memory -mentions-> entity edge.
type EntityMention struct {
	MemoryKind string    `json:"memory_kind"`
	MemoryID   string    `json:"memory_id"`
	EntityID   string    `json:"entity_id"`
	Relation   string    `json:"relation"`
	Origin     string    `json:"origin"`
	EventAt    time.Time `json:"event_at"`
}

type namedEntityStore interface {
	// LockNamedEntities serializes entity writers of one scope until the transaction ends.
	LockNamedEntities(context.Context, Scope) error
	// NamedEntities returns up to limit entities of a scope, tombstones included.
	NamedEntities(context.Context, Scope, int) ([]NamedEntity, error)
	PutNamedEntity(context.Context, Scope, NamedEntity) error
	// DeleteNamedEntity removes the row; its mention edges go with it.
	DeleteNamedEntity(context.Context, Scope, string) error
	// EntityMentions returns up to limit mention edges (both relation kinds) of a scope.
	EntityMentions(context.Context, Scope, int) ([]EntityMention, error)
	ReplaceEntityMentions(ctx context.Context, scope Scope, remove, add []EntityMention) error
}

// entityGraph is a scope's entities and mention edges, loaded once per call.
type entityGraph struct {
	entities  map[string]*NamedEntity
	byKey     map[string][]*NamedEntity
	mentions  []EntityMention
	truncated bool
	grams     map[string]map[string]bool // trigram sets of names, filled by similar
}

func loadEntityGraph(ctx context.Context, store namedEntityStore, scope Scope) (*entityGraph, error) {
	ents, err := store.NamedEntities(ctx, scope, entityScanLimit+1)
	if err != nil {
		return nil, err
	}
	mentions, err := store.EntityMentions(ctx, scope, mentionScanLimit+1)
	if err != nil {
		return nil, err
	}
	g := &entityGraph{entities: map[string]*NamedEntity{}, byKey: map[string][]*NamedEntity{}}
	if len(ents) > entityScanLimit || len(mentions) > mentionScanLimit {
		g.truncated = true
		ents, mentions = ents[:min(len(ents), entityScanLimit)], mentions[:min(len(mentions), mentionScanLimit)]
	}
	for i := range ents {
		g.add(&ents[i])
	}
	g.mentions = mentions
	return g, nil
}

func (g *entityGraph) add(e *NamedEntity) {
	g.entities[e.ID] = e
	for _, k := range e.Keys {
		g.byKey[k] = append(g.byKey[k], e)
	}
}

// resolve finds the entity a name refers to: a live one of the same type, or
// of any type when either side is the generic "concept". A tombstone with the
// key means a person deleted that name, so nothing is returned and skip is set.
func (g *entityGraph) resolve(name, typ string) (e *NamedEntity, skip bool) {
	var loose *NamedEntity
	for _, c := range g.byKey[entityKey(name)] {
		switch {
		case c.Deleted:
			if c.Type == typ || typ == entityTypeConcept {
				skip = true
			}
		case c.Type == typ:
			return c, false
		case loose == nil && (typ == entityTypeConcept || c.Type == entityTypeConcept):
			loose = c
		}
	}
	if loose != nil {
		return loose, false
	}
	return nil, skip
}

func entityKeys(e NamedEntity) []string {
	keys := []string{entityKey(e.Name)}
	for _, a := range e.Aliases {
		if k := entityKey(a); !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

func memoryEntityKind(r Record) string {
	if r.Kind == "lesson" {
		return "lesson"
	}
	return "memory"
}

func memoryEventAt(r Record) time.Time {
	if r.MemoryContext != nil && r.MemoryContext.ObservedAt != nil {
		return r.MemoryContext.ObservedAt.UTC()
	}
	if r.EffectiveAt != nil {
		return r.EffectiveAt.UTC()
	}
	return r.CreatedAt.UTC()
}

// entityExtractable: only approved, readable memory content is a source.
func entityExtractable(r Record) bool {
	return r.Status == statusActive && r.ApprovedBy != "" && !r.ContentErased && !r.PrivacyQuarantined
}

func entityText(r Record) string {
	return r.Text + "\n" + r.Summary
}

func (s *Service) entityStore() (namedEntityStore, error) {
	store, ok := s.Repo.(namedEntityStore)
	if !ok {
		return nil, ErrUnavailable
	}
	return store, nil
}

func entityTx(tx Tx) (namedEntityStore, error) {
	store, ok := tx.(namedEntityStore)
	if !ok {
		return nil, ErrUnavailable
	}
	return store, nil
}

// entityEditor authorizes an entity change: people only, as the product rules require.
func (s *Service) entityEditor(ctx context.Context, token string, scope Scope, id string, roles ...string) (Decision, error) {
	if !validScope(scope) || id != "" && !graphID.MatchString(id) {
		return Decision{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:entities", scope, id, "")
	if err != nil {
		return d, err
	}
	if !slices.Contains(append([]string{roleHuman}, roles...), d.Role) {
		return Decision{}, ErrDenied
	}
	return d, nil
}

// afterMemoryApproval is the one hook for a memory that became active (a review,
// a person's approved -> active, a restore or a supersede): it counts toward
// automatic derivation (auto_derive.go) and runs the deterministic entity pass.
// It is best effort and time boxed: approval never fails or waits long on it.
func (s *Service) afterMemoryApproval(ctx context.Context, actor string, active *Record) {
	if active == nil {
		return
	}
	r := *active
	defer func() {
		if p := recover(); p != nil {
			slog.Warn("entity extraction after approval panicked", "memory", r.ID)
		}
	}()
	s.noteApprovedMemory(ctx, r.Scope)
	if _, err := s.entityStore(); err != nil || !entityExtractable(r) || ctx.Err() != nil {
		return // no entity store, nothing approved to read, or the request is over
	}
	ctx, cancel := context.WithTimeout(ctx, entityApproveMax)
	defer cancel()
	cfg, err := s.spaceSettings(ctx, r.Scope)
	if err != nil {
		slog.Warn("entity extraction after approval: space settings unreadable", "memory", r.ID, "error", err)
		return
	}
	if _, err = s.applyEntityExtraction(ctx, r.Scope, actor, cfg, memoryEntityFinds(r, cfg), false); err != nil && !errors.Is(err, ErrUnavailable) {
		slog.Warn("entity extraction after approval failed", "memory", r.ID, "error", err)
	}
}

// entityNameInputs states plain entity names (ingest options) as concept entities of a claim.
func entityNameInputs(names []string) []EntityInput {
	out := make([]EntityInput, 0, len(names))
	for _, n := range names {
		out = append(out, EntityInput{Name: n})
	}
	return out
}

func errEntityNotFound(g *entityGraph, id string) (*NamedEntity, error) {
	e := g.entities[id]
	if e == nil || e.Deleted {
		return nil, ErrNotFound
	}
	return e, nil
}
