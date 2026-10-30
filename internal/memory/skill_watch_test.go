package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// watchFixture applies version 1, then version 2 on top of it, both with an independent passing comparison.
func watchFixture(t *testing.T) (*fixture, Record, Record) {
	f, v := watchVersions(t, 2)
	return f, v[0], v[1]
}

// watchVersions applies n versions in turn, each on top of the one before.
func watchVersions(t *testing.T, n int) (*fixture, []Record) {
	t.Helper()
	f := newFixture(t)
	in := ImportSkillInput{Scope: scopeA, SkillID: "watched-review", Path: "/skills/watched-review/SKILL.md", Content: "# Review\nCheck the outcome.", Analysis: detailedSkillAnalysis(), ComparisonPlan: comparisonPlan()}
	apply := func() Record {
		got, e := f.svc.ImportSkill(testCtx, f.human, in)
		if e != nil {
			t.Fatal(e)
		}
		target := Ref{Collection: colSkillVersions, ID: got.Record.ID, Revision: got.Record.Revision}
		pass := true
		report := SkillComparisonReport{PlanSHA256: got.ComparisonPlanSHA256, BaselineScore: .8, CandidateScore: .9, CandidateCost: 1, CandidateDurationMS: 500, RegressionPassed: true, SafetyPassed: true}
		ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "Independent comparison", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &pass, ComparisonReport: &report}})
		if _, e = f.svc.Transition(testCtx, f.human, colSkillVersions, got.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Record.Revision, Status: "approved", ValidationID: ev.ID}); e != nil {
			t.Fatal(e)
		}
		skill, _ := f.repo.Get(testCtx, colSkills, scopeA, in.SkillID)
		if _, e = f.svc.ApplySkill(testCtx, f.human, in.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: skill.Revision, VersionID: got.Record.ID}, false); e != nil {
			t.Fatal(e)
		}
		v, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, got.Record.ID)
		return v
	}
	out := []Record{apply()}
	for len(out) < n {
		in.Content += fmt.Sprintf("\nStep %d.", len(out))
		plan := *in.ComparisonPlan
		plan.BaselineVersion = out[len(out)-1].ID
		in.ComparisonPlan = &plan
		out = append(out, apply())
	}
	return f, out
}

// unevaluated drops a version's evaluation, like an imported first version.
func unevaluated(t *testing.T, f *fixture, v Record) {
	t.Helper()
	old, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, v.ID)
	old.ValidationID = ""
	if e := f.repo.Replace(testCtx, colSkillVersions, old, old.Revision); e != nil {
		t.Fatal(e)
	}
}

func failedUses(t *testing.T, f *fixture, v Record, n int) {
	t.Helper()
	time.Sleep(2 * time.Millisecond)
	for i := 0; i < n; i++ {
		at := time.Now().UTC()
		u := SkillUsage{SkillID: v.SkillID, Version: 2, VersionID: v.ID, ContentHash: v.Analysis.ContentHash, Runtime: "codex", Status: "failure", StartedAt: at, CompletedAt: at}
		if e := f.repo.Insert(testCtx, "sources", Record{ID: fmt.Sprintf("use-%s-%d", v.ID, i), Scope: scopeA, Kind: skillUsageEventKind, Status: "active", Revision: 1, Text: structuredText(u), CreatedAt: at, UpdatedAt: at}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestSkillWatchRollsBackOnlyWhenRealUseGetsWorse(t *testing.T) {
	f, v1, v2 := watchFixture(t)
	f.auth.actors["bot"] = actor("watch-bot", "service", true, allVerbs...)
	if _, e := f.svc.WatchSkill(testCtx, "bot", v2.SkillID, SkillWatchInput{Scope: scopeA, VersionID: v2.ID}); !errors.Is(e, ErrDenied) {
		t.Fatal("automation started a person's watch", e)
	}
	if _, e := f.svc.WatchSkill(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA, VersionID: v1.ID}); !errors.Is(e, ErrConflict) {
		t.Fatal("watched a version that is not the applied one", e)
	}
	w, e := f.svc.WatchSkill(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA, VersionID: v2.ID})
	if e != nil || w.State != "watching" || w.PreviousVersion != v1.ID || !w.CanRollback || w.Until.Sub(w.AppliedAt) != skillWatchPeriod {
		t.Fatal("watch", w, e)
	}
	if again, e := f.svc.WatchSkill(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA, VersionID: v2.ID}); e != nil || again.AppliedAt != w.AppliedAt {
		t.Fatal("replay", again, e)
	}
	failedUses(t, f, v2, skillWatchMinFailures-1)
	if got, e := f.svc.CheckSkillWatch(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA}); e != nil || got.State != "watching" || got.Failures != skillWatchMinFailures-1 {
		t.Fatal("rolled back on too little evidence", got, e)
	}
	if _, e := f.svc.CheckSkillWatch(testCtx, "bot", v2.SkillID, SkillWatchInput{Scope: scopeA}); !errors.Is(e, ErrDenied) {
		t.Fatal("automation ran a person's rollback", e)
	}
	u := SkillUsage{SkillID: v2.SkillID, Version: 2, VersionID: v2.ID, ContentHash: v2.Analysis.ContentHash, Runtime: "codex", Status: "failure", StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()}
	if e := f.repo.Insert(testCtx, "sources", Record{ID: "use-last", Scope: scopeA, Kind: skillUsageEventKind, Status: "active", Revision: 1, Text: structuredText(u)}); e != nil {
		t.Fatal(e)
	}
	got, e := f.svc.CheckSkillWatch(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA})
	if e != nil || got.State != "rolled_back" || got.Failures != skillWatchMinFailures {
		t.Fatal("worse version kept", got, e)
	}
	skill, _ := f.repo.Get(testCtx, colSkills, scopeA, v2.SkillID)
	if skill.ActiveVersion != v1.ID {
		t.Fatal("previous version not restored", skill.ActiveVersion)
	}
	if again, e := f.svc.CheckSkillWatch(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA}); e != nil || again.State != "rolled_back" {
		t.Fatal("finished watch reopened", again, e)
	}
}

func TestSkillWatchRollsBackAVersionWithoutEvaluationOnlyOnAClearRise(t *testing.T) {
	f, v1, v2 := watchFixture(t)
	if _, e := f.svc.WatchSkill(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA, VersionID: v2.ID}); e != nil {
		t.Fatal(e)
	}
	unevaluated(t, f, v1)
	failedUses(t, f, v2, skillWatchOutcomeMinFailures-1)
	if got, e := f.svc.CheckSkillWatch(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA}); e != nil || got.State != "watching" || !got.CanRollback {
		t.Fatal("rolled back an unevaluated version on too little evidence", got, e)
	}
	u := SkillUsage{SkillID: v2.SkillID, Version: 2, VersionID: v2.ID, ContentHash: v2.Analysis.ContentHash, Runtime: "codex", Status: "failure", StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()}
	if e := f.repo.Insert(testCtx, "sources", Record{ID: "use-clear", Scope: scopeA, Kind: skillUsageEventKind, Status: "active", Revision: 1, Text: structuredText(u)}); e != nil {
		t.Fatal(e)
	}
	got, e := f.svc.CheckSkillWatch(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA})
	if e != nil || got.State != "rolled_back" || got.Failures != skillWatchOutcomeMinFailures {
		t.Fatal("clear rise kept", got, e)
	}
	skill, _ := f.repo.Get(testCtx, colSkills, scopeA, v2.SkillID)
	back, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, v1.ID)
	if skill.ActiveVersion != v1.ID || back.Status != "active" || back.ValidationID != "" {
		t.Fatal("previous version not restored as it was", skill.ActiveVersion, back.Status, back.ValidationID)
	}
}

func TestSkillWatchRule(t *testing.T) {
	for _, c := range []struct {
		failures, baseline int
		evaluated, want    bool
	}{{2, 0, true, false}, {3, 0, true, true}, {3, 3, true, false}, {4, 0, false, false}, {5, 0, false, true}, {5, 3, false, false}, {6, 3, false, true}} {
		if got := watchWorse(c.failures, c.baseline, c.evaluated); got != c.want {
			t.Fatal(c, got)
		}
	}
}

func (f *fakeRepo) OpenSkillWatches(_ context.Context, limit int) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Record{}
	for _, r := range f.rowsLocked(colJobs) {
		if r.Kind == skillWatchKind && r.Status == "managed" && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestSkillWatchRunsWithoutAnyoneOpeningTheSkill(t *testing.T) {
	f, v1, v2 := watchFixture(t)
	f.auth.actors["worker"] = actor("worker", "service", true, allVerbs...)
	f.auth.actors["agent-x"] = actor("agent-x", "agent", true, allVerbs...)
	if _, e := f.svc.WatchSkill(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA, VersionID: v2.ID}); e != nil {
		t.Fatal(e)
	}
	failedUses(t, f, v2, skillWatchMinFailures)
	if n, e := f.svc.RunSkillWatches(testCtx, "agent-x"); n != 0 || !errors.Is(e, ErrDenied) {
		t.Fatal("an agent ran the owner's watch", n, e)
	}
	if skill, _ := f.repo.Get(testCtx, colSkills, scopeA, v2.SkillID); skill.ActiveVersion != v2.ID {
		t.Fatal("rolled back by an agent")
	}
	if n, e := f.svc.RunSkillWatches(testCtx, "worker"); n != 1 || e != nil {
		t.Fatal("worker pass", n, e)
	}
	skill, _ := f.repo.Get(testCtx, colSkills, scopeA, v2.SkillID)
	if skill.ActiveVersion != v1.ID {
		t.Fatal("worker did not roll back", skill.ActiveVersion)
	}
	// The owner's read shows what the worker did, and a closed watch is not checked again.
	if got, e := f.svc.CheckSkillWatch(testCtx, f.human, v2.SkillID, SkillWatchInput{Scope: scopeA}); e != nil || got.State != "rolled_back" {
		t.Fatal("read after worker", got, e)
	}
	if n, e := f.svc.RunSkillWatches(testCtx, "worker"); n != 0 || e != nil {
		t.Fatal("closed watch checked again", n, e)
	}
}

func TestSkillWatchOwnerMayGoBackByHandToTheVersionJustBefore(t *testing.T) {
	f, v := watchVersions(t, 3)
	v1, v2, v3 := v[0], v[1], v[2]
	f.auth.actors["agent-x"] = actor("agent-x", "agent", true, allVerbs...)
	unevaluated(t, f, v1)
	unevaluated(t, f, v2)
	rollback := func(token string, to Record) error {
		head, _ := f.repo.Get(testCtx, colSkills, scopeA, v3.SkillID)
		_, e := f.svc.ApplySkill(testCtx, token, v3.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: head.Revision, VersionID: to.ID}, true)
		return e
	}
	if rollback(f.human, v2) == nil {
		t.Fatal("unevaluated rollback without a watch")
	}
	w, e := f.svc.WatchSkill(testCtx, f.human, v3.SkillID, SkillWatchInput{Scope: scopeA, VersionID: v3.ID})
	if e != nil || !w.CanRollback || w.PreviousVersion != v2.ID {
		t.Fatal("watch", w, e)
	}
	if rollback(f.human, v1) == nil {
		t.Fatal("went back past the version just before without an evaluation")
	}
	if rollback("agent-x", v2) == nil {
		t.Fatal("an agent used the owner's rollback")
	}
	if e = rollback(f.human, v2); e != nil {
		t.Fatal("owner could not go back", e)
	}
	skill, _ := f.repo.Get(testCtx, colSkills, scopeA, v3.SkillID)
	back, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, v2.ID)
	if skill.ActiveVersion != v2.ID || back.ValidationID != "" {
		t.Fatal("previous version not restored as it was", skill.ActiveVersion, back.ValidationID)
	}
	// The watch has ended, so the exception is gone: v3 has its evaluation, v1 still does not.
	if rollback(f.human, v1) == nil {
		t.Fatal("exception outlived the watch")
	}
}
