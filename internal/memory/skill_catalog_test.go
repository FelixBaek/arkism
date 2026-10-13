package memory

import (
	"errors"
	"testing"
)

func detailedSkillAnalysis() *SkillAnalysis {
	return &SkillAnalysis{
		SkillID: "design-review", Version: "0123456789abcdef", ContentHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Name: "Design review", Description: "Review a product interface before release.", Problem: "Find material interface defects.",
		UseWhen: []string{"Before releasing a user interface."}, InputFormat: "A runnable interface.", OutputFormat: "Prioritized findings.",
		SuccessCriteria: []string{"Material defects are identified."}, Verification: []string{"Inspect desktop and mobile views."},
		SourcePath: "/skills/design-review/SKILL.md", SourceFile: "SKILL.md",
	}
}

func TestImportedSkillAnalysisMustBeDetailed(t *testing.T) {
	a := detailedSkillAnalysis()
	if !validAnalysis(a) {
		t.Fatal("detailed imported analysis rejected")
	}
	a.Description = ""
	if validAnalysis(a) {
		t.Fatal("empty description accepted")
	}
}

func TestImportSkillCreatesAndReusesBaseline(t *testing.T) {
	f := newFixture(t)
	in := ImportSkillInput{Scope: scopeA, SkillID: "design-review", Path: "/skills/design-review/SKILL.md", Content: "# Design review\n\nInspect the interface.", Analysis: detailedSkillAnalysis()}
	first, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil || first.Reused || first.Record.Status != statusActive || first.Skill.ActiveVersion != first.Record.ID {
		t.Fatal("first import", err, first.Reused, first.Record.Status)
	}
	second, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil || !second.Reused || second.Record.ID != first.Record.ID {
		t.Fatal("reused import", err, second.Reused)
	}
	if jobs := f.embeddingJobs(); len(jobs) != 1 || jobs[0].Target == nil || jobs[0].Target.ID != first.Record.ID {
		t.Fatal("import embedding job", jobs)
	}
	if _, err = f.svc.ImportSkill(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("agent imported baseline", err)
	}
}

func TestImportSkillCanRedactExampleSecrets(t *testing.T) {
	f := newFixture(t)
	in := ImportSkillInput{Scope: scopeA, SkillID: "safe-example", Path: "/skills/safe-example/SKILL.md", Content: "# Example\n\npassword=synthetic-example", Analysis: detailedSkillAnalysis(), RedactSecrets: true}
	in.Analysis.SkillID = in.SkillID
	result, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil || result.Record.Inspection == nil || !result.Record.Inspection.Redacted {
		t.Fatal("redacted import", err)
	}
	source, err := f.repo.Get(testCtx, "sources", scopeA, result.Record.Sources[0].SourceID)
	if err != nil || source.Text != "# Example\n\n[REDACTED]" || containsSecret(source.Text) {
		t.Fatal("secret example retained", err)
	}
}
