package memory

import (
	"context"
	"testing"
	"time"
)

// 설정 › 역할별 모델 › 스킬 후보 찾기: the judge sees at most SkillCandidates skills.
func TestRouteSkillsCandidateLimit(t *testing.T) {
	for _, tc := range []struct{ limit, want int }{{0, 3}, {2, 2}, {1, 1}} {
		f := newFixture(t)
		for _, id := range []string{"design-review", "design-review-b", "design-review-c"} {
			if _, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: id, Path: "/skills/" + id + "/SKILL.md", Content: "---\nname: " + id + "\n---\nReview.", Analysis: detailedSkillAnalysis()}); err != nil {
				t.Fatal(err)
			}
		}
		seen := 0
		f.svc.SkillCandidates = tc.limit
		f.svc.Router = judgeFunc(func(_ context.Context, _ string, c []SkillCandidate) (RouteResult, error) {
			seen = len(c)
			return RouteResult{SelectedID: c[0].ID, Provider: "test", Judgments: []SkillJudgment{{ID: c[0].ID, Suitability: 3, Confidence: 1}}}, nil
		})
		if _, err := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "design review"}); err != nil {
			t.Fatal(err)
		}
		if seen != tc.want {
			t.Fatalf("limit %d: judge saw %d candidates, want %d", tc.limit, seen, tc.want)
		}
	}
}

// A skill the host scan judged DO_NOT_INSTALL is not routed until a person turns it on.
func TestRouteSkillsPausesRiskySkillUntilTurnedOn(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"design-review", "design-review-b"} {
		a := detailedSkillAnalysis()
		a.Name = "Design review " + id[len("design-review"):] // the host scan matches skills by name
		if _, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: id, Path: "/skills/" + id + "/SKILL.md", Content: "---\nname: " + id + "\n---\nReview.", Analysis: a}); err != nil {
			t.Fatal(err)
		}
	}
	checks := []SkillCheck{{Agent: "claude_code", Name: "Design review -b", Path: "~/.claude/skills/design-review-b", Verdict: "DO_NOT_INSTALL", Findings: 1, ScannedAt: time.Now().UTC()}}
	if _, err := f.svc.ReportAgentTools(testCtx, f.human, AgentToolsReport{Scope: scopeA, SkillChecks: &checks}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	f.svc.Router = judgeFunc(func(_ context.Context, _ string, c []SkillCandidate) (RouteResult, error) {
		seen = nil
		for _, x := range c {
			seen = append(seen, x.SkillID)
		}
		return RouteResult{SelectedID: c[0].ID, Provider: "test", Judgments: []SkillJudgment{{ID: c[0].ID, Suitability: 3, Confidence: 1}}}, nil
	})
	route := func(runtime string) []string {
		t.Helper()
		if _, err := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "design review", Runtime: runtime}); err != nil {
			t.Fatal(err)
		}
		return seen
	}
	if got := route("claude_code"); len(got) != 1 || got[0] != "design-review" {
		t.Fatal("risky skill routed before a person turned it on:", got)
	}
	skill, err := f.svc.Get(testCtx, f.human, colSkills, scopeA, "design-review-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetSkillAvailability(testCtx, f.human, skill.ID, SkillAvailabilityInput{Scope: scopeA, ExpectedRevision: skill.Revision, Status: statusActive, Compatibility: map[string]string{"claude_code": "supported"}}); err != nil {
		t.Fatal(err)
	}
	if got := route("claude_code"); len(got) != 2 {
		t.Fatal("turned-on risky skill not routed:", got)
	}
	if got := route("codex"); len(got) != 1 {
		t.Fatal("risky skill routed to an agent it was not turned on for:", got)
	}
}
