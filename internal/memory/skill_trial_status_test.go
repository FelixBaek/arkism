package memory

import (
	"errors"
	"testing"
	"time"
)

func TestSkillTrialStatusReadOnlyDoesNotClaimOutcomes(t *testing.T) {
	f, candidate, trial, _ := skillAutomationFixture(t)
	before := structuredText(f.repo.store)
	status, err := f.svc.ReadSkillTrialStatus(testCtx, f.human, trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if err != nil || status.State != "active" || status.ObservationState != "not_available" || status.Authority != AuthorityDataOnly {
		t.Fatal(status, err)
	}
	if before != structuredText(f.repo.store) {
		t.Fatal("read endpoint mutated state")
	}
	if status.Trial.ID != trial.ID || status.Candidate.ID != candidate.ID {
		t.Fatal("wrong target", status)
	}
	for _, in := range []SkillTrialReadInput{{Scope: scopeB, SkillID: candidate.SkillID}, {Scope: scopeA, SkillID: "different"}} {
		if _, err = f.svc.ReadSkillTrialStatus(testCtx, f.human, trial.ID, in); err == nil {
			t.Fatal("foreign target accepted")
		}
	}
	if _, err = f.svc.ReadSkillTrialStatus(testCtx, "unknown", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID}); !errors.Is(err, ErrDenied) {
		t.Fatal("missing grant accepted", err)
	}
}
func TestSkillTrialStatusCanonicalTerminalStates(t *testing.T) {
	for _, state := range []string{"promoted", "expired", "stopped", "stopped_regression", "recovery_pending", "rolled_back"} {
		t.Run(state, func(t *testing.T) {
			f, candidate, trial, in := skillAutomationFixture(t)
			switch state {
			case "promoted":
				if _, err := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false); err != nil {
					t.Fatal(err)
				}
			case "expired":
				trial.SkillTrial.EndsAt = time.Now().Add(-time.Second)
			case "stopped", "stopped_regression":
				trial.Status = "stopped"
				if state == "stopped_regression" {
					trial.VerificationStatus = "regression_observed"
				}
			case "recovery_pending", "rolled_back":
				candidate.Status = "rejected"
				candidate.RequestHash = ContentHash("trial-regression/" + trial.ID)
				f.repo.store[colSkillVersions][recKey(scopeA, candidate.ID)] = candidate
				parent := f.repo.store[colSkills][recKey(scopeA, candidate.SkillID)]
				if state == "recovery_pending" {
					parent.ActiveVersion = candidate.ID
				} else {
					parent.ActiveVersion = trial.SkillTrial.BaselineVersion
				}
				f.repo.store[colSkills][recKey(scopeA, parent.ID)] = parent
			}
			f.repo.store[colSkillTrials][recKey(scopeA, trial.ID)] = trial
			before := structuredText(f.repo.store)
			got, err := f.svc.ReadSkillTrialStatus(testCtx, f.human, trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
			if err != nil || got.State != state {
				t.Fatal(got, err)
			}
			if before != structuredText(f.repo.store) {
				t.Fatal("status inspection mutated canonical records")
			}
		})
	}
}
