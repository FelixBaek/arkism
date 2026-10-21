package memory

import (
	"errors"
	"testing"
	"time"
)

func TestSkillTrialSchedulerLeaseAndRecovery(t *testing.T) {
	f, candidate, trial, _ := skillAutomationFixture(t)
	input := SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID}
	first, e := f.svc.ClaimSkillTrialCheck(testCtx, testPrincipalToken("learner"), trial.ID, input)
	if e != nil || first.LeaseToken == "" {
		t.Fatal(first, e)
	}
	if _, e = f.svc.ClaimSkillTrialCheck(testCtx, testPrincipalToken("learner"), trial.ID, input); !errors.Is(e, ErrNotFound) {
		t.Fatal("double lease", e)
	}
	job, _ := f.repo.Get(testCtx, colJobs, scopeA, first.ID)
	job.LeaseUntil = time.Now().Add(-time.Second)
	if e = f.repo.Replace(testCtx, colJobs, job, job.Revision); e != nil {
		t.Fatal(e)
	}
	next, e := f.svc.ClaimSkillTrialCheck(testCtx, testPrincipalToken("learner"), trial.ID, input)
	if e != nil || next.LeaseToken == first.LeaseToken {
		t.Fatal("expired reclaim", e)
	}
	finish := SkillTrialCheckFinish{Scope: scopeA, SkillID: candidate.SkillID, ExpectedRevision: first.Revision, LeaseToken: first.LeaseToken, Outcome: "checked"}
	if e = f.svc.FinishSkillTrialCheck(testCtx, testPrincipalToken("learner"), trial.ID, finish); !errors.Is(e, ErrConflict) {
		t.Fatal("stale lease", e)
	}
	finish.ExpectedRevision = next.Revision
	finish.LeaseToken = next.LeaseToken
	if e = f.svc.FinishSkillTrialCheck(testCtx, testPrincipalToken("learner"), trial.ID, finish); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.FinishSkillTrialCheck(testCtx, testPrincipalToken("learner"), trial.ID, finish); e != nil {
		t.Fatal("finish retry", e)
	}
	job, _ = f.repo.Get(testCtx, colJobs, scopeA, first.ID)
	if job.Status != JobStatusRetry || job.Attempts != 0 || job.LeaseToken != "" {
		t.Fatal("reschedule", job.Status, job.Attempts)
	}
}
