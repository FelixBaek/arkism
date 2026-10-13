package memory

import (
	"context"
)

// Lifecycle transitions may advance Version.Revision without changing content.
// The exact body and comparison evaluation remain independently bound.
type SkillBasis struct {
	Version       Ref    `json:"version" bson:"version"`
	ContentSHA256 string `json:"content_sha256" bson:"content_sha256"`
	Validation    *Ref   `json:"validation,omitempty" bson:"validation,omitempty"`
}

func checkSkillBasis(ctx context.Context, tx Tx, r Record) error {
	if r.SkillBasis == nil {
		return nil
	}
	if r.Kind != proposedSkillKind && r.Kind != proposedSkillSourceKind || r.SkillID == "" {
		return ErrInvalid
	}
	if r.Kind == proposedSkillKind && (r.ComparisonPlan == nil || r.ComparisonPlan.BaselineVersion != r.SkillBasis.Version.ID) {
		return ErrConflict
	}
	seen := map[string]bool{r.ID: true}
	for basis := r.SkillBasis; basis != nil; {
		if err := ctx.Err(); err != nil {
			return err
		}
		ref := basis.Version
		if len(seen) >= 64 || ref.Collection != colSkillVersions || !validRunLabel(ref.ID, 256) || ref.Revision < 1 || !sourceHashPattern.MatchString(basis.ContentSHA256) || seen[ref.ID] {
			return ErrConflict
		}
		seen[ref.ID] = true
		baseline, err := tx.Get(ctx, colSkillVersions, r.Scope, ref.ID)
		if err != nil {
			return err
		}
		if baseline.Scope != r.Scope || baseline.SkillID != r.SkillID || baseline.Revision < ref.Revision || baseline.ContentErased || baseline.ConflictID != "" || baseline.Status != "active" && baseline.Status != "superseded" || !validAnalysis(baseline.Analysis) || baseline.Analysis.ContentHash != basis.ContentSHA256 {
			return ErrConflict
		}
		// Superseded only means a newer version is selected; it does not revoke
		// the evidence. All temporal and privacy constraints still apply.
		visible := baseline
		visible.Status = "active"
		if !live(visible) {
			return ErrConflict
		}
		if err = checkSources(ctx, tx, baseline); err != nil {
			return err
		}
		if err = checkDirectRelations(ctx, tx, baseline); err != nil {
			return err
		}
		if basis.Validation == nil {
			if baseline.ComparisonPlan != nil || baseline.Kind != importedSkillBaselineKind || baseline.CreatedByType != roleHuman || baseline.ApprovedBy == "" {
				return ErrDenied
			}
		} else {
			v := *basis.Validation
			if v.Collection != "evaluations" || v.ID == "" || v.ID != baseline.ValidationID || v.Revision < 1 {
				return ErrConflict
			}
			evaluation, e := tx.Get(ctx, v.Collection, r.Scope, v.ID)
			if e != nil {
				return e
			}
			if evaluation.Revision != v.Revision {
				return ErrConflict
			}
			if e = validation(ctx, tx, baseline, v.ID, false); e != nil {
				return e
			}
		}
		if baseline.SkillBasis != nil && (baseline.Kind != proposedSkillKind || baseline.ComparisonPlan == nil || baseline.ComparisonPlan.BaselineVersion != baseline.SkillBasis.Version.ID) {
			return ErrConflict
		}
		basis = baseline.SkillBasis
	}
	return nil
}
