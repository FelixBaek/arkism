package memory

import (
	"context"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// countingAuth wraps an authorizer, counts its calls, and claims scope-only answers when asked to.
type countingAuth struct {
	inner Authorizer
	only  bool
	calls atomic.Int64
}

func (a *countingAuth) Authorize(ctx context.Context, token string, req Authorization) (Decision, error) {
	a.calls.Add(1)
	return a.inner.Authorize(ctx, token, req)
}
func (a *countingAuth) ScopeOnly() bool { return a.only }

// #166: asking a scope-only authorizer once per request must give every listed item the same allow
// or deny that one call per item gives, including items authorizeAt refuses by itself.
func TestItemAuthorizerMatchesPerItemCalls(t *testing.T) {
	f := newFixture(t)
	seedGraph(newGraphWorld(t, func(col string, r Record) error { return f.repo.Insert(testCtx, col, r) }, f.emb.space))
	seedGraphUses(t, f.repo)
	secret := f.activeMemory("hidden key-like id") // a listed secret-looking id fails the catalog, so it stays out of its query
	secret.ID = "AKIA" + "ABCDEFGHIJKLMNOP"
	if err := f.repo.Insert(testCtx, colMemories, secret); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"s1", "s2", "AKIA" + "QRSTUVWXYZ012345", "s3"} {
		if err := f.repo.Insert(testCtx, "sources", Record{ID: id, Scope: scopeA, Revision: 1, Status: "active", Kind: "note", Text: "a note", CollectedAt: &at, Sensitivity: "private", CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	// The catalog runs on plain approved memories (the graph world's rows are not catalog-shaped).
	lists := newFixture(t)
	for range 5 {
		lists.activeMemory("a catalog rule")
	}
	type result struct {
		graph   MemoryGraphMap
		center  MemoryGraph
		list    []string
		sources []string
		err     []error
		calls   int64
	}
	run := func(ctx context.Context, token string, only bool) result {
		auth := &countingAuth{inner: f.auth, only: only}
		f.svc.Auth, lists.svc.Auth = auth, auth
		var r result
		g, e1 := f.svc.MemoryGraphMap(ctx, token, MemoryGraphQuery{Scope: scopeA, ProjectKey: "owner/mine"})
		c, e2 := f.svc.MemoryGraph(ctx, token, graphQuery(2))
		l, e3 := lists.svc.ListMemories(ctx, token, MemoryListInput{Scope: scopeA})
		s, e4 := f.svc.ListSources(ctx, token, SourceListInput{Scope: scopeA, Query: "note", Limit: 100})
		r.graph, r.center, r.err = g, c, []error{e1, e2, e3, e4}
		for _, m := range l.Items {
			r.list = append(r.list, m.ID)
		}
		for _, m := range s.Items {
			r.sources = append(r.sources, m.ID)
		}
		r.calls = auth.calls.Load()
		return r
	}
	for _, token := range []string{f.human, f.work, testPrincipalToken("tok-unknown")} {
		each, once := run(testCtx, token, false), run(testCtx, token, true)
		eachCalls, onceCalls := each.calls, once.calls
		each.calls, once.calls = 0, 0
		if !reflect.DeepEqual(each, once) {
			t.Fatalf("%s: per-item and scope-only results differ\n%+v\n%+v", token, each, once)
		}
		if token == f.human {
			if len(each.graph.Nodes) == 0 || len(each.list) != 5 || !slices.Contains(each.sources, "s3") || slices.Contains(each.sources, "AKIA"+"QRSTUVWXYZ012345") || each.err[2] != nil {
				t.Fatal("fixture", len(each.graph.Nodes), each.list, each.sources, each.err)
			}
			for _, n := range once.graph.Nodes {
				if n.ID == secret.ID {
					t.Fatal("secret-looking id listed")
				}
			}
			if onceCalls >= eachCalls/4 {
				t.Fatalf("scope-only asked %d times, per-item %d", onceCalls, eachCalls)
			}
		}
	}
	// A request whose actor changed is refused per item either way.
	actor := &httpActor{principal: "human-1", role: "human"}
	actor.changed.Store(true)
	ctx := context.WithValue(testCtx, httpActorKey{}, actor)
	each, once := run(ctx, f.human, false), run(ctx, f.human, true)
	each.calls, once.calls = 0, 0
	if !reflect.DeepEqual(each, once) {
		t.Fatalf("changed actor: results differ\n%+v\n%+v", each, once)
	}
}
