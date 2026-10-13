package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

func (tx fakeTx) SourceDerivedReferences(_ context.Context, sourceID string, in DerivedCountInput) ([]Ref, error) {
	in, e := normalizeDerivedCount(in)
	if e != nil {
		return nil, e
	}
	out := []Ref{}
	for _, r := range tx.store[in.Collection] {
		if r.Scope == in.Scope && directlyDerived(r, sourceID) {
			out = append(out, Ref{Collection: in.Collection, ID: r.ID, Revision: r.Revision})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > sourceDerivedCountLimit+1 {
		out = out[:sourceDerivedCountLimit+1]
	}
	return out, nil
}
func (f *fakeRepo) SourceDerivedReferences(ctx context.Context, sourceID string, in DerivedCountInput) ([]Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().SourceDerivedReferences(ctx, sourceID, in)
}
func exerciseDerivedCount(t *testing.T, repo Repository) {
	t.Helper()
	f, src, other, rows := derivedFixture(t, repo)
	for _, test := range []struct {
		collection string
		count      int
	}{{"memories", 2}, {"memory_candidates", 3}} {
		out, e := f.svc.CountSourceDerived(testCtx, f.human, src.ID, DerivedCountInput{Scope: scopeA, Collection: test.collection})
		if e != nil || out.Count != test.count || out.Collection != test.collection || out.Source.ID != src.ID || out.Authority != AuthorityDataOnly {
			t.Fatal("count/history", e, out)
		}
	}
	for _, scope := range []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		refs, e := repo.SourceDerivedReferences(testCtx, src.ID, DerivedCountInput{Scope: scope})
		if e != nil || len(refs) != 0 {
			t.Fatal("scoped count query", e)
		}
		out, e := f.svc.CountSourceDerived(testCtx, f.human, src.ID, DerivedCountInput{Scope: scope})
		if !errors.Is(e, ErrNotFound) || out.Authority != "" {
			t.Fatal("count scope leak", e)
		}
	}
	empty := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "no derivations here"}))
	out, e := f.svc.CountSourceDerived(testCtx, f.human, empty.ID, DerivedCountInput{Scope: scopeA})
	if e != nil || out.Count != 0 || out.Authority != AuthorityDataOnly {
		t.Fatal("empty count", e)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: rows[1].ID}
	out, e = f.svc.CountSourceDerived(testCtx, f.human, src.ID, DerivedCountInput{Scope: scopeA})
	if !errors.Is(e, ErrDenied) || out.Authority != "" || out.Count != 0 {
		t.Fatal("partial count on denial", e)
	}
	f.svc.Auth = f.auth
	if e = f.svc.Delete(testCtx, f.human, "sources", other.ID, ChangeInput{Scope: scopeA, ExpectedRevision: other.Revision}); e != nil {
		t.Fatal(e)
	}
	out, e = f.svc.CountSourceDerived(testCtx, f.human, src.ID, DerivedCountInput{Scope: scopeA})
	if e == nil || out.Authority != "" {
		t.Fatal("invalid evidence count", e)
	}
}
func TestSourceDerivedCount(t *testing.T) { exerciseDerivedCount(t, newFakeRepo()) }
func TestSourceDerivedCountHTTP(t *testing.T) {
	f, src, _, _ := derivedFixture(t, newFakeRepo())
	q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}}
	request := func(suffix, token string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/derived/count?"+q.Encode()+suffix, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		Handler(f.svc).ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
		if want != 200 && strings.Contains(w.Body.String(), "\"count\"") {
			t.Fatal("failed count exposed value")
		}
		return w.Body.String()
	}
	var out SourceDerivedCount
	if json.Unmarshal([]byte(request("", f.human, 200)), &out) != nil || out.Count != 2 {
		t.Fatal("HTTP count")
	}
	for _, suffix := range []string{"&collection=", "&collection=artifacts", "&collection=memories&collection=memory_candidates", "&limit=1", "&cursor=", "&unknown=x", "&bad=%ZZ"} {
		request(suffix, f.human, 400)
	}
	request("", "", 403)
	f.repo.failAudit = true
	request("", f.human, 503)
	f.repo.failAudit = false
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, outage: true}
	request("", f.human, 503)
	w := httptest.NewRecorder()
	RecoveryHandler(f.svc).ServeHTTP(w, httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/derived/count?"+q.Encode(), nil))
	if w.Code == 200 {
		t.Fatal("recovery opened count")
	}
}
func TestSourceDerivedCountSetAndActorChanges(t *testing.T) {
	for _, mode := range []string{"insert", "revision", "principal", "role", "span", "collection_denied"} {
		t.Run(mode, func(t *testing.T) {
			f, src, other, rows := derivedFixture(t, newFakeRepo())
			changed := false
			f.svc.Auth = sourceListChangeAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
				if in.Action != "read:memories" || changed {
					return nil
				}
				changed = true
				switch mode {
				case "principal":
					d.PrincipalID = "different"
				case "role":
					d.Role = "agent"
				case "insert":
					r := rows[0]
					r.ID = "inserted-count-record"
					return f.repo.Insert(testCtx, colMemories, r)
				case "revision":
					r := rows[0]
					r.Revision++
					return f.repo.Replace(testCtx, colMemories, r, r.Revision-1)
				case "span":
					r := other
					r.Text = "changed evidence"
					f.repo.mu.Lock()
					f.repo.store["sources"][recKey(scopeA, r.ID)] = r
					f.repo.mu.Unlock()
				}
				return nil
			}}
			if mode == "collection_denied" {
				f.svc.Auth = countCollectionDenyAuth{Authorizer: f.auth}
			}
			out, e := f.svc.CountSourceDerived(testCtx, f.human, src.ID, DerivedCountInput{Scope: scopeA})
			want := ErrConflict
			if mode == "principal" || mode == "role" || mode == "collection_denied" {
				want = ErrDenied
			}
			if !errors.Is(e, want) || out.Authority != "" {
				t.Fatal("changed count admitted", e)
			}
		})
	}
}

type countCollectionDenyAuth struct{ Authorizer }

func (a countCollectionDenyAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.Action == "read:memories" && in.ResourceID == "" {
		return Decision{}, nil
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
func exerciseDerivedCountLimit(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	// Metadata fixture models many historical records. Duplicate source refs count once per record.
	for i := 0; i < sourceDerivedCountLimit; i++ {
		r := Record{ID: fmt.Sprintf("count-%04d", i), Scope: scopeA, Revision: 1, Status: "expired", Sources: append(refs(src), refs(src)...)}
		if e := repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
	out, e := f.svc.CountSourceDerived(testCtx, f.human, src.ID, DerivedCountInput{Scope: scopeA})
	if e != nil || out.Count != sourceDerivedCountLimit {
		t.Fatal("count at bound", e, out.Count)
	}
	r := Record{ID: "overflow-count", Scope: scopeA, Revision: 1, Sources: refs(src)}
	if e = repo.Insert(testCtx, colMemories, r); e != nil {
		t.Fatal(e)
	}
	out, e = f.svc.CountSourceDerived(testCtx, f.human, src.ID, DerivedCountInput{Scope: scopeA})
	if !errors.Is(e, ErrUnavailable) || out.Authority != "" {
		t.Fatal("truncated count", e)
	}
}
func TestSourceDerivedCountLimit(t *testing.T) { exerciseDerivedCountLimit(t, newFakeRepo()) }
