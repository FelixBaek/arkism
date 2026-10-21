package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) ListMemories(_ context.Context, in MemoryListInput) ([]Record, error) {
	in, after, e := normalizeMemoryList(in)
	if e != nil {
		return nil, e
	}
	rows := []Record{}
	for _, r := range f.rows(colMemories) {
		if memoryListMatches(r, in) && r.ID > after {
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	if len(rows) > in.Limit+1 {
		rows = rows[:in.Limit+1]
	}
	return rows, nil
}

type memoryListAuth struct {
	Authorizer
	deny   map[string]bool
	change string
	mutate func()
	fired  bool
}

func (a *memoryListAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if e == nil && in.Action == "read:memories" && in.ResourceID != "" {
		if a.deny[in.ResourceID] {
			d.Allowed = false
			return d, nil
		}
		if a.change == "principal" {
			d.PrincipalID = "different"
		}
		if a.change == "role" {
			d.Role = "agent"
		}
		if a.mutate != nil && !a.fired {
			a.fired = true
			a.mutate()
		}
	}
	return d, e
}
func memoryListFixture(t *testing.T, repo Repository) (*fixture, []Record) {
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	rows := []Record{}
	for _, status := range []string{"active", "superseded", "conflicted", "rejected", "expired"} {
		r := f.activeMemory("needle [a.*] 한글 "+status, "needle")
		r.Status = status
		if e := repo.Replace(testCtx, colMemories, r, r.Revision); e != nil {
			t.Fatal(e)
		}
		rows = append(rows, r)
	}
	r := f.activeMemory("needle expired timestamp", "needle")
	expired := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	r.ExpiresAt = &expired
	if e := repo.Replace(testCtx, colMemories, r, r.Revision); e != nil {
		t.Fatal(e)
	}
	rows = append(rows, r)
	return f, rows
}
func exerciseMemoryList(t *testing.T, repo Repository) {
	t.Helper()
	f, rows := memoryListFixture(t, repo)
	query := func(in MemoryListInput) MemoryPage {
		t.Helper()
		p, e := f.svc.ListMemories(testCtx, f.human, in)
		if e != nil {
			t.Fatal(in, e)
		}
		return p
	}
	in := MemoryListInput{Scope: scopeA, Query: "needle"}
	p := query(in)
	if len(p.Items) != 1 || p.Items[0].ID != rows[0].ID {
		t.Fatal("active default", p)
	}
	in.IncludeInactive = true
	in.Limit = 2
	ids := []string{}
	for {
		p = query(in)
		for _, r := range p.Items {
			ids = append(ids, r.ID)
		}
		if p.NextCursor == "" {
			break
		}
		in.Cursor = p.NextCursor
	}
	if len(ids) != 6 || !slices.IsSorted(ids) {
		t.Fatal("history pages", ids)
	}
	in.Cursor = ""
	p = query(in)
	for _, change := range []func(*MemoryListInput){
		func(x *MemoryListInput) { x.Query = "other" }, func(x *MemoryListInput) { x.IncludeInactive = false },
		func(x *MemoryListInput) { x.Status = "superseded" }, func(x *MemoryListInput) { x.Kind = "decision" },
		func(x *MemoryListInput) { x.Scope.UserID = "other" },
		func(x *MemoryListInput) { t := time.Now(); x.UpdatedSince = &t },
	} {
		bad := in
		bad.Cursor = p.NextCursor
		change(&bad)
		if _, e := f.svc.ListMemories(testCtx, f.human, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("cursor not filter bound", bad, e)
		}
	}
	in.Limit = 100
	in.Status = "superseded"
	if p = query(in); len(p.Items) != 1 || p.Items[0].Status != "superseded" {
		t.Fatal("status", p)
	}
	in.Status = ""
	in.Query = "[a.*]"
	if len(query(in).Items) != 5 {
		t.Fatal("literal search")
	}
	in.Query = "ne.dle"
	if len(query(in).Items) != 0 {
		t.Fatal("regex interpreted")
	}
	in.Query = "한글"
	if len(query(in).Items) != 5 {
		t.Fatal("unicode")
	}
	in.Query = "needle"
	in.Kind = "decision"
	if len(query(in).Items) != 0 {
		t.Fatal("kind filter")
	}
	in.Kind = ""
	// Windows clocks can stamp the rows in the same millisecond as Now, so cut after the newest row.
	cut := time.Now().UTC()
	for _, row := range rows {
		if row.UpdatedAt.After(cut) {
			cut = row.UpdatedAt
		}
	}
	cut = cut.Truncate(time.Millisecond).Add(time.Millisecond)
	changed := rows[2]
	changed.UpdatedAt = cut.Add(time.Second)
	if e := repo.Replace(testCtx, colMemories, changed, changed.Revision); e != nil {
		t.Fatal(e)
	}
	in.UpdatedSince = &cut
	if p = query(in); len(p.Items) != 1 || p.Items[0].ID != changed.ID {
		t.Fatal("updated since", p)
	}
	in.UpdatedSince = nil
	for _, scope := range []Scope{{WorkspaceID: "other", ProjectID: scopeA.ProjectID, UserID: scopeA.UserID, AgentID: scopeA.AgentID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: "other", UserID: scopeA.UserID, AgentID: scopeA.AgentID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other", AgentID: scopeA.AgentID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: scopeA.UserID, AgentID: "other"}} {
		outside := in
		outside.Scope = scope
		if len(query(outside).Items) != 0 {
			t.Fatal("scope leaked")
		}
	}
	denied := map[string]bool{}
	for _, id := range ids[:4] {
		denied[id] = true
	}
	f.svc.Auth = &memoryListAuth{Authorizer: f.auth, deny: denied}
	in.Limit = 1
	p = query(in)
	if len(p.Items) != 1 || p.Items[0].ID != ids[4] || p.NextCursor == "" {
		t.Fatal("scan denied batches", p)
	}
	in.Cursor = p.NextCursor
	p = query(in)
	if len(p.Items) != 1 || p.Items[0].ID != ids[5] || p.NextCursor != "" {
		t.Fatal("skip cursor", p)
	}
	in.Cursor = ""
	for _, id := range ids {
		denied[id] = true
	}
	empty := query(in)
	in.Query = "absent"
	if structuredText(empty) != structuredText(query(in)) {
		t.Fatal("denial presence oracle")
	}
	in.Query = "needle"
	for _, field := range []string{"principal", "role"} {
		f.svc.Auth = &memoryListAuth{Authorizer: f.auth, change: field}
		p, e := f.svc.ListMemories(testCtx, f.human, in)
		if !errors.Is(e, ErrDenied) || len(p.Items) != 0 || p.NextCursor != "" {
			t.Fatal("mixed actor", e, p)
		}
	}
	f.svc.Auth = f.auth
	search, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "needle"})
	if e != nil || len(search.Items) != 1 || search.Items[0].ID != rows[0].ID {
		t.Fatal("history entered context", e, search)
	}
}
func TestMemoryList(t *testing.T) { exerciseMemoryList(t, newFakeRepo()) }
func TestMemoryListUpdatedSinceQuery(t *testing.T) {
	in, e := parseMemoryListQuery("workspace_id=ws-a&project_id=proj-a&updated_since=2026-10-17T01:02:03.456Z")
	if e != nil || in.UpdatedSince == nil || !in.UpdatedSince.Equal(time.Date(2026, 10, 17, 1, 2, 3, 456e6, time.UTC)) {
		t.Fatal(in, e)
	}
	for _, bad := range []string{"yesterday", ""} {
		if _, e = parseMemoryListQuery("workspace_id=ws-a&project_id=proj-a&updated_since=" + bad); !errors.Is(e, ErrInvalid) {
			t.Fatal(bad, e)
		}
	}
}
func TestMemoryListHTTP(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("needle")
	f.activeMemory("second needle")
	base := "workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID + "&user_id=" + scopeA.UserID + "&agent_id=" + scopeA.AgentID
	for _, extra := range []string{"&q=", "&q=%20", "&q=%00", "&q=" + strings.Repeat("a", 257), "&q=a&q=b", "&include_inactive=1", "&include_inactive=", "&status=superseded", "&kind=bad", "&kind=", "&include_inactive=true&status=", "&limit=0", "&limit=101", "&unknown=true", "&cursor=bad"} {
		req := httptest.NewRequest("GET", "/v1/memories?"+base+extra, nil)
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal(extra, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "/v1/memories?"+base+"&include_inactive=true", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "context_id") || strings.Contains(w.Body.String(), "context_data") {
		t.Fatal(w.Code, w.Body.String())
	}
	requestPage := func(cursor string) MemoryPage {
		req := httptest.NewRequest("GET", "/v1/memories?"+base+"&limit=1&cursor="+url.QueryEscape(cursor), nil)
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		var p MemoryPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Items) != 1 || p.Authority != AuthorityDataOnly {
			t.Fatal("HTTP page", w.Code, w.Body.String())
		}
		return p
	}
	first := requestPage("")
	if first.NextCursor == "" {
		t.Fatal("missing HTTP cursor")
	}
	last := requestPage(first.NextCursor)
	if last.NextCursor != "" || first.Items[0].ID >= last.Items[0].ID {
		t.Fatal("HTTP cursor round trip", first, last)
	}

	for _, r := range f.repo.rows("jobs") {
		if r.Kind == "context_usage" {
			t.Fatal("inventory made usage")
		}
	}
}
func TestMemoryListChangedDuringPolicy(t *testing.T) {
	for _, target := range []string{"body", "source", "memory", "audit"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			r := f.activeMemory("needle")
			f.svc.Auth = &memoryListAuth{Authorizer: f.auth, mutate: func() {
				var e error
				switch target {
				case "audit":
					f.repo.failAudit = true
				case "body":
					r.Text = "changed"
					e = f.repo.Replace(testCtx, colMemories, r, r.Revision)
				case "memory":
					e = f.repo.Delete(testCtx, colMemories, r.Scope, r.ID, r.Revision)
				case "source":
					src, _ := f.repo.Get(testCtx, "sources", r.Scope, r.Sources[0].SourceID)
					e = f.repo.Delete(testCtx, "sources", r.Scope, src.ID, src.Revision)
				}
				if e != nil {
					t.Fatal(e)
				}
			}}
			p, e := f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA, Query: "needle"})
			if e == nil || len(p.Items) != 0 || p.NextCursor != "" {
				t.Fatal("stale inventory", p, e)
			}
		})
	}
}

// The PostgreSQL path share-locks listed rows instead of fence writes; same answers and refusals.
func TestPostgresMemoryList(t *testing.T) { exerciseMemoryList(t, isolatedPostgres(t)) }
func TestPostgresMemoryListChangedDuringPolicy(t *testing.T) {
	for _, target := range []string{"body", "source", "memory", "quarantine"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = isolatedPostgres(t)
			r := f.activeMemory("needle")
			repo := f.svc.Repo
			f.svc.Auth = &memoryListAuth{Authorizer: f.auth, mutate: func() {
				var e error
				switch target {
				case "body":
					r.Text = "changed"
					e = repo.Replace(testCtx, colMemories, r, r.Revision)
				case "memory":
					e = repo.Delete(testCtx, colMemories, r.Scope, r.ID, r.Revision)
				case "source", "quarantine":
					src, _ := repo.Get(testCtx, "sources", r.Scope, r.Sources[0].SourceID)
					if target == "source" {
						e = repo.Delete(testCtx, "sources", r.Scope, src.ID, src.Revision)
					} else {
						prev := src.Revision
						src.Revision++
						src.PrivacyQuarantined = true
						e = repo.Replace(testCtx, "sources", src, prev)
					}
				}
				if e != nil {
					t.Fatal(e)
				}
			}}
			p, e := f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA, Query: "needle"})
			if e == nil || len(p.Items) != 0 || p.NextCursor != "" {
				t.Fatal("stale inventory", p, e)
			}
		})
	}
	// Unchanged rows still list, and the read leaves the rows untouched (no fence write).
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	r := f.activeMemory("needle")
	p, e := f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA, Query: "needle"})
	if e != nil || len(p.Items) != 1 {
		t.Fatal("list", p, e)
	}
	after, e := f.svc.Repo.Get(testCtx, colMemories, scopeA, r.ID)
	if e != nil || after.MemoryReferenceFence != r.MemoryReferenceFence {
		t.Fatal("list read rewrote the memory row", e)
	}
}
