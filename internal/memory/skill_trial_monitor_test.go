package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func (f *fakeRepo) SkillRegressionSourceIDs(ctx context.Context, scope Scope, version string, since time.Time) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().SkillRegressionSourceIDs(ctx, scope, version, since)
}
func (x fakeTx) SkillRegressionSourceIDs(_ context.Context, scope Scope, version string, since time.Time) ([]string, error) {
	out := []string{}
	for _, r := range x.store["sources"] {
		var u SkillUsage
		if r.Scope == scope && json.Unmarshal([]byte(r.Text), &u) == nil && u.VersionID == version && !u.StartedAt.Before(since) {
			out = append(out, r.ID)
		}
	}
	return out, nil
}
func TestSkillTrialMonitorDisablesFailedPromotionWithoutInventingBaseline(t *testing.T) {
	f, candidate, trial, in := skillAutomationFixture(t)
	if _, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false); e != nil {
		t.Fatal(e)
	}
	current, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, candidate.ID)
	src, _ := f.repo.Get(testCtx, "sources", scopeA, in.TrialEvidence[0].ID)
	var u SkillUsage
	if e := json.Unmarshal([]byte(src.Text), &u); e != nil {
		t.Fatal(e)
	}
	u.TrialID = ""
	u.StartedAt = current.UpdatedAt.Add(time.Millisecond)
	u.CompletedAt = u.StartedAt
	u.Status = "failure"
	u.RuntimeErrors = 1
	time.Sleep(2 * time.Millisecond)
	src.Text = structuredText(u)
	prev := src.Revision
	src.Revision++
	if e := f.repo.Replace(testCtx, "sources", src, prev); e != nil {
		t.Fatal(e)
	}
	got, e := f.svc.MonitorSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil || got.State != "recovery_unavailable" || len(got.Evidence) != 1 {
		t.Fatal(got, e)
	}
	current, _ = f.repo.Get(testCtx, colSkillVersions, scopeA, candidate.ID)
	if live(current) || current.Status != "rejected" {
		t.Fatal("failed candidate still live")
	}
	tr, _ := f.repo.Get(testCtx, colSkillTrials, scopeA, trial.ID)
	if tr.Text == "" {
		t.Fatal("lost canonical failure evidence")
	}
	got, e = f.svc.MonitorSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil || got.State != "recovery_unavailable" {
		t.Fatal("recovery replay", got, e)
	}
}

func TestPostgresSkillAutomationRegressionQuerySeesIndependentNegativeOutcome(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	f, _, trial, in := skillAutomationFixture(t, f)
	item := in.ScopedTrialEvidence[0]
	f.auth.actors["checker"] = actor("negative-checker", "service", true, allVerbs...)
	if _, e := f.svc.RecordSkillTrialOutcome(testCtx, "checker", SkillTrialOutcomeInput{Scope: item.Scope, TrialScope: scopeA, TrialID: trial.ID, EventID: "negative-query", Source: item.Source, Evidence: []Ref{item.Source}, CheckerSHA256: ContentHash("fixed checker"), Passed: false}); e != nil {
		t.Fatal(e)
	}
	ids, e := f.svc.Repo.(*Postgres).SkillRegressionSourceIDs(testCtx, item.Scope, trial.SkillTrial.Candidate.ID, time.Time{})
	if e != nil || len(ids) != 1 || ids[0] != item.Source.ID {
		t.Fatal("negative source query", ids, e)
	}
}

func TestSkillTrialMonitorIndependentFailureAndAtomicAudit(t *testing.T) {
	for _, mode := range []string{"independent", "model_self", "audit_failure"} {
		t.Run(mode, func(t *testing.T) {
			f, candidate, trial, in := skillAutomationFixture(t)
			if _, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false); e != nil {
				t.Fatal(e)
			}
			current, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, candidate.ID)
			src, _ := f.repo.Get(testCtx, "sources", scopeA, in.TrialEvidence[0].ID)
			var u SkillUsage
			json.Unmarshal([]byte(src.Text), &u)
			u.TrialID = ""
			u.StartedAt = current.UpdatedAt.Add(time.Millisecond)
			u.CompletedAt = u.StartedAt
			time.Sleep(2 * time.Millisecond)
			prev := src.Revision
			src.Revision++
			src.Text = structuredText(u)
			if e := f.repo.Replace(testCtx, "sources", src, prev); e != nil {
				t.Fatal(e)
			}
			target := Ref{Collection: "sources", ID: src.ID, Revision: src.Revision}
			passed := false
			kind := "user_rating"
			if mode == "model_self" {
				kind = "model_self"
			}
			if e := f.repo.Insert(testCtx, "evaluations", Record{ID: "negative-monitor-eval", Scope: scopeA, Status: "active", Revision: 1, CreatedBy: "independent-checker", CreatedByType: roleHuman, Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: kind, Passed: &passed}}); e != nil {
				t.Fatal(e)
			}
			f.repo.failAudit = mode == "audit_failure"
			got, e := f.svc.MonitorSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
			current, _ = f.repo.Get(testCtx, colSkillVersions, scopeA, candidate.ID)
			if mode == "audit_failure" {
				if e == nil || current.Status != "active" {
					t.Fatal("audit did not rollback", e, current.Status)
				}
				return
			}
			if mode == "model_self" {
				if e != nil || got.State != "monitoring" || current.Status != "active" {
					t.Fatal("model self triggered rejection", got, e)
				}
				return
			}
			if e != nil || got.State != "recovery_unavailable" || len(got.Evidence) != 1 || !got.Evidence[0].IndependentlyVerified || current.Status != "rejected" {
				t.Fatal(got, e, current.Status)
			}
			src.Status = "deleted"
			if e = f.repo.Replace(testCtx, "sources", src, src.Revision); e != nil {
				t.Fatal(e)
			}
			if _, e = f.svc.MonitorSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID}); e == nil {
				t.Fatal("recovery reused deleted failure evidence")
			}
		})
	}
}

func TestSkillTrialMonitorRestoresOnlyValidatedBaseline(t *testing.T) {
	f, candidate, trial, in := skillAutomationFixture(t)
	if _, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false); e != nil {
		t.Fatal(e)
	}
	baseline := candidate
	baseline.ID = "validated-baseline"
	baseline.Kind = importedSkillBaselineKind
	baseline.SkillBasis = nil
	baseline.Relations = nil
	baseline.Status = "superseded"
	baseline.Revision = 1
	baseline.ComparisonPlan = nil
	baseline.ValidationID = ""
	baseline.CreatedBy = "baseline-author"
	if e := f.repo.Insert(testCtx, colSkillVersions, baseline); e != nil {
		t.Fatal(e)
	}
	target := Ref{Collection: colSkillVersions, ID: baseline.ID, Revision: 1}
	passed := true
	ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "Independent baseline validation", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
	baseline.ValidationID = ev.ID
	if e := f.repo.Replace(testCtx, colSkillVersions, baseline, baseline.Revision); e != nil {
		t.Fatal(e)
	}
	trial.SkillTrial.BaselineVersion = baseline.ID
	if e := f.repo.Replace(testCtx, colSkillTrials, trial, trial.Revision); e != nil {
		t.Fatal(e)
	}
	current, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, candidate.ID)
	src, _ := f.repo.Get(testCtx, "sources", scopeA, in.TrialEvidence[0].ID)
	var usage SkillUsage
	json.Unmarshal([]byte(src.Text), &usage)
	usage.TrialID = ""
	usage.Status = "failure"
	usage.StartedAt = current.UpdatedAt.Add(time.Millisecond)
	usage.CompletedAt = usage.StartedAt
	time.Sleep(2 * time.Millisecond)
	src.Text = structuredText(usage)
	if e := f.repo.Replace(testCtx, "sources", src, src.Revision); e != nil {
		t.Fatal(e)
	}
	got, e := f.svc.MonitorSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil || got.State != "rolled_back" {
		parent, _ := f.repo.Get(testCtx, colSkills, scopeA, candidate.SkillID)
		_, retryErr := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: parent.Revision, VersionID: baseline.ID}, true)
		t.Fatal(got, e, retryErr)
	}
	parent, _ := f.repo.Get(testCtx, colSkills, scopeA, candidate.SkillID)
	if parent.ActiveVersion != baseline.ID {
		t.Fatal("baseline not restored")
	}
	view, e := f.svc.ObserveSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil || view.State != "rolled_back" {
		t.Fatal("terminal recovery observation", view, e)
	}
	lease, e := f.svc.ClaimSkillTrialCheck(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.svc.FinishSkillTrialCheck(testCtx, "learner", trial.ID, SkillTrialCheckFinish{Scope: scopeA, SkillID: candidate.SkillID, ExpectedRevision: lease.Revision, LeaseToken: lease.LeaseToken, Outcome: "checked"}); e != nil {
		t.Fatal(e)
	}
	job, _ := f.repo.Get(testCtx, colJobs, scopeA, lease.ID)
	if job.Status != JobStatusDone {
		t.Fatal("recovered job not retired", job.Status)
	}

}
