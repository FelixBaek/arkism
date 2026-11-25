package memory

import "context"

const baselineQualificationKind = "measured_baseline_qualification"

// The comparison recorder already verified all paired cases, their fixed plan,
// hashes and scope. Score the baseline independently of the candidate.
func recordBaselineQualification(ctx context.Context, tx Tx, in RecordSkillComparisonInput, base Record, plan *SkillComparisonPlan, measured SkillComparisonReport, paired Record, ref, run Ref) error {
	if base.Status != "active" || base.Kind != importedSkillBaselineKind || base.ID != in.Result.BaselineID || base.Revision != in.Result.BaselineRevision || base.CreatedBy == paired.CreatedBy || base.ApprovedBy == paired.CreatedBy {
		return ErrConflict
	}
	if err := tx.Replace(ctx, colSkillVersions, base, base.Revision); err != nil {
		return err
	}
	passed := measured.BaselineCost <= plan.MaxCost && measured.BaselineDurationMS <= plan.MaxDurationMS && (base.SkillBundleSHA256 == "" || measured.CompanionCoverage == "materialized")
	for _, row := range in.Result.Cases {
		if row.Variant == "baseline" && !row.Passed {
			passed = false
		}
	}
	target := Ref{Collection: colSkillVersions, ID: base.ID, Revision: base.Revision}
	record := Record{ID: ref.ID, Scope: in.Scope, Revision: 1, Status: statusActive, Kind: baselineQualificationKind, Text: "Fixed comparison baseline qualification", Target: &target, Passed: passed, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}, Relations: []Ref{run}, Sources: paired.Sources, Sensitivity: paired.Sensitivity, CreatedAt: paired.CreatedAt, UpdatedAt: paired.UpdatedAt, CreatedBy: paired.CreatedBy, CreatedByType: paired.CreatedByType}
	return insertSourceRecord(ctx, tx, "evaluations", record)
}
