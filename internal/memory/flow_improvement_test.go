package memory

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func improvementFixture(t *testing.T, baseline bool) (*fixture, flowHTTPHarness, FlowImprovementStart, Record) {
	t.Helper()
	f, _, runInput := flowFixture(t)
	run, e := f.svc.CreateCrewRun(testCtx, tokEngine, runInput)
	if e != nil {
		t.Fatal(e)
	}
	lease := leaseFlow(t, f, run.ID)
	finished := *run.CrewRun
	finished.Status = "succeeded"
	finished.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "research": {Status: "done", Output: `{"answer":"Fixture evidence","sources":[{"url":"https://example.com/evidence","title":"Fixture"}]}`}, "end": {Status: "done"}}
	run, e = f.svc.UpdateCrewRun(testCtx, tokEngine, run.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: finished, LeaseToken: lease})
	if e != nil {
		t.Fatal(e)
	}
	// Exercise the real coarse authorization and service handlers for this
	// module while unrelated route modules are integrated independently.
	mux := newAuthorizationMux(f.svc)
	flowImprovementRoutes(mux, f.svc)
	h := flowHTTPHarness{t, mux.mux}
	generation, _ := generationFixture(t, f)
	var base Record
	if baseline {
		imported, e := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: generation.SkillID, Path: generation.Path, Content: "# Baseline\nCheck a result.", Analysis: detailedSkillAnalysis()})
		if e != nil {
			t.Fatal(e)
		}
		base = imported.Record
		generation.ComparisonPlan.BaselineVersion = base.ID
	}
	f.auth.actors["flow-checker"] = actor("flow-checker", "service", true, allVerbs...)
	f.auth.actors["flow-learner"] = actor("flow-learner", "service", true, append(allVerbs, "auto_learn")...)
	f.svc.SkillAutomationPolicies = []SkillAutomationPolicy{{PrincipalID: "flow-learner", Scope: scopeA, SkillIDs: []string{generation.SkillID}, Runtimes: []string{"codex"}, ExpiresAt: time.Now().Add(2 * time.Hour), MaxTrialSeconds: 3600, MinTrialRuns: 2}}
	return f, h, FlowImprovementStart{Scope: scopeA, RequestID: "one-improvement", SourceRun: recordRef(colCrewRuns, run), Generation: generation, Runtime: "codex", TrialEndsAt: time.Now().Add(30 * time.Minute)}, base
}

func improvementComparison(t *testing.T, f *fixture, view FlowImprovementView) SkillComparisonResult {
	t.Helper()
	c, e := f.repo.Get(testCtx, colSkillVersions, scopeA, view.Improvement.Candidate.ID)
	if e != nil {
		t.Fatal(e)
	}
	plan := view.Improvement.Generation.ComparisonPlan
	result := SkillComparisonResult{FormatVersion: 1, Candidate: view.Improvement.Candidate, BaselineID: plan.BaselineVersion, ContentSHA256: map[string]string{"baseline": ContentHash(""), "candidate": c.Analysis.ContentHash}, DatasetSHA256: plan.DatasetSHA256, EnvironmentSHA256: plan.EnvironmentSHA256, Passed: true, Report: &SkillComparisonReport{PlanSHA256: comparisonPlanHash(plan), BaselineScore: 0, CandidateScore: 1, CandidateDurationMS: 30, RegressionPassed: true, SafetyPassed: true}}
	if plan.BaselineVersion != "" {
		b, e := f.repo.Get(testCtx, colSkillVersions, scopeA, plan.BaselineVersion)
		if e != nil {
			t.Fatal(e)
		}
		result.BaselineRevision = b.Revision
		result.ContentSHA256["baseline"] = b.Analysis.ContentHash
	}
	end := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	for _, category := range []string{"quality", "regression", "safety"} {
		for _, variant := range []string{"baseline", "candidate"} {
			result.Cases = append(result.Cases, SkillComparisonCase{CaseSHA256: ContentHash(category), Category: category, Variant: variant, Passed: variant == "candidate" || category != "quality", OutputSHA256: ContentHash(category + variant), DurationMS: 10, StartedAt: end.Add(-10 * time.Millisecond), FinishedAt: end})
		}
	}
	return result
}

func addImprovementObservation(t *testing.T, f *fixture, v FlowImprovementView, index int, afterApply bool) {
	t.Helper()
	addScopedImprovementObservation(t, f, v, index, afterApply, scopeA)
}

func addScopedImprovementObservation(t *testing.T, f *fixture, v FlowImprovementView, index int, afterApply bool, scope Scope) {
	t.Helper()
	c, e := f.repo.Get(testCtx, colSkillVersions, scopeA, v.Improvement.Candidate.ID)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	u := SkillUsage{TrialID: v.Improvement.Trial.ID, SkillID: c.SkillID, VersionID: c.ID, Version: 2, Runtime: "codex", ContentHash: c.Analysis.ContentHash, Status: "success", StartedAt: now, CompletedAt: now}
	if afterApply {
		u.TrialID = ""
		u.Status = "failure"
		u.RuntimeErrors = 1
	}
	id := fmt.Sprintf("flow-observation-%d", index)
	source := Record{ID: id, Scope: scope, Kind: "note", Status: "active", Revision: 1, Text: structuredText(u), TraceID: id + "-trace", CreatedBy: "flow-runtime", CreatedByType: "agent", CreatedAt: now, UpdatedAt: now}
	if e = f.repo.Insert(testCtx, "sources", source); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Insert(testCtx, "execution_traces", Record{ID: source.TraceID, Scope: scope, Status: "active", Revision: 1}); e != nil {
		t.Fatal(e)
	}
	if !afterApply {
		target := recordRef("sources", source)
		_, e = f.svc.RecordSkillTrialOutcome(testCtx, "flow-checker", SkillTrialOutcomeInput{Scope: scope, TrialScope: scopeA, TrialID: v.Improvement.Trial.ID, EventID: id, Source: target, Evidence: []Ref{target}, CheckerSHA256: ContentHash("fixed independent fixture checker"), Passed: true})
		if e != nil {
			t.Fatal(e)
		}
	}
}

func TestFlowImprovementConnectsSourceThroughTrialPromotionMonitorAndRollback(t *testing.T) {
	f, h, start, baseline := improvementFixture(t, true)
	var v FlowImprovementView
	h.call("POST", "/v1/flow/improvements", f.agent, start, http.StatusOK, &v)
	step := func(token, action, id string, comparison *SkillComparisonResult, application *ChangeInput) {
		t.Helper()
		in := FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: id, Action: action, Comparison: comparison, Application: application}
		h.call("POST", "/v1/flow/improvements/"+v.ID+"/advance", token, in, http.StatusOK, &v)
	}
	step(f.agent, "generate", "generate", nil, nil)
	initial := improvementComparison(t, f, v)
	step("flow-checker", "compare", "comparison", &initial, nil)
	if v.Improvement.Stage != "approval_required" {
		t.Fatal(v)
	}
	if _, e := f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "self-approve", Action: "approve"}); !errors.Is(e, ErrDenied) {
		t.Fatal("agent approval", e)
	}
	step(f.human, "approve", "human-approval", nil, nil)
	step("flow-learner", "trial", "bounded-trial", nil, nil)
	step("flow-learner", "observe", "empty-observation", nil, nil)
	if v.Improvement.Stage != "trial_running" || v.Improvement.ObservationState != "awaiting_observation" {
		t.Fatal(v)
	}
	for i := 0; i < 2; i++ {
		addImprovementObservation(t, f, v, i, false)
	}
	step("flow-learner", "observe", "observe-executions", nil, nil)
	if v.Improvement.Stage != "trial_comparison_required" {
		t.Fatal(v)
	}
	trialComparison := improvementComparison(t, f, v)
	step("flow-checker", "compare", "trial-comparison", &trialComparison, nil)
	step("flow-learner", "apply", "policy-apply", nil, nil)
	if v.Improvement.Stage != "monitoring" {
		t.Fatal(v)
	}
	step("flow-learner", "monitor", "no-observation-yet", nil, nil)
	if v.Improvement.ObservationState != "monitoring" {
		t.Fatal("no run is not success", v)
	}
	addImprovementObservation(t, f, v, 2, true)
	step("flow-learner", "monitor", "observed-regression", nil, nil)
	if v.Improvement.Stage != "recovery_unavailable" {
		t.Fatal("unvalidated baseline was restored", v)
	}
	current, e := f.repo.Get(testCtx, colSkillVersions, scopeA, v.Improvement.Candidate.ID)
	if e != nil || current.Status != "rejected" {
		t.Fatal(current, e)
	}
	base, e := f.repo.Get(testCtx, colSkillVersions, scopeA, baseline.ID)
	if e != nil {
		t.Fatal(e)
	}
	target := recordRef(colSkillVersions, base)
	passed := true
	checked := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "Independent baseline recovery check", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
	step(f.human, "rollback", "human-recovery", nil, &ChangeInput{Scope: scopeA, ValidationID: checked.ID})
	if v.Improvement.Stage != "rolled_back" {
		t.Fatal(v)
	}
	parent, e := f.repo.Get(testCtx, colSkills, scopeA, start.Generation.SkillID)
	if e != nil || parent.ActiveVersion != baseline.ID {
		t.Fatal(parent, e)
	}
	h.call("GET", "/v1/flow/improvements/"+v.ID+flowHTTPQuery, f.human, nil, http.StatusOK, &v)
}

func TestFlowImprovementRequestBindingAndNoSelfEvaluation(t *testing.T) {
	f, _, start, _ := improvementFixture(t, false)
	v, e := f.svc.StartFlowImprovement(testCtx, f.agent, start)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.svc.StartFlowImprovement(testCtx, f.agent, start)
	if e != nil || again.ID != v.ID {
		t.Fatal(again, e)
	}
	changed := start
	changed.SourceRun.Revision++
	if _, e = f.svc.StartFlowImprovement(testCtx, f.agent, changed); !errors.Is(e, ErrConflict) {
		t.Fatal("changed source", e)
	}
	in := FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "generate", Action: "generate"}
	v, e = f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	f.svc.Drafter = nil
	again, e = f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, in)
	if e != nil || again.Revision != v.Revision {
		t.Fatal("idempotent generation", again, e)
	}
	in.Action = "approve"
	if _, e = f.svc.AdvanceFlowImprovement(testCtx, f.human, v.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("changed action/actor", e)
	}
	comparison := improvementComparison(t, f, v)
	f.auth.actors["self-checker"] = actor("agent-1", "service", true, allVerbs...)
	in = FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "self-evaluation", Action: "compare", Comparison: &comparison}
	if _, e = f.svc.AdvanceFlowImprovement(testCtx, "self-checker", v.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("self comparison", e)
	}
	current, e := f.svc.GetFlowImprovement(testCtx, f.human, v.ID, scopeA)
	if e != nil || current.Improvement.Stage != "comparison_required" {
		t.Fatal(current, e)
	}
	if _, e = f.svc.GetFlowImprovement(testCtx, f.agent, v.ID, Scope{WorkspaceID: "other", ProjectID: "project"}); e == nil {
		t.Fatal("cross scope")
	}
}
