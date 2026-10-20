package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

func (f *fakeRepo) ListCandidates(_ context.Context, in ReviewInput) ([]Record, error) {
	in, after, e := normalizeReview(in)
	if e != nil {
		return nil, e
	}
	var rows []Record
	for _, r := range f.rows("memory_candidates") {
		if r.Scope != in.Scope || r.Status != in.Status || in.Kind != "" && r.Kind != in.Kind || in.Proposer != "" && r.CreatedBy != in.Proposer || in.CreatedFrom != nil && r.CreatedAt.Before(*in.CreatedFrom) || in.CreatedTo != nil && !r.CreatedAt.Before(*in.CreatedTo) {
			continue
		}
		if after != nil && !reviewLess(reviewPosition(r, ""), *after) {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return reviewLess(reviewPosition(rows[j], ""), reviewPosition(rows[i], "")) })
	if len(rows) > in.Limit+1 {
		rows = rows[:in.Limit+1]
	}
	return rows, nil
}
func reviewLess(a, b reviewCursor) bool {
	if a.Trust != b.Trust {
		return a.Trust < b.Trust
	}
	if a.Evidence != b.Evidence {
		return a.Evidence < b.Evidence
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}
func TestReviewPaginationAndPermissions(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	var records []Record
	for _, text := range []string{"first candidate", "second candidate", "third candidate"} {
		records = append(records, f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src)})))
	}
	records[0].Trust = 1
	f.repo.store["memory_candidates"][recKey(scopeA, records[0].ID)] = records[0]
	in := ReviewInput{Scope: scopeA, Limit: 1}
	page, e := f.svc.ListCandidates(testCtx, f.human, in)
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != records[0].ID || page.NextCursor == "" || page.Authority != "data_only_untrusted" {
		t.Fatal("review page", e)
	}
	in.Cursor = page.NextCursor
	second, e := f.svc.ListCandidates(testCtx, f.human, in)
	if e != nil || len(second.Items) != 1 || second.Items[0].ID == page.Items[0].ID {
		t.Fatal("cursor repeated", e)
	}
	in.Kind = "decision"
	if _, e = f.svc.ListCandidates(testCtx, f.human, in); !errors.Is(e, ErrInvalid) {
		t.Fatal("changed cursor filter accepted", e)
	}
	if _, e = f.svc.ListCandidates(testCtx, f.agent, ReviewInput{Scope: scopeA}); !errors.Is(e, ErrDenied) {
		t.Fatal("agent saw reviewer list", e)
	}
	f.auth.actors["reader"] = actor("reader", "human", true, "read")
	if _, e = f.svc.ListCandidates(testCtx, "reader", ReviewInput{Scope: scopeA}); !errors.Is(e, ErrDenied) {
		t.Fatal("read-only reviewer", e)
	}
	f.auth.actors["reviewer"] = actor("arkism-memory-reviewer", roleClassifier, false, "review")
	if page, e := f.svc.ListCandidates(testCtx, "reviewer", ReviewInput{Scope: scopeA}); e != nil || len(page.Items) != 3 {
		t.Fatal("memory reviewer could not read the waiting list", e)
	}
	if _, e = f.svc.ReviewCandidate(testCtx, "reviewer", records[1].ID, CandidateReviewInput{Scope: scopeA, Action: "approve", ExpectedRevision: records[1].Revision}); !errors.Is(e, ErrDenied) {
		t.Fatal("classifier approved a candidate", e)
	}
	f.auth.actors["harness"] = actor("harness", roleService, false, "read", "write", "review")
	if _, e = f.svc.ListCandidates(testCtx, "harness", ReviewInput{Scope: scopeA}); !errors.Is(e, ErrDenied) {
		t.Fatal("a service saw the list", e)
	}
	f.repo.failAudit = true
	if _, e = f.svc.ListCandidates(testCtx, f.human, ReviewInput{Scope: scopeA}); e == nil {
		t.Fatal("audit failed open")
	}
}

type reviewDenyAuth struct {
	Authorizer
	denyID string
	outage bool
}

func (a reviewDenyAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if a.outage {
		return Decision{}, ErrUnavailable
	}
	if in.ResourceID == a.denyID && a.denyID != "" {
		return Decision{}, nil
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
func TestReviewHTTPAndRevokedLookahead(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	r := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "private review content", Sources: refs(src)}))
	q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}, "user_id": {scopeA.UserID}, "agent_id": {scopeA.AgentID}}
	h := Handler(f.svc)
	request := func(query, token string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/memory_candidates?"+query, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("review HTTP got %d want %d: %s", w.Code, want, w.Body.String())
		}
		if want != 200 && strings.Contains(w.Body.String(), r.Text) {
			t.Fatal("denied body leaked")
		}
	}
	request(q.Encode(), f.human, 200)
	for _, suffix := range []string{"&limit=0", "&limit=101", "&limit=1&limit=2", "&group=true", "&unknown=1", "&created_from=broken", "&cursor=bad!", "&kind=unknown", "&status=active", "&bad=%ZZ"} {
		request(q.Encode()+suffix, f.human, 400)
	}
	request(q.Encode(), "", 403)
	request(q.Encode(), f.agent, 403)
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, outage: true}
	request(q.Encode(), f.human, 503)
	f.svc.Auth = f.auth
	r2 := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "other review content", Sources: refs(src)}))
	r.Trust = 1
	f.repo.store["memory_candidates"][recKey(scopeA, r.ID)] = r
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: r2.ID}
	// A denied lookahead must not leak existence through next_cursor.
	request(q.Encode()+"&limit=1", f.human, 403)
	for _, event := range f.repo.rows("audit_events") {
		if event.Kind == "review:list" && (event.Text != "" || event.Summary != "" || len(event.Sources) != 0) {
			t.Fatal("review audit retained content")
		}
	}
}
