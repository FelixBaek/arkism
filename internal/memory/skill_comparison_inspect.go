package memory

import (
	"encoding/json"
	"reflect"
	"strings"
)

// InspectRecordedSkillComparison replays the producer's paired-case checks for
// existing, separately authorized records. It does not authorize a transition.
func InspectRecordedSkillComparison(scope Scope, base, candidate, evaluation, source Record, runs [2]Record) (*SkillComparisonResult, bool) {
	plan := candidate.ComparisonPlan
	prefix := strings.TrimSuffix(evaluation.ID, "-evaluation")
	if !strings.HasPrefix(prefix, "comparison-") || !sourceHashPattern.MatchString(strings.TrimPrefix(prefix, "comparison-")) || evaluation.ID != prefix+"-evaluation" ||
		plan == nil || !validComparisonPlan(plan) || plan.BaselineVersion != base.ID || base.ID == candidate.ID || base.SkillID == "" || base.SkillID != candidate.SkillID ||
		base.Scope != scope || candidate.Scope != scope || candidate.CreatedBy == "" || base.Analysis == nil || candidate.Analysis == nil {
		return nil, false
	}
	if !independentSelectionRecords(scope, candidate.CreatedBy, evaluation, source, runs) {
		return nil, false
	}
	if evaluation.Kind != "skill_comparison" || evaluation.Target == nil || evaluation.Evaluation == nil || evaluation.Evaluation.EvalType != "automated_check" || evaluation.Evaluation.Passed == nil ||
		source.Kind != "skill_comparison_result" || source.ID != prefix+"-source" || source.SourceRef != prefix || source.OriginProduct != "arkism" || source.ContentHash != ContentHash(source.Text) ||
		len(source.Sources) == 0 || !reflect.DeepEqual(source.Sources[0], sourceReference(source, "comparison")) || !reflect.DeepEqual(source.Sources, evaluation.Sources) || len(evaluation.Relations) != 2 {
		return nil, false
	}
	var r SkillComparisonResult
	if err := json.Unmarshal([]byte(source.Text), &r); err != nil {
		var wrapped struct {
			TrialID string                `json:"trial_id"`
			Result  SkillComparisonResult `json:"result"`
		}
		if json.Unmarshal([]byte(source.Text), &wrapped) != nil || wrapped.TrialID == "" {
			return nil, false
		}
		r = wrapped.Result
	}
	if invalidComparisonResult(r) || r.Candidate != *evaluation.Target || r.Candidate.ID != candidate.ID || r.Candidate.Revision > candidate.Revision ||
		r.BaselineID != base.ID || r.BaselineRevision < 1 || r.BaselineRevision > base.Revision || r.ContentSHA256["candidate"] != candidate.Analysis.ContentHash ||
		r.ContentSHA256["baseline"] != base.Analysis.ContentHash || r.DatasetSHA256 != plan.DatasetSHA256 || r.EnvironmentSHA256 != plan.EnvironmentSHA256 || r.Report.PlanSHA256 != comparisonPlanHash(plan) ||
		r.Report.BundleSHA256["candidate"] != candidate.SkillBundleSHA256 || r.Report.BundleSHA256["baseline"] != base.SkillBundleSHA256 || r.Passed != *evaluation.Evaluation.Passed || r.Passed != evaluation.Passed {
		return nil, false
	}
	expected, measured, err := comparisonMeasurements(r, scope)
	if err != nil || !reflect.DeepEqual(&measured, evaluation.Evaluation.ComparisonReport) {
		return nil, false
	}
	holdoutPassed, err := skillHoldoutPasses(plan.Holdout, r.Cases)
	if err != nil || r.Passed != (comparisonPasses(candidate, evaluation) && holdoutPassed) {
		return nil, false
	}
	for i, variant := range []string{"baseline", "candidate"} {
		run := runs[i]
		if run.ID != prefix+"-"+variant || run.Kind != "skill_comparison" || evaluation.Relations[i] != (Ref{Collection: "agent_runs", ID: run.ID, Revision: 1}) ||
			!reflect.DeepEqual(run.Sources, source.Sources) || !reflect.DeepEqual(run.Run, &expected[i]) {
			return nil, false
		}
	}
	return &r, true
}

func independentSelectionRecords(scope Scope, author string, evaluation, source Record, runs [2]Record) bool {
	actor := evaluation.CreatedBy
	role := evaluation.CreatedByType
	if actor == "" || actor == author || (role != "service" && role != "pipeline") {
		return false
	}
	for _, r := range []Record{evaluation, source, runs[0], runs[1]} {
		if !live(r) || r.ContentErased || r.Revision != 1 || r.Scope != scope || r.CreatedBy != actor || r.CreatedByType != role {
			return false
		}
	}
	return true
}
