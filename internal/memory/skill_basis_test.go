package memory

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func applyGeneratedForBasis(t *testing.T, f *fixture, got ImportSkillResult) (Record, Record) {
	t.Helper()
	passed := true
	target := Ref{Collection: colSkillVersions, ID: got.Record.ID, Revision: got.Record.Revision}
	ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "independent comparison", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed, ComparisonReport: &SkillComparisonReport{PlanSHA256: got.ComparisonPlanSHA256, BaselineScore: .5, CandidateScore: .8, CandidateCost: 1, CandidateDurationMS: 500, RegressionPassed: true, SafetyPassed: true}}})
	approved, err := f.svc.Transition(testCtx, f.human, colSkillVersions, got.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Record.Revision, Status: "approved", ValidationID: ev.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplySkill(testCtx, f.human, got.Record.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Skill.Revision, VersionID: approved.ID}, false); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, got.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r, ev
}

func skillBasisChain(t *testing.T, f *fixture) ([]GenerateSkillInput, []Record, []Record) {
	t.Helper()
	var requests []GenerateSkillInput
	var versions, evaluations []Record
	baseline := ""
	for i := 0; i < 3; i++ {
		in, _ := generationFixture(t, f)
		in.EventID = fmt.Sprintf("generation-%d", i)
		in.ComparisonPlan.BaselineVersion = baseline
		got, err := f.svc.GenerateSkill(testCtx, f.agent, in)
		if err != nil {
			t.Fatal("generation", i, err)
		}
		version, ev := applyGeneratedForBasis(t, f, got)
		requests, versions, evaluations = append(requests, in), append(versions, version), append(evaluations, ev)
		baseline = version.ID
	}
	return requests, versions, evaluations
}

func TestSkillBasisWithdrawal(t *testing.T) {
	for _, kind := range []string{"lesson", "evaluation", "evaluation_revision"} {
		t.Run(kind, func(t *testing.T) {
			checkSkillBasisWithdrawal(t, newFixture(t), kind)
		})
	}
}
func TestPostgresSkillBasisWithdrawal(t *testing.T) {
	for _, kind := range []string{"lesson", "evaluation", "evaluation_revision"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = isolatedPostgres(t)
			checkSkillBasisWithdrawal(t, f, kind)
		})
	}
}
func checkSkillBasisWithdrawal(t *testing.T, f *fixture, kind string) {
	t.Helper()
	requests, versions, evaluations := skillBasisChain(t, f)
	latest := versions[2]
	if err := checkActiveSkill(testCtx, f.svc.Repo, latest); err != nil {
		t.Fatal("normal supersession blocked", err)
	}
	if kind == "lesson" {
		ref := requests[0].Lessons[0]
		if _, err := f.svc.Transition(testCtx, f.human, ref.Collection, ref.ID, ChangeInput{Scope: scopeA, ExpectedRevision: ref.Revision, Status: "rejected"}); err != nil {
			t.Fatal(err)
		}
	} else if kind == "evaluation_revision" {
		ev, err := f.svc.Repo.Get(testCtx, "evaluations", scopeA, evaluations[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		ev.Revision++
		if err := f.svc.Repo.Replace(testCtx, "evaluations", ev, ev.Revision-1); err != nil {
			t.Fatal(err)
		}
	} else {
		ev := evaluations[0]
		if err := f.svc.Delete(testCtx, f.human, "evaluations", ev.ID, ChangeInput{Scope: scopeA, ExpectedRevision: ev.Revision}); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkActiveSkill(testCtx, f.svc.Repo, latest); err == nil {
		t.Fatal("ancestor withdrawal did not block active descendant")
	}
	if _, err := f.svc.GenerateSkill(testCtx, f.agent, requests[2]); err == nil {
		t.Fatal("receipt bypassed ancestor withdrawal")
	}
	if _, err := f.svc.Get(testCtx, f.human, "sources", scopeA, latest.Sources[0].SourceID); err == nil {
		t.Fatal("raw generated content bypassed ancestor withdrawal")
	}
	resolved, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{latest.SkillID + "@latest-approved"}})
	if err == nil && len(resolved.Items) == 1 && resolved.Items[0].Instructions != "" {
		t.Fatal("router exposed descendant")
	}
}

func TestSkillBasisGuards(t *testing.T) {
	f := newFixture(t)
	_, versions, _ := skillBasisChain(t, f)
	latest := versions[2]
	if latest.SkillBasis == nil || latest.SkillBasis.Validation == nil {
		t.Fatal("missing exact basis")
	}
	for _, kind := range []string{"cycle", "hash", "revision", "validation", "skill", "scope"} {
		t.Run(kind, func(t *testing.T) {
			changed := latest
			basis := *latest.SkillBasis
			plan := *latest.ComparisonPlan
			changed.SkillBasis, changed.ComparisonPlan = &basis, &plan
			switch kind {
			case "cycle":
				basis.Version.ID, plan.BaselineVersion = latest.ID, latest.ID
			case "hash":
				basis.ContentSHA256 = strings.Repeat("f", 64)
			case "revision":
				basis.Version.Revision += 100
			case "validation":
				basis.Validation = nil
			case "skill":
				changed.SkillID = "foreign-skill"
			case "scope":
				changed.Scope = scopeFar
			}
			if err := checkSkillBasis(testCtx, f.repo, changed); err == nil {
				t.Fatal("invalid basis accepted", kind)
			}
			if kind != "scope" && sameProvenance(latest, changed) {
				t.Fatal("mutable basis", kind)
			}
		})
	}
}

func TestSkillBasisApprovalGate(t *testing.T) {
	f := newFixture(t)
	_, versions, evaluations := skillBasisChain(t, f)
	in, _ := generationFixture(t, f)
	in.EventID, in.ComparisonPlan.BaselineVersion = "fourth", versions[2].ID
	got, err := f.svc.GenerateSkill(testCtx, f.agent, in)
	if err != nil {
		t.Fatal(err)
	}
	passed := true
	target := Ref{Collection: colSkillVersions, ID: got.Record.ID, Revision: got.Record.Revision}
	ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "independent comparison", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed, ComparisonReport: &SkillComparisonReport{PlanSHA256: got.ComparisonPlanSHA256, BaselineScore: .5, CandidateScore: .8, CandidateCost: 1, CandidateDurationMS: 500, RegressionPassed: true, SafetyPassed: true}}})
	old := evaluations[0]
	if err = f.svc.Delete(testCtx, f.human, "evaluations", old.ID, ChangeInput{Scope: scopeA, ExpectedRevision: old.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Transition(testCtx, f.human, colSkillVersions, got.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Record.Revision, Status: "approved", ValidationID: ev.ID}); err == nil {
		t.Fatal("ancestor withdrawal bypassed approval")
	}
}

func TestPostgresSkillBasisDeletion(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	logBasisJobsOnFailure(t, f.svc.Repo.(*Postgres))
	requests, versions, _ := skillBasisChain(t, f)
	// Finish setup's embedding jobs with an embedding principal before handing
	// this shared queue to the deliberately erasure-only worker below.
	for _, job := range pgCollection(t, f.svc.Repo.(*Postgres), colJobs) {
		if job.Kind == "embedding" && job.Status == JobStatusPending {
			if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
				t.Fatal("setup embedding", err)
			}
		}
	}
	original := versions[0].Sources[1].SourceID
	ref := requests[0].Lessons[0]
	if err := f.svc.Delete(testCtx, f.human, ref.Collection, ref.ID, ChangeInput{Scope: scopeA, ExpectedRevision: ref.Revision}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		err := f.svc.WorkOnce(testCtx, erasureWorker(f))
		if errors.Is(err, ErrNotFound) {
			pending := false
			for _, r := range pgCollection(t, f.svc.Repo.(*Postgres), colJobs) {
				if slices.Contains(erasureJobKinds, r.Kind) && r.Status != JobStatusDone {
					pending = true
				}
			}
			if !pending {
				break
			}
			continue // A stale embedding can return not-found with erasure pending.
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	status, err := f.svc.deletionStatus(testCtx, f.human, ref.Collection, scopeA, ref.ID)
	if err != nil || status.DependentCleanup == nil || !status.DependentCleanup.Complete || status.DependentCleanup.ObservedJobs < 3 {
		t.Fatal("multi-generation deletion summary", status, err)
	}
	for _, version := range versions {
		for _, ref := range []Ref{{Collection: colSkillVersions, ID: version.ID}, {Collection: "sources", ID: version.Sources[0].SourceID}} {
			if _, err := f.svc.Repo.Get(testCtx, ref.Collection, scopeA, ref.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal("descendant content remains", ref, err)
			}
		}
	}
	if _, err := f.svc.Repo.Get(testCtx, "sources", scopeA, original); err != nil {
		t.Fatal("original source erased", err)
	}
}

func logBasisJobsOnFailure(t *testing.T, repo *Postgres) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, r := range pgCollection(t, repo, colJobs) {
			if r.Status != JobStatusDone {
				t.Logf("job %s kind=%s status=%s attempts=%d target=%v parent=%s run_at=%v", r.ID, r.Kind, r.Status, r.Attempts, r.Target, r.ErasureParentID, r.RunAt)
			}
		}
	})
}
