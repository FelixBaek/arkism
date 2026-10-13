package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type interruptedTwinPages struct {
	Repository
	memories bool
}

func (r interruptedTwinPages) ListCandidates(ctx context.Context, in ReviewInput) ([]Record, error) {
	if !r.memories && in.Cursor != "" {
		return nil, ErrUnavailable
	}
	return r.Repository.ListCandidates(ctx, in)
}
func (r interruptedTwinPages) ListMemories(ctx context.Context, in MemoryListInput) ([]Record, error) {
	if r.memories && in.Cursor != "" {
		return nil, ErrUnavailable
	}
	return r.Repository.ListMemories(ctx, in)
}
func TestTwinPastFirstPage(t *testing.T) { checkTwinPastFirstPage(t, newFixture(t)) }
func TestPostgresTwinPastFirstPage(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkTwinPastFirstPage(t, f)
}
func checkTwinPastFirstPage(t *testing.T, f *fixture) {
	t.Helper()
	confidence := .9
	source := f.source()
	for _, status := range []string{"candidate", "rejected", "active"} {
		collection := "memory_candidates"
		if status == "active" {
			collection = "memories"
		}
		for i := 0; i < 103; i++ {
			r := Record{ID: fmt.Sprintf("twin-page-%s-%03d", status, i), Scope: scopeA, Sources: refs(source), Kind: "user", Status: status, Revision: 1, Text: "User observation", Trust: float64(103-i) / 104, CreatedAt: time.Now().UTC().Truncate(time.Millisecond), Confidence: &confidence}
			if i >= 100 {
				r.Tags = []string{"twin:preference"}
				for n := 0; n < 3; n++ {
					r.Relations = append(r.Relations, Ref{Collection: "agent_runs", ID: fmt.Sprintf("run-%d", n), Revision: 1})
				}
			}
			if status == "active" && i == 102 {
				r.Status = "superseded"
			}
			if err := f.svc.Repo.Insert(testCtx, collection, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := f.svc.ListTwinClaims(testCtx, f.human, scopeA, nil)
	if err != nil || len(page.Items) != 9 {
		t.Fatalf("later-page claims missing: count=%d err=%v", len(page.Items), err)
	}
	for status, count := range map[string]int{"inferred": 3, "rejected": 3, "confirmed": 2, "superseded": 1, "candidate": 0} {
		page, err = f.svc.ListTwinClaims(testCtx, f.human, scopeA, []string{status})
		if err != nil || len(page.Items) != count {
			t.Fatalf("%s claims: got=%d want=%d err=%v", status, len(page.Items), count, err)
		}
		for _, c := range page.Items {
			if c.Status != status || c.DistinctRuns != 3 {
				t.Fatal("claim status/evidence lost")
			}
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/twin/claims?workspace_id="+scopeA.WorkspaceID+"&project_id="+scopeA.ProjectID, nil)
	request.Header.Set("Authorization", "Bearer "+f.human)
	response := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(response, request)
	var httpPage IntelligencePage[TwinClaim]
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &httpPage) != nil || len(httpPage.Items) != 9 || httpPage.Authority != intelligenceAuthority {
		t.Fatal("HTTP twin contract lost later-page claims", response.Code)
	}
	repo := f.svc.Repo
	for _, memories := range []bool{false, true} {
		f.svc.Repo = interruptedTwinPages{repo, memories}
		page, err = f.svc.ListTwinClaims(testCtx, f.human, scopeA, nil)
		if err != ErrUnavailable || len(page.Items) != 0 {
			t.Fatal("partial claims returned after page failure", memories, err)
		}
	}
}
