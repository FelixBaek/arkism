package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type searchActorAuth struct {
	Authorizer
	action, field, id string
	changed           bool
}

func (a *searchActorAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if e == nil && in.Action == a.action && (a.id == "" || a.id == in.ResourceID) {
		if a.field == "principal" {
			d.PrincipalID = "different-user"
		} else {
			d.Role = "agent"
		}
		a.changed = true
	}
	return d, e
}
func exerciseSearchActor(t *testing.T, repo Repository, mode, field string) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	m := f.activeMemory("actor needle", "needle")
	input := SearchInput{Scope: scopeA, Query: "needle", BudgetTokens: 2000, TokenEncoding: "cl100k_base"}
	action, id := "context:read", ""
	switch mode {
	case "external":
		input.Destination = "external:test"
	case "run":
		run := f.create(f.human, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "run", Sources: m.Sources}))
		input.RunID = run.ID
		action = "read:agent_runs"
	}
	good, e := f.svc.Search(testCtx, f.human, input)
	if e != nil || len(good.Items) == 0 || good.ContextID == "" || good.ContextData == "" {
		t.Fatal("positive search control", e, good)
	}
	a := &searchActorAuth{Authorizer: f.auth, action: action, field: field, id: id}
	f.svc.Auth = a
	out, e := f.svc.Search(testCtx, f.human, input)
	if !a.changed {
		t.Fatal("did not reach actor change")
	}
	if !errors.Is(e, ErrDenied) || len(out.Items) != 0 || out.ContextID != "" || out.ContextData != "" {
		t.Fatal("mixed actor search returned", e, out)
	}
}
func TestSearchActorConsistency(t *testing.T) {
	for _, mode := range []string{"internal", "external", "run"} {
		for _, field := range []string{"principal", "role"} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				repo := newFakeRepo()
				exerciseSearchActor(t, repo, mode, field)
				count := 0
				for _, job := range repo.rows("jobs") {
					if job.Kind == "context_usage" {
						count++
					}
				}
				if count != 1 {
					t.Fatal("failed search wrote usage", count)
				}
				count = 0
				for _, event := range repo.rows("audit_events") {
					if event.Kind == "search" {
						count++
					}
				}
				if count != 1 {
					t.Fatal("failed search wrote search audit", count)
				}
			})
		}
	}
}
func TestSearchActorHTTP(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("actor-protected text", "needle")
	f.svc.Auth = &searchActorAuth{Authorizer: f.auth, action: "context:read", field: "principal"}
	r := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(SearchInput{Scope: scopeA, Query: "needle"})))
	r.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, r)
	if w.Code != 403 || strings.Contains(w.Body.String(), "actor-protected") || strings.Contains(w.Body.String(), "context_id") || strings.Contains(w.Body.String(), "items") {
		t.Fatal("mixed actor HTTP response", w.Code, w.Body.String())
	}
}
