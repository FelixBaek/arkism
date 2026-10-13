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

func (f *fakeRepo) ListSourceArtifacts(_ context.Context, sourceID string, in ArtifactListInput) ([]Record, error) {
	in, e := normalizeArtifactList(in)
	if e != nil {
		return nil, e
	}
	var rows []Record
	for _, r := range f.rows(colArtifacts) {
		if r.Scope != in.Scope || r.ID <= in.Cursor {
			continue
		}
		for _, ref := range r.Sources {
			if ref.SourceID == sourceID {
				rows = append(rows, r)
				break
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if len(rows) > in.Limit+1 {
		rows = rows[:in.Limit+1]
	}
	return rows, nil
}
func artifactListFixture(t *testing.T, repo Repository) (*fixture, Record, []Record) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	if fake, ok := repo.(*fakeRepo); ok {
		f.repo = fake
	}
	src := f.source()
	var rows []Record
	d, e := f.svc.authorize(testCtx, f.human, "write:artifacts", scopeA, "", "")
	if e != nil {
		t.Fatal(e)
	}
	for _, text := range []string{"listed file one", "listed file two", "listed file three"} {
		e = repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			r, e := attachArtifactMetadata(ctx, tx, scopeA, Ref{Collection: "sources", ID: src.ID, Revision: src.Revision}, "code", artifactMetadata{Digest: ContentHash(text), MIME: "text/plain", Bytes: int64(len(text))}, d)
			if e == nil {
				rows = append(rows, r)
			}
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return f, src, rows
}
func exerciseArtifactList(t *testing.T, repo Repository) {
	t.Helper()
	f, src, rows := artifactListFixture(t, repo)
	// No local bytes or blob root exist: this API describes registrations only.
	in := ArtifactListInput{Scope: scopeA, Limit: 1}
	var ids []string
	for n := 0; n < 4; n++ {
		page, e := f.svc.ListSourceArtifacts(testCtx, f.human, src.ID, in)
		if e != nil || len(page.Items) != 1 || page.Source.ID != src.ID || page.Authority != AuthorityDataOnly {
			t.Fatal("file page", e, page)
		}
		ids = append(ids, page.Items[0].ID)
		if page.NextCursor == "" {
			break
		}
		in.Cursor = page.NextCursor
	}
	if !slices.Equal(ids, []string{rows[0].ID, rows[1].ID, rows[2].ID}) {
		t.Fatal("file pagination", ids)
	}
	// Raw cursor positions never provide authority for another scope or source.
	for _, scope := range []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		page, e := f.svc.ListSourceArtifacts(testCtx, f.human, src.ID, ArtifactListInput{Scope: scope, Cursor: rows[0].ID})
		if !errors.Is(e, ErrNotFound) || len(page.Items) != 0 {
			t.Fatal("scope leak", e)
		}
	}
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "another source without files"}))
	page, e := f.svc.ListSourceArtifacts(testCtx, f.human, other.ID, ArtifactListInput{Scope: scopeA, Cursor: rows[0].ID})
	if e != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatal("empty list", e)
	}
	q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}, "limit": {"1"}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/artifacts?"+q.Encode(), nil)
	r.Header.Set("Authorization", "Bearer "+f.human)
	Handler(f.svc).ServeHTTP(w, r)
	var got SourceArtifacts
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Items) != 1 || got.Items[0].Digest != rows[0].Artifact.Digest || got.Items[0].Bytes != rows[0].Artifact.Bytes || got.Items[0].MIME != "text/plain" {
		t.Fatal("HTTP descriptor", w.Code, w.Body.String())
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: rows[1].ID}
	page, e = f.svc.ListSourceArtifacts(testCtx, f.human, src.ID, ArtifactListInput{Scope: scopeA, Limit: 1})
	if !errors.Is(e, ErrDenied) || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatal("denied lookahead leaked", e)
	}
}
func TestArtifactList(t *testing.T) { exerciseArtifactList(t, newFakeRepo()) }
func TestArtifactListHTTPRefuses(t *testing.T) {
	f, src, _ := artifactListFixture(t, newFakeRepo())
	q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}}
	request := func(suffix, token string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/artifacts?"+q.Encode()+suffix, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		Handler(f.svc).ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), "digest") || strings.Contains(w.Body.String(), "next_cursor") {
			t.Fatal("invalid request", w.Code, w.Body.String())
		}
	}
	for _, suffix := range []string{"&limit=0", "&limit=101", "&limit=1&limit=2", "&cursor=bogus", "&cursor=artifact-xyz", "&kind=code", "&bad=%ZZ", "&workspace_id=other"} {
		request(suffix, f.human, 400)
	}
	request("", "", 403)
	f.repo.failAudit = true
	request("", f.human, 503)
	f.repo.failAudit = false
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, outage: true}
	request("", f.human, 503)
	w := httptest.NewRecorder()
	RecoveryHandler(f.svc).ServeHTTP(w, httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/artifacts?"+q.Encode(), nil))
	if w.Code == 200 {
		t.Fatal("recovery opened files")
	}
}
func TestArtifactListChanges(t *testing.T) {
	for _, mode := range []string{"generation", "source", "principal", "role", "sensitivity"} {
		t.Run(mode, func(t *testing.T) {
			f, src, rows := artifactListFixture(t, newFakeRepo())
			changed := false
			f.svc.Auth = sourceListChangeAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
				if in.Action != "read:artifacts" || changed {
					return nil
				}
				changed = true
				switch mode {
				case "principal":
					d.PrincipalID = "other"
				case "role":
					d.Role = "agent"
				case "source":
					return f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision)
				case "generation", "sensitivity":
					r := rows[0]
					if mode == "generation" {
						r.ArtifactGeneration = ContentHash("replacement generation")
					} else {
						r.Sensitivity = "restricted"
					}
					// A detach/re-attach can retain ID and revision 1 while changing generation.
					f.repo.mu.Lock()
					f.repo.store[colArtifacts][recKey(scopeA, r.ID)] = r
					f.repo.mu.Unlock()
					return nil
				}
				return nil
			}}
			page, e := f.svc.ListSourceArtifacts(testCtx, f.human, src.ID, ArtifactListInput{Scope: scopeA})
			want := ErrConflict
			if mode == "principal" || mode == "role" {
				want = ErrDenied
			}
			if mode == "source" {
				want = ErrNotFound
			}
			if !errors.Is(e, want) || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatal("changed registration leaked", e)
			}
		})
	}
}
