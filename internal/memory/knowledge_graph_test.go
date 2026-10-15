package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type graphTestRepo struct {
	Repository
	edges    []KnowledgeRelation
	beforeTx func()
}
type graphTestTx struct {
	Tx
	owner *graphTestRepo
}

func (r *graphTestRepo) PutKnowledgeRelation(_ context.Context, e KnowledgeRelation) error {
	r.edges = append(r.edges, e)
	return nil
}
func (r *graphTestRepo) KnowledgeRelations(_ context.Context, s Scope, n GraphEntity, limit int) ([]KnowledgeRelation, error) {
	out := []KnowledgeRelation{}
	for _, e := range r.edges {
		if e.Scope == s && e.Source == n && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}
func (t graphTestTx) PutKnowledgeRelation(ctx context.Context, e KnowledgeRelation) error {
	return t.owner.PutKnowledgeRelation(ctx, e)
}
func (t graphTestTx) KnowledgeRelations(ctx context.Context, s Scope, n GraphEntity, l int) ([]KnowledgeRelation, error) {
	return t.owner.KnowledgeRelations(ctx, s, n, l)
}
func (r *graphTestRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if r.beforeTx != nil {
		f := r.beforeTx
		r.beforeTx = nil
		f()
	}
	old := append([]KnowledgeRelation{}, r.edges...)
	err := r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, graphTestTx{tx, r}) })
	if err != nil {
		r.edges = old
	}
	return err
}
func graphMemory(t *testing.T, f *fixture, repo Repository, id string) Record {
	t.Helper()
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "original graph evidence " + id}))
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: id, Scope: scopeA, Kind: "decision", Text: "approved graph evidence", Status: "active", ApprovedBy: "human", Revision: 1, Sources: refs(src), CreatedAt: now, UpdatedAt: now}
	if err := repo.Insert(testCtx, colMemories, r); err != nil {
		t.Fatal(err)
	}
	return r
}
func graphClaim(r Record, a, b string) KnowledgeRelationInput {
	return KnowledgeRelationInput{Scope: r.Scope, Source: GraphEntity{"task", a}, Target: GraphEntity{"task", b}, Relation: "depends_on", Evidence: Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}}
}

func TestKnowledgeGraphHumanEvidenceAndBounds(t *testing.T) {
	f := newFixture(t)
	repo := &graphTestRepo{Repository: f.repo}
	f.svc.Repo = repo
	r := graphMemory(t, f, repo, "basis")
	if _, err := f.svc.PutKnowledgeRelation(testCtx, f.agent, graphClaim(r, "a", "b")); !errors.Is(err, ErrDenied) {
		t.Fatal("agent asserted", err)
	}
	for _, pair := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}} {
		if _, err := f.svc.PutKnowledgeRelation(testCtx, f.human, graphClaim(r, pair[0], pair[1])); err != nil {
			t.Fatal(err)
		}
	}
	in := KnowledgeGraphInput{Scope: scopeA, Start: GraphEntity{"task", "a"}, MaxDepth: 3, Limit: 50}
	got, err := f.svc.ExpandKnowledgeGraph(testCtx, f.agent, in)
	if err != nil || len(got.Items) != 3 {
		t.Fatal(got, err)
	}
	in.Limit = 1
	got, err = f.svc.ExpandKnowledgeGraph(testCtx, f.agent, in)
	if err != nil || len(got.Items) != 1 || !got.Truncated {
		t.Fatal(got, err)
	}
	in.Limit = 50
	in.Scope = scopeB
	got, err = f.svc.ExpandKnowledgeGraph(testCtx, f.agent, in)
	if err != nil || len(got.Items) != 0 {
		t.Fatal("scope leak", got, err)
	}
	// Canonical revision changes invalidate graph evidence even if store retains a stale edge.
	r.Revision++
	if err = repo.Replace(testCtx, colMemories, r, 1); err != nil {
		t.Fatal(err)
	}
	in.Scope = scopeA
	got, err = f.svc.ExpandKnowledgeGraph(testCtx, f.agent, in)
	if err != nil || len(got.Items) != 0 {
		t.Fatal("stale graph", got, err)
	}
}
func TestKnowledgeGraphDeniedBridgeAndConcurrentErasure(t *testing.T) {
	f := newFixture(t)
	repo := &graphTestRepo{Repository: f.repo}
	f.svc.Repo = repo
	r := graphMemory(t, f, repo, "first")
	second := graphMemory(t, f, repo, "second")
	for _, in := range []KnowledgeRelationInput{graphClaim(r, "a", "b"), graphClaim(second, "b", "c")} {
		if _, err := f.svc.PutKnowledgeRelation(testCtx, f.human, in); err != nil {
			t.Fatal(err)
		}
	}
	in := KnowledgeGraphInput{Scope: scopeA, Start: GraphEntity{"task", "a"}, MaxDepth: 3}
	// Expired first edge cannot serve as a bridge to live second evidence.
	past := time.Now().Add(-time.Hour)
	r.ExpiresAt = &past
	if err := repo.Replace(testCtx, colMemories, r, 1); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.ExpandKnowledgeGraph(testCtx, f.agent, in)
	if err != nil || len(got.Items) != 0 {
		t.Fatal(got, err)
	}
	r.ExpiresAt = nil
	if err = repo.Replace(testCtx, colMemories, r, 1); err != nil {
		t.Fatal(err)
	}
	repo.beforeTx = func() {
		r.ContentErased = true
		if e := repo.Replace(testCtx, colMemories, r, 1); e != nil {
			t.Fatal(e)
		}
	}
	got, err = f.svc.ExpandKnowledgeGraph(testCtx, f.agent, in)
	if !errors.Is(err, ErrConflict) || len(got.Items) != 0 {
		t.Fatal("erasure race", got, err)
	}
}
func TestKnowledgeGraphScanCap(t *testing.T) {
	f := newFixture(t)
	repo := &graphTestRepo{Repository: f.repo}
	f.svc.Repo = repo
	r := graphMemory(t, f, repo, "basis")
	r.Revision = 7 // Every edge stale, so result limit alone would not bound work.
	for i := 0; i < 210; i++ {
		repo.edges = append(repo.edges, KnowledgeRelation{KnowledgeRelationInput: graphClaim(r, "a", fmt.Sprintf("node%d", i)), AssertedBy: "human"})
	}
	got, err := f.svc.ExpandKnowledgeGraph(testCtx, f.agent, KnowledgeGraphInput{Scope: scopeA, Start: GraphEntity{"task", "a"}, MaxDepth: 3})
	if err != nil || len(got.Items) != 0 || !got.Truncated {
		t.Fatal(got, err)
	}
}

func TestKnowledgeGraphRejectsUnusableEvidence(t *testing.T) {
	for _, state := range []string{"future", "expired", "unapproved", "conflicted", "quarantined", "erased"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			repo := &graphTestRepo{Repository: f.repo}
			f.svc.Repo = repo
			r := graphMemory(t, f, repo, "basis")
			future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
			switch state {
			case "future":
				r.EffectiveAt = &future
			case "expired":
				r.ExpiresAt = &past
			case "unapproved":
				r.ApprovedBy = ""
			case "conflicted":
				r.Status = "conflicted"
			case "quarantined":
				r.PrivacyQuarantined = true
			case "erased":
				r.ContentErased = true
			}
			if err := repo.Replace(testCtx, colMemories, r, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.PutKnowledgeRelation(testCtx, f.human, graphClaim(r, "a", "b")); err == nil {
				t.Fatal("unusable evidence accepted")
			}
			if len(repo.edges) != 0 {
				t.Fatal("failed write stored relation")
			}
		})
	}
}

type graphDenyEvidenceAuth struct {
	Authorizer
	id string
}

func (a graphDenyEvidenceAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.Action == "read:memories" && in.ResourceID == a.id {
		return Decision{}, ErrDenied
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
func TestKnowledgeGraphCannotTraverseDeniedEvidence(t *testing.T) {
	f := newFixture(t)
	repo := &graphTestRepo{Repository: f.repo}
	f.svc.Repo = repo
	first := graphMemory(t, f, repo, "first")
	second := graphMemory(t, f, repo, "second")
	for _, in := range []KnowledgeRelationInput{graphClaim(first, "a", "b"), graphClaim(second, "b", "c")} {
		if _, err := f.svc.PutKnowledgeRelation(testCtx, f.human, in); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.Auth = graphDenyEvidenceAuth{f.auth, first.ID}
	out, err := f.svc.ExpandKnowledgeGraph(testCtx, f.agent, KnowledgeGraphInput{Scope: scopeA, Start: GraphEntity{"task", "a"}, MaxDepth: 3})
	if err != nil || len(out.Items) != 0 {
		t.Fatal("traversed denied evidence", out, err)
	}
	// The accessible second edge still exists, proving it was skipped because
	// the bridge was denied rather than because all memory reads were denied.
	out, err = f.svc.ExpandKnowledgeGraph(testCtx, f.agent, KnowledgeGraphInput{Scope: scopeA, Start: GraphEntity{"task", "b"}, MaxDepth: 3})
	if err != nil || len(out.Items) != 1 {
		t.Fatal("accessible edge missing", out, err)
	}
}
