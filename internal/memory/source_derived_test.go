package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
)

func (f *fakeRepo) ListSourceDerived(_ context.Context, sourceID string, in DerivedListInput) ([]Record, error) {
	in, e := normalizeDerivedList(in)
	if e != nil {
		return nil, e
	}
	var rows []Record
	for _, r := range f.rows(in.Collection) {
		if r.Scope == in.Scope && r.ID > in.Cursor && directlyDerived(r, sourceID) {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if len(rows) > in.Limit+1 {
		rows = rows[:in.Limit+1]
	}
	return rows, nil
}
func derivedFixture(t *testing.T, repo Repository) (*fixture, Record, Record, []Record) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	if fake, ok := repo.(*fakeRepo); ok {
		f.repo = fake
	}
	src := f.source()
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "second evidence"}))
	var memories []Record
	for i, text := range []string{"first derived memory", "second derived memory", "still a candidate"} {
		c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: append(refs(src), refs(other)...)}))
		if i < 2 {
			m, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
			if e != nil {
				t.Fatal(e)
			}
			if i == 1 {
				m = f.transition(f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision, Status: "expired"})
			}
			memories = append(memories, m)
		}
	}
	sort.Slice(memories, func(i, j int) bool { return memories[i].ID < memories[j].ID })
	return f, src, other, memories
}
func exerciseSourceDerived(t *testing.T, repo Repository) {
	t.Helper()
	f, src, other, memories := derivedFixture(t, repo)
	for _, collection := range []string{colMemories, "memory_candidates"} {
		in := DerivedListInput{Scope: scopeA, Collection: collection, Limit: 1}
		var ids []string
		for n := 0; n < 5; n++ {
			page, e := f.svc.ListSourceDerived(testCtx, f.human, src.ID, in)
			if e != nil || len(page.Items) != 1 || page.Source.ID != src.ID || page.Collection != collection || page.Authority != AuthorityDataOnly {
				t.Fatal("derived page", e, page)
			}
			ids = append(ids, page.Items[0].ID)
			if collection == colMemories && page.Items[0].ContentHash != ContentHash(recordContent(page.Items[0])) {
				t.Fatal("memory hash")
			}
			if page.NextCursor == "" {
				break
			}
			in.Cursor = page.NextCursor
		}
		if !sort.StringsAreSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			t.Fatal("duplicate/unordered derived list", ids)
		}
		want := 3
		if collection == colMemories {
			want = 2
		}
		if len(ids) != want {
			t.Fatal("history missing", collection, len(ids))
		}
	}
	// Other scopes cannot reach the parent or match rows in the repository itself.
	for _, scope := range []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		in := DerivedListInput{Scope: scope}
		rows, e := repo.ListSourceDerived(testCtx, src.ID, in)
		if e != nil || len(rows) != 0 {
			t.Fatal("repository scope leak", e)
		}
		page, e := f.svc.ListSourceDerived(testCtx, f.human, src.ID, in)
		if !errors.Is(e, ErrNotFound) || len(page.Items) != 0 {
			t.Fatal("source scope leak", e)
		}
	}
	empty := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "no derived memories"}))
	page, e := f.svc.ListSourceDerived(testCtx, f.human, empty.ID, DerivedListInput{Scope: scopeA, Cursor: memories[0].ID})
	if e != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatal("other source cursor", e)
	}
	q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}, "collection": {"memory_candidates"}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/derived?"+q.Encode(), nil)
	r.Header.Set("Authorization", "Bearer "+f.human)
	Handler(f.svc).ServeHTTP(w, r)
	var got SourceDerived
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Items) != 3 {
		t.Fatal("derived HTTP", w.Code, w.Body.String())
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: memories[1].ID}
	page, e = f.svc.ListSourceDerived(testCtx, f.human, src.ID, DerivedListInput{Scope: scopeA, Limit: 1})
	if !errors.Is(e, ErrDenied) || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatal("lookahead disclosure", e)
	}
	f.svc.Auth = f.auth
	if e = f.svc.Delete(testCtx, f.human, "sources", other.ID, ChangeInput{Scope: scopeA, ExpectedRevision: other.Revision}); e != nil {
		t.Fatal(e)
	}
	page, e = f.svc.ListSourceDerived(testCtx, f.human, src.ID, DerivedListInput{Scope: scopeA})
	if e == nil || len(page.Items) != 0 {
		t.Fatal("deleted second evidence leaked", e)
	}
}
func TestSourceDerived(t *testing.T) { exerciseSourceDerived(t, newFakeRepo()) }
func TestSourceDerivedGuards(t *testing.T) {
	for _, mode := range []string{"audit", "principal", "role", "record", "span"} {
		t.Run(mode, func(t *testing.T) {
			f, src, other, rows := derivedFixture(t, newFakeRepo())
			changed := false
			if mode == "audit" {
				f.repo.failAudit = true
			} else {
				f.svc.Auth = sourceListChangeAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
					if in.Action != "read:memories" || changed {
						return nil
					}
					changed = true
					switch mode {
					case "principal":
						d.PrincipalID = "other"
					case "role":
						d.Role = "agent"
					case "record", "span":
						col, r := colMemories, rows[0]
						if mode == "span" {
							col, r = "sources", other
						}
						r.Text = "content changed without revision"
						f.repo.mu.Lock()
						f.repo.store[col][recKey(scopeA, r.ID)] = r
						f.repo.mu.Unlock()
					}
					return nil
				}}
			}
			page, e := f.svc.ListSourceDerived(testCtx, f.human, src.ID, DerivedListInput{Scope: scopeA})
			want := ErrConflict
			if mode == "audit" {
				want = ErrUnavailable
			}
			if mode == "principal" || mode == "role" {
				want = ErrDenied
			}
			if !errors.Is(e, want) || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatal("guard failed", e)
			}
		})
	}
	f := newFixture(t)
	src := f.source()
	q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}}
	for _, suffix := range []string{"&collection=", "&collection=artifacts", "&collection=jobs", "&collection=memories&collection=memory_candidates", "&limit=0", "&limit=101", "&limit=1&limit=2", "&cursor=%00", "&status=active", "&unknown=x", "&bad=%ZZ"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/derived?"+q.Encode()+suffix, nil)
		r.Header.Set("Authorization", "Bearer "+f.human)
		Handler(f.svc).ServeHTTP(w, r)
		if w.Code != 400 || strings.Contains(w.Body.String(), "next_cursor") {
			t.Fatal("invalid query", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	RecoveryHandler(f.svc).ServeHTTP(w, httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/derived?"+q.Encode(), nil))
	if w.Code == 200 {
		t.Fatal("recovery opened derived records")
	}
}
