package memory

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func comparisonRecordFixture(t *testing.T, f *fixture) (RecordSkillComparisonInput, ImportSkillResult) {
	t.Helper()
	proposed := proposalFixture(t, f)
	candidate, e := f.svc.ProposeSkill(testCtx, f.agent, proposed)
	if e != nil {
		t.Fatal(e)
	}
	f.auth.actors["comparison-service"] = actor("independent-runner", "service", true, allVerbs...)
	result := SkillComparisonResult{FormatVersion: 1, Candidate: Ref{Collection: colSkillVersions, ID: candidate.Record.ID, Revision: 1}, ContentSHA256: map[string]string{"baseline": ContentHash(""), "candidate": candidate.Record.Analysis.ContentHash}, DatasetSHA256: proposed.Skill.ComparisonPlan.DatasetSHA256, EnvironmentSHA256: proposed.Skill.ComparisonPlan.EnvironmentSHA256, Passed: true, Report: &SkillComparisonReport{PlanSHA256: candidate.ComparisonPlanSHA256, BaselineScore: 0, CandidateScore: 1, CandidateCost: 0, CandidateDurationMS: 30, RegressionPassed: true, SafetyPassed: true}}
	end := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	for _, category := range []string{"quality", "regression", "safety"} {
		for _, variant := range []string{"baseline", "candidate"} {
			result.Cases = append(result.Cases, SkillComparisonCase{CaseSHA256: ContentHash(category), Category: category, Variant: variant, Passed: variant == "candidate" || category != "quality", OutputSHA256: ContentHash(category + variant), DurationMS: 10, StartedAt: end.Add(-10 * time.Millisecond), FinishedAt: end})
		}
	}
	return RecordSkillComparisonInput{Scope: scopeA, EventID: "measured-once", Result: result}, candidate
}
func TestSkillComparisonRecord(t *testing.T) { checkSkillComparisonRecord(t, newFixture(t)) }
func TestPostgresSkillComparisonRecord(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillComparisonRecord(t, f)
}
func checkSkillComparisonRecord(t *testing.T, f *fixture) {
	t.Helper()
	in, candidate := comparisonRecordFixture(t, f)
	first, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in)
	if e != nil || structuredText(first) != structuredText(again) {
		t.Fatal("retry", e)
	}
	changed := in
	changed.Result.Cases = append([]SkillComparisonCase(nil), in.Result.Cases...)
	changed.Result.Cases[0].OutputSHA256 = ContentHash("changed")
	if _, e = f.svc.RecordSkillComparison(testCtx, "comparison-service", changed); !errors.Is(e, ErrConflict) {
		t.Fatal("same key changed payload", e)
	}
	summary, e := f.svc.SummarizeEvaluations(testCtx, f.human, EvaluationSummaryInput{Scope: scopeA, Target: in.Result.Candidate})
	if e != nil || len(summary.EvaluationRefs) != 1 || summary.EvaluationRefs[0] != first.Evaluation {
		t.Fatalf("review evaluation discovery: %+v %v", summary, e)
	}
	ev, e := f.svc.Get(testCtx, f.human, "evaluations", scopeA, summary.EvaluationRefs[0].ID)
	if e != nil || ev.Revision != first.Evaluation.Revision || ev.Evaluation == nil || ev.Evaluation.ComparisonReport == nil {
		t.Fatalf("review evaluation read: %+v %v", ev, e)
	}
	approved, e := f.svc.Transition(testCtx, f.human, colSkillVersions, candidate.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved", ValidationID: summary.EvaluationRefs[0].ID})
	if e != nil || approved.Status != "approved" {
		t.Fatal(e)
	}
	again, e = f.svc.RecordSkillComparison(testCtx, "comparison-service", in)
	if e != nil || structuredText(first) != structuredText(again) {
		t.Fatal("approved retry", e)
	}
	if e = f.svc.Delete(testCtx, f.human, "sources", first.SourceID, ChangeInput{Scope: scopeA, ExpectedRevision: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.RecordSkillComparison(testCtx, "comparison-service", in); e == nil {
		t.Fatal("erased source resurrected")
	}
}
func TestSkillComparisonRecordAtomicAndDenied(t *testing.T) {
	for _, name := range []string{"audit", "agent", "human", "self", "stale", "pair", "aggregate", "cost_rounding", "missing_category", "future", "expired_source", "denied"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			in, _ := comparisonRecordFixture(t, f)
			token := "comparison-service"
			before := map[string]int{}
			for _, col := range []string{"sources", "agent_runs", "evaluations"} {
				before[col] = len(f.repo.rows(col))
			}
			switch name {
			case "audit":
				f.repo.failAudit = true
			case "agent":
				token = f.agent
			case "human":
				token = f.human
			case "self":
				f.auth.actors[token] = actor("agent-1", "service", true, allVerbs...)
			case "stale":
				in.Result.Candidate.Revision++
			case "pair":
				in.Result.Cases[1].CaseSHA256 = ContentHash("unpaired")
			case "cost_rounding":
				in.Result.Cases[1].Cost = 2 + 1e-10
				in.Result.Report.CandidateCost = 2
			case "aggregate":
				in.Result.Report.CandidateScore = .8
			case "missing_category":
				for i := range in.Result.Cases {
					if in.Result.Cases[i].Category == "safety" {
						in.Result.Cases[i].Category = "regression"
					}
				}
			case "future":
				in.Result.Cases[0].FinishedAt = time.Now().Add(time.Hour)
			case "denied":
				f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "write:evaluations"}
			case "expired_source":
				src := f.source()
				prev := src.Revision
				src.Status = "expired"
				src.Revision++
				if e := f.repo.Replace(testCtx, "sources", src, prev); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.svc.RecordSkillComparison(testCtx, token, in); e == nil {
				t.Fatal("invalid recording accepted")
			}
			for col, count := range before {
				if len(f.repo.rows(col)) != count {
					t.Fatal("partial transaction", col)
				}
			}
		})
	}
}
func TestSkillComparisonCaseRequiresMeasurements(t *testing.T) {
	f := newFixture(t)
	in, _ := comparisonRecordFixture(t, f)
	raw, _ := json.Marshal(in.Result.Cases[0])
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		t.Fatal(e)
	}
	for field := range fields {
		clone := map[string]json.RawMessage{}
		for key, value := range fields {
			if key != field {
				clone[key] = value
			}
		}
		raw, _ = json.Marshal(clone)
		var out SkillComparisonCase
		if json.Unmarshal(raw, &out) == nil {
			t.Fatal("omitted measurement", field)
		}
	}
}

func TestPostgresSkillComparisonRecordRollback(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	in, _ := comparisonRecordFixture(t, f)
	repo := f.svc.Repo
	before := map[string]int{}
	for _, col := range []string{"sources", "agent_runs", "evaluations"} {
		rows, e := repo.ListRecords(testCtx, scopeA, col, 100)
		if e != nil {
			t.Fatal(e)
		}
		before[col] = len(rows)
	}
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "record:skill_comparison"}
	if _, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in); !errors.Is(e, ErrUnavailable) {
		t.Fatal("injected audit failure", e)
	}
	for col, count := range before {
		rows, e := repo.ListRecords(testCtx, scopeA, col, 100)
		if e != nil || len(rows) != count {
			t.Fatal("partial DB transaction", col, e)
		}
	}
	f.svc.Repo = repo
	if _, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in); e != nil {
		t.Fatal("retry after rollback", e)
	}
}

func TestPostgresSkillComparisonRecordConcurrent(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	in, _ := comparisonRecordFixture(t, f)
	type response struct {
		result RecordSkillComparisonResult
		err    error
	}
	ch := make(chan response, 2)
	for i := 0; i < 2; i++ {
		go func() { v, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", in); ch <- response{v, e} }()
	}
	first, second := <-ch, <-ch
	if first.err != nil || second.err != nil || structuredText(first.result) != structuredText(second.result) {
		t.Fatal("concurrent retry", first.err, second.err)
	}
	rows, e := f.svc.Repo.EvaluationsFor(testCtx, scopeA, in.Result.Candidate)
	if e != nil || len(rows) != 1 {
		t.Fatal("duplicate concurrent evaluation", e)
	}
}
