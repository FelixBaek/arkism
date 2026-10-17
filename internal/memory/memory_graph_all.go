package memory

import (
	"context"
	"errors"
	"slices"
)

// ST-820 all mode: every visible memory of the scope and the lines between them, for the
// whole-map view. Same node rule and line rules as the center mode; nodes carry no text.
type MemoryGraphMapNode struct {
	ID            string `json:"id"`
	Reach         string `json:"reach"`
	Kind          string `json:"kind"`
	Uses          int    `json:"uses"`
	VectorPending bool   `json:"vector_pending"`
}
type MemoryGraphMapEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}
type MemoryGraphMap struct {
	Nodes     []MemoryGraphMapNode `json:"nodes"`
	Edges     []MemoryGraphMapEdge `json:"edges"`
	Truncated bool                 `json:"truncated"`
	Authority string               `json:"authority"`
}

func (s *Service) MemoryGraphMap(ctx context.Context, token string, in MemoryGraphQuery) (MemoryGraphMap, error) {
	in.Mode = MemoryGraphModeAll
	in, e := normalizeMemoryGraphQuery(in)
	if e != nil {
		return MemoryGraphMap{}, e
	}
	if s.Repo == nil {
		return MemoryGraphMap{}, ErrUnavailable
	}
	who, e := s.authorize(ctx, token, "read:memories", in.Scope, "", "")
	if e != nil {
		return MemoryGraphMap{}, e
	}
	for _, action := range []string{"read:memory_relations", "read:usage"} {
		d, e := s.authorize(ctx, token, action, in.Scope, "", "")
		if e != nil {
			return MemoryGraphMap{}, e
		}
		if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
			return MemoryGraphMap{}, ErrDenied
		}
	}
	rows, e := s.Repo.MemoryGraphMapNodes(ctx, in)
	if e != nil {
		return MemoryGraphMap{}, e
	}
	b := &memoryGraphBuild{s: s, token: token, who: who, in: in, nodes: map[string]Record{}, depth: map[string]int{}, seen: map[string]bool{}, deny: map[string]bool{}, cap: memoryGraphMapEdges, trunc: len(rows) > in.Limit}
	for _, r := range rows[:min(len(rows), in.Limit)] {
		if _, dup := b.nodes[r.ID]; dup || !memoryGraphVisible(r, in) {
			return MemoryGraphMap{}, ErrUnavailable
		}
		// shortcut: one authorizer call per node (5000 max) like ListMemories; batch it if the map gets slow.
		d, e := s.authorize(ctx, token, "read:memories", in.Scope, r.ID, structuredText(r))
		if errors.Is(e, ErrDenied) {
			continue
		}
		if e != nil {
			return MemoryGraphMap{}, e
		}
		if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
			return MemoryGraphMap{}, ErrDenied
		}
		b.nodes[r.ID], b.depth[r.ID] = r, 0
		b.order = append(b.order, r.ID)
	}
	for chunk := range slices.Chunk(b.order, memoryGraphMaxNodes) {
		for _, lines := range []func(context.Context, []string) ([]MemoryGraphEdge, error){b.storedLines, b.sessionLines, b.similarLines} {
			found, e := lines(ctx, chunk)
			if e != nil {
				return MemoryGraphMap{}, e
			}
			for _, edge := range found {
				_, f := b.depth[edge.From]
				_, t := b.depth[edge.To]
				if f && t {
					b.addEdge(edge)
				}
			}
		}
	}
	uses := map[string]int{}
	for chunk := range slices.Chunk(b.order, memorySignalBatch) {
		counts, e := s.Repo.MemoryUseCounts(ctx, in.Scope, chunk)
		if e != nil {
			return MemoryGraphMap{}, e
		}
		for _, c := range counts {
			if !slices.Contains(chunk, c.MemoryID) || c.Uses < 1 {
				return MemoryGraphMap{}, ErrUnavailable
			}
			uses[c.MemoryID] = c.Uses
		}
	}
	out := MemoryGraphMap{Nodes: make([]MemoryGraphMapNode, 0, len(b.order)), Edges: make([]MemoryGraphMapEdge, 0, len(b.edges)), Truncated: b.trunc, Authority: AuthorityDataOnly}
	for _, id := range b.order {
		r := b.nodes[id]
		out.Nodes = append(out.Nodes, MemoryGraphMapNode{ID: id, Reach: memoryReach(r), Kind: r.Kind, Uses: uses[id], VectorPending: !s.vectorCurrent(r)})
	}
	for _, edge := range b.edges {
		out.Edges = append(out.Edges, MemoryGraphMapEdge{From: edge.From, To: edge.To, Type: edge.Type})
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, who.PrincipalID, "read:memory_graph", MemoryGraphModeAll)
	}); e != nil {
		return MemoryGraphMap{}, e
	}
	return out, nil
}
