package memory

import (
	"context"
	"errors"
	"testing"
)

func advanceImprovementFixture(t *testing.T, f *fixture, v FlowImprovementView, token, action, id string, comparison *SkillComparisonResult) FlowImprovementView {
	t.Helper()
	out, e := f.svc.AdvanceFlowImprovement(testCtx, token, v.ID, FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: id, Action: action, Comparison: comparison})
	if e != nil {
		t.Fatal(action, e)
	}
	return out
}

func improvementTrialFixture(t *testing.T) (*fixture, FlowImprovementView) {
	t.Helper()
	f, _, start, _ := improvementFixture(t, false)
	v, e := f.svc.StartFlowImprovement(testCtx, f.agent, start)
	if e != nil {
		t.Fatal(e)
	}
	v = advanceImprovementFixture(t, f, v, f.agent, "generate", "generate", nil)
	comparison := improvementComparison(t, f, v)
	v = advanceImprovementFixture(t, f, v, "flow-checker", "compare", "initial-comparison", &comparison)
	v = advanceImprovementFixture(t, f, v, f.human, "approve", "approve", nil)
	v = advanceImprovementFixture(t, f, v, "flow-learner", "trial", "trial", nil)
	return f, v
}

type improvementScopeAuth struct {
	Authorizer
	denied Scope
}

func (a improvementScopeAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.Scope == a.denied && in.Action == "read:sources" {
		return Decision{}, ErrDenied
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

func TestFlowImprovementObservationsKeepIdentityAcrossAuthorizedAgentScopes(t *testing.T) {
	f, v := improvementTrialFixture(t)
	other := scopeA
	other.AgentID = "observed-worker"
	f.svc.SkillAutomationPolicies[0].EvidenceAgents = []string{other.AgentID}
	addImprovementObservation(t, f, v, 0, false)
	addScopedImprovementObservation(t, f, v, 1, false, other)
	v = advanceImprovementFixture(t, f, v, "flow-learner", "observe", "observe", nil)
	if v.Improvement.Stage != "trial_comparison_required" || v.Improvement.Pending != nil {
		t.Fatal(v)
	}
	if _, e := f.svc.GetFlowImprovement(testCtx, f.human, v.ID, scopeA); e != nil {
		t.Fatal(e)
	}
	page, e := f.svc.ListFlowImprovements(testCtx, f.human, scopeA, "", 10)
	if e != nil || len(page.Items) != 1 {
		t.Fatal(page, e)
	}
	f.svc.Auth = improvementScopeAuth{Authorizer: f.auth, denied: other}
	if _, e = f.svc.GetFlowImprovement(testCtx, f.human, v.ID, scopeA); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked cross-scope read", e)
	}
	page, e = f.svc.ListFlowImprovements(testCtx, f.human, scopeA, "", 10)
	if e != nil || len(page.Items) != 0 {
		t.Fatal("revoked evidence leaked", page, e)
	}
}

func TestFlowImprovementHumanApplyRechecksNewIndependentFailure(t *testing.T) {
	f, v := improvementTrialFixture(t)
	for i := 0; i < 2; i++ {
		addImprovementObservation(t, f, v, i, false)
	}
	v = advanceImprovementFixture(t, f, v, "flow-learner", "observe", "observe", nil)
	comparison := improvementComparison(t, f, v)
	v = advanceImprovementFixture(t, f, v, "flow-checker", "compare", "trial-comparison", &comparison)
	item := v.Improvement.Observations[0]
	_, e := f.svc.RecordSkillTrialOutcome(testCtx, "flow-checker", SkillTrialOutcomeInput{Scope: item.Scope, TrialScope: scopeA, TrialID: v.Improvement.Trial.ID, EventID: "later-independent-failure", Source: item.Source, Evidence: []Ref{item.Source}, CheckerSHA256: ContentHash("fixed negative checker"), Passed: false})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.svc.AdvanceFlowImprovement(testCtx, f.human, v.ID, FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "human-apply", Action: "apply"})
	if !errors.Is(e, ErrDenied) {
		t.Fatal("new negative evidence ignored", e)
	}
	parent, e := f.repo.Get(testCtx, colSkills, scopeA, v.Improvement.Generation.SkillID)
	if e != nil || parent.ActiveVersion != "" {
		t.Fatal(parent, e)
	}
}

func TestFlowImprovementObservationRequiresCurrentAutomationPolicy(t *testing.T) {
	f, v := improvementTrialFixture(t)
	f.svc.SkillAutomationPolicies = nil
	_, e := f.svc.AdvanceFlowImprovement(testCtx, "flow-learner", v.ID, FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "observe", Action: "observe"})
	if !errors.Is(e, ErrDenied) {
		t.Fatal("removed service policy ignored", e)
	}
	_, e = f.svc.AdvanceFlowImprovement(testCtx, f.human, v.ID, FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "human-observe", Action: "observe"})
	if !errors.Is(e, ErrDenied) {
		t.Fatal("human impersonated service", e)
	}
}
