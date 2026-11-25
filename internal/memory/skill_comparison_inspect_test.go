package memory

import "testing"

func checkStoredSelectionEvidence(t *testing.T, f *fixture, base, candidate Record, links RecordSkillComparisonResult, passed bool) {
	t.Helper()
	get := func(col, id string) Record {
		r, err := f.svc.Get(testCtx, f.human, col, scopeA, id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	eval := get("evaluations", links.Evaluation.ID)
	src := get("sources", links.SourceID)
	runs := [2]Record{get("agent_runs", links.Runs[0].ID), get("agent_runs", links.Runs[1].ID)}
	checked, ok := InspectRecordedSkillComparison(scopeA, base, candidate, eval, src, runs)
	if !ok || checked.Passed != passed {
		t.Fatal("independent measured result missing")
	}
	self := candidate
	self.CreatedBy = eval.CreatedBy
	if _, ok = InspectRecordedSkillComparison(scopeA, base, self, eval, src, runs); ok {
		t.Fatal("self assessment accepted")
	}
	if _, ok = InspectRecordedSkillComparison(scopeA, base, candidate, eval, src, [2]Record{runs[1], runs[0]}); ok {
		t.Fatal("runs interchanged")
	}
	different := *candidate.ComparisonPlan
	different.DatasetSHA256 = ContentHash("unrelated dataset")
	candidate.ComparisonPlan = &different
	if _, ok = InspectRecordedSkillComparison(scopeA, base, candidate, eval, src, runs); ok {
		t.Fatal("mismatched preregistration")
	}
}
func TestInspectStoredSelectionEvidence(t *testing.T) {
	for _, pass := range []bool{true, false} {
		template, _ := comparisonRecordFixture(t, newFixture(t))
		f := newFixture(t)
		p := proposalFixture(t, f)
		initial := p.Skill
		initial.Content = "# Initial\nInspect supplied material."
		initial.Analysis = detailedSkillAnalysis()
		initial.ComparisonPlan = nil
		base, err := f.svc.ImportSkill(testCtx, f.human, initial)
		if err != nil {
			t.Fatal(err)
		}
		p.Skill.ComparisonPlan.BaselineVersion = base.Record.ID

		candidate, err := f.svc.ProposeSkill(testCtx, f.agent, p)
		if err != nil {
			t.Fatal(err)
		}
		f.auth.actors["comparison-service"] = actor("independent-runner", "service", true, allVerbs...)
		r := &template.Result
		r.Candidate = Ref{Collection: colSkillVersions, ID: candidate.Record.ID, Revision: 1}
		r.BaselineID = base.Record.ID
		r.BaselineRevision = base.Record.Revision
		r.ContentSHA256["candidate"] = candidate.Record.Analysis.ContentHash
		r.ContentSHA256["baseline"] = base.Record.Analysis.ContentHash
		r.Report.PlanSHA256 = candidate.ComparisonPlanSHA256
		r.Passed = pass
		if !pass {
			for i, c := range r.Cases {
				if c.Variant == "baseline" && c.Category == "quality" {
					r.Cases[i].Passed = true
				}
			}
			r.Report.BaselineScore = 1
		}
		receipt, err := f.svc.RecordSkillComparison(testCtx, "comparison-service", template)
		if err != nil {
			t.Fatal(err)
		}
		checkStoredSelectionEvidence(t, f, base.Record, candidate.Record, receipt, pass)
	}
}
