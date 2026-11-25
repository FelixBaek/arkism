package memory

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPostgresIndependentRollbackQualificationLifecycle(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	template, _ := comparisonRecordFixture(t, newFixture(t))
	f.auth.actors["comparison-service"] = actor("independent-runner", "service", true, allVerbs...)
	p := proposalFixture(t, f)
	initial := p.Skill
	initial.Content = "# Initial\nInspect provided material."
	initial.Analysis = detailedSkillAnalysis()
	initial.ComparisonPlan = nil
	base, e := f.svc.ImportSkill(testCtx, f.human, initial)
	if e != nil {
		t.Fatal(e)
	}
	plan := p.Skill.ComparisonPlan
	plan.BaselineVersion = base.Record.ID
	q := &SkillRollbackQualificationPlan{DatasetSHA256: ContentHash("fixed fallback corpus"), EnvironmentSHA256: ContentHash("fixed fallback runner"), MaxCost: plan.MaxCost, MaxDurationMS: plan.MaxDurationMS}
	for _, cat := range []string{"quality", "regression", "safety"} {
		q.Tasks = append(q.Tasks, SkillHoldoutTask{ContentHash("fallback/" + cat), cat})
	}
	plan.RollbackQualification = q
	candidate, e := f.svc.ProposeSkill(testCtx, f.agent, p)
	if e != nil {
		t.Fatal(e)
	}
	template.Result.Candidate = Ref{Collection: colSkillVersions, ID: candidate.Record.ID, Revision: candidate.Record.Revision}
	template.Result.ContentSHA256 = map[string]string{"candidate": candidate.Record.Analysis.ContentHash, "baseline": base.Record.Analysis.ContentHash}
	template.Result.BaselineID = base.Record.ID
	template.Result.BaselineRevision = base.Record.Revision
	template.Result.Report.PlanSHA256 = candidate.ComparisonPlanSHA256
	in := template
	in.QualifyBaseline = true
	in.EventID = "qualification"
	if _, e = f.svc.RecordSkillComparison(testCtx, "comparison-service", in); e == nil {
		t.Fatal("candidate data qualified fallback")
	}
	r := &in.Result
	report := *r.Report
	r.Report = &report
	r.Report.BaselineScore = 1
	r.Passed = false
	r.DatasetSHA256 = q.DatasetSHA256
	r.EnvironmentSHA256 = q.EnvironmentSHA256
	r.Cases = append([]SkillComparisonCase(nil), r.Cases...)
	for i := range r.Cases {
		c := &r.Cases[i]
		c.CaseSHA256 = ContentHash("fallback/" + c.Category)
		c.Passed = true
	}
	mixed := in
	mixed.QualifyBaseline = false
	if _, e = f.svc.RecordSkillComparison(testCtx, "comparison-service", mixed); e == nil {
		t.Fatal("fallback data certified promotion")
	}
	old := r.Cases[0].CaseSHA256
	r.Cases[0].CaseSHA256 = ContentHash("unregistered")
	r.Cases[1].CaseSHA256 = r.Cases[0].CaseSHA256
	if _, e = f.svc.RecordSkillComparison(testCtx, "comparison-service", in); e == nil {
		t.Fatal("undeclared fallback case accepted")
	}
	r.Cases[0].CaseSHA256 = old
	r.Cases[1].CaseSHA256 = old
	out, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in)
	if e != nil {
		t.Fatal("qualification", e)
	}
	if out.BaselineQualification == nil {
		t.Fatal("no fallback result")
	}
	ev, e := f.svc.Get(testCtx, f.human, "evaluations", scopeA, out.Evaluation.ID)
	if e != nil || ev.Passed {
		t.Fatal("fallback pass escaped to candidate")
	}
	_, e = f.svc.Transition(testCtx, f.human, colSkillVersions, candidate.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved", ValidationID: ev.ID})
	if e == nil {
		t.Fatal("qualified candidate on fallback dataset")
	}
	hdr, e := f.svc.Get(testCtx, f.human, colSkills, scopeA, initial.SkillID)
	if e != nil {
		t.Fatal(e)
	}
	apply := ChangeInput{Scope: scopeA, ExpectedRevision: hdr.Revision, VersionID: base.Record.ID, ValidationID: out.BaselineQualification.ID}
	hdr, e = f.svc.ApplySkill(testCtx, f.human, initial.SkillID, apply, false)
	if e != nil {
		t.Fatal("human qualification", e)
	}
	newBase, e := f.svc.Get(testCtx, f.human, colSkillVersions, scopeA, base.Record.ID)
	if e != nil || newBase.ValidationID != apply.ValidationID || newBase.Revision == base.Record.Revision {
		t.Fatal("not qualified", e)
	}
	if _, e = f.svc.RecordSkillComparison(testCtx, "comparison-service", template); !errors.Is(e, ErrConflict) {
		t.Fatal("stale comparison", e)
	}
	template.Result.BaselineRevision = newBase.Revision
	candidate.Skill = hdr
	_, approved, trial, change := skillAutomationFromComparison(t, f, template, candidate)
	template.TrialID = trial.ID
	template.EventID = "fresh-approved-comparison"
	template.Result.Candidate.Revision = approved.Revision
	measured, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", template)
	if e != nil {
		t.Fatal("fresh approval rev", e)
	}
	change.ValidationID = measured.Evaluation.ID
	hdr, e = f.svc.ApplySkill(testCtx, testPrincipalToken("learner"), initial.SkillID, change, false)
	if e != nil || hdr.ActiveVersion != approved.ID {
		t.Fatal("promotion", e)
	}
	checkRegressionRestoresBaseline(t, f, approved, trial, candidate.Version, base.Record.ID)
}

func checkRegressionRestoresBaseline(t *testing.T, f *fixture, approved, trial Record, version int, baseID string) {
	t.Helper()
	scope := scopeA
	scope.AgentID = "codex-runtime"
	time.Sleep(3 * time.Millisecond)
	end := time.Now().UTC().Truncate(time.Millisecond)
	u := SkillUsage{SkillID: approved.SkillID, VersionID: approved.ID, Version: version, Runtime: "codex", ContentHash: approved.Analysis.ContentHash, Status: "failure", StartedAt: end, CompletedAt: end}
	raw, _ := json.Marshal(u)
	_, e := f.svc.IngestCompletedTrace(testCtx, f.agent, TraceIngestInput{Scope: scope, TraceID: "post-promotion-regression", ThreadID: "after-promotion", AgentRun: CompletedAgentRunInput{ID: "regression-run", TaskType: "run", Outcome: "failed", StartedAt: end, FinishedAt: end, Text: "Synthetic regression after promotion"}, Events: []TraceEventInput{{ID: "regression-usage", Kind: skillUsageEventKind, OccurredAt: end, Payload: raw}}})
	if e != nil {
		t.Fatal("regression ingest", e)
	}
	got, e := f.svc.MonitorSkillTrial(testCtx, testPrincipalToken("learner"), trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: approved.SkillID})
	if e != nil || got.State != "rolled_back" {
		t.Fatal("regression monitor", got.State, e)
	}
	hdr, e := f.svc.Get(testCtx, f.human, colSkills, scopeA, approved.SkillID)
	if e != nil || hdr.ActiveVersion != baseID {
		t.Fatal("not restored", e)
	}
}
