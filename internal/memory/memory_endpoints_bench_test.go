package memory

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BenchmarkMemoryEndpoints (#166) times the memory screens' endpoints over loopback HTTP on a
// disposable RLS-restricted Postgres with desktop-sized data: thousands of memories and ~86k
// context_usage receipts (a quarter in another scope, like the stray staging rows).
// Needs POSTGRES_TEST_DSN; seeding takes a while, so run it with -benchtime=Nx:
//
//	go test ./internal/memory -run XXX -bench BenchmarkMemoryEndpoints -benchtime=3x
func BenchmarkMemoryEndpoints(b *testing.B) {
	for _, n := range []int{640, 2000} { // 640: the desktop store in #166
		b.Run(fmt.Sprintf("memories=%d", n), func(b *testing.B) { benchMemoryEndpoints(b, n) })
	}
}

func benchMemoryEndpoints(b *testing.B, memories int) {
	srv, token := seedMemoryEndpointBench(b, memories, 300, 86000)
	scope := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}}.Encode()
	calls := []struct{ name, method, path, body string }{
		{"memories_query_recent", "POST", "/v1/memories/query", `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"}}`},
		{"memories_query_used", "POST", "/v1/memories/query", `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"sort":"used"}`},
		{"memories_query_idle", "POST", "/v1/memories/query", `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"state":"idle"}`},
		{"memory_usage", "GET", "/v1/memory_usage?" + scope, ""},
		{"sources", "GET", "/v1/sources?limit=100&" + scope, ""},
		{"sources_query", "GET", "/v1/sources?q=original&limit=100&" + scope, ""},
		{"session_archives_expire", "POST", "/v1/session-archives/expire", `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"}}`},
		{"memory_graph_all", "GET", "/v1/memory-graph?mode=all&" + scope, ""},
	}
	do := func(b *testing.B, method, path, body string) {
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			b.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			b.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
	}
	for _, c := range calls {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				do(b, c.method, c.path, c.body)
			}
		})
	}
	// The desktop opens these screens together; each request then waits on the others' queries.
	b.Run("together", func(b *testing.B) {
		for b.Loop() {
			var wg sync.WaitGroup
			for _, c := range calls {
				wg.Go(func() { do(b, c.method, c.path, c.body) })
			}
			wg.Wait()
		}
	})
}

func seedMemoryEndpointBench(b *testing.B, memories, sources, receipts int) (*httptest.Server, string) {
	admin := isolatedPostgres(b)
	f := newFixture(b)
	if err := admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "human-1", Role: "human", Token: f.human, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); err != nil {
		b.Fatal(err)
	}
	f.svc.Repo = admin
	template := f.activeMemory("bench template rule")
	srcs := make([]Record, sources)
	for i := range srcs {
		srcs[i] = f.source()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	ids := make([]string, memories)
	for i := range ids {
		r := template
		r.ID = fmt.Sprintf("m-%05d", i)
		r.Text = fmt.Sprintf("bench rule %d about topic %d with enough words to look like a real memory body", i, i%40)
		r.Sources = refs(srcs[i%sources])
		r.Tags = []string{fmt.Sprintf("session:s%d", i/8), fmt.Sprintf("repo:owner/r%d", i%5)}
		r.CreatedAt = now.Add(-time.Duration(i) * time.Hour)
		r.Embedding = &Embedding{Provider: "fake", Model: "fake", Space: f.emb.space, Dimension: VectorDimension, EmbeddedAt: now, Vector: graphVector(float64(i) * 2 * math.Pi / float64(memories)), ContentHash: ContentHash(recordContent(r))}
		if err := admin.Insert(testCtx, colMemories, r); err != nil {
			b.Fatal(err)
		}
		ids[i] = r.ID
	}
	stray := Scope{WorkspaceID: "ws-staging", ProjectID: "proj-test"}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for w := range 8 {
		wg.Go(func() {
			for i := w; i < receipts; i += 8 {
				scope := scopeA
				if i%4 == 3 {
					scope = stray
				}
				items := []MemoryUse{}
				for k := range 1 + i%5 {
					items = append(items, MemoryUse{Memory: Ref{Collection: colMemories, ID: ids[(i*7+k*131)%memories], Revision: template.Revision}, Rank: k + 1})
				}
				at := now.Add(-time.Duration(i%(60*24)) * time.Hour / 24)
				r := Record{ID: fmt.Sprintf("use-%06d", i), Scope: scope, Kind: "context_usage", Status: "done", Revision: 1, CreatedAt: at, UpdatedAt: at, CreatedBy: "agent-1", Usage: &ContextUsage{Items: items}}
				if err := admin.Insert(testCtx, "jobs", r); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		b.Fatal(err)
	}
	if _, err := admin.pool.Exec(testCtx, "ANALYZE"); err != nil {
		b.Fatal(err)
	}
	// Production runs as a non-owner role under FORCE RLS with a 10-connection pool.
	cfg := restrictedPostgres(b, admin).pool.Config()
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(testCtx, cfg)
	if err != nil {
		b.Fatal(err)
	}
	app := &Postgres{pool: pool, ownsPool: true}
	b.Cleanup(app.Close)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		b.Fatal(err)
	}
	f.svc.Repo, f.svc.Auth = app, auth
	srv := httptest.NewServer(Handler(f.svc))
	b.Cleanup(srv.Close)
	return srv, f.human
}
