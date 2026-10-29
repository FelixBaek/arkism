package memory

import (
	"context"
	"errors"
	"testing"
)

// A route decision learns whether its pick was loaded and how the run ended (#430).
func TestSkillRouteRecordsLoadAndOutcome(t *testing.T) {
	f := newFixture(t)
	v := analyzedSkill(t, f)
	f.svc.Router = judgeFunc(func(context.Context, string, []SkillCandidate) (RouteResult, error) {
		return RouteResult{SelectedID: v.ID, Provider: "jev", Judgments: []SkillJudgment{{ID: v.ID, Suitability: 3, Confidence: .9}}}, nil
	})
	routed, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
	if e != nil || routed.RouteID == "" {
		t.Fatalf("route must name its kept decision: %+v %v", routed, e)
	}
	mark := func(token, event, version string) (SkillRouteItem, error) {
		return f.svc.MarkSkillRoute(testCtx, token, routed.RouteID, SkillRouteUseInput{Scope: scopeA, Event: event, VersionID: version})
	}
	// An outcome before any load, another principal, an unknown version or event are refused.
	if _, e := mark(f.human, "success", v.ID); !errors.Is(e, ErrConflict) {
		t.Fatalf("outcome before load: %v", e)
	}
	if _, e := mark(f.human2, "loaded", v.ID); !errors.Is(e, ErrDenied) {
		t.Fatalf("another principal reported on the decision: %v", e)
	}
	if _, e := mark(f.human, "loaded", ID()); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unknown version: %v", e)
	}
	if _, e := mark(f.human, "opened", v.ID); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unknown event: %v", e)
	}
	if _, e := f.svc.MarkSkillRoute(testCtx, f.human, ID(), SkillRouteUseInput{Scope: scopeA, Event: "loaded", VersionID: v.ID}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unknown decision: %v", e)
	}
	if got, e := mark(f.human, "loaded", v.ID); e != nil || got.Loaded != v.ID || got.Outcome != "" {
		t.Fatalf("load not kept: %+v %v", got, e)
	}
	if got, e := mark(f.human, "failure", v.ID); e != nil || got.Outcome != "failure" {
		t.Fatalf("outcome not kept: %+v %v", got, e)
	}
	// The outcome is final.
	if _, e := mark(f.human, "success", v.ID); !errors.Is(e, ErrConflict) {
		t.Fatalf("outcome overwritten: %v", e)
	}
	logged, e := f.svc.SkillRoutes(testCtx, f.human, scopeA)
	if e != nil || len(logged) != 1 || logged[0].ID != routed.RouteID || logged[0].Loaded != v.ID || logged[0].Outcome != "failure" {
		t.Fatalf("ROUTER does not see load and outcome: %+v %v", logged, e)
	}
	// #466: routed and loaded are counted apart, so "routed but never loaded" outlives the 50 rows ROUTER keeps.
	requireDecision(t, f.repo, "skill_routed", "judge")
	requireDecision(t, f.repo, "skill_route_loaded", "pick")
}
