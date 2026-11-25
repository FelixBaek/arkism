package memory

// The owner can review a distinct qualification contract together with the
// proposed improvement. The immutable parent comparison hash covers both.
type SkillRollbackQualificationPlan struct {
	DatasetSHA256     string             `json:"dataset_sha256" bson:"dataset_sha256"`
	EnvironmentSHA256 string             `json:"environment_sha256" bson:"environment_sha256"`
	MaxCost           float64            `json:"max_cost" bson:"max_cost"`
	MaxDurationMS     int64              `json:"max_duration_ms" bson:"max_duration_ms"`
	Tasks             []SkillHoldoutTask `json:"tasks" bson:"tasks"`
}

func validRollbackQualificationPlan(p *SkillComparisonPlan) bool {
	q := p.RollbackQualification
	if q == nil {
		return true
	}
	if p.BaselineVersion == "" || !sourceHashPattern.MatchString(q.DatasetSHA256) || q.DatasetSHA256 == p.DatasetSHA256 || !sourceHashPattern.MatchString(q.EnvironmentSHA256) || !finiteNonnegative(q.MaxCost) || q.MaxCost > p.MaxCost || q.MaxDurationMS <= 0 || q.MaxDurationMS > p.MaxDurationMS || len(q.Tasks) < 3 || len(q.Tasks) > 64 {
		return false
	}
	categories := map[string]bool{}
	hashes := map[string]bool{}
	for _, task := range q.Tasks {
		if !sourceHashPattern.MatchString(task.CaseSHA256) || hashes[task.CaseSHA256] {
			return false
		}
		switch task.Category {
		case "quality", "regression", "safety":
		default:
			return false
		}
		categories[task.Category] = true
		hashes[task.CaseSHA256] = true
	}
	return len(categories) == 3
}

func rollbackMeasurementPlan(p *SkillComparisonPlan, rows []SkillComparisonCase) (*SkillComparisonPlan, error) {
	if !validRollbackQualificationPlan(p) || p.RollbackQualification == nil {
		return nil, ErrInvalid
	}
	q := p.RollbackQualification
	want := map[string]string{}
	for _, task := range q.Tasks {
		want[task.CaseSHA256] = task.Category
	}
	// comparisonMeasurements already checked the pairs. Verify their exact membership
	// against the separately registered task hashes before accepting this result.
	for _, row := range rows {
		if row.Variant == "baseline" {
			if want[row.CaseSHA256] != row.Category {
				return nil, ErrConflict
			}
			delete(want, row.CaseSHA256)
		}
	}
	if len(want) != 0 {
		return nil, ErrConflict
	}
	selected := *p
	selected.DatasetSHA256, selected.EnvironmentSHA256 = q.DatasetSHA256, q.EnvironmentSHA256
	selected.MaxCost, selected.MaxDurationMS = q.MaxCost, q.MaxDurationMS
	selected.Holdout = nil
	return &selected, nil
}

func (p *SkillRollbackQualificationPlan) UnmarshalJSON(data []byte) error {
	type plain SkillRollbackQualificationPlan
	return decodeComparison(data, (*plain)(p), []string{"dataset_sha256", "environment_sha256", "max_cost", "max_duration_ms", "tasks"})
}
