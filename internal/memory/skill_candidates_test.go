package memory

import (
	"context"
	"testing"
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
