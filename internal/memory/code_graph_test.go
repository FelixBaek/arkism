package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/codegraph"
)

// fakeCodeGraph is an in-memory codeGraphStore with the Postgres semantics:
// per-file replacement, revision check, symbol ownership moves on upsert.
type fakeCodeGraph struct {
	mu    sync.Mutex
	repos map[string]*fakeCodeRepo // scope|repo
}

type fakeCodeRepo struct {
	scope   Scope
	state   CodeRepo
	files   map[string]string
	symbols map[string]CodeSymbol
	edges   map[string]CodeEdge
}

func newFakeCodeGraph() *fakeCodeGraph { return &fakeCodeGraph{repos: map[string]*fakeCodeRepo{}} }

func fakeKey(s Scope, repo string) string { return s.WorkspaceID + "|" + s.ProjectID + "|" + repo }

func (f *fakeCodeRepo) view() CodeRepo {
	r := f.state
	r.Files, r.Symbols, r.Edges = len(f.files), len(f.symbols), len(f.edges)
	return r
}

func (f *fakeCodeGraph) CodeRepos(_ context.Context, s Scope) ([]CodeRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []CodeRepo{}
	for _, r := range f.repos {
		if r.scope == s {
			out = append(out, r.view())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}

func (f *fakeCodeGraph) CodeRepoFiles(_ context.Context, s Scope, repo string) (CodeGraphFiles, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[fakeKey(s, repo)]
	if r == nil {
		return CodeGraphFiles{}, ErrNotFound
	}
	files := map[string]string{}
	for k, v := range r.files {
		files[k] = v
	}
	return CodeGraphFiles{Repo: repo, Revision: r.state.Revision, Files: files}, nil
}

func (f *fakeCodeGraph) ApplyCodeBatch(_ context.Context, s Scope, in CodeGraphBatch, now time.Time) (CodeRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[fakeKey(s, in.Repo)]
	rev := int64(0)
	if r != nil {
		rev = r.state.Revision
	}
	if rev != in.ExpectedRevision {
		return CodeRepo{}, ErrConflict
	}
	if r == nil {
		r = &fakeCodeRepo{scope: s, state: CodeRepo{Repo: in.Repo, Stats: json.RawMessage("{}")}, files: map[string]string{}, symbols: map[string]CodeSymbol{}, edges: map[string]CodeEdge{}}
		f.repos[fakeKey(s, in.Repo)] = r
	}
	paths := map[string]bool{}
	for _, file := range in.Files {
		paths[file.Path] = true
	}
	for _, p := range in.Remove {
		paths[p] = true
		delete(r.files, p)
	}
	for id, sym := range r.symbols {
		if paths[sym.Path] {
			delete(r.symbols, id)
		}
	}
	for k, e := range r.edges {
		if paths[e.Path] {
			delete(r.edges, k)
		}
	}
	for _, sym := range in.Symbols {
		r.symbols[sym.ID] = sym
	}
	for _, e := range in.Edges {
		r.edges[e.From+"|"+e.To+"|"+e.Kind] = e
	}
	for _, file := range in.Files {
		r.files[file.Path] = file.Hash
	}
	r.state.Revision++
	r.state.UpdatedAt = now
	return r.view(), nil
}

func (f *fakeCodeGraph) FinishCodeIndex(_ context.Context, s Scope, in CodeGraphFinish, now time.Time) (CodeRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[fakeKey(s, in.Repo)]
	if r == nil && in.ExpectedRevision != 0 || r != nil && r.state.Revision != in.ExpectedRevision {
		return CodeRepo{}, ErrConflict
	}
	r.state.Commit, r.state.Stats, r.state.IndexedAt = in.Commit, in.Stats, &now
	r.state.Revision++
	return r.view(), nil
}

func (f *fakeCodeGraph) DeleteCodeRepo(_ context.Context, s Scope, repo string, expected int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[fakeKey(s, repo)]
	if r == nil {
		return ErrNotFound
	}
	if r.state.Revision != expected {
		return ErrConflict
	}
	delete(f.repos, fakeKey(s, repo))
	return nil
}

func (f *fakeCodeGraph) FindCodeSymbols(_ context.Context, s Scope, repo, q string, exact bool, limit int) ([]CodeSymbolHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []CodeSymbolHit
	lq := strings.ToLower(q)
	for _, r := range f.repos {
		if r.scope != s || repo != "" && r.state.Repo != repo {
			continue
		}
		for _, sym := range r.symbols {
			rank := -1
			if exact {
				switch {
				case sym.ID == q:
					rank = 0
				case sym.QName == q:
					rank = 1
				case strings.HasSuffix(sym.QName, "/"+q) || strings.HasSuffix(sym.QName, "."+q) || strings.HasSuffix(sym.QName, "#"+q):
					rank = 2
				case strings.EqualFold(sym.Name, q):
					rank = 3
				}
			} else {
				switch name := strings.ToLower(sym.Name); {
				case name == lq:
					rank = 0
				case strings.Contains(name, lq):
					rank = 1
				case strings.Contains(strings.ToLower(sym.QName), lq):
					rank = 2
				}
			}
			if rank >= 0 {
				out = append(out, CodeSymbolHit{Repo: r.state.Repo, Rank: rank, Symbol: sym})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if (a.Symbol.Kind == "package") != (b.Symbol.Kind == "package") {
			return b.Symbol.Kind == "package"
		}
		if len(a.Symbol.QName) != len(b.Symbol.QName) && !exact {
			return len(a.Symbol.QName) < len(b.Symbol.QName)
		}
		return a.Symbol.QName < b.Symbol.QName
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeCodeGraph) CodeNeighbors(_ context.Context, s Scope, repo string, ids []string, incoming bool, kinds []string, limit int) ([]CodeNeighbor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if repo == "" || len(ids) == 0 || len(ids) > 2000 {
		return nil, ErrInvalid
	}
	r := f.repos[fakeKey(s, repo)]
	if r == nil {
		return []CodeNeighbor{}, nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []CodeNeighbor
	for _, e := range r.edges {
		match, other := e.To, e.From
		if !incoming {
			match, other = e.From, e.To
		}
		sym, ok := r.symbols[other]
		if !want[match] || !ok || !contains(kinds, e.Kind) {
			continue
		}
		out = append(out, CodeNeighbor{Edge: e, Symbol: sym})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Symbol.Path != b.Symbol.Path {
			return a.Symbol.Path < b.Symbol.Path
		}
		if a.Symbol.StartLine != b.Symbol.StartLine {
			return a.Symbol.StartLine < b.Symbol.StartLine
		}
		return a.Edge.Kind < b.Edge.Kind
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeCodeGraph) CodeSymbolsInRanges(_ context.Context, s Scope, repo string, ranges []CodeLineRange, kinds []string, limit int) ([]CodeSymbol, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if repo == "" || len(ranges) == 0 || len(kinds) == 0 || limit < 1 {
		return nil, ErrInvalid
	}
	r := f.repos[fakeKey(s, repo)]
	out := []CodeSymbol{}
	if r == nil {
		return out, nil
	}
	for _, sym := range r.symbols {
		if !contains(kinds, sym.Kind) {
			continue
		}
		for _, x := range ranges {
			if x.Path == sym.Path && sym.StartLine <= x.EndLine && sym.EndLine >= x.StartLine {
				out = append(out, sym)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		return a.QName < b.QName
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type codeGraphRepo struct {
	*fakeRepo
	*fakeCodeGraph
}

// batches turns an index result into upload batches of at most n files.
func batches(scope Scope, repo string, res codegraph.Result, n int) []CodeGraphBatch {
	var out []CodeGraphBatch
	cur := CodeGraphBatch{Scope: scope, Repo: repo, Remove: res.Removed}
	for _, f := range res.Files {
		if len(cur.Files) == n {
			out = append(out, cur)
			cur = CodeGraphBatch{Scope: scope, Repo: repo}
		}
		cur.Files = append(cur.Files, CodeFile{Path: f.Path, Hash: f.Hash, Lang: f.Lang})
		for _, s := range f.Symbols {
			cur.Symbols = append(cur.Symbols, CodeSymbol(s))
		}
		for _, e := range f.Edges {
			cur.Edges = append(cur.Edges, CodeEdge(e))
		}
	}
	if len(cur.Files)+len(cur.Remove) > 0 {
		out = append(out, cur)
	}
	return out
}

func indexFixture(t *testing.T) codegraph.Result {
	t.Helper()
	res, err := codegraph.Index(context.Background(), codegraph.Options{Root: "../codegraph/testdata/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// codeGraphEnv is one store under test: the service and a context binder per token.
type codeGraphEnv struct {
	svc                 *Service
	human, agent, other string // other is a human of scopeB only
	ctx                 func(token string) context.Context
}

func upload(t *testing.T, env codeGraphEnv, scope Scope, repo string, res codegraph.Result, rev int64) int64 {
	t.Helper()
	for _, b := range batches(scope, repo, res, 3) {
		b.ExpectedRevision = rev
		out, err := env.svc.UploadCodeGraph(env.ctx(env.human), env.human, b)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		rev = out.Repo.Revision
	}
	repoState, err := env.svc.FinishCodeGraph(env.ctx(env.human), env.human, CodeGraphFinish{Scope: scope, Repo: repo, ExpectedRevision: rev, Commit: "cbd18c7", Stats: json.RawMessage(`{"mode":"full"}`)})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	return repoState.Revision
}

func qnames(items []CodeHit) []string {
	var out []string
	for _, h := range items {
		out = append(out, h.QName)
	}
	return out
}

// runCodeGraphScenario is the behaviour every store must show.
func runCodeGraphScenario(t *testing.T, env codeGraphEnv) {
	res := indexFixture(t)
	rev := upload(t, env, scopeA, "fixture", res, 0)
	ctx, human, agent := env.ctx(env.human), env.human, env.agent
	q := func(symbol string) CodeGraphQuery { return CodeGraphQuery{Scope: scopeA, Symbol: symbol} }

	repos, err := env.svc.CodeGraphRepos(ctx, human, scopeA)
	if err != nil || len(repos) != 1 || repos[0].Commit != "cbd18c7" || repos[0].Files != len(res.Files) || repos[0].Symbols != res.Stats.Symbols || repos[0].Edges != res.Stats.Edges {
		t.Fatalf("repos %+v %v (want %d files %d symbols %d edges)", repos, err, len(res.Files), res.Stats.Symbols, res.Stats.Edges)
	}

	// search, then callers by the found id
	found, err := env.svc.SearchCodeSymbols(env.ctx(agent), agent, CodeGraphQuery{Scope: scopeA, Query: "total"})
	if err != nil || len(found.Items) == 0 || found.Items[0].QName != "shapes.Total" || found.Authority != AuthorityDataOnly || found.Items[0].Loc != "shapes/shapes.go:19" {
		t.Fatalf("search %+v %v", found, err)
	}
	callers, err := env.svc.CodeCallers(env.ctx(agent), agent, q(found.Items[0].ID))
	if err != nil || strings.Join(qnames(callers.Items), ",") != "report.Summary" || callers.Items[0].At != "report/report.go:6" || callers.Items[0].Via != "calls" {
		t.Fatalf("callers %+v %v", callers, err)
	}
	// a method's callers include calls through the interface it implements
	callers, err = env.svc.CodeCallers(ctx, human, q("shapes.Square.Area"))
	if err != nil || strings.Join(qnames(callers.Items), ",") != "shapes.Total" || callers.Items[0].Via != "interface" {
		t.Fatalf("interface callers %+v %v", callers, err)
	}
	// an ambiguous name answers with candidates, not a guess
	amb, err := env.svc.CodeCallers(ctx, human, q("Area"))
	if err != nil || amb.Symbol != nil || len(amb.Candidates) != 3 || len(amb.Items) != 0 {
		t.Fatalf("ambiguous %+v %v", amb, err)
	}
	if _, err = env.svc.CodeCallers(ctx, human, q("nothing.Here")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown symbol %v", err)
	}
	callees, err := env.svc.CodeCallees(ctx, human, q("report.Summary"))
	if err != nil || strings.Join(qnames(callees.Items), ",") != "shapes.Shape,shapes.Circle,shapes.Total,shapes.NewSquare" {
		t.Fatalf("callees %v %v", qnames(callees.Items), err)
	}
	js, err := env.svc.CodeCallees(ctx, human, q("web/app.js#View.render"))
	if err != nil || strings.Join(qnames(js.Items), ",") != "app.js#View.title,util.js#twice" {
		t.Fatalf("js callees %v %v", qnames(js.Items), err)
	}

	// impact: breadth first with depths, and the depth cap
	impact, err := env.svc.CodeImpact(ctx, human, q("shapes.Total"))
	if err != nil || impact.Depth != 3 || impact.Truncated {
		t.Fatalf("impact %+v %v", impact, err)
	}
	var got []string
	for _, h := range impact.Items {
		got = append(got, h.QName+"@"+itoaCode(h.Depth))
	}
	if strings.Join(got, ",") != "report.Summary@1,report.Twice@2,report.Top@3" {
		t.Fatalf("impact order %v", got)
	}
	one, err := env.svc.CodeImpact(ctx, human, CodeGraphQuery{Scope: scopeA, Symbol: "shapes.Total", Depth: 1})
	if err != nil || strings.Join(qnames(one.Items), ",") != "report.Summary" {
		t.Fatalf("depth 1 %+v %v", one, err)
	}
	capped, err := env.svc.CodeImpact(ctx, human, CodeGraphQuery{Scope: scopeA, Symbol: "shapes.Total", Depth: 50})
	if err != nil || capped.Depth != codeImpactMaxDepth || !capped.Truncated || capped.Reason != "depth_cap" || len(capped.Items) != 3 {
		t.Fatalf("depth cap %+v %v", capped, err)
	}
	viaIface, err := env.svc.CodeImpact(ctx, human, q("shapes.Square.Area"))
	if err != nil || strings.Join(qnames(viaIface.Items), ",") != "shapes.Total,report.Summary,report.Twice" {
		t.Fatalf("impact through interface %v %v", qnames(viaIface.Items), err)
	}

	// changed lines: the functions and methods they touch, each with its impact
	changed, err := env.svc.CodeChanged(ctx, human, CodeChangedQuery{Scope: scopeA, Repo: "fixture", Ranges: []CodeLineRange{
		{Path: "shapes/shapes.go", StartLine: 21, EndLine: 22}, {Path: "shapes/shapes.go", StartLine: 12, EndLine: 12},
		{Path: "report/report.go", StartLine: 9, EndLine: 9}, {Path: "shapes/shapes.go", StartLine: 27, EndLine: 27}}})
	if err != nil || changed.Commit != "cbd18c7" || changed.Depth != 2 || changed.Authority != AuthorityDataOnly || len(changed.Items) != 3 {
		t.Fatalf("changed %+v %v", changed, err)
	}
	var touched []string
	for _, c := range changed.Items {
		touched = append(touched, c.Symbol.QName+"="+strings.Join(qnames(c.Impact), "+"))
	}
	if strings.Join(touched, ",") != "report.Twice=report.Top,shapes.Square.Area=shapes.Total+report.Summary,shapes.Total=report.Summary+report.Twice" {
		t.Fatalf("changed symbols %v", touched)
	}
	if changed.Items[2].Symbol.Loc != "shapes/shapes.go:19" || changed.Items[2].EndLine != 25 || changed.Items[2].Impact[1].Depth != 2 {
		t.Fatalf("changed item %+v", changed.Items[2])
	}
	few, err := env.svc.CodeChanged(ctx, human, CodeChangedQuery{Scope: scopeA, Repo: "fixture", Limit: 1, ImpactLimit: 1, Depth: 9,
		Ranges: []CodeLineRange{{Path: "shapes/shapes.go", StartLine: 1, EndLine: 40}}})
	if err != nil || len(few.Items) != 1 || !few.Truncated || few.Reason != "limit" || few.Depth != codeImpactMaxDepth || len(few.Items[0].Impact) != 1 || !few.Items[0].Truncated {
		t.Fatalf("changed limits %+v %v", few, err)
	}
	for name, bad := range map[string]CodeChangedQuery{
		"no ranges":   {Scope: scopeA, Repo: "fixture"},
		"bad path":    {Scope: scopeA, Repo: "fixture", Ranges: []CodeLineRange{{Path: "../x.go", StartLine: 1, EndLine: 2}}},
		"backwards":   {Scope: scopeA, Repo: "fixture", Ranges: []CodeLineRange{{Path: "x.go", StartLine: 5, EndLine: 2}}},
		"zero line":   {Scope: scopeA, Repo: "fixture", Ranges: []CodeLineRange{{Path: "x.go", StartLine: 0, EndLine: 2}}},
		"limit range": {Scope: scopeA, Repo: "fixture", Limit: 101, Ranges: []CodeLineRange{{Path: "x.go", StartLine: 1, EndLine: 2}}},
	} {
		if _, err := env.svc.CodeChanged(ctx, human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := env.svc.CodeChanged(ctx, human, CodeChangedQuery{Scope: scopeA, Repo: "missing", Ranges: []CodeLineRange{{Path: "x.go", StartLine: 1, EndLine: 2}}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo %v", err)
	}

	// token cap
	small, err := env.svc.SearchCodeSymbols(ctx, human, CodeGraphQuery{Scope: scopeA, Query: "a", Limit: 200, MaxTokens: 200})
	if err != nil || !small.Truncated || small.Reason != "token_cap" || small.TokensUsed > 200 || len(small.Items) == 0 {
		t.Fatalf("token cap %+v %v", small, err)
	}
	limited, err := env.svc.SearchCodeSymbols(ctx, human, CodeGraphQuery{Scope: scopeA, Query: "a", Limit: 2})
	if err != nil || len(limited.Items) != 2 || limited.Reason != "limit" {
		t.Fatalf("limit %+v %v", limited, err)
	}

	// incremental: one file re-uploaded replaces only its facts
	files, err := env.svc.CodeGraphFiles(ctx, human, scopeA, "fixture")
	if err != nil || files.Revision != rev || len(files.Files) != len(res.Files) {
		t.Fatalf("files %+v %v", files, err)
	}
	var report codegraph.FileFacts
	for _, f := range res.Files {
		if f.Path == "report/report.go" {
			report = f
		}
	}
	trimmed := report
	trimmed.Hash = strings.Repeat("e", 64)
	trimmed.Edges = nil
	for _, e := range report.Edges {
		if e.Kind != codegraph.EdgeCalls || e.To != codegraph.SymbolID("go", "example.com/fixture/shapes.Total") {
			trimmed.Edges = append(trimmed.Edges, e)
		}
	}
	b := batches(scopeA, "fixture", codegraph.Result{Files: []codegraph.FileFacts{trimmed}}, 10)[0]
	b.ExpectedRevision = rev - 1
	if _, err = env.svc.UploadCodeGraph(ctx, human, b); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision %v", err)
	}
	b.ExpectedRevision = rev
	out, err := env.svc.UploadCodeGraph(ctx, human, b)
	if err != nil || out.Repo.Revision != rev+1 || out.Repo.Edges != res.Stats.Edges-1 || out.Repo.Files != len(res.Files) {
		t.Fatalf("re-upload %+v %v", out, err)
	}
	rev = out.Repo.Revision
	if callers, _ := env.svc.CodeCallers(ctx, human, q("shapes.Total")); len(callers.Items) != 0 {
		t.Fatalf("stale edge kept %+v", callers.Items)
	}
	// removing a file drops its symbols
	rm := CodeGraphBatch{Scope: scopeA, Repo: "fixture", ExpectedRevision: rev, Remove: []string{"web/types.ts"}}
	if out, err = env.svc.UploadCodeGraph(ctx, human, rm); err != nil || out.Repo.Files != len(res.Files)-1 {
		t.Fatalf("remove %+v %v", out, err)
	}
	rev = out.Repo.Revision
	if _, err = env.svc.CodeCallees(ctx, human, q("web/types.ts#load")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed file still answers %v", err)
	}

	// scope: another project sees nothing, the user part of a scope does not split a project
	if repos, err := env.svc.CodeGraphRepos(env.ctx(env.other), env.other, scopeB); err != nil || len(repos) != 0 {
		t.Fatalf("scope B repos %+v %v", repos, err)
	}
	if _, err := env.svc.CodeCallers(env.ctx(env.other), env.other, CodeGraphQuery{Scope: scopeB, Symbol: "shapes.Total"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scope B sees scope A: %v", err)
	}
	// A foreign token is refused. With Postgres the denial's audit row belongs to
	// the project it was refused for, which RLS keeps the token out of, so the
	// service answers unavailable rather than denied; either way nothing is read.
	if out, err := env.svc.CodeCallers(env.ctx(env.other), env.other, q("shapes.Total")); !errors.Is(err, ErrDenied) && !errors.Is(err, ErrUnavailable) || len(out.Items) != 0 {
		t.Fatalf("scope B token read scope A: %v", err)
	}
	withUser := scopeA
	withUser.UserID = "someone"
	if found, err := env.svc.SearchCodeSymbols(ctx, human, CodeGraphQuery{Scope: withUser, Query: "Total"}); err != nil || len(found.Items) == 0 {
		t.Fatalf("project scope %+v %v", found, err)
	}

	// only a person deletes a repository's graph, with the revision they saw
	if err := env.svc.DeleteCodeGraphRepo(env.ctx(agent), agent, CodeGraphRepoInput{Scope: scopeA, Repo: "fixture", ExpectedRevision: rev}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent delete %v", err)
	}
	if err := env.svc.DeleteCodeGraphRepo(ctx, human, CodeGraphRepoInput{Scope: scopeA, Repo: "fixture", ExpectedRevision: rev}); err != nil {
		t.Fatalf("delete %v", err)
	}
	if repos, _ := env.svc.CodeGraphRepos(ctx, human, scopeA); len(repos) != 0 {
		t.Fatalf("deleted repo listed %+v", repos)
	}
}

func fakeCodeGraphEnv(t *testing.T) (codeGraphEnv, *fixture) {
	f := newFixture(t)
	f.svc.Repo = codeGraphRepo{fakeRepo: f.repo, fakeCodeGraph: newFakeCodeGraph()}
	f.auth.actors["tok-other"] = fakeActor{principal: "human-b", role: "human", verbs: map[string]bool{"read": true, "write": true}}
	auth := &scopedAuth{inner: f.auth, only: map[string]Scope{"tok-other": scopeB}}
	f.svc.Auth = auth
	return codeGraphEnv{svc: f.svc, human: f.human, agent: f.agent, other: "tok-other", ctx: func(string) context.Context { return testCtx }}, f
}

// scopedAuth denies listed tokens outside their one project.
type scopedAuth struct {
	inner Authorizer
	only  map[string]Scope
}

func (a *scopedAuth) Authorize(ctx context.Context, token string, req Authorization) (Decision, error) {
	if s, ok := a.only[token]; ok && projectScope(req.Scope) != s {
		return Decision{}, ErrDenied
	}
	if _, ok := a.only[token]; !ok && projectScope(req.Scope) == scopeB {
		return Decision{}, ErrDenied
	}
	return a.inner.Authorize(ctx, token, req)
}

func TestCodeGraphScenarioFake(t *testing.T) {
	env, _ := fakeCodeGraphEnv(t)
	runCodeGraphScenario(t, env)
}

func TestCodeGraphBatchValidation(t *testing.T) {
	env, _ := fakeCodeGraphEnv(t)
	res := indexFixture(t)
	good := batches(scopeA, "fixture", res, 100)[0]
	try := func(name string, change func(*CodeGraphBatch), want error) {
		t.Helper()
		b := good
		b.Files = append([]CodeFile(nil), good.Files...)
		b.Symbols = append([]CodeSymbol(nil), good.Symbols...)
		b.Edges = append([]CodeEdge(nil), good.Edges...)
		change(&b)
		if _, err := env.svc.UploadCodeGraph(testCtx, env.human, b); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
	}
	try("path traversal", func(b *CodeGraphBatch) { b.Files[0].Path = "../etc/passwd" }, ErrInvalid)
	try("absolute path", func(b *CodeGraphBatch) { b.Files[0].Path = "/etc/passwd" }, ErrInvalid)
	try("repo name", func(b *CodeGraphBatch) { b.Repo = "a/../b" }, ErrInvalid)
	try("symbol of an unlisted file", func(b *CodeGraphBatch) { b.Symbols[0].Path = "elsewhere.go" }, ErrInvalid)
	try("edge from another file's symbol", func(b *CodeGraphBatch) {
		for i, e := range b.Edges {
			if e.Path == "report/report.go" {
				b.Edges[i].Path = "shapes/shapes.go"
				return
			}
		}
	}, ErrInvalid)
	try("duplicate symbol", func(b *CodeGraphBatch) { b.Symbols = append(b.Symbols, b.Symbols[0]) }, ErrInvalid)
	try("unknown kind", func(b *CodeGraphBatch) { b.Symbols[0].Kind = "macro" }, ErrInvalid)
	try("bad hash", func(b *CodeGraphBatch) { b.Files[0].Hash = "abc" }, ErrInvalid)
	try("empty", func(b *CodeGraphBatch) { b.Files, b.Symbols, b.Edges = nil, nil, nil }, ErrInvalid)
	try("first write needs revision 0", func(b *CodeGraphBatch) { b.ExpectedRevision = 3 }, ErrConflict)
	if _, err := env.svc.UploadCodeGraph(testCtx, "tok-nobody", good); !errors.Is(err, ErrDenied) {
		t.Fatalf("unknown token %v", err)
	}
	// secret-looking text is redacted, not stored
	b := good
	b.Symbols = append([]CodeSymbol(nil), good.Symbols...)
	b.Symbols[0].Doc = "uses api_key=sk-" + strings.Repeat("a", 24)
	out, err := env.svc.UploadCodeGraph(testCtx, env.human, b)
	if err != nil || out.Redacted != 1 {
		t.Fatalf("redaction %+v %v", out, err)
	}
	hits, _ := env.svc.Repo.(codeGraphStore).FindCodeSymbols(testCtx, scopeA, "fixture", b.Symbols[0].ID, true, 1)
	if len(hits) != 1 || strings.Contains(hits[0].Symbol.Doc, "sk-") || !strings.Contains(hits[0].Symbol.Doc, "[REDACTED]") {
		t.Fatalf("stored doc %+v", hits)
	}
	// finish validates commit and stats
	if _, err := env.svc.FinishCodeGraph(testCtx, env.human, CodeGraphFinish{Scope: scopeA, Repo: "fixture", ExpectedRevision: out.Repo.Revision, Commit: "not a sha"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("commit %v", err)
	}
	if _, err := env.svc.FinishCodeGraph(testCtx, env.human, CodeGraphFinish{Scope: scopeA, Repo: "fixture", ExpectedRevision: out.Repo.Revision, Stats: json.RawMessage(`{"note":"password: hunter22"}`)}); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("secret stats %v", err)
	}
}

func TestCodeGraphImpactNodeCap(t *testing.T) {
	env, _ := fakeCodeGraphEnv(t)
	// a star: 400 functions in 400 files call root
	b := CodeGraphBatch{Scope: scopeA, Repo: "star"}
	rootID := codegraph.SymbolID("go", "x.root")
	b.Files = append(b.Files, CodeFile{Path: "root.go", Hash: strings.Repeat("0", 64), Lang: "go"})
	b.Symbols = append(b.Symbols, CodeSymbol{ID: rootID, Path: "root.go", Lang: "go", Kind: "func", Name: "root", QName: "x.root", StartLine: 1, EndLine: 1})
	for i := 0; i < 400; i++ {
		name := "f" + itoaCode(i)
		path := name + ".go"
		id := codegraph.SymbolID("go", "x."+name)
		b.Files = append(b.Files, CodeFile{Path: path, Hash: strings.Repeat("1", 64), Lang: "go"})
		b.Symbols = append(b.Symbols, CodeSymbol{ID: id, Path: path, Lang: "go", Kind: "func", Name: name, QName: "x." + name, StartLine: 1, EndLine: 1})
		b.Edges = append(b.Edges, CodeEdge{From: id, To: rootID, Kind: "calls", Path: path, Line: 1})
	}
	if _, err := env.svc.UploadCodeGraph(testCtx, env.human, b); err != nil {
		t.Fatal(err)
	}
	out, err := env.svc.CodeImpact(testCtx, env.human, CodeGraphQuery{Scope: scopeA, Symbol: "x.root", Limit: 200, MaxTokens: codeTokenMax})
	if err != nil || !out.Truncated || out.Reason != "node_cap" || len(out.Items) == 0 || len(out.Items) > 200 {
		t.Fatalf("node cap %+v %v", out.Reason, err)
	}
}

func TestCodeGraphHTTPRoutes(t *testing.T) {
	env, _ := fakeCodeGraphEnv(t)
	upload(t, env, scopeA, "fixture", indexFixture(t), 0)
	h := Handler(env.svc)
	do := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+env.agent)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := do("GET", "/v1/code_graph/repos?workspace_id=ws-a&project_id=proj-a", "")
	if code != 200 || len(out["repos"].([]any)) != 1 || out["authority"] != AuthorityDataOnly {
		t.Fatal(code, out)
	}
	code, out = do("GET", "/v1/code_graph/files?workspace_id=ws-a&project_id=proj-a&repo=fixture", "")
	if code != 200 || len(out["files"].(map[string]any)) != 6 {
		t.Fatal(code, out)
	}
	scope := `"scope":{"workspace_id":"ws-a","project_id":"proj-a"}`
	for path, body := range map[string]string{
		"search": `{` + scope + `,"query":"Summary"}`, "callers": `{` + scope + `,"symbol":"report.Summary"}`,
		"callees": `{` + scope + `,"symbol":"report.Summary"}`, "impact": `{` + scope + `,"symbol":"shapes.Total","depth":2}`,
	} {
		code, out = do("POST", "/v1/code_graph/"+path, body)
		if code != 200 || out["op"] != path || out["authority"] != AuthorityDataOnly || len(out["items"].([]any)) == 0 {
			t.Fatal(path, code, out)
		}
	}
	code, out = do("POST", "/v1/code_graph/changed", `{`+scope+`,"repo":"fixture","ranges":[{"path":"shapes/shapes.go","start_line":20,"end_line":20}]}`)
	if code != 200 || out["op"] != "changed" || out["authority"] != AuthorityDataOnly || len(out["items"].([]any)) != 1 {
		t.Fatal("changed", code, out)
	}
	if code, out = do("POST", "/v1/code_graph/search", `{`+scope+`,"query":"x","unknown":1}`); code != 400 {
		t.Fatal("unknown field accepted", code, out)
	}
	if code, _ = do("POST", "/v1/code_graph/callers", `{`+scope+`,"symbol":"nope.Nothing"}`); code != 404 {
		t.Fatal("missing symbol", code)
	}
}
