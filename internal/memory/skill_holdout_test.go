package memory

import (
	"errors"
	"testing"
)

func holdoutFixture(t *testing.T, f *fixture) (RecordSkillComparisonInput, ImportSkillResult) {
	t.Helper()
	plan := &SkillHoldoutPlan{DevelopmentTasks: []string{ContentHash("development-task")}, Tasks: []SkillHoldoutTask{{CaseSHA256: ContentHash("quality"), Category: "unseen-task"}}, MinCategoryScore: .9, MinCategoryCases: 1}
	in, candidate := comparisonRecordFixture(t, f, plan)
	return in, candidate
}
func TestSkillHoldoutPlanLeakage(t *testing.T) {
	p := &SkillHoldoutPlan{DevelopmentTasks: []string{ContentHash("dev")}, Tasks: []SkillHoldoutTask{{ContentHash("holdout"), "reasoning"}}, MinCategoryScore: .9, MinCategoryCases: 1}
	if !validSkillHoldout(p) {
		t.Fatal("valid manifest")
	}
	p.DevelopmentTasks = append(p.DevelopmentTasks, ContentHash("holdout"))
	if validSkillHoldout(p) {
		t.Fatal("development overlap accepted")
	}
	p.DevelopmentTasks = p.DevelopmentTasks[:1]
	p.MinCategoryCases = 2
	if validSkillHoldout(p) {
		t.Fatal("undersized category accepted")
	}
	p.MinCategoryCases = 1
	p.MinCategoryScore = .89
	if validSkillHoldout(p) {
		t.Fatal("weak threshold accepted")
	}
}
func TestSkillHoldoutRejectsMissingAndRelabelledTasks(t *testing.T) {
	for _, name := range []string{"missing", "development", "extra", "relabel", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			in, candidate := holdoutFixture(t, f)
			switch name {
			case "missing":
				in.Result.Cases = in.Result.Cases[2:]
			case "development":
				candidate.Record.ComparisonPlan.Holdout.DevelopmentTasks = append(candidate.Record.ComparisonPlan.Holdout.DevelopmentTasks, ContentHash("safety"))
			case "extra":
				in.Result.Cases[0].CaseSHA256 = ContentHash("unknown")
			case "relabel":
				in.Result.Cases[0].Category = "safety"
			case "duplicate":
				in.Result.Cases = append(in.Result.Cases, in.Result.Cases[0])
			}
			if _, e := skillHoldoutPasses(candidate.Record.ComparisonPlan.Holdout, in.Result.Cases); e == nil {
				t.Fatal("invalid holdout accepted")
			}
		})
	}
}
func TestSkillHoldoutCategoryCannotHideBehindAverage(t *testing.T) {
	p := &SkillHoldoutPlan{DevelopmentTasks: []string{ContentHash("dev")}, MinCategoryScore: .9, MinCategoryCases: 1}
	var rows []SkillComparisonCase
	for i := 0; i < 10; i++ {
		h := ContentHash(string(rune('a' + i)))
		category := "easy"
		if i == 9 {
			category = "hard"
		}
		p.Tasks = append(p.Tasks, SkillHoldoutTask{h, category})
		for _, variant := range []string{"baseline", "candidate"} {
			rows = append(rows, SkillComparisonCase{CaseSHA256: h, Category: "quality", Variant: variant, Passed: i < 9})
		}
	}
	if passed, e := skillHoldoutPasses(p, rows); e != nil || passed {
		t.Fatal("90% aggregate concealed 0% hard category", passed, e)
	}
}
func TestSkillHoldoutRecordedApproval(t *testing.T) {
	checkSkillHoldoutRecordedApproval(t, newFixture(t))
}
func TestPostgresSkillHoldoutRecordedApproval(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillHoldoutRecordedApproval(t, f)
}
func checkSkillHoldoutRecordedApproval(t *testing.T, f *fixture) {
	t.Helper()
	in, candidate := holdoutFixture(t, f)
	// An independently authored ordinary aggregate report is insufficient.
	passed := true
	fake := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "aggregate claim", Sources: refs(f.source()), Target: &in.Result.Candidate, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed, ComparisonReport: in.Result.Report}})
	if _, e := f.svc.Transition(testCtx, f.human, colSkillVersions, candidate.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved", ValidationID: fake.ID}); !errors.Is(e, ErrDenied) {
		t.Fatal("aggregate bypass", e)
	}
	result, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.Transition(testCtx, f.human, colSkillVersions, candidate.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved", ValidationID: result.Evaluation.ID}); e != nil {
		t.Fatal("recorded holdout approval", e)
	}
}
func TestSkillHoldoutMissingFailsAtomicRecording(t *testing.T) {
	f := newFixture(t)
	in, candidate := holdoutFixture(t, f)
	candidate.Record.ComparisonPlan.Holdout.Tasks = append(candidate.Record.ComparisonPlan.Holdout.Tasks, SkillHoldoutTask{ContentHash("missing"), "unseen-task"})
	if e := f.svc.Repo.Replace(testCtx, colSkillVersions, candidate.Record, 1); e != nil {
		t.Fatal(e)
	}
	in.Result.Report.PlanSHA256 = comparisonPlanHash(candidate.Record.ComparisonPlan)
	before := len(f.repo.rows("evaluations"))
	if _, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if len(f.repo.rows("evaluations")) != before {
		t.Fatal("partial evidence committed")
	}
}

// The reserved source kind is part of the recorder provenance boundary: generic
// source writes cannot fabricate the source accepted by holdout approval.
func TestSkillHoldoutReservedSourceCannotBeCreated(t *testing.T) {
	f := newFixture(t)
	in, _ := holdoutFixture(t, f)
	f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "write:agent_runs"}
	if _, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in); e == nil {
		t.Fatal("recorder unexpectedly permitted")
	}
	if _, e := f.svc.Create(testCtx, "comparison-service", "sources", CreateInput{Scope: scopeA, Kind: "skill_comparison_result", Text: structuredText(in.Result), OriginProduct: "arkism", SourceRef: "forged-comparison"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("reserved comparison source created", e)
	}
}
