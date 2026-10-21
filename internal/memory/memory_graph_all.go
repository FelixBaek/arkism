package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// ST-820 all mode: every visible memory of the scope and the lines between them, for the
// whole-map view. Same node rule and line rules as the center mode; nodes carry a short label
// (the same per-node read:memories check guards it) so the map can name points past the first list page.
type MemoryGraphMapNode struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Status        string `json:"status"`
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
	Truncated bool                 `json:"truncated"` // either cap below; kept for older clients
	// NodesTruncated: more readable memories than the node limit. EdgesTruncated: a line cap cut lines.
	NodesTruncated bool   `json:"nodes_truncated"`
	EdgesTruncated bool   `json:"edges_truncated"`
	Authority      string `json:"authority"`
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
	b := &memoryGraphBuild{s: s, token: token, who: who, in: in, nodes: map[string]Record{}, depth: map[string]int{}, seen: map[string]bool{}, deny: map[string]bool{}, cap: memoryGraphMapEdges, fresh: map[string]bool{}, labels: map[string]string{}, read: s.itemAuthorizer(ctx, token, "read:memories", in.Scope)}
	if e = b.mapNodes(ctx); e != nil {
		return MemoryGraphMap{}, e
	}
	nodesCut := b.trunc // from here on only the line caps set trunc
	b.trunc = false
	if e = b.mapLines(ctx); e != nil {
		return MemoryGraphMap{}, e
	}
	uses := map[string]int{}
	if len(b.order) > 0 {
		counts, e := s.Repo.MemoryUseCounts(ctx, in.Scope, b.order)
		if e != nil {
			return MemoryGraphMap{}, e
		}
		for _, c := range counts {
			if _, placed := b.depth[c.MemoryID]; !placed || c.Uses < 1 {
				return MemoryGraphMap{}, ErrUnavailable
			}
			uses[c.MemoryID] = c.Uses
		}
	}
	out := MemoryGraphMap{Nodes: make([]MemoryGraphMapNode, 0, len(b.order)), Edges: make([]MemoryGraphMapEdge, 0, len(b.edges)), Truncated: nodesCut || b.trunc, NodesTruncated: nodesCut, EdgesTruncated: b.trunc, Authority: AuthorityDataOnly}
	for _, id := range b.order {
		r := b.nodes[id]
		out.Nodes = append(out.Nodes, MemoryGraphMapNode{ID: id, Label: b.labels[id], Status: r.Status, Reach: memoryReach(r), Kind: r.Kind, Uses: uses[id], VectorPending: !b.fresh[id]})
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

// mapLines adds the session, similar and stored lines between the placed nodes.
func (b *memoryGraphBuild) mapLines(ctx context.Context) error {
	// Session and similar lines are asked for every node at once, so each query builds its candidate
	// set once instead of once per 100 nodes (#166); their lines are then added in the same order as
	// per-100-node asking, so the line cap keeps the same lines. Stored lines keep their per-chunk scan cap.
	chunkOf := map[string]int{}
	for i, id := range b.order {
		chunkOf[id] = i / memoryGraphMaxNodes
	}
	byChunk := make([][]MemoryGraphEdge, 2*(len(b.order)/memoryGraphMaxNodes+1))
	for i, lines := range []func(context.Context, []string) ([]MemoryGraphEdge, error){b.sessionLines, b.similarLines} {
		if len(b.order) == 0 {
			break
		}
		found, e := lines(ctx, b.order)
		if e != nil {
			return e
		}
		for _, edge := range found {
			k := 2*chunkOf[edge.From] + i
			byChunk[k] = append(byChunk[k], edge)
		}
	}
	for n, chunk := range slices.Collect(slices.Chunk(b.order, memoryGraphMaxNodes)) {
		stored, e := b.storedLines(ctx, chunk)
		if e != nil {
			return e
		}
		for _, found := range [][]MemoryGraphEdge{stored, byChunk[2*n], byChunk[2*n+1]} {
			for _, edge := range found {
				_, f := b.depth[edge.From]
				_, t := b.depth[edge.To]
				if f && t {
					b.addEdge(edge)
				}
			}
		}
	}
	return nil
}

// mapNodes pages through visible memories by id and keeps those the caller may read, up to
// the node limit; truncated means one more readable memory exists. Only a slim copy of each
// record stays in memory: the fields the line rules use, and the vector state.
func (b *memoryGraphBuild) mapNodes(ctx context.Context) error {
	for after := ""; ; {
		rows, e := b.s.Repo.MemoryGraphMapNodes(ctx, b.in, after, memoryGraphMapPage)
		if e != nil {
			return e
		}
		for _, r := range rows {
			if _, dup := b.nodes[r.ID]; dup || !memoryGraphVisible(r, b.in) {
				return ErrUnavailable
			}
			d, e := b.read(r.ID, structuredText(r))
			if errors.Is(e, ErrDenied) {
				continue
			}
			if e != nil {
				return e
			}
			if d.PrincipalID != b.who.PrincipalID || d.Role != b.who.Role {
				return ErrDenied
			}
			if len(b.order) == b.in.Limit {
				b.trunc = true
				return nil
			}
			b.nodes[r.ID] = Record{ID: r.ID, Scope: r.Scope, Kind: r.Kind, Status: r.Status, Revision: r.Revision, Reach: r.Reach, ReachProject: r.ReachProject, Tags: r.Tags}
			b.fresh[r.ID], b.depth[r.ID], b.labels[r.ID] = b.s.vectorCurrent(r), 0, memoryGraphMapLabel(r)
			b.order = append(b.order, r.ID)
		}
		if len(rows) < memoryGraphMapPage {
			return nil
		}
		after = rows[len(rows)-1].ID
	}
}

// memoryGraphMapLabel keeps map labels short: up to 5000 nodes go out in one answer.
func memoryGraphMapLabel(r Record) string {
	t := strings.Join(strings.Fields(r.Text), " ")
	if rs := []rune(t); len(rs) > memoryGraphMapLabelRunes {
		t = string(rs[:memoryGraphMapLabelRunes]) + "…"
	}
	return t
}
