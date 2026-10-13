package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type routeActorAuth struct {
	Authorizer
	mode, field string
	reads       int
	changed     bool
}

func (a *routeActorAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if in.Action == "context:read" {
		a.reads++
	}
	match := in.Action == "route:skills" && ((a.mode == "candidate" && in.ResourceID != "") || (a.mode == "payload" && in.ResourceID == ""))
	match = match || (in.Action == "context:read" && a.reads > 1 && (a.mode == "final" || a.mode == "rules"))
	if e == nil && match {
		a.changed = true
		if a.field == "principal" {
			d.PrincipalID = "different-user"
		} else {
			d.Role = "agent"
		}
	}
	return d, e
}
func TestRouteActorConsistency(t *testing.T) {
	for _, mode := range []string{"candidate", "payload", "final", "rules"} {
		for _, field := range []string{"principal", "role"} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				f := newFixture(t)
				v := analyzedSkill(t, f)
				calls := 0
				if mode != "rules" {
					f.svc.Router = judgeFunc(func(_ context.Context, _ string, c []SkillCandidate) (RouteResult, error) {
						calls++
						return RouteResult{SelectedID: c[0].ID, Provider: "test", Judgments: []SkillJudgment{{ID: c[0].ID, Suitability: 3, Confidence: 1}}}, nil
					})
				}
				in := SearchInput{Scope: scopeA, Query: "index"}
				good, e := f.svc.RouteSkills(testCtx, f.human, in)
				if e != nil || good.SelectedID != v.ID {
					t.Fatal("positive control", good, e)
				}
				calls = 0
				auth := &routeActorAuth{Authorizer: f.auth, mode: mode, field: field}
				f.svc.Auth = auth
				out, e := f.svc.RouteSkills(testCtx, f.human, in)
				if !auth.changed {
					t.Fatal("did not reach actor change")
				}
				if !errors.Is(e, ErrDenied) || out.SelectedID != "" || len(out.Judgments) != 0 {
					t.Fatal("mixed actor route returned", out, e)
				}
				want := 0
				if mode == "final" {
					want = 1
				}
				if calls != want {
					t.Fatal("provider calls", calls, want)
				}
				count := 0
				for _, r := range f.repo.rows("jobs") {
					if r.Kind == "context_usage" {
						count++
					}
				}
				if count != 2 {
					t.Fatal("completed searches must retain their receipts", count)
				}
			})
		}
	}
}

func TestRouteActorHTTP(t *testing.T) {
	f := newFixture(t)
	v := analyzedSkill(t, f)
	f.svc.Auth = &routeActorAuth{Authorizer: f.auth, mode: "rules", field: "principal"}
	request := httptest.NewRequest("POST", "/v1/skills/route", strings.NewReader(structuredText(SearchInput{Scope: scopeA, Query: "index"})))
	request.Header.Set("Authorization", "Bearer "+f.human)
	response := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != 403 || !strings.Contains(body, "access_denied") || strings.Contains(body, v.ID) || strings.Contains(body, "selected_id") || strings.Contains(body, "judgments") {
		t.Fatal("mixed actor HTTP response", response.Code, body)
	}
}
