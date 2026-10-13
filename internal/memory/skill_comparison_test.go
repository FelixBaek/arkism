package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
)

func comparisonPlan() *SkillComparisonPlan {
	return &SkillComparisonPlan{DatasetSHA256: ContentHash("held-out-v1"), EnvironmentSHA256: ContentHash("runner-v1"), MinScoreGain: .1, MaxCost: 2, MaxDurationMS: 1000}
}
func TestSkillComparisonCandidateAndApproval(t *testing.T) { checkSkillComparison(t, newFixture(t)) }
func TestPostgresSkillComparisonCandidateAndApproval(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillComparison(t, f)
}
func checkSkillComparison(t *testing.T, f *fixture) {
	t.Helper()
	in := ImportSkillInput{Scope: scopeA, SkillID: "improved-review", Path: "/skills/improved-review/SKILL.md", Content: "# Review\nCheck the outcome.", Analysis: detailedSkillAnalysis(), ComparisonPlan: comparisonPlan()}
	candidate, e := f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	if candidate.Record.Status != "candidate" || candidate.Record.ApprovedBy != "" || candidate.Skill.ActiveVersion != "" {
		t.Fatal("new improvement activated without review")
	}
	get := httptest.NewRequest("GET", "/v1/skill_versions/"+candidate.Record.ID+"?"+url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}, "user_id": {scopeA.UserID}, "agent_id": {scopeA.AgentID}}.Encode(), nil)
	get.Header.Set("Authorization", "Bearer "+f.human)
	response := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(response, get)
	var snapshot struct {
		ComparisonPlanSHA256 string `json:"comparison_plan_sha256"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || snapshot.ComparisonPlanSHA256 != candidate.ComparisonPlanSHA256 {
		t.Fatal("read snapshot lost fixed comparison hash")
	}
	again, e := f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil || !again.Reused || again.Record.ID != candidate.Record.ID {
		t.Fatal("retry", e)
	}
	var winningSource Record
	approve := func(report *SkillComparisonReport) error {
		target := Ref{Collection: "skill_versions", ID: candidate.Record.ID, Revision: candidate.Record.Revision}
		src := f.source()
		winningSource = src
		pass := true
		evaluation := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "Independent comparison", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &pass, ComparisonReport: report}})
		_, e := f.svc.Transition(testCtx, f.human, "skill_versions", candidate.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: candidate.Record.Revision, Status: "approved", ValidationID: evaluation.ID})
		return e
	}
	if e = approve(nil); !errors.Is(e, ErrDenied) {
		t.Fatal("boolean approval bypass", e)
	}
	report := SkillComparisonReport{PlanSHA256: candidate.ComparisonPlanSHA256, BaselineScore: .8, CandidateScore: .9, CandidateCost: 2, CandidateDurationMS: 1000, RegressionPassed: true, SafetyPassed: true}
	cases := []func(*SkillComparisonReport){func(v *SkillComparisonReport) { v.PlanSHA256 = ContentHash("other") }, func(v *SkillComparisonReport) { v.CandidateScore = .85 }, func(v *SkillComparisonReport) { v.CandidateCost = 2.01 }, func(v *SkillComparisonReport) { v.CandidateDurationMS = 1001 }, func(v *SkillComparisonReport) { v.RegressionPassed = false }, func(v *SkillComparisonReport) { v.SafetyPassed = false }}
	for i, change := range cases {
		v := report
		change(&v)
		if e = approve(&v); !errors.Is(e, ErrDenied) {
			t.Fatalf("case %d allowed: %v", i, e)
		}
	}
	if e = approve(&report); e != nil {
		t.Fatal("valid comparison denied", e)
	}
	parent, e := f.svc.ApplySkill(testCtx, f.human, in.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: candidate.Skill.Revision, VersionID: candidate.Record.ID}, false)
	if e != nil || parent.ActiveVersion != candidate.Record.ID {
		t.Fatal("apply", e)
	}
	// A stale basis cannot be imported as another improvement candidate.
	in.Content += "\nCheck edge cases."
	if _, e = f.svc.ImportSkill(testCtx, f.human, in); !errors.Is(e, ErrConflict) {
		t.Fatal("stale baseline", e)
	}
	in.ComparisonPlan.BaselineVersion = parent.ActiveVersion
	changed, e := f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil || changed.Record.Status != "candidate" || changed.Skill.ActiveVersion != parent.ActiveVersion {
		t.Fatal("replacement candidate", e)
	}
	oldID := changed.Record.ID
	plan := *in.ComparisonPlan
	plan.DatasetSHA256 = ContentHash("held-out-v2")
	in.ComparisonPlan = &plan
	changed, e = f.svc.ImportSkill(testCtx, f.human, in)
	if e != nil || changed.Record.ID == oldID {
		t.Fatal("changed plan reused candidate", e)
	}
	active, e := f.svc.Repo.Get(testCtx, "skill_versions", scopeA, parent.ActiveVersion)
	if e != nil || checkActiveSkill(testCtx, f.svc.Repo, active) != nil {
		t.Fatal("active comparison unreadable", e)
	}
	oldRevision := winningSource.Revision
	winningSource.Revision++
	winningSource.Status = "expired"
	if e = f.svc.Repo.Replace(testCtx, "sources", winningSource, oldRevision); e != nil {
		t.Fatal(e)
	}
	if e = checkActiveSkill(testCtx, f.svc.Repo, active); e == nil {
		t.Fatal("revoked evaluation still routes improvement")
	}
	plan.DatasetSHA256 = ContentHash("after-revocation")
	if _, e = f.svc.ImportSkill(testCtx, f.human, in); e == nil {
		t.Fatal("revoked baseline accepted")
	}
}

func TestSkillComparisonMissingMeasurements(t *testing.T) {
	valid := `{"plan_sha256":"` + ContentHash("plan") + `","baseline_score":0,"candidate_score":1,"candidate_cost":0,"candidate_duration_ms":0,"regression_passed":true,"safety_passed":true}`
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(valid), &fields); err != nil {
		t.Fatal(err)
	}
	for field := range fields {
		clone := map[string]json.RawMessage{}
		for k, v := range fields {
			clone[k] = v
		}
		delete(clone, field)
		raw, _ := json.Marshal(clone)
		var out SkillComparisonReport
		if json.Unmarshal(raw, &out) == nil {
			t.Fatal("missing measurement accepted", field)
		}
	}
}
