package memory

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportedBaselineQualificationFromFixedComparison(t *testing.T) {
	testBaselineQualification(t, false)
}
func TestPostgresImportedBaselineQualificationFromFixedComparison(t *testing.T) {
	testBaselineQualification(t, true)
}
func testBaselineQualification(t *testing.T, postgres bool) {
	for _, test := range []struct {
		name     string
		category string
		ok       bool
	}{{"all_pass", "", true}, {"quality", "quality", false}, {"safety", "safety", false}, {"regression", "regression", false}} {
		t.Run(test.name, func(t *testing.T) {
			template, _ := comparisonRecordFixture(t, newFixture(t))
			f := newFixture(t)
			if postgres {
				f.svc.Repo = isolatedPostgres(t)
			}
			p := proposalFixture(t, f)
			initial := p.Skill
			initial.Content = "# Initial\nInspect provided material."
			initial.Analysis = detailedSkillAnalysis()
			initial.ComparisonPlan = nil
			base, e := f.svc.ImportSkill(testCtx, f.human, initial)
			if e != nil {
				t.Fatal(e)
			}
			p.Skill.ComparisonPlan.BaselineVersion = base.Record.ID
			candidate, e := f.svc.ProposeSkill(testCtx, f.agent, p)
			if e != nil {
				t.Fatal(e)
			}
			f.auth.actors["comparison-service"] = actor("independent-runner", "service", true, allVerbs...)
			in := template
			in.QualifyBaseline = true
			res := &in.Result
			res.Candidate = Ref{Collection: colSkillVersions, ID: candidate.Record.ID, Revision: 1}
			res.BaselineID = base.Record.ID
			res.BaselineRevision = 1
			res.ContentSHA256["candidate"] = candidate.Record.Analysis.ContentHash
			res.ContentSHA256["baseline"] = base.Record.Analysis.ContentHash
			res.Report.PlanSHA256 = candidate.ComparisonPlanSHA256
			res.Report.BaselineScore = 1
			res.Passed = false
			for i := range res.Cases {
				c := &res.Cases[i]
				if c.Variant == "baseline" {
					c.Passed = c.Category != test.category
					if c.Category == "quality" && !c.Passed {
						res.Report.BaselineScore = 0
						res.Passed = true
					}
				}
			}
			post := func(route, token string, body any) (int, []byte) {
				req := httptest.NewRequest("POST", route, strings.NewReader(structuredText(body)))
				req.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				Handler(f.svc).ServeHTTP(w, req)
				return w.Code, w.Body.Bytes()
			}
			code, raw := post("/v1/skills/comparisons/record", "comparison-service", in)
			if code != 200 {
				t.Fatalf("comparison HTTP %d: %s", code, raw)
			}
			var out RecordSkillComparisonResult
			e = json.Unmarshal(raw, &out)
			if e != nil {
				t.Fatal(e)
			}
			ev, e := f.svc.Get(testCtx, f.human, "evaluations", scopeA, out.BaselineQualification.ID)
			if e != nil || ev.Passed != test.ok || ev.Target.ID != base.Record.ID || ev.Target.Revision != 1 {
				t.Fatal(ev, e)
			}
			cached, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in)
			if e != nil || *cached.BaselineQualification != *out.BaselineQualification {
				t.Fatal(e)
			}
			before, e := f.svc.Get(testCtx, f.human, colSkills, scopeA, initial.SkillID)
			if e != nil || before.ActiveVersion != base.Record.ID {
				t.Fatal(e)
			}
			b, e := f.svc.Get(testCtx, f.human, colSkillVersions, scopeA, base.Record.ID)
			if e != nil || b.ValidationID != "" || b.Revision != 1 {
				t.Fatal(b, e)
			}
			code, raw = post("/v1/skills/"+initial.SkillID+"/apply", f.human, ChangeInput{Scope: scopeA, ExpectedRevision: before.Revision, VersionID: b.ID, ValidationID: ev.ID})
			if test.ok && code != 200 || !test.ok && code != 403 {
				t.Fatalf("qualification apply: %d %s", code, raw)
			}
			qualified, e := f.svc.Get(testCtx, f.human, colSkillVersions, scopeA, b.ID)
			if e != nil || qualified.Status != "active" || test.ok && (qualified.ValidationID != ev.ID || qualified.Revision != b.Revision+1) || !test.ok && (qualified.ValidationID != "" || qualified.Revision != b.Revision) {
				t.Fatal(qualified, e)
			}
			code, raw = post("/v1/evaluations", "comparison-service", CreateInput{Scope: scopeA, Kind: baselineQualificationKind, Text: "direct reviewer claim", Target: ev.Target, Sources: ev.Sources, Evaluation: ev.Evaluation})
			if code != 403 {
				t.Fatalf("manually supplied qualification: %d %s", code, raw)
			}
			after, e := f.svc.Get(testCtx, f.human, colSkills, scopeA, initial.SkillID)
			if e != nil || after.ActiveVersion != before.ActiveVersion {
				t.Fatal(e)
			}
		})
	}
}
