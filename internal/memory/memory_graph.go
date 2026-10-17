package memory

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// ST-820 / FR-012: relation graph around one memory. Stored relations (supersede, conflict,
// support, human link) may cross reaches; derived lines (same session, similar) stay inside
// one reach. Nodes carry a short label, never sources, reasons or vectors.
const (
	GraphSupersedes  = "supersedes"
	GraphConflicts   = "conflicts"
	GraphSupports    = "supports"
	GraphHumanLink   = "human_link"
	GraphSameSession = "same_session"
	GraphSimilar     = "similar"

	memoryGraphSimilarMin   = 0.80 // cosine; pgvector distance <= 1-0.80
	memoryGraphPerNode      = 3    // similar and same-session lines per node
	memoryGraphDefaultNodes = 60
	memoryGraphMaxNodes     = 100
	memoryGraphMaxEdges     = 300
	memoryGraphScan         = 1000 // stored relation rows read per step
	memoryGraphLabelRunes   = 120

	MemoryGraphModeAll  = "all" // whole visible map: no center, no labels
	memoryGraphMapNodes = 5000
	memoryGraphMapEdges = 20000
	memoryGraphMapPage  = 500 // records read and authorized per page; only slim copies are kept
)

var memoryGraphReaches = []string{ReachPersonal, ReachGeneral, ReachProject}

// MemoryGraphQuery selects the graph. Reaches lists the reaches to show (all when empty);
// a project memory is shown only when its reach_project equals ProjectKey (reachVisible).
type MemoryGraphQuery struct {
	Scope      Scope    `json:"scope"`
	Mode       string   `json:"mode,omitempty"`
	MemoryID   string   `json:"memory_id"`
	Depth      int      `json:"depth"`
	Reaches    []string `json:"reaches"`
	ProjectKey string   `json:"project_key"`
	Limit      int      `json:"limit"`
}

type MemoryGraphNode struct {
	Memory        Ref    `json:"memory"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Reach         string `json:"reach"`
	ReachProject  string `json:"reach_project,omitempty"`
	Label         string `json:"label"`
	Depth         int    `json:"depth"`
	VectorPending bool   `json:"vector_pending"`
}
type MemoryGraphEdge struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Type     string   `json:"type"`
	Relation *Ref     `json:"relation,omitempty"`
	Score    *float64 `json:"score,omitempty"`
}
type MemoryGraph struct {
	Center    Ref               `json:"center"`
	Nodes     []MemoryGraphNode `json:"nodes"`
	Edges     []MemoryGraphEdge `json:"edges"`
	Truncated bool              `json:"truncated"`
	Authority string            `json:"authority"`
}

// MemoryGraphSimilarPair is one repository vector neighbour; Distance is pgvector cosine distance.
type MemoryGraphSimilarPair struct {
	From, To string
	Distance float64
}

// MemoryGraphSessionHit is one memory sharing a session tag with the asking memory From.
type MemoryGraphSessionHit struct {
	From   string
	Memory Record
}

func normalizeMemoryGraphQuery(in MemoryGraphQuery) (MemoryGraphQuery, error) {
	all, maxNodes := in.Mode == MemoryGraphModeAll, memoryGraphMaxNodes
	if all {
		maxNodes = memoryGraphMapNodes
		if in.Limit == 0 {
			in.Limit = memoryGraphMapNodes
		}
	}
	if in.Depth == 0 && !all {
		in.Depth = 2
	}
	if in.Limit == 0 {
		in.Limit = memoryGraphDefaultNodes
	}
	if len(in.Reaches) == 0 {
		in.Reaches = slices.Clone(memoryGraphReaches)
	}
	in.Reaches = slices.Clone(in.Reaches)
	slices.Sort(in.Reaches)
	in.ProjectKey = strings.ToLower(strings.TrimSpace(in.ProjectKey))
	center := in.Mode == "" && validRunLabel(in.MemoryID, 256) && !containsSecret(in.MemoryID) && in.Depth >= 1 && in.Depth <= 2
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || !center && !(all && in.MemoryID == "" && in.Depth == 0) ||
		in.Limit < 1 || in.Limit > maxNodes || !validProjectKey(in.ProjectKey) || len(slices.Compact(slices.Clone(in.Reaches))) != len(in.Reaches) {
		return in, ErrInvalid
	}
	for _, r := range in.Reaches {
		if !slices.Contains(memoryGraphReaches, r) {
			return in, ErrInvalid
		}
	}
	return in, nil
}

// memoryGraphVisible is the node rule both repositories apply (Postgres: memoryGraphWhere).
func memoryGraphVisible(r Record, in MemoryGraphQuery) bool {
	reach := memoryReach(r)
	return r.Scope == in.Scope && !r.ContentErased && !r.PrivacyQuarantined && r.Revision > 0 && (live(r) || r.Status == "superseded") &&
		slices.Contains(in.Reaches, reach) && reachVisible(reach, r.ReachProject, in.ProjectKey)
}

// sameReach: derived lines never cross a reach (or two projects' reaches).
func sameReach(a, b Record) bool {
	return memoryReach(a) == memoryReach(b) && a.ReachProject == b.ReachProject
}

// vectorOK reads the vector state saved for a slim all-mode node, else checks the record.
func (b *memoryGraphBuild) vectorOK(r Record) bool {
	if v, ok := b.fresh[r.ID]; ok {
		return v
	}
	return b.s.vectorCurrent(r)
}

func (s *Service) vectorCurrent(r Record) bool {
	e := r.Embedding
	return e != nil && len(e.Vector) == VectorDimension && (s.Embedder == nil || e.Space == s.Embedder.Space()) && e.ContentHash == ContentHash(recordContent(r))
}

func sessionTags(r Record) []string {
	out := []string{}
	for _, t := range r.Tags {
		if strings.HasPrefix(t, "session:") && len(t) > len("session:") {
			out = append(out, t)
		}
	}
	return out
}

// storedGraphEdge maps one memory_relations row to a line between two memories, or false.
func storedGraphEdge(rel Record) (MemoryGraphEdge, bool) {
	if rel.Status != "active" || rel.ContentErased || rel.PrivacyQuarantined || len(rel.Relations) != 2 || rel.Relations[0].Collection != colMemories || rel.Relations[1].Collection != colMemories || rel.Relations[0].ID == rel.Relations[1].ID {
		return MemoryGraphEdge{}, false
	}
	typ := ""
	switch {
	case rel.Kind == "supersedes":
		typ = GraphSupersedes
	case rel.Kind == "conflicts_with":
		typ = GraphConflicts
	case rel.Kind == "supports":
		typ = GraphSupports
	case rel.CreatedByType == roleHuman:
		typ = GraphHumanLink
	default:
		return MemoryGraphEdge{}, false
	}
	ref := Ref{Collection: "memory_relations", ID: rel.ID, Revision: rel.Revision}
	return MemoryGraphEdge{From: rel.Relations[0].ID, To: rel.Relations[1].ID, Type: typ, Relation: &ref}, true
}

func memoryGraphLabel(r Record) string {
	t := strings.Join(strings.Fields(r.Text), " ")
	if rs := []rune(t); len(rs) > memoryGraphLabelRunes {
		t = string(rs[:memoryGraphLabelRunes]) + "…"
	}
	return t
}

func parseMemoryGraphQuery(raw string) (MemoryGraphQuery, error) {
	q, e := url.ParseQuery(raw)
	if e != nil {
		return MemoryGraphQuery{}, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 || v[0] == "" && k != "user_id" && k != "agent_id" && k != "project_key" || !slices.Contains([]string{"workspace_id", "project_id", "user_id", "agent_id", "mode", "memory_id", "depth", "reach", "project_key", "limit"}, k) {
			return MemoryGraphQuery{}, ErrInvalid
		}
	}
	in := MemoryGraphQuery{Scope: Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}, Mode: q.Get("mode"), MemoryID: q.Get("memory_id"), ProjectKey: q.Get("project_key")}
	if v, ok := q["reach"]; ok {
		in.Reaches = strings.Split(v[0], ",")
	}
	for key, dst := range map[string]*int{"depth": &in.Depth, "limit": &in.Limit} {
		if v, ok := q[key]; ok {
			n, e := strconv.Atoi(v[0])
			if e != nil || n < 1 {
				return in, ErrInvalid
			}
			*dst = n
		}
	}
	return in, nil
}

func memoryGraphRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/memory-graph", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseMemoryGraphQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		if in.Mode == MemoryGraphModeAll {
			out, e := s.MemoryGraphMap(r.Context(), bearer(r), in)
			respond(w, out, e)
			return
		}
		out, e := s.MemoryGraph(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

type memoryGraphBuild struct {
	s     *Service
	token string
	who   Decision
	in    MemoryGraphQuery
	nodes map[string]Record
	depth map[string]int
	order []string
	edges []MemoryGraphEdge
	seen  map[string]bool
	deny  map[string]bool // read denied once; never asked again in this request
	cap   int             // line cap
	fresh map[string]bool // all mode: vector state kept for slim node records
	trunc bool
}

func (b *memoryGraphBuild) addEdge(e MemoryGraphEdge) {
	key := e.Type + "|" + e.From + "|" + e.To
	if e.Type == GraphSameSession || e.Type == GraphSimilar || e.Type == GraphConflicts {
		lo, hi := min(e.From, e.To), max(e.From, e.To)
		e.From, e.To, key = lo, hi, e.Type+"|"+lo+"|"+hi
	}
	if e.Relation != nil {
		key += "|" + e.Relation.ID
	}
	if b.seen[key] {
		return
	}
	if len(b.edges) >= b.cap {
		b.trunc = true
		return
	}
	b.seen[key] = true
	b.edges = append(b.edges, e)
}

// MemoryGraph walks up to two steps from one memory. Every repository read carries the
// scope and the reach filter (memoryGraphWhere / memoryGraphVisible); results are checked
// again here so a repository bug cannot widen the graph.
func (s *Service) MemoryGraph(ctx context.Context, token string, in MemoryGraphQuery) (MemoryGraph, error) {
	in, e := normalizeMemoryGraphQuery(in)
	if e != nil {
		return MemoryGraph{}, e
	}
	if s.Repo == nil {
		return MemoryGraph{}, ErrUnavailable
	}
	who, e := s.authorize(ctx, token, "read:memories", in.Scope, "", "")
	if e != nil {
		return MemoryGraph{}, e
	}
	for _, check := range []struct{ action, id string }{{"read:memories", in.MemoryID}, {"read:memory_relations", ""}} {
		d, e := s.authorize(ctx, token, check.action, in.Scope, check.id, "")
		if e != nil {
			return MemoryGraph{}, e
		}
		if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
			return MemoryGraph{}, ErrDenied
		}
	}
	center, e := s.memoryGraphNodes(ctx, in, []string{in.MemoryID})
	if e != nil {
		return MemoryGraph{}, e
	}
	if len(center) != 1 {
		return MemoryGraph{}, ErrNotFound
	}
	b := &memoryGraphBuild{s: s, token: token, who: who, in: in, nodes: map[string]Record{in.MemoryID: center[0]}, depth: map[string]int{in.MemoryID: 0}, order: []string{in.MemoryID}, seen: map[string]bool{}, deny: map[string]bool{}, cap: memoryGraphMaxEdges}
	frontier := []string{in.MemoryID}
	for step := 1; step <= in.Depth && len(frontier) > 0; step++ {
		if frontier, e = b.expand(ctx, frontier, step); e != nil {
			return MemoryGraph{}, e
		}
	}
	out := MemoryGraph{Center: Ref{Collection: colMemories, ID: center[0].ID, Revision: center[0].Revision}, Nodes: make([]MemoryGraphNode, 0, len(b.order)), Edges: []MemoryGraphEdge{}, Truncated: b.trunc, Authority: AuthorityDataOnly}
	for _, id := range b.order {
		r := b.nodes[id]
		out.Nodes = append(out.Nodes, MemoryGraphNode{Memory: Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}, Kind: r.Kind, Status: r.Status, Reach: memoryReach(r), ReachProject: r.ReachProject, Label: memoryGraphLabel(r), Depth: b.depth[id], VectorPending: !s.vectorCurrent(r)})
	}
	for _, edge := range b.edges {
		if _, ok := b.nodes[edge.From]; ok {
			if _, ok = b.nodes[edge.To]; ok {
				out.Edges = append(out.Edges, edge)
			}
		}
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, who.PrincipalID, "read:memory_graph", in.MemoryID)
	}); e != nil {
		return MemoryGraph{}, e
	}
	return out, nil
}

func (s *Service) memoryGraphNodes(ctx context.Context, in MemoryGraphQuery, ids []string) ([]Record, error) {
	out := []Record{}
	for chunk := range slices.Chunk(ids, memoryGraphScan) {
		rows, e := s.Repo.MemoryGraphNodes(ctx, in, chunk)
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			if !slices.Contains(chunk, r.ID) || !memoryGraphVisible(r, in) {
				return nil, ErrUnavailable
			}
		}
		out = append(out, rows...)
	}
	return out, nil
}

// expand reads every line of the frontier, then adds unseen endpoints (sorted, up to the
// node limit) as the next frontier. Lines to nodes left out are dropped at the end.
func (b *memoryGraphBuild) expand(ctx context.Context, frontier []string, step int) ([]string, error) {
	candidates := []MemoryGraphEdge{}
	for _, lines := range []func(context.Context, []string) ([]MemoryGraphEdge, error){b.storedLines, b.sessionLines, b.similarLines} {
		found, e := lines(ctx, frontier)
		if e != nil {
			return nil, e
		}
		candidates = append(candidates, found...)
	}
	if e := b.load(ctx, candidates); e != nil {
		return nil, e
	}
	return b.place(ctx, candidates, step)
}

func (b *memoryGraphBuild) storedLines(ctx context.Context, frontier []string) ([]MemoryGraphEdge, error) {
	links, e := b.s.Repo.MemoryGraphLinks(ctx, b.in, frontier)
	if e != nil {
		return nil, e
	}
	if len(links) > memoryGraphScan {
		b.trunc, links = true, links[:memoryGraphScan]
	}
	out := []MemoryGraphEdge{}
	for _, rel := range links {
		if rel.Scope != b.in.Scope {
			return nil, ErrUnavailable
		}
		if edge, ok := storedGraphEdge(rel); ok && (slices.Contains(frontier, edge.From) || slices.Contains(frontier, edge.To)) {
			out = append(out, edge)
		}
	}
	return out, nil
}

// sessionLines: up to memoryGraphPerNode same-session memories per node, same reach only.
// The repository applies reach per asking node, so other reaches never use up the cap.
func (b *memoryGraphBuild) sessionLines(ctx context.Context, frontier []string) ([]MemoryGraphEdge, error) {
	asking := []string{}
	for _, id := range frontier {
		if len(sessionTags(b.nodes[id])) > 0 {
			asking = append(asking, id)
		}
	}
	if len(asking) == 0 {
		return nil, nil
	}
	hits, e := b.s.Repo.MemoryGraphSessions(ctx, b.in, asking, memoryGraphPerNode+1)
	if e != nil {
		return nil, e
	}
	byNode := map[string][]string{}
	for _, h := range hits {
		n, ok := b.nodes[h.From]
		if !ok || !slices.Contains(asking, h.From) || h.Memory.ID == h.From || !sameReach(n, h.Memory) || !memoryGraphVisible(h.Memory, b.in) ||
			!slices.ContainsFunc(sessionTags(n), func(t string) bool { return slices.Contains(h.Memory.Tags, t) }) {
			return nil, ErrUnavailable
		}
		if !slices.Contains(byNode[h.From], h.Memory.ID) {
			byNode[h.From] = append(byNode[h.From], h.Memory.ID)
		}
		if _, ok := b.nodes[h.Memory.ID]; !ok {
			b.nodes[h.Memory.ID] = h.Memory // record only; joins the graph when placed
		}
	}
	out := []MemoryGraphEdge{}
	for _, id := range asking {
		others := byNode[id]
		slices.Sort(others)
		if len(others) > memoryGraphPerNode {
			b.trunc, others = true, others[:memoryGraphPerNode]
		}
		for _, o := range others {
			out = append(out, MemoryGraphEdge{From: id, To: o, Type: GraphSameSession})
		}
	}
	return out, nil
}

// similarLines: up to memoryGraphPerNode nearest current vectors per node, same reach only.
func (b *memoryGraphBuild) similarLines(ctx context.Context, frontier []string) ([]MemoryGraphEdge, error) {
	vectored := []string{}
	for _, id := range frontier {
		if b.vectorOK(b.nodes[id]) {
			vectored = append(vectored, id)
		}
	}
	if len(vectored) == 0 {
		return nil, nil
	}
	// Twice the cap, so neighbours dropped below for a stale vector still leave three.
	pairs, e := b.s.Repo.MemoryGraphSimilar(ctx, b.in, vectored, 1-memoryGraphSimilarMin, 2*memoryGraphPerNode)
	if e != nil {
		return nil, e
	}
	need := []MemoryGraphEdge{}
	for _, p := range pairs {
		if !slices.Contains(vectored, p.From) || p.From == p.To || p.Distance < 0 || p.Distance > 1-memoryGraphSimilarMin {
			return nil, ErrUnavailable
		}
		need = append(need, MemoryGraphEdge{From: p.From, To: p.To})
	}
	if e = b.load(ctx, need); e != nil {
		return nil, e
	}
	out, perNode := []MemoryGraphEdge{}, map[string]int{}
	for _, p := range pairs {
		to, ok := b.nodes[p.To]
		if !ok || !b.vectorOK(to) || !sameReach(b.nodes[p.From], to) || perNode[p.From] >= memoryGraphPerNode {
			continue // neighbour gone, its vector is stale, or a repository returned another reach
		}
		perNode[p.From]++
		score := 1 - p.Distance
		out = append(out, MemoryGraphEdge{From: p.From, To: p.To, Type: GraphSimilar, Score: &score})
	}
	return out, nil
}

// load reads line ends not seen yet through the node filter; filtered ones stay absent.
func (b *memoryGraphBuild) load(ctx context.Context, lines []MemoryGraphEdge) error {
	need := []string{}
	for _, edge := range lines {
		for _, id := range []string{edge.From, edge.To} {
			if _, ok := b.nodes[id]; !ok && !slices.Contains(need, id) {
				need = append(need, id)
			}
		}
	}
	if len(need) == 0 {
		return nil
	}
	rows, e := b.s.memoryGraphNodes(ctx, b.in, need)
	if e != nil {
		return e
	}
	for _, r := range rows {
		b.nodes[r.ID] = r
	}
	return nil
}

// place adds new loaded ends the caller may read, in id order up to the node limit, and
// keeps lines between placed nodes.
func (b *memoryGraphBuild) place(ctx context.Context, candidates []MemoryGraphEdge, step int) ([]string, error) {
	next := []string{}
	for _, edge := range candidates {
		for _, id := range []string{edge.From, edge.To} {
			r, loaded := b.nodes[id]
			if _, placed := b.depth[id]; !loaded || placed || b.deny[id] || slices.Contains(next, id) {
				continue
			}
			// Per-memory read check like ListMemories: a denied neighbour is left out, never labelled.
			d, e := b.s.authorize(ctx, b.token, "read:memories", b.in.Scope, id, structuredText(r))
			if errors.Is(e, ErrDenied) {
				b.deny[id] = true
				continue
			}
			if e != nil {
				return nil, e
			}
			if d.PrincipalID != b.who.PrincipalID || d.Role != b.who.Role {
				return nil, ErrDenied
			}
			next = append(next, id)
		}
	}
	slices.Sort(next)
	if room := b.in.Limit - len(b.order); len(next) > room {
		b.trunc, next = true, next[:room]
	}
	for _, id := range next {
		b.depth[id] = step
		b.order = append(b.order, id)
	}
	for _, edge := range candidates {
		_, f := b.depth[edge.From]
		_, t := b.depth[edge.To]
		if f && t {
			b.addEdge(edge)
		}
	}
	for id := range b.nodes { // drop records loaded but not placed, so the final filter is exact
		if _, ok := b.depth[id]; !ok {
			delete(b.nodes, id)
		}
	}
	return next, nil
}
