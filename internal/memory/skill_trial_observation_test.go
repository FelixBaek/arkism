package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func (f *fakeRepo) SkillTrialSourceIDs(ctx context.Context, scope Scope, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().SkillTrialSourceIDs(ctx, scope, id)
}
func TestSkillTrialObservationRequiresIndependentOutcomes(t *testing.T) {
	f, candidate, trial, _ := skillAutomationFixture(t)
	out, e := f.svc.ObserveSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil || out.State != "awaiting_independent_evaluation" || len(out.Items) != 2 {
		t.Fatal(out, e)
	}
	for _, item := range out.Items {
		if item.IndependentlyVerified || item.Outcome != "success" {
			t.Fatal("runtime success became verified", item)
		}
	}
}
func TestSkillTrialObservationStopsRegressionAtomically(t *testing.T) {
	for _, auditFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "rollback"}[auditFailure], func(t *testing.T) {
			f, candidate, trial, in := skillAutomationFixture(t)
			source, e := f.svc.Repo.Get(testCtx, "sources", scopeA, in.TrialEvidence[0].ID)
			if e != nil {
				t.Fatal(e)
			}
			var usage SkillUsage
			json.Unmarshal([]byte(source.Text), &usage)
			usage.Status = "failure"
			raw, _ := json.Marshal(usage)
			source.Text = string(raw)
			if e = f.svc.Repo.Replace(testCtx, "sources", source, source.Revision); e != nil {
				t.Fatal(e)
			}
			f.repo.failAudit = auditFailure
			out, e := f.svc.ObserveSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
			current, _ := f.repo.Get(testCtx, colSkillTrials, scopeA, trial.ID)
			if auditFailure {
				if !errors.Is(e, ErrUnavailable) || current.Status != "active" {
					t.Fatal("rollback", out, e, current.Status)
				}
			} else if e != nil || out.State != "stopped_regression" || current.Status != "stopped" {
				t.Fatal("stop", out, e, current.Status)
			}
			job, e := f.repo.Get(testCtx, colJobs, scopeA, skillTrialCheckID(scopeA, trial.ID))
			if e != nil || auditFailure == (job.Status == JobStatusDone) {
				t.Fatal("trial check job", job.Status, e)
			}
			parent, _ := f.repo.Get(testCtx, colSkills, scopeA, candidate.SkillID)
			if parent.ActiveVersion != "" {
				t.Fatal("baseline changed")
			}
		})
	}
}
