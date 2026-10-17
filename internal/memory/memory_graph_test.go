package memory

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) graphRows(col string) []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Record{}
	for _, r := range f.store[col] {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (f *fakeRepo) MemoryGraphMapNodes(_ context.Context, in MemoryGraphQuery) ([]Record, error) {
	in, e := normalizeMemoryGraphQuery(in)
	if e != nil || in.Mode != MemoryGraphModeAll {
		return nil, ErrInvalid
	}
	out := []Record{}
	for _, r := range f.graphRows(colMemories) {
		if len(out) <= in.Limit && memoryGraphVisible(r, in) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeRepo) MemoryGraphNodes(_ context.Context, in MemoryGraphQuery, ids []string) ([]Record, error) {
	in, e := normalizeMemoryGraphQuery(in)
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, r := range f.graphRows(colMemories) {
		if slices.Contains(ids, r.ID) && memoryGraphVisible(r, in) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeRepo) MemoryGraphLinks(_ context.Context, in MemoryGraphQuery, ids []string) ([]Record, error) {
	in, e := normalizeMemoryGraphQuery(in)
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, r := range f.graphRows("memory_relations") {
		if r.Scope == in.Scope && r.Status == "active" && !r.ContentErased && slices.ContainsFunc(r.Relations, func(ref Ref) bool { return ref.Collection == colMemories && slices.Contains(ids, ref.ID) }) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeRepo) MemoryGraphSessions(_ context.Context, in MemoryGraphQuery, ids []string, limit int) ([]MemoryGraphSessionHit, error) {
	in, e := normalizeMemoryGraphQuery(in)
	if e != nil {
		return nil, e
	}
	rows := f.graphRows(colMemories)
	out := []MemoryGraphSessionHit{}
	for _, c := range rows {
		if !slices.Contains(ids, c.ID) {
			continue
		}
		n := 0
		for _, m := range rows {
			shared := slices.ContainsFunc(sessionTags(c), func(t string) bool { return slices.Contains(m.Tags, t) })
			if n < limit && m.ID != c.ID && shared && sameReach(c, m) && memoryGraphVisible(m, in) {
				out, n = append(out, MemoryGraphSessionHit{From: c.ID, Memory: m}), n+1
			}
		}
	}
	return out, nil
}
func (f *fakeRepo) MemoryGraphSimilar(_ context.Context, in MemoryGraphQuery, ids []string, maxDistance float64, limit int) ([]MemoryGraphSimilarPair, error) {
	in, e := normalizeMemoryGraphQuery(in)
	if e != nil {
		return nil, e
	}
	rows := f.graphRows(colMemories)
	out := []MemoryGraphSimilarPair{}
	for _, c := range rows {
		if !slices.Contains(ids, c.ID) || c.Embedding == nil || c.Scope != in.Scope {
			continue
		}
		near := []MemoryGraphSimilarPair{}
		for _, m := range rows {
			if m.ID == c.ID || m.Embedding == nil || m.Embedding.Space != c.Embedding.Space || !sameReach(c, m) || !memoryGraphVisible(m, in) {
				continue
			}
			dot := 0.0
			for i := range m.Embedding.Vector {
				dot += m.Embedding.Vector[i] * c.Embedding.Vector[i]
			}
			if d := 1 - dot; d <= maxDistance {
				near = append(near, MemoryGraphSimilarPair{c.ID, m.ID, d})
			}
		}
		sort.Slice(near, func(i, j int) bool {
			return near[i].Distance < near[j].Distance || near[i].Distance == near[j].Distance && near[i].To < near[j].To
		})
		out = append(out, near[:min(len(near), limit)]...)
	}
	return out, nil
}

// graphVector is a unit vector at angle a (radians) in the first two dimensions.
func graphVector(a float64) []float64 {
	v := make([]float64, VectorDimension)
	v[0], v[1] = math.Cos(a), math.Sin(a)
	return v
}

type graphWorld struct {
	f   *fixture
	put func(id, text, reach, project string, angle float64, tags ...string) Record
	rel func(id, kind, role, from, to string)
}

func newGraphWorld(t *testing.T, store func(string, Record) error, space string) graphWorld {
	now := time.Now().UTC().Truncate(time.Millisecond)
	w := graphWorld{}
	w.put = func(id, text, reach, project string, angle float64, tags ...string) Record {
		r := Record{ID: id, Scope: scopeA, Kind: "lesson", Status: "active", Revision: 1, Text: text, Tags: tags, ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now, Reach: reach, ReachProject: project}
		if !math.IsNaN(angle) {
			r.Embedding = &Embedding{Provider: "fake", Model: "fake", Space: space, Dimension: VectorDimension, EmbeddedAt: now, Vector: graphVector(angle), ContentHash: ContentHash(recordContent(r))}
		}
		if e := store(colMemories, r); e != nil {
			t.Fatal(e)
		}
		return r
	}
	w.rel = func(id, kind, role, from, to string) {
		r := Record{ID: id, Scope: scopeA, Kind: kind, Status: "active", Revision: 1, Text: "relation reason " + id, CreatedByType: role, CreatedAt: now, UpdatedAt: now,
			Relations: []Ref{{Collection: colMemories, ID: from, Revision: 1}, {Collection: colMemories, ID: to, Revision: 1}}}
		if e := store("memory_relations", r); e != nil {
			t.Fatal(e)
		}
	}
	return w
}

// seedGraph: center c links to every line type; one step further sits a2; other reaches,
// other projects and erased memories must stay out or connect only by stored relations.
func seedGraph(w graphWorld) {
	nan := math.NaN()
	w.put("c", "center rule body", ReachGeneral, "", 0, "session:s1")
	w.put("sup-old", "old rule", ReachGeneral, "", nan)
	w.put("con", "conflicting rule", ReachGeneral, "", nan)
	w.put("pro", "supporting rule", ReachPersonal, "", nan)
	w.put("hum", "human linked", ReachProject, "owner/mine", nan)
	w.put("ai", "agent linked", ReachGeneral, "", nan)
	w.put("ses", "same session rule", ReachGeneral, "", nan, "session:s1")
	w.put("ses-personal", "same session other reach", ReachPersonal, "", nan, "session:s1")
	for i, a := range []float64{0.1, 0.2, 0.3, 0.35} { // cos 0.995..0.94: four near, three kept
		w.put("sim"+string(rune('1'+i)), "similar rule", ReachGeneral, "", a)
	}
	w.put("far", "unrelated vector", ReachGeneral, "", 1.0) // cos 0.54
	w.put("sim-personal", "similar other reach", ReachPersonal, "", 0.05)
	w.put("sim-other-project", "similar other project", ReachProject, "owner/other", 0.05)
	w.put("a2", "two steps away", ReachGeneral, "", nan)
	w.put("a3", "three steps away", ReachGeneral, "", nan)
	w.put("other-proj", "other project relation", ReachProject, "owner/other", nan)
	w.rel("r-sup", "supersedes", roleHuman, "c", "sup-old")
	w.rel("r-con", "conflicts_with", roleService, "con", "c")
	w.rel("r-pro", "supports", "agent", "pro", "c")
	w.rel("r-hum", "related", roleHuman, "c", "hum")
	w.rel("r-ai", "related", "agent", "c", "ai") // not human, not a known kind: no line
	w.rel("r-a2", "supports", roleHuman, "con", "a2")
	w.rel("r-a3", "supports", roleHuman, "a2", "a3")
	w.rel("r-other", "supports", roleHuman, "c", "other-proj")
	// A separate crowded session: lower-id personal memories must not use up x-c's three general lines.
	w.put("x-c", "crowded session center", ReachGeneral, "", nan, "session:crowd")
	for _, id := range []string{"x-a1", "x-a2", "x-a3", "x-a4"} {
		w.put(id, "crowded personal", ReachPersonal, "", nan, "session:crowd")
	}
	for _, id := range []string{"x-g1", "x-g2", "x-g3", "x-g4"} {
		w.put(id, "crowded general", ReachGeneral, "", nan, "session:crowd")
	}
}

func graphQuery(depth int) MemoryGraphQuery {
	return MemoryGraphQuery{Scope: scopeA, MemoryID: "c", Depth: depth, ProjectKey: "owner/mine"}
}

func hasNode(nodes map[string]int, id string) bool { _, ok := nodes[id]; return ok }

func graphShape(g MemoryGraph) (map[string]int, map[string]string) {
	nodes, edges := map[string]int{}, map[string]string{}
	for _, n := range g.Nodes {
		nodes[n.Memory.ID] = n.Depth
	}
	for _, e := range g.Edges {
		edges[e.From+">"+e.To] = e.Type
	}
	return nodes, edges
}

// checkGraph is shared by the fake and Postgres repositories: same filters, same answer.
func checkGraph(t *testing.T, svc *Service, token string) {
	t.Helper()
	g, e := svc.MemoryGraph(testCtx, token, graphQuery(2))
	if e != nil {
		t.Fatal(e)
	}
	nodes, edges := graphShape(g)
	want := map[string]string{"c>sup-old": GraphSupersedes, "c>con": GraphConflicts, "pro>c": GraphSupports, "c>hum": GraphHumanLink, "c>ses": GraphSameSession,
		"c>sim1": GraphSimilar, "c>sim2": GraphSimilar, "c>sim3": GraphSimilar, "con>a2": GraphSupports}
	for k, typ := range want {
		if edges[k] != typ {
			t.Fatalf("missing %s %s: %v", k, typ, edges)
		}
	}
	for _, id := range []string{"ai", "ses-personal", "far", "sim-personal", "sim-other-project", "other-proj", "a3"} {
		if hasNode(nodes, id) {
			t.Fatalf("%s must not be in the graph: %v %v", id, nodes, edges)
		}
	}
	if edges["c>sim4"] != "" || nodes["sim4"] != 2 || nodes["c"] != 0 || nodes["con"] != 1 || nodes["a2"] != 2 || g.Center.ID != "c" || g.Authority != AuthorityDataOnly || g.Truncated {
		t.Fatalf("depths: %v %+v", nodes, g)
	}
	for _, n := range g.Nodes {
		if pending := n.Memory.ID != "c" && !strings.HasPrefix(n.Memory.ID, "sim"); n.VectorPending != pending {
			t.Fatalf("vector_pending of %s = %v", n.Memory.ID, n.VectorPending)
		}
	}
	for _, edge := range g.Edges {
		if edge.Type == GraphSimilar && (edge.Score == nil || *edge.Score < memoryGraphSimilarMin) || edge.Type != GraphSimilar && edge.Score != nil {
			t.Fatalf("score: %+v", edge)
		}
		if (edge.Type == GraphSimilar || edge.Type == GraphSameSession) != (edge.Relation == nil) {
			t.Fatalf("relation ref: %+v", edge)
		}
	}
	raw, _ := json.Marshal(g)
	if strings.Contains(string(raw), "relation reason") || strings.Contains(string(raw), `"embedding"`) {
		t.Fatal("graph leaked relation reasons or vectors", string(raw))
	}

	// One step, personal reach only: the general center is hidden, so the graph is not found.
	q := graphQuery(1)
	if g, e = svc.MemoryGraph(testCtx, token, q); e != nil {
		t.Fatal(e)
	}
	if nodes, _ = graphShape(g); len(nodes) != 9 { // c + sup-old con pro hum ses sim1..3
		t.Fatal("depth 1", nodes)
	}
	q.Reaches = []string{ReachPersonal}
	if _, e = svc.MemoryGraph(testCtx, token, q); !errors.Is(e, ErrNotFound) {
		t.Fatal("center outside the reach filter", e)
	}
	// General only: the cross-reach stored lines (pro, hum) disappear with their nodes.
	q.Reaches = []string{ReachGeneral}
	g, _ = svc.MemoryGraph(testCtx, token, q)
	if nodes, _ = graphShape(g); len(nodes) != 7 || hasNode(nodes, "pro") || hasNode(nodes, "hum") {
		t.Fatal("general only", nodes)
	}
	// Without the project key the project memory never shows.
	q = graphQuery(1)
	q.ProjectKey = ""
	g, _ = svc.MemoryGraph(testCtx, token, q)
	if nodes, _ = graphShape(g); hasNode(nodes, "hum") {
		t.Fatal("project memory without its key", nodes)
	}
	// Node limit truncates deterministically.
	q = graphQuery(2)
	q.Limit = 3
	if g, e = svc.MemoryGraph(testCtx, token, q); e != nil || len(g.Nodes) != 3 || !g.Truncated {
		t.Fatal("limit", g, e)
	}
	// Same-session cap is per node and reach: other reaches never crowd out lines, and a cut says truncated.
	q = graphQuery(1)
	q.MemoryID = "x-c"
	g, e = svc.MemoryGraph(testCtx, token, q)
	if nodes, edges = graphShape(g); e != nil || len(nodes) != 4 || !g.Truncated || edges["x-c>x-g1"] != GraphSameSession || edges["x-c>x-g3"] != GraphSameSession || hasNode(nodes, "x-a1") {
		t.Fatal("crowded session", nodes, edges, g.Truncated, e)
	}
}

// checkGraphMap: all mode returns every visible memory and its lines, never text, and
// keeps derived lines inside one reach.
func checkGraphMap(t *testing.T, svc *Service, token string) {
	t.Helper()
	q := MemoryGraphQuery{Scope: scopeA, Mode: MemoryGraphModeAll, ProjectKey: "owner/mine"}
	g, e := svc.MemoryGraphMap(testCtx, token, q)
	if e != nil {
		t.Fatal(e)
	}
	nodes := map[string]MemoryGraphMapNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	edges := map[string]string{}
	for _, l := range g.Edges {
		edges[l.From+">"+l.To] = l.Type
		a, b := nodes[l.From], nodes[l.To]
		if a.ID == "" || b.ID == "" || (l.Type == GraphSameSession || l.Type == GraphSimilar) && a.Reach != b.Reach {
			t.Fatal("line outside the map or across reaches", l)
		}
	}
	if len(nodes) != 25 || !g.Truncated || nodes["c"].Uses != 2 || nodes["sim1"].Uses != 0 || !nodes["con"].VectorPending || nodes["sim1"].VectorPending || nodes["hum"].Reach != ReachProject {
		t.Fatal("map nodes", len(nodes), g.Truncated, nodes["c"], nodes["con"], nodes["sim1"], nodes["hum"])
	}
	for _, id := range []string{"sim-other-project", "other-proj"} {
		if _, ok := nodes[id]; ok {
			t.Fatal("hidden project memory in map", id)
		}
	}
	for k, v := range map[string]string{"c>sup-old": GraphSupersedes, "c>con": GraphConflicts, "pro>c": GraphSupports, "c>hum": GraphHumanLink, "c>ses": GraphSameSession,
		"c>sim1": GraphSimilar, "con>a2": GraphSupports, "a2>a3": GraphSupports, "x-c>x-g1": GraphSameSession} {
		if edges[k] != v {
			t.Fatal("map line", k, edges[k], edges)
		}
	}
	if edges["c>ai"] != "" || edges["c>x-a1"] != "" {
		t.Fatal("unexpected map line", edges)
	}
	raw, _ := json.Marshal(g)
	for _, bad := range []string{"center rule body", "label", "relation reason"} {
		if strings.Contains(string(raw), bad) {
			t.Fatal("text in map", bad)
		}
	}
	// Truncated above: the crowded session has more than three same-session lines per node.
	// Reach filter and node cap behave as in the center mode.
	q.Reaches, q.ProjectKey = []string{ReachGeneral}, ""
	if g, e = svc.MemoryGraphMap(testCtx, token, q); e != nil || len(g.Nodes) != 17 {
		t.Fatal("general map", len(g.Nodes), e)
	}
	q.Limit = 5
	if g, e = svc.MemoryGraphMap(testCtx, token, q); e != nil || len(g.Nodes) != 5 || !g.Truncated {
		t.Fatal("map cap", len(g.Nodes), g.Truncated, e)
	}
	for _, bad := range []MemoryGraphQuery{{Scope: scopeA, MemoryID: "c"}, {Scope: scopeA, Depth: 1}, {Scope: scopeA, Limit: memoryGraphMapNodes + 1}} {
		if _, e = svc.MemoryGraphMap(testCtx, token, bad); e != ErrInvalid {
			t.Fatal("bad map query", bad, e)
		}
	}
}

func seedGraphUses(t *testing.T, repo interface {
	Insert(context.Context, string, Record) error
}) {
	now := time.Now().UTC()
	useReceipt(t, repo, "use-1", scopeA, Ref{Collection: colMemories, ID: "c", Revision: 1}, now)
	useReceipt(t, repo, "use-2", scopeA, Ref{Collection: colMemories, ID: "c", Revision: 1}, now)
}

// ST-820 / FR-012 / TC-FUNC-035: all mode, the whole visible map without text.
func TestMemoryGraphMap(t *testing.T) {
	f := newFixture(t)
	seedGraph(newGraphWorld(t, func(col string, r Record) error { return f.repo.Insert(testCtx, col, r) }, f.emb.space))
	seedGraphUses(t, f.repo)
	checkGraphMap(t, f.svc, f.human)
	// A memory the caller may not read is left out with its lines.
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "read:memories" && req.ResourceID == "c" {
			return Decision{}, ErrDenied
		}
		return f.auth.Authorize(ctx, token, req)
	})
	g, e := f.svc.MemoryGraphMap(testCtx, f.human, MemoryGraphQuery{Scope: scopeA, ProjectKey: "owner/mine"})
	if e != nil || len(g.Nodes) != 24 {
		t.Fatal("denied map node", len(g.Nodes), e)
	}
	for _, l := range g.Edges {
		if l.From == "c" || l.To == "c" {
			t.Fatal("line to denied node", l)
		}
	}
	// read:usage is required, for the same principal.
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "read:usage" {
			return Decision{}, ErrDenied
		}
		return f.auth.Authorize(ctx, token, req)
	})
	if _, e = f.svc.MemoryGraphMap(testCtx, f.human, MemoryGraphQuery{Scope: scopeA}); e != ErrDenied {
		t.Fatal("map without read:usage", e)
	}
}

// ST-820 / FR-012 / TC-FUNC-035 / TC-SEC-011: every line type, reach rules, two steps, vector pending.
func TestMemoryGraphLinesReachesAndDepth(t *testing.T) {
	f := newFixture(t)
	seedGraph(newGraphWorld(t, func(col string, r Record) error { return f.repo.Insert(testCtx, col, r) }, f.emb.space))
	checkGraph(t, f.svc, f.human)
	// Erasing a neighbour removes it; a stale vector reads as pending and draws no similar line.
	sim1, _ := f.repo.Get(testCtx, colMemories, scopeA, "sim1")
	sim1.Text = "edited text, vector not refreshed yet"
	if e := f.repo.Replace(testCtx, colMemories, sim1, 1); e != nil {
		t.Fatal(e)
	}
	con, _ := f.repo.Get(testCtx, colMemories, scopeA, "con")
	con.ContentErased = true
	if e := f.repo.Replace(testCtx, colMemories, con, con.Revision); e != nil {
		t.Fatal(e)
	}
	g, e := f.svc.MemoryGraph(testCtx, f.human, graphQuery(2))
	if e != nil {
		t.Fatal(e)
	}
	nodes, edges := graphShape(g)
	if hasNode(nodes, "con") || edges["c>sim1"] != "" || edges["c>sim4"] != GraphSimilar {
		t.Fatal("after erase and edit", nodes, edges)
	}
}

// ST-820 / TC-SEC-011: a neighbour the caller may not read never shows, nor what lies behind it.
func TestMemoryGraphHonorsNeighbourReadDenial(t *testing.T) {
	f := newFixture(t)
	seedGraph(newGraphWorld(t, func(col string, r Record) error { return f.repo.Insert(testCtx, col, r) }, f.emb.space))
	switched, asked := false, map[string]int{}
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "read:memories" && (req.ResourceID == "con" || req.ResourceID == "sim1") {
			asked[req.ResourceID]++
			return Decision{}, ErrDenied
		}
		d, e := f.auth.Authorize(ctx, token, req)
		if switched && req.Action == "read:memories" && req.ResourceID == "ses" {
			d.PrincipalID = "other-user"
		}
		return d, e
	})
	q := graphQuery(2)
	q.Reaches = []string{ReachGeneral, ReachPersonal}
	q.MemoryID = "pro"
	g, e := f.svc.MemoryGraph(testCtx, f.human, q)
	if e != nil {
		t.Fatal(e)
	}
	nodes, _ := graphShape(g)
	raw, _ := json.Marshal(g)
	if !hasNode(nodes, "c") || hasNode(nodes, "con") || hasNode(nodes, "a2") || strings.Contains(string(raw), "two steps away") {
		t.Fatal("denied neighbour", nodes)
	}
	// sim1 is reached from c at step 1 and from sim2, sim3 at step 2: one denial is remembered.
	asked = map[string]int{}
	if g, e = f.svc.MemoryGraph(testCtx, f.human, graphQuery(2)); e != nil || asked["sim1"] != 1 {
		t.Fatal("denied neighbour asked", asked, e)
	}
	if nodes, _ = graphShape(g); hasNode(nodes, "sim1") || !hasNode(nodes, "sim2") {
		t.Fatal("denied similar neighbour", nodes)
	}
	// A decision for another principal on a neighbour stops the whole read.
	switched = true
	if _, e = f.svc.MemoryGraph(testCtx, f.human, graphQuery(1)); e != ErrDenied {
		t.Fatal("principal switch", e)
	}
}

func TestMemoryGraphRefusesBadInputAndOtherPrincipals(t *testing.T) {
	f := newFixture(t)
	seedGraph(newGraphWorld(t, func(col string, r Record) error { return f.repo.Insert(testCtx, col, r) }, f.emb.space))
	for _, bad := range []MemoryGraphQuery{{Scope: scopeA}, {Scope: scopeA, MemoryID: "c", Depth: 3}, {Scope: scopeA, MemoryID: "c", Limit: 101}, {Scope: scopeA, MemoryID: "c", Reaches: []string{"everywhere"}}, {Scope: scopeA, MemoryID: "c", Reaches: []string{ReachGeneral, ReachGeneral}}} {
		if _, e := f.svc.MemoryGraph(testCtx, f.human, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("accepted", bad, e)
		}
	}
	if _, e := f.svc.MemoryGraph(testCtx, f.human, MemoryGraphQuery{Scope: scopeB, MemoryID: "c"}); !errors.Is(e, ErrNotFound) {
		t.Fatal("other scope", e)
	}
	f.auth.actors["far"] = fakeActor{principal: "far", role: roleHuman, verbs: map[string]bool{"read": true}, wrongScope: true}
	f.auth.actors["mute"] = actor("mute", roleHuman, true, "write")
	for _, token := range []string{"far", "mute", "nobody"} {
		if _, e := f.svc.MemoryGraph(testCtx, token, graphQuery(2)); !errors.Is(e, ErrDenied) {
			t.Fatal(token, e)
		}
	}
}

func TestMemoryGraphHTTP(t *testing.T) {
	f := newFixture(t)
	seedGraph(newGraphWorld(t, func(col string, r Record) error { return f.repo.Insert(testCtx, col, r) }, f.emb.space))
	h := Handler(f.svc)
	call := func(q string) (int, string) {
		r := httptest.NewRequest("GET", "/v1/memory-graph?workspace_id="+scopeA.WorkspaceID+"&project_id="+scopeA.ProjectID+q, nil)
		r.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	code, body := call("&memory_id=c&depth=1&reach=general,project&project_key=Owner/Mine&limit=60")
	var g MemoryGraph
	if code != http.StatusOK || json.Unmarshal([]byte(body), &g) != nil || len(g.Nodes) != 8 || !strings.Contains(body, `"type":"human_link"`) {
		t.Fatal(code, body)
	}
	code, body = call("&mode=all&reach=general&limit=5000")
	var m MemoryGraphMap
	if code != http.StatusOK || json.Unmarshal([]byte(body), &m) != nil || len(m.Nodes) != 17 || strings.Contains(body, "label") || strings.Contains(body, "center") {
		t.Fatal(code, body)
	}
	for _, bad := range []string{"", "&memory_id=c&depth=0", "&memory_id=c&depth=x", "&memory_id=c&extra=1", "&memory_id=c&reach=", "&memory_id=c&limit=0",
		"&mode=all&memory_id=c", "&mode=all&depth=1", "&mode=all&limit=5001", "&mode=near&memory_id=c"} {
		if code, _ = call(bad); code != http.StatusBadRequest {
			t.Fatal(bad, code)
		}
	}
}

func TestPostgresMemoryGraph(t *testing.T) {
	p := isolatedPostgres(t)
	emb := &fakeEmbedder{space: "fake:embed:v1:1024d", dim: VectorDimension}
	seedGraph(newGraphWorld(t, func(col string, r Record) error { return p.Insert(testCtx, col, r) }, emb.space))
	// The same ids in another scope must never join the graph.
	other := Record{ID: "sim-leak", Scope: scopeB, Kind: "lesson", Status: "active", Revision: 1, Text: "leak", Tags: []string{"session:s1"}, ApprovedBy: "r",
		Embedding: &Embedding{Space: emb.space, Dimension: VectorDimension, Vector: graphVector(0.01)}}
	if e := p.Insert(testCtx, colMemories, other); e != nil {
		t.Fatal(e)
	}
	auth := &fakeAuth{actors: map[string]fakeActor{"tok": actor("human-1", roleHuman, true, allVerbs...)}}
	seedGraphUses(t, p)
	checkGraph(t, &Service{Repo: p, Auth: auth, Embedder: emb}, "tok")
	checkGraphMap(t, &Service{Repo: p, Auth: auth, Embedder: emb}, "tok")
}
