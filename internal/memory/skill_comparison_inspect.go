package memory

import (
	"encoding/json"
	"reflect"
	"strings"
)

// InspectRecordedSkillComparison replays the producer's paired-case checks for
// existing, separately authorized records. It does not authorize a transition.
func InspectRecordedSkillComparison(scope Scope, base, candidate, evaluation, source Record, runs [2]Record) (*SkillComparisonResult, bool) {
	prefix := strings.TrimSuffix(evaluation.ID, "-evaluation")
	if !selectionVersionPair(scope, base, candidate) || !independentSelectionRecords(scope, candidate.CreatedBy, evaluation, source, runs) ||
		!selectionEvidenceLinks(prefix, evaluation, source) {
		return nil, false
	}
	result, ok := readStoredComparison(source.Text)
	if !ok || !resultMatchesSelection(result, base, candidate, evaluation) {
		return nil, false
	}
	expected, measured, err := comparisonMeasurements(result, scope)
	if err != nil || !reflect.DeepEqual(&measured, evaluation.Evaluation.ComparisonReport) {
		return nil, false
	}
	holdout, err := skillHoldoutPasses(candidate.ComparisonPlan.Holdout, result.Cases)
	if err != nil || result.Passed != (comparisonPasses(candidate, evaluation) && holdout) {
		return nil, false
	}
	if !selectionRunsMatch(prefix, evaluation, source, runs, expected) {
		return nil, false
	}
	return &result, true
}

func selectionVersionPair(scope Scope, base, candidate Record) bool {
	plan := candidate.ComparisonPlan
	return plan != nil && validComparisonPlan(plan) && plan.BaselineVersion == base.ID && base.ID != candidate.ID &&
		base.SkillID != "" && base.SkillID == candidate.SkillID && base.Scope == scope && candidate.Scope == scope &&
		candidate.CreatedBy != "" && base.Analysis != nil && candidate.Analysis != nil
}

func selectionEvidenceLinks(prefix string, evaluation, source Record) bool {
	return strings.HasPrefix(prefix, "comparison-") && sourceHashPattern.MatchString(strings.TrimPrefix(prefix, "comparison-")) && evaluation.ID == prefix+"-evaluation" &&
		evaluation.Kind == "skill_comparison" && evaluation.Target != nil && evaluation.Evaluation != nil && evaluation.Evaluation.EvalType == "automated_check" && evaluation.Evaluation.Passed != nil &&
		source.Kind == "skill_comparison_result" && source.ID == prefix+"-source" && source.SourceRef == prefix && source.OriginProduct == "arkism" && source.ContentHash == ContentHash(source.Text) &&
		len(source.Sources) > 0 && reflect.DeepEqual(source.Sources[0], sourceReference(source, "comparison")) && reflect.DeepEqual(source.Sources, evaluation.Sources) && len(evaluation.Relations) == 2
}

func readStoredComparison(text string) (SkillComparisonResult, bool) {
	var r SkillComparisonResult
	if json.Unmarshal([]byte(text), &r) == nil {
		return r, true
	}
	var wrapped struct {
		TrialID string                `json:"trial_id"`
		Result  SkillComparisonResult `json:"result"`
	}
	err := json.Unmarshal([]byte(text), &wrapped)
	return wrapped.Result, err == nil && wrapped.TrialID != ""
}

func resultMatchesSelection(r SkillComparisonResult, base, candidate, evaluation Record) bool {
	plan := candidate.ComparisonPlan
	return !invalidComparisonResult(r) && r.Candidate == *evaluation.Target && r.Candidate.ID == candidate.ID && r.Candidate.Revision <= candidate.Revision &&
		r.BaselineID == base.ID && r.BaselineRevision >= 1 && r.BaselineRevision <= base.Revision &&
		r.ContentSHA256["candidate"] == candidate.Analysis.ContentHash && r.ContentSHA256["baseline"] == base.Analysis.ContentHash &&
		r.DatasetSHA256 == plan.DatasetSHA256 && r.EnvironmentSHA256 == plan.EnvironmentSHA256 && r.Report.PlanSHA256 == comparisonPlanHash(plan) &&
		r.Report.BundleSHA256["candidate"] == candidate.SkillBundleSHA256 && r.Report.BundleSHA256["baseline"] == base.SkillBundleSHA256 &&
		r.Passed == *evaluation.Evaluation.Passed && r.Passed == evaluation.Passed
}

func selectionRunsMatch(prefix string, evaluation, source Record, runs [2]Record, expected [2]RunDetails) bool {
	for i, variant := range []string{"baseline", "candidate"} {
		run := runs[i]
		if run.ID != prefix+"-"+variant || run.Kind != "skill_comparison" || evaluation.Relations[i] != (Ref{Collection: "agent_runs", ID: run.ID, Revision: 1}) ||
			!reflect.DeepEqual(run.Sources, source.Sources) || !reflect.DeepEqual(run.Run, &expected[i]) {
			return false
		}
	}
	return true
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
