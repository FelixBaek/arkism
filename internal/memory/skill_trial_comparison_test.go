package memory

import (
	"context"
	"testing"
	"time"
)

func TestSkillTrialCannotCherryPickIndependentOutcomes(t *testing.T) {
	f, candidate, trial, in := skillAutomationFixture(t)
	f.auth.actors["checker"] = actor("independent-trial-checker", "service", true, allVerbs...)
	target := in.TrialEvidence[0]
	if _, e := f.svc.RecordSkillTrialOutcome(testCtx, "checker", SkillTrialOutcomeInput{Scope: scopeA, TrialScope: scopeA, TrialID: trial.ID, EventID: "failed", Source: target, Evidence: []Ref{target}, CheckerSHA256: ContentHash("fixed checker"), Passed: false}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false); e == nil {
		t.Fatal("negative independent observation ignored")
	}
}
func TestSkillTrialFreshComparisonAndPromotion(t *testing.T) {
	f, candidate, trial, in := skillAutomationFixture(t)
	f.auth.actors["checker"] = actor("independent-trial-checker", "service", true, allVerbs...)
	for i, source := range in.TrialEvidence {
		event := []string{"first", "second"}[i]
		evaluated, e := f.svc.RecordSkillTrialOutcome(testCtx, "checker", SkillTrialOutcomeInput{Scope: scopeA, TrialScope: scopeA, TrialID: trial.ID, EventID: event, Source: source, Evidence: []Ref{source}, CheckerSHA256: ContentHash("fixed checker"), Passed: true})
		if e != nil {
			t.Fatal(e)
		}
		in.ScopedTrialEvidence = append(in.ScopedTrialEvidence, ScopedTrialEvidence{Scope: scopeA, Source: source, Evaluation: evaluated})
	}
	in.TrialEvidence = nil
	now := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	plan := candidate.ComparisonPlan
	result := SkillComparisonResult{FormatVersion: 1, Candidate: Ref{Collection: colSkillVersions, ID: candidate.ID, Revision: candidate.Revision}, ContentSHA256: map[string]string{"baseline": ContentHash(""), "candidate": candidate.Analysis.ContentHash}, DatasetSHA256: plan.DatasetSHA256, EnvironmentSHA256: plan.EnvironmentSHA256, Passed: true, Report: &SkillComparisonReport{PlanSHA256: comparisonPlanHash(plan), BaselineScore: 0, CandidateScore: 1, RegressionPassed: true, SafetyPassed: true}}
	for _, category := range []string{"quality", "regression", "safety"} {
		for _, variant := range []string{"baseline", "candidate"} {
			result.Cases = append(result.Cases, SkillComparisonCase{CaseSHA256: ContentHash(category), Category: category, Variant: variant, Passed: variant == "candidate" || category != "quality", OutputSHA256: ContentHash(category + variant), StartedAt: now, FinishedAt: now})
		}
	}
	recorded, e := f.svc.RecordSkillComparison(testCtx, "checker", RecordSkillComparisonInput{Scope: scopeA, TrialID: trial.ID, EventID: "after-trial", Result: result})
	if e != nil {
		t.Fatal("trial comparison", e)
	}
	in.ValidationID = recorded.Evaluation.ID
	if e = f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return checkAutomaticSkillPromotion(ctx, tx, candidate, in, f.svc.SkillAutomationPolicies[0])
	}); e != nil {

		t.Fatal("promotion guard", e)
	}
	actor, _ := f.svc.authorize(testCtx, "learner", "apply:skills", scopeA, candidate.SkillID, "")
	if e = f.svc.authorizeTrialEvidence(bindRequestActor(testCtx, actor), "learner", actor, in, f.svc.SkillAutomationPolicies[0]); e != nil {
		t.Fatal("evidence authorization", e)
	}
	applied, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false)
	if e != nil || applied.ActiveVersion != candidate.ID {
		t.Fatal("measured promotion", e)
	}
}
