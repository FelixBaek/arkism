package memory

import (
	"context"
	"time"
)

// skillTrialTerminal recognizes only canonical completion. An elapsed deadline
// is absence of further observation, never evidence that a trial succeeded.
func skillTrialTerminal(ctx context.Context, tx Tx, trial Record) (string, error) {
	if trial.SkillTrial == nil || trial.ContentErased || trial.PrivacyQuarantined {
		return "", ErrNotFound
	}
	if trial.Status != "active" {
		return trial.Status, nil
	}
	parent, err := tx.Get(ctx, colSkills, trial.Scope, trial.SkillID)
	if err != nil {
		return "", err
	}
	if !live(parent) || parent.ContentErased || parent.PrivacyQuarantined {
		return "", ErrConflict
	}
	candidate, err := tx.Get(ctx, colSkillVersions, trial.Scope, trial.SkillTrial.Candidate.ID)
	if err != nil {
		return "", err
	}
	if (candidate.Status == "rejected" || candidate.Status == "superseded") && candidate.RequestHash == ContentHash("trial-regression/"+trial.ID) {
		if parent.ActiveVersion == trial.SkillTrial.BaselineVersion {
			return "rolled_back", nil
		}
		if parent.ActiveVersion == candidate.ID {
			return "recovery_pending", nil
		}
	}
	if parent.ActiveVersion == trial.SkillTrial.Candidate.ID {
		candidate, err := tx.Get(ctx, colSkillVersions, trial.Scope, parent.ActiveVersion)
		if err != nil {
			return "", err
		}
		if candidate.SkillID != trial.SkillID || candidate.Revision != trial.SkillTrial.Candidate.Revision+1 {
			return "", ErrConflict
		}
		if err = checkActiveSkill(ctx, tx, candidate); err != nil {
			return "", err
		}
		if err = checkSources(ctx, tx, parent); err != nil {
			return "", err
		}
		return "promoted", nil
	}
	if !trial.SkillTrial.EndsAt.After(time.Now()) {
		return "expired", nil
	}
	return "", nil
}
