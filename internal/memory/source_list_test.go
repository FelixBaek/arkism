package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) ListSources(_ context.Context, in SourceListInput) ([]Record, error) {
	in, after, e := normalizeSourceList(in)
	if e != nil {
		return nil, e
	}
	var rows []Record
	for _, r := range f.rows("sources") {
		if !strings.Contains(r.Text, in.Query) || r.Scope != in.Scope || !live(r) || r.CollectedAt == nil || !r.CollectedAt.After(time.Time{}) || in.Kind != "" && r.Kind != in.Kind || in.OriginProduct != "" && r.OriginProduct != in.OriginProduct || in.CollectedFrom != nil && r.CollectedAt.Before(*in.CollectedFrom) || in.CollectedTo != nil && !r.CollectedAt.Before(*in.CollectedTo) {
			continue
		}
		if after != nil && !(r.CollectedAt.Before(after.CollectedAt) || r.CollectedAt.Equal(after.CollectedAt) && r.ID < after.ID) {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CollectedAt.Equal(*rows[j].CollectedAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CollectedAt.After(*rows[j].CollectedAt)
	})
	if len(rows) > in.Limit+1 {
		rows = rows[:in.Limit+1]
	}
	return rows, nil
}

func exerciseSourceList(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-time.Hour)
	makeRow := func(id string, at time.Time) Record {
		return Record{ID: id, Scope: scopeA, Revision: 1, Status: "active", Kind: "note", Text: strings.Repeat("한", 159) + "\r\nnot a title", OriginProduct: "demo-note", CollectedAt: &at, Sensitivity: "private", CreatedAt: at, UpdatedAt: at}
	}
	rows := []Record{makeRow("z", now), makeRow("a", now), makeRow("old", old)}
	for i := 0; i < 4; i++ {
		r := makeRow("foreign-"+string(rune('0'+i)), now)
		r.Text += " foreign-only-" + string(rune('0'+i))
		switch i {
		case 0:
			r.Scope.WorkspaceID = "foreign"
		case 1:
			r.Scope.ProjectID = "foreign"
		case 2:
			r.Scope.UserID = "foreign"
		case 3:
			r.Scope.AgentID = "foreign"
		}
		rows = append(rows, r)
	}
	r := makeRow("expired", now)
	r.ExpiresAt = &old
	rows = append(rows, r)
	r = makeRow("inactive", now)
	r.Status = "rejected"
	rows = append(rows, r)
	r = makeRow("undated", now)
	r.CollectedAt = nil
	rows = append(rows, r)
	for _, r := range rows {
		if e := repo.Insert(testCtx, "sources", r); e != nil {
			t.Fatal(e)
		}
	}
	in := SourceListInput{Scope: scopeA, Limit: 1}
	var ids []string
	for i := 0; i < 4; i++ {
		page, e := f.svc.ListSources(testCtx, f.human, in)
		if e != nil {
			t.Fatal(e)
		}
		if page.Authority != "data_only_untrusted" || len(page.Items) != 1 {
			t.Fatalf("unexpected page: %+v", page)
		}
		if page.Items[0].FirstLine != strings.Repeat("한", 159) {
			t.Fatal("unicode/CRLF summary")
		}
		ids = append(ids, page.Items[0].ID)
		if page.NextCursor == "" {
			break
		}
		in.Cursor = page.NextCursor
	}
	if !slices.Equal(ids, []string{"z", "a", "old"}) {
		t.Fatalf("pagination %v", ids)
	}
	from := now
	to := now.Add(time.Second)
	page, e := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Kind: "note", OriginProduct: "demo-note", CollectedFrom: &from, CollectedTo: &to})
	if e != nil || len(page.Items) != 2 {
		t.Fatal("filter", e, len(page.Items))
	}
	page, e = f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, CollectedTo: &now})
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != "old" {
		t.Fatal("exclusive upper bound", e)
	}
	for _, in := range []SourceListInput{{Scope: scopeA, Kind: "document"}, {Scope: scopeA, OriginProduct: "elsewhere"}, {Scope: scopeFar}} {
		page, e = f.svc.ListSources(testCtx, f.human, in)
		if e != nil || page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatal("empty filtered list", e)
		}
	}
	page, e = f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	for _, in := range []SourceListInput{{Scope: scopeA, Kind: "note", Cursor: page.NextCursor}, {Scope: scopeB, Cursor: page.NextCursor}} {
		if _, e = f.svc.ListSources(testCtx, f.human, in); !errors.Is(e, ErrInvalid) {
			t.Fatal("cursor binding", e)
		}
	}
	page, e = f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Limit: 100, Cursor: page.NextCursor})
	if e != nil || len(page.Items) != 2 {
		t.Fatal("limit change", e)
	}
	// Match beyond the 160-rune projection, retaining the original summary.
	searched, e := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "  not a title  ", Limit: 1})
	if e != nil || len(searched.Items) != 1 || searched.Items[0].ID != "z" || searched.NextCursor == "" {
		t.Fatal("full body search", e, searched)
	}
	for _, query := range []string{"not a title", " not a title "} {
		p, err := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: query, Limit: 1, Cursor: searched.NextCursor})
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != "a" {
			t.Fatal("search cursor normalization", err, p)
		}
	}
	if _, e := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "not", Cursor: searched.NextCursor}); !errors.Is(e, ErrInvalid) {
		t.Fatal("search cursor accepted changed query", e)
	}
	for _, query := range []string{".*", "$text", "NOT A TITLE", "missing", "foreign-only-0", "foreign-only-1", "foreign-only-2", "foreign-only-3"} {
		p, err := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: query})
		if err != nil || len(p.Items) != 0 || p.NextCursor != "" {
			t.Fatal("literal/case-sensitive search", err, p)
		}
	}
	p, err := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "한", Kind: "note", CollectedTo: &now})
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != "old" {
		t.Fatal("unicode search with filters", err, p)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: "a"}
	page, e = f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Limit: 1})
	if !errors.Is(e, ErrDenied) || page.NextCursor != "" || len(page.Items) != 0 {
		t.Fatal("denied lookahead leaked", e)
	}
	searched, e = f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "not a title", Limit: 1})
	if e != nil || len(searched.Items) != 1 || searched.Items[0].ID != "z" || searched.NextCursor == "" {
		t.Fatal("authorized search page", e, searched)
	}
	next, e := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "not a title", Limit: 1, Cursor: searched.NextCursor})
	if e != nil || len(next.Items) != 1 || next.Items[0].ID != "old" || next.NextCursor != "" {
		t.Fatal("search skipped denied lookahead", e, next)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.svc.Auth, denyID: "z"}
	searched, e = f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "not a title", Limit: 1})
	if e != nil || len(searched.Items) != 1 || searched.Items[0].ID != "old" || searched.NextCursor != "" {
		t.Fatal("search filled page after denied batch", e, searched)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.svc.Auth, denyID: "old"}
	var baseline string
	for _, query := range []string{"not a title", "absent body phrase"} {
		p, err := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: query, Limit: 1})
		if err != nil || len(p.Items) != 0 || p.NextCursor != "" {
			t.Fatal("denied body oracle", err, p)
		}
		if baseline != "" && baseline != structuredText(p) {
			t.Fatal("denied match response differs from no match")
		}
		baseline = structuredText(p)
	}
}
func TestSourceList(t *testing.T) { exerciseSourceList(t, newFakeRepo()) }

func TestSourceListHTTPAndAudit(t *testing.T) {
	f := newFixture(t)
	f.source()
	observed := &inspectionAuth{Authorizer: f.auth}
	f.svc.Auth = observed
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(oldLogger)
	q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}}
	h := Handler(f.svc)
	request := func(query, token string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/sources?"+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
		}
		if want != 200 && (strings.Contains(w.Body.String(), "first_line") || strings.Contains(w.Body.String(), "next_cursor")) {
			t.Fatal("failed request leaked data")
		}
	}
	request(q.Encode(), f.human, 200)
	request(q.Encode()+"&q=source-search-observation", f.human, 200)
	if !strings.Contains(strings.Join(observed.payloads, ""), "source-search-observation") {
		t.Fatal("query did not reach read policy")
	}
	for _, suffix := range []string{"&q=password%3Dsynthetic-search-secret", "&q=", "&q=+", "&q=x&q=y", "&q=%FF", "&q=%00", "&q=" + strings.Repeat("a", 257), "&limit=0", "&limit=101", "&limit=1&limit=2", "&status=active", "&unknown=x", "&kind=invalid", "&origin_product=bad.product", "&collected_from=invalid", "&collected_from=2026-01-02T00:00:00Z&collected_to=2026-01-01T00:00:00Z", "&cursor=bad!", "&bad=%ZZ", "&cursor=" + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"a","extra":1}`))} {
		request(q.Encode()+suffix, f.human, 400)
	}
	if strings.Contains(logs.String(), "source-search-observation") || strings.Contains(logs.String(), "synthetic-search-secret") || strings.Contains(strings.Join(observed.payloads, ""), "synthetic-search-secret") {
		t.Fatal("query logged or rejected secret reached policy")
	}
	request(q.Encode(), "", 403)
	f.repo.failAudit = true
	request(q.Encode(), f.human, 503)
	f.repo.failAudit = false
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, outage: true}
	request(q.Encode(), f.human, 503)
	for _, event := range f.repo.rows("audit_events") {
		if strings.Contains(structuredText(event), "source-search-observation") {
			t.Fatal("audit retained query")
		}
		if event.Kind == "read:sources:list" && (event.Text != "" || event.Summary != "" || len(event.Sources) != 0) {
			t.Fatal("audit retained content")
		}
	}
	w := httptest.NewRecorder()
	RecoveryHandler(f.svc).ServeHTTP(w, httptest.NewRequest("GET", "/v1/sources?"+q.Encode(), nil))
	if w.Code == 200 {
		t.Fatal("recovery opened source list")
	}
}

type sourceListChangeAuth struct {
	Authorizer
	change func(Authorization, *Decision) error
}

func (a sourceListChangeAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if e == nil && in.ResourceID != "" {
		e = a.change(in, &d)
	}
	return d, e
}
func TestSourceListChangedAuthorizationAndRecord(t *testing.T) {
	for _, mode := range []string{"principal", "role", "scope", "source"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			f.svc.Auth = sourceListChangeAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
				switch mode {
				case "principal":
					d.PrincipalID = "different"
				case "role":
					d.Role = "agent"
				case "scope":
					d.Scope = scopeB
				case "source":
					src.Sensitivity = "restricted"
					return f.repo.Replace(testCtx, "sources", src, src.Revision)
				}
				return nil
			}}
			page, e := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA})
			want := ErrDenied
			if mode == "source" {
				want = ErrConflict
			}
			if !errors.Is(e, want) || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatal("changed authorization/record leaked", e)
			}
		})
	}
}

func exerciseSourceSearchChangedBody(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	text := strings.Repeat("a", 170) + " needle [a.*] $text 한글"
	src, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: text}))
	if e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{"needle", "[a.*]", "$text", "한글"} {
		page, e := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: query})
		if e != nil || len(page.Items) != 1 {
			t.Fatal("literal body match", e, page)
		}
	}
	page, err := f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "ne.dle"})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("regex dot was interpreted", err, page)
	}
	f.svc.Auth = sourceListChangeAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
		// Same revision and summary: only the matching tail disappears.
		src.Text = strings.Repeat("a", 170) + " changed tail"
		return repo.Replace(testCtx, "sources", src, src.Revision)
	}}
	page, e = f.svc.ListSources(testCtx, f.human, SourceListInput{Scope: scopeA, Query: "needle"})
	if !errors.Is(e, ErrConflict) || page.Authority != "" {
		t.Fatal("stale match returned", e, page)
	}
}

func TestSourceSearchChangedBody(t *testing.T) { exerciseSourceSearchChangedBody(t, newFakeRepo()) }
