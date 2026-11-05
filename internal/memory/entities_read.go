package memory

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

const (
	entityListDefault    = 50
	entityListMax        = 100
	entityDetailMemories = 20
	entityRelatedMax     = 20
	entityGraphNodesMax  = 100
	entityGraphEdgesMax  = 500
)

type EntitySummary struct {
	NamedEntity
	Mentions         int        `json:"mentions"`
	ProposedMentions int        `json:"proposed_mentions"`
	FirstSeen        *time.Time `json:"first_seen,omitempty"`
	LastSeen         *time.Time `json:"last_seen,omitempty"`
}

type EntityListInput struct {
	Scope  Scope
	Query  string
	Type   string
	Limit  int
	Cursor string
}

type EntityList struct {
	Items      []EntitySummary `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	Total      int             `json:"total"`
	Truncated  bool            `json:"truncated,omitempty"`
}

type EntityMemoryRef struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Relation string    `json:"relation"`
	EventAt  time.Time `json:"event_at"`
	Snippet  string    `json:"snippet,omitempty"`
}

type RelatedEntity struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Weight int    `json:"weight"`
}

type EntityTimelineBucket struct {
	Month string `json:"month"`
	Count int    `json:"count"`
}

type EntityDetail struct {
	EntitySummary
	Memories []EntityMemoryRef      `json:"memories"`
	Related  []RelatedEntity        `json:"related"`
	Timeline []EntityTimelineBucket `json:"timeline"`
}

type EntityGraphInput struct {
	Scope    Scope
	Type     string
	From, To *time.Time
	Limit    int
}

type EntityGraphNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Mentions int    `json:"mentions"`
}

type EntityGraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Weight int    `json:"weight"`
}

type EntityGraph struct {
	Nodes     []EntityGraphNode `json:"nodes"`
	Edges     []EntityGraphEdge `json:"edges"`
	Truncated bool              `json:"truncated,omitempty"`
}

// entityView is what readers see: people also see proposals; agents only
// confirmed entities and edges.
type entityView struct {
	g        *entityGraph
	human    bool
	mentions []EntityMention
}

func (s *Service) entityReader(ctx context.Context, token string, scope Scope, id string) (*entityView, error) {
	if !validScope(scope) || id != "" && !graphID.MatchString(id) {
		return nil, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "read:entities", scope, id, "")
	if err != nil {
		return nil, err
	}
	store, err := s.entityStore()
	if err != nil {
		return nil, err
	}
	g, err := loadEntityGraph(ctx, store, scope)
	if err != nil {
		return nil, err
	}
	v := &entityView{g: g, human: d.Role == roleHuman}
	for _, m := range g.mentions {
		if e := v.entity(m.EntityID); e != nil && (v.human || m.Relation == relMentions) {
			v.mentions = append(v.mentions, m)
		}
	}
	return v, nil
}

func (v *entityView) entity(id string) *NamedEntity {
	e := v.g.entities[id]
	if e == nil || e.Deleted || e.Proposed && !v.human {
		return nil
	}
	return e
}

func publicEntity(e NamedEntity) NamedEntity {
	e.Keys, e.Deleted = nil, false
	if e.Aliases == nil {
		e.Aliases = []string{}
	}
	return e
}

func (v *entityView) summaries() map[string]*EntitySummary {
	out := map[string]*EntitySummary{}
	for id, e := range v.g.entities {
		if v.entity(id) != nil {
			out[id] = &EntitySummary{NamedEntity: publicEntity(*e)}
		}
	}
	for _, m := range v.mentions {
		s := out[m.EntityID]
		if m.Relation == relMentionsProposed {
			s.ProposedMentions++
			continue
		}
		s.Mentions++
		at := m.EventAt
		if s.FirstSeen == nil || at.Before(*s.FirstSeen) {
			s.FirstSeen = &at
		}
		if s.LastSeen == nil || at.After(*s.LastSeen) {
			s.LastSeen = &at
		}
	}
	return out
}

func entityMatches(e NamedEntity, query, typ string) bool {
	if typ != "" && e.Type != typ {
		return false
	}
	if query == "" {
		return true
	}
	return slices.ContainsFunc(append([]string{e.Name}, e.Aliases...), func(n string) bool { return strings.Contains(strings.ToLower(n), query) })
}

// ListEntities pages a scope's entities, most mentioned first.
func (s *Service) ListEntities(ctx context.Context, token string, in EntityListInput) (EntityList, error) {
	offset, ok := parseOffsetCursor(in.Cursor, math.MaxInt-entityListMax)
	in.Limit = cmp.Or(in.Limit, entityListDefault)
	if !ok || in.Limit < 1 || in.Limit > entityListMax || len(in.Query) > 200 || in.Type != "" && !entityTypes[in.Type] {
		return EntityList{}, ErrInvalid
	}
	v, err := s.entityReader(ctx, token, in.Scope, "")
	if err != nil {
		return EntityList{}, err
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))
	items := []EntitySummary{}
	for _, sum := range v.summaries() {
		if entityMatches(sum.NamedEntity, query, in.Type) {
			items = append(items, *sum)
		}
	}
	slices.SortFunc(items, func(a, b EntitySummary) int {
		return cmp.Or(b.Mentions-a.Mentions, strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID))
	})
	page, next := offsetPage(items, offset, in.Limit)
	return EntityList{Items: page, Total: len(items), Truncated: v.g.truncated, NextCursor: next}, nil
}

// GetEntity is one entity page: mentioning memories, related entities, timeline.
func (s *Service) GetEntity(ctx context.Context, token string, scope Scope, id string) (EntityDetail, error) {
	v, err := s.entityReader(ctx, token, scope, id)
	if err != nil {
		return EntityDetail{}, err
	}
	sum := v.summaries()[id]
	if sum == nil {
		return EntityDetail{}, ErrNotFound
	}
	out := EntityDetail{EntitySummary: *sum, Memories: []EntityMemoryRef{}, Related: v.related(id), Timeline: []EntityTimelineBucket{}}
	var own []EntityMention
	months := map[string]int{}
	for _, m := range v.mentions {
		if m.EntityID == id {
			own = append(own, m)
			months[m.EventAt.UTC().Format("2006-01")]++
		}
	}
	for _, month := range slices.Sorted(maps.Keys(months)) {
		out.Timeline = append(out.Timeline, EntityTimelineBucket{Month: month, Count: months[month]})
	}
	slices.SortFunc(own, func(a, b EntityMention) int { return b.EventAt.Compare(a.EventAt) })
	for i, m := range own {
		if len(out.Memories) == entityDetailMemories || i == 3*entityDetailMemories {
			break
		}
		ref, ok, err := s.entityMemoryRef(ctx, token, scope, sum.NamedEntity, m)
		if err != nil {
			return EntityDetail{}, err
		}
		if ok {
			out.Memories = append(out.Memories, ref)
		}
	}
	return out, nil
}

// entityMemoryRef reads the memory as the caller; unreadable or withheld ones are skipped.
func (s *Service) entityMemoryRef(ctx context.Context, token string, scope Scope, e NamedEntity, m EntityMention) (EntityMemoryRef, bool, error) {
	r, err := s.Get(ctx, token, colMemories, scope, m.MemoryID)
	if errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return EntityMemoryRef{}, false, nil
	}
	if err != nil {
		return EntityMemoryRef{}, false, err
	}
	if !entityExtractable(r) {
		return EntityMemoryRef{}, false, nil
	}
	ref := EntityMemoryRef{ID: r.ID, Kind: r.Kind, Relation: m.Relation, EventAt: m.EventAt}
	for _, name := range append([]string{e.Name}, e.Aliases...) {
		if hit, ok := usageSearchSnippet("", r.Text, name); ok {
			ref.Snippet = hit.Snippet
			break
		}
	}
	return ref, true, nil
}

// memorySets maps each memory to the entities it mentions (confirmed edges only).
func (v *entityView) memorySets(keep func(EntityMention) bool) map[string][]string {
	sets := map[string][]string{}
	for _, m := range v.mentions {
		if m.Relation == relMentions && keep(m) && !slices.Contains(sets[m.MemoryID], m.EntityID) {
			sets[m.MemoryID] = append(sets[m.MemoryID], m.EntityID)
		}
	}
	return sets
}

// related weights other entities by how many memories mention both.
func (v *entityView) related(id string) []RelatedEntity {
	weights := map[string]int{}
	for _, set := range v.memorySets(func(EntityMention) bool { return true }) {
		if !slices.Contains(set, id) {
			continue
		}
		for _, other := range set {
			if other != id {
				weights[other]++
			}
		}
	}
	out := []RelatedEntity{}
	for other, w := range weights {
		e := v.g.entities[other]
		out = append(out, RelatedEntity{ID: other, Name: e.Name, Type: e.Type, Weight: w})
	}
	slices.SortFunc(out, func(a, b RelatedEntity) int { return cmp.Or(b.Weight-a.Weight, strings.Compare(a.ID, b.ID)) })
	return out[:min(len(out), entityRelatedMax)]
}

// EntityGraphView is the co-occurrence graph: the most mentioned entities and
// edges weighted by the memories that mention both, bounded in size.
func (s *Service) EntityGraphView(ctx context.Context, token string, in EntityGraphInput) (EntityGraph, error) {
	in.Limit = cmp.Or(in.Limit, entityListDefault)
	if in.Limit < 1 || in.Limit > entityGraphNodesMax || in.Type != "" && !entityTypes[in.Type] || in.From != nil && in.To != nil && in.To.Before(*in.From) {
		return EntityGraph{}, ErrInvalid
	}
	v, err := s.entityReader(ctx, token, in.Scope, "")
	if err != nil {
		return EntityGraph{}, err
	}
	inWindow := func(m EntityMention) bool {
		return (in.From == nil || !m.EventAt.Before(*in.From)) && (in.To == nil || !m.EventAt.After(*in.To)) &&
			(in.Type == "" || v.g.entities[m.EntityID].Type == in.Type)
	}
	sets := v.memorySets(inWindow)
	counts := map[string]int{}
	for _, set := range sets {
		for _, id := range set {
			counts[id]++
		}
	}
	out := EntityGraph{Nodes: []EntityGraphNode{}, Edges: []EntityGraphEdge{}, Truncated: v.g.truncated || len(counts) > in.Limit}
	for id, n := range counts {
		e := v.g.entities[id]
		out.Nodes = append(out.Nodes, EntityGraphNode{ID: id, Name: e.Name, Type: e.Type, Mentions: n})
	}
	slices.SortFunc(out.Nodes, func(a, b EntityGraphNode) int { return cmp.Or(b.Mentions-a.Mentions, strings.Compare(a.ID, b.ID)) })
	out.Nodes = out.Nodes[:min(len(out.Nodes), in.Limit)]
	out.Edges = graphEdges(sets, out.Nodes)
	if len(out.Edges) > entityGraphEdgesMax {
		out.Edges, out.Truncated = out.Edges[:entityGraphEdgesMax], true
	}
	return out, nil
}

func graphEdges(sets map[string][]string, nodes []EntityGraphNode) []EntityGraphEdge {
	kept := map[string]bool{}
	for _, n := range nodes {
		kept[n.ID] = true
	}
	weights := map[[2]string]int{}
	for _, set := range sets {
		for i, a := range set {
			for _, b := range set[i+1:] {
				if kept[a] && kept[b] {
					weights[[2]string{min(a, b), max(a, b)}]++
				}
			}
		}
	}
	edges := []EntityGraphEdge{}
	for pair, w := range weights {
		edges = append(edges, EntityGraphEdge{Source: pair[0], Target: pair[1], Weight: w})
	}
	slices.SortFunc(edges, func(a, b EntityGraphEdge) int {
		return cmp.Or(b.Weight-a.Weight, strings.Compare(a.Source, b.Source), strings.Compare(a.Target, b.Target))
	})
	return edges
}
