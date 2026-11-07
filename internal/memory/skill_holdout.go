package memory

import (
	"context"
	"encoding/json"
)

// These manifests declare task identity; hashes cannot prove that developers
// disclosed every task, or that two different tasks are semantically independent.
// The independent comparison recorder remains responsible for that provenance.
type SkillHoldoutPlan struct {
	DevelopmentTasks []string           `json:"development_tasks" bson:"development_tasks"`
	Tasks            []SkillHoldoutTask `json:"tasks" bson:"tasks"`
	MinCategoryScore float64            `json:"min_category_score" bson:"min_category_score"`
	MinCategoryCases int                `json:"min_category_cases" bson:"min_category_cases"`
}
type SkillHoldoutTask struct {
	CaseSHA256 string `json:"case_sha256" bson:"case_sha256"`
	Category   string `json:"category" bson:"category"`
}

func validSkillHoldout(p *SkillHoldoutPlan) bool {
	if p == nil {
		return true
	}
	if len(p.DevelopmentTasks) == 0 || len(p.DevelopmentTasks) > 128 || len(p.Tasks) == 0 || len(p.Tasks) > 62 || !finiteNonnegative(p.MinCategoryScore) || p.MinCategoryScore < .9 || p.MinCategoryScore > 1 || p.MinCategoryCases < 1 || p.MinCategoryCases > 62 {
		return false
	}
	seen := map[string]bool{}
	for _, h := range p.DevelopmentTasks {
		if !sourceHashPattern.MatchString(h) || seen[h] {
			return false
		}
		seen[h] = true
	}
	counts := map[string]int{}
	for _, task := range p.Tasks {
		if !sourceHashPattern.MatchString(task.CaseSHA256) || seen[task.CaseSHA256] || !validRunLabel(task.Category, 64) {
			return false
		}
		seen[task.CaseSHA256] = true
		counts[task.Category]++
	}
	for _, count := range counts {
		if count < p.MinCategoryCases {
			return false
		}
	}
	return true
}

// Every declared holdout is paired and scored. Development tasks cannot appear
// even under a different quality/regression/safety label. Extra quality tasks
// cannot inflate the aggregate and replace missing holdout evidence.
func skillHoldoutPasses(p *SkillHoldoutPlan, rows []SkillComparisonCase) (bool, error) {
	if p == nil {
		return true, nil
	}
	if !validSkillHoldout(p) {
		return false, ErrInvalid
	}
	development := map[string]bool{}
	tasks := map[string]string{}
	for _, h := range p.DevelopmentTasks {
		development[h] = true
	}
	for _, task := range p.Tasks {
		tasks[task.CaseSHA256] = task.Category
	}
	seen := map[string]map[string]bool{}
	counts := map[string]int{}
	passed := map[string]int{}
	for _, row := range rows {
		if development[row.CaseSHA256] {
			return false, ErrInvalid
		}
		category, declared := tasks[row.CaseSHA256]
		if declared != (row.Category == "quality") {
			return false, ErrInvalid
		}
		if !declared {
			continue
		}
		if row.Variant != "baseline" && row.Variant != "candidate" {
			return false, ErrInvalid
		}
		if seen[row.CaseSHA256] == nil {
			seen[row.CaseSHA256] = map[string]bool{}
		}
		if seen[row.CaseSHA256][row.Variant] {
			return false, ErrInvalid
		}
		seen[row.CaseSHA256][row.Variant] = true
		if row.Variant == "candidate" {
			counts[category]++
			if row.Passed {
				passed[category]++
			}
		}
	}
	for h := range tasks {
		if !seen[h]["baseline"] || !seen[h]["candidate"] {
			return false, ErrInvalid
		}
	}
	for category, count := range counts {
		if float64(passed[category])/float64(count)+1e-12 < p.MinCategoryScore {
			return false, nil
		}
	}
	return true, nil
}

// A normal evaluation with invented aggregate scores cannot satisfy holdout
// approval. Re-read the exact recorded execution source and recompute its cases.
func checkSkillHoldoutEvidence(ctx context.Context, tx Tx, r, v Record) error {
	if r.ComparisonPlan == nil || r.ComparisonPlan.Holdout == nil {
		return nil
	}
	if r.Analysis == nil || v.Target == nil || v.Evaluation == nil {
		return ErrDenied
	}
	for _, ref := range v.Sources {
		source, e := tx.Get(ctx, "sources", r.Scope, ref.SourceID)
		if e != nil {
			return e
		}
		if source.Kind != "skill_comparison_result" {
			continue
		}
		if !live(source) || source.ContentErased || source.PrivacyQuarantined || source.CreatedBy != v.CreatedBy || source.CreatedBy == r.CreatedBy || (source.CreatedByType != "service" && source.CreatedByType != "pipeline") || source.ContentHash != ContentHash(source.Text) {
			return ErrDenied
		}
		var result SkillComparisonResult
		if json.Unmarshal([]byte(source.Text), &result) != nil {
			var envelope struct {
				TrialID string                `json:"trial_id"`
				Result  SkillComparisonResult `json:"result"`
			}
			if json.Unmarshal([]byte(source.Text), &envelope) != nil || envelope.TrialID == "" {
				return ErrDenied
			}
			result = envelope.Result
		}
		if result.Candidate != *v.Target || result.Report == nil || result.Report.PlanSHA256 != comparisonPlanHash(r.ComparisonPlan) || result.DatasetSHA256 != r.ComparisonPlan.DatasetSHA256 || result.EnvironmentSHA256 != r.ComparisonPlan.EnvironmentSHA256 || result.ContentSHA256["candidate"] != r.Analysis.ContentHash {
			return ErrDenied
		}
		_, measured, e := comparisonMeasurements(result, r.Scope)
		if e != nil {
			return e
		}
		if structuredText(measured) != structuredText(v.Evaluation.ComparisonReport) {
			return ErrDenied
		}
		passed, e := skillHoldoutPasses(r.ComparisonPlan.Holdout, result.Cases)
		if e != nil {
			return e
		}
		if !passed {
			return ErrDenied
		}
		return nil
	}
	return ErrDenied
}
