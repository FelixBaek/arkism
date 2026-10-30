package memory

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
)

// SkillComparisonPlan is fixed before a candidate is evaluated. Hashes identify
// an externally retained dataset/environment; they do not certify execution.
type SkillComparisonPlan struct {
	BaselineVersion   string  `json:"baseline_version" bson:"baseline_version"`
	DatasetSHA256     string  `json:"dataset_sha256" bson:"dataset_sha256"`
	EnvironmentSHA256 string  `json:"environment_sha256" bson:"environment_sha256"`
	MinScoreGain      float64 `json:"min_score_gain" bson:"min_score_gain"`
	MaxCost           float64 `json:"max_cost" bson:"max_cost"`
	MaxDurationMS     int64   `json:"max_duration_ms" bson:"max_duration_ms"`
}
type SkillComparisonReport struct {
	PlanSHA256          string  `json:"plan_sha256" bson:"plan_sha256"`
	BaselineScore       float64 `json:"baseline_score" bson:"baseline_score"`
	CandidateScore      float64 `json:"candidate_score" bson:"candidate_score"`
	CandidateCost       float64 `json:"candidate_cost" bson:"candidate_cost"`
	CandidateDurationMS int64   `json:"candidate_duration_ms" bson:"candidate_duration_ms"`
	// The baseline run's totals, measured from its cases by the server like the candidate's; absent on older reports.
	BaselineCost       float64 `json:"baseline_cost,omitempty" bson:"baseline_cost,omitempty"`
	BaselineDurationMS int64   `json:"baseline_duration_ms,omitempty" bson:"baseline_duration_ms,omitempty"`
	RegressionPassed   bool    `json:"regression_passed" bson:"regression_passed"`
	SafetyPassed       bool    `json:"safety_passed" bson:"safety_passed"`
	// Companion binding: the pinned bundle hash of each variant ("" when unpinned) and whether
	// the adapter was given the files (materialized), only some (partial) or just SKILL.md.
	BundleSHA256      map[string]string `json:"bundle_sha256,omitempty" bson:"bundle_sha256,omitempty"`
	CompanionCoverage string            `json:"companion_coverage,omitempty" bson:"companion_coverage,omitempty"`
}

func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func validComparisonPlan(p *SkillComparisonPlan) bool {
	return p == nil || ((p.BaselineVersion == "" || validRunLabel(p.BaselineVersion, 256)) && sourceHashPattern.MatchString(p.DatasetSHA256) && sourceHashPattern.MatchString(p.EnvironmentSHA256) && finiteNonnegative(p.MinScoreGain) && p.MinScoreGain > 0 && p.MinScoreGain <= 1 && finiteNonnegative(p.MaxCost) && p.MaxDurationMS > 0)
}
func validComparisonReport(r *SkillComparisonReport) bool {
	if r == nil {
		return true
	}
	if r.CompanionCoverage == "" {
		if r.BundleSHA256 != nil {
			return false
		}
	} else if !slices.Contains([]string{"body_only", "materialized", "partial"}, r.CompanionCoverage) || len(r.BundleSHA256) != 2 {
		return false
	}
	for variant, hash := range r.BundleSHA256 {
		if variant != "baseline" && variant != "candidate" || hash != "" && !sourceHashPattern.MatchString(hash) {
			return false
		}
	}
	return sourceHashPattern.MatchString(r.PlanSHA256) && finiteNonnegative(r.BaselineScore) && r.BaselineScore <= 1 && finiteNonnegative(r.CandidateScore) && r.CandidateScore <= 1 && finiteNonnegative(r.CandidateCost) && r.CandidateDurationMS >= 0 && finiteNonnegative(r.BaselineCost) && r.BaselineDurationMS >= 0
}

// A body-only measurement cannot vouch for changed companion files.
func comparisonCoversBundle(r Record, v *SkillComparisonReport) bool {
	candidate := v.BundleSHA256["candidate"]
	return candidate == r.SkillBundleSHA256 && (candidate == v.BundleSHA256["baseline"] || v.CompanionCoverage == "materialized")
}
func comparisonPasses(r, evaluation Record) bool {
	p := r.ComparisonPlan
	if p == nil {
		return true
	}
	if evaluation.Evaluation == nil {
		return false
	}
	v := evaluation.Evaluation.ComparisonReport
	return validComparisonPlan(p) && v != nil && validComparisonReport(v) && v.PlanSHA256 == ContentHash(structuredText(p)) && comparisonCoversBundle(r, v) && v.CandidateScore-v.BaselineScore+1e-12 >= p.MinScoreGain && v.CandidateCost <= p.MaxCost && v.CandidateDurationMS <= p.MaxDurationMS && v.RegressionPassed && v.SafetyPassed
}

// Required numeric zeroes must be explicit; omitted measurements are not zero.
func decodeComparison(data []byte, out any, fields []string) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return ErrInvalid
	}
	for _, name := range fields {
		if value, ok := raw[name]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrInvalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}
func (p *SkillComparisonPlan) UnmarshalJSON(data []byte) error {
	type plain SkillComparisonPlan
	return decodeComparison(data, (*plain)(p), []string{"baseline_version", "dataset_sha256", "environment_sha256", "min_score_gain", "max_cost", "max_duration_ms"})
}
func (p *SkillComparisonReport) UnmarshalJSON(data []byte) error {
	type plain SkillComparisonReport
	return decodeComparison(data, (*plain)(p), []string{"plan_sha256", "baseline_score", "candidate_score", "candidate_cost", "candidate_duration_ms", "regression_passed", "safety_passed"})
}

func comparisonPlanHash(p *SkillComparisonPlan) string {
	if p == nil {
		return ""
	}
	return ContentHash(structuredText(p))
}
