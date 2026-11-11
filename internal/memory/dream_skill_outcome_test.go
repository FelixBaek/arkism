package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSkillDraftMustBeUsable(t *testing.T) {
	in, good := draftFixture()
	noAvoid := good
	noAvoid.Analysis.AvoidWhen = nil
	// A person's edit keeps the rules it always had: the gate is the dream's, not the drafter's.
	if err := normalizeSkillDraft(in, &noAvoid); err != nil {
		t.Fatal("a person's draft without avoid_when refused", err)
	}
	if got := draftUnusable(&good.Analysis, good.Content, false); got != "" {
		t.Fatal("usable draft refused", got)
	}
	if got := draftUnusable(&noAvoid.Analysis, good.Content, false); got != "skill_draft_no_avoid_when" {
		t.Fatal("no avoid_when", got)
	}
	for file, want := range map[string]string{
		"# 확인\n결과를 잘 확인한다.":                    "skill_draft_no_steps",
		"- 결과를 본다\n- 다시 본다":                    "skill_draft_no_steps", // bullets are not steps
		"1. 결과를 본다\n2. 다시 본다":                  "",
		"## 1. 결과를 본다\n## 2. 다시 본다":            "",
		"**1.** 결과를 본다\n**2.** 다시 본다":          "",
		"Step 1: look\n### Step 2: look again": "",
		"1단계 결과를 본다\n2단계: 다시 본다":               "",
		"① 결과를 본다\n② 다시 본다":                    "",
		"- 1) 결과를 본다\n- 2) 다시 본다":              "",
		"2024. 올해의 기록\n결과를 잘 확인한다.":            "skill_draft_no_steps",
	} {
		if got := draftUnusable(&good.Analysis, file, false); got != want {
			t.Errorf("%q: %q", file, got)
		}
		if got := draftUnusable(&good.Analysis, file, true); got != "" {
			t.Errorf("revision keeps its owner's form, %q: %q", file, got)
		}
	}
}

func TestRevisionWorse(t *testing.T) {
	base := "# Deploy\n1. Run the tests.\n2. Tag the release.\n3. Watch the error rate for 10 minutes.\n4. Do not deploy on Fridays.\n5. 실패한 배포는 다시 시도하지 않는다."
	lessons := []string{"watch the error rate for 30 minutes, 10 was too short", "announce the release in the team channel after tagging", "never deploy during the weekly freeze"}
	for name, tc := range map[string]struct{ next, want string }{
		"replaces a value":      {strings.Replace(base, "10 minutes", "30 minutes", 1), ""},
		"adds a step":           {base + "\n6. Announce the release in the team channel.", ""},
		"adds a prohibition":    {base + "\n6. Never deploy during the weekly freeze.", ""},
		"merges two steps":      {strings.Replace(base, "2. Tag the release.\n3. Watch", "2. Tag the release, then watch", 1), ""},
		"drops steps":           {"# Deploy\n1. Run the tests.\n4. Do not deploy on Fridays.\n5. 실패한 배포는 다시 시도하지 않는다.", "skill_revision_drops_steps"},
		"invents a step":        {base + "\n6. Rotate the database credentials and purge the cache every night.", "skill_revision_unfaithful"},
		"rewrites it whole":     {"# Shipping\nBe careful and thoughtful whenever software goes out.", "skill_revision_flips_negation"},
		"removes a not":         {strings.Replace(base, "Do not deploy", "Deploy", 1), "skill_revision_flips_negation"},
		"removes a 않는다":         {strings.Replace(base, "시도하지 않는다", "시도한다", 1), "skill_revision_flips_negation"},
		"adds a not":            {strings.Replace(base, "Run the tests", "Do not run the tests", 1), "skill_revision_flips_negation"},
		"deletes a prohibition": {strings.Replace(base, "\n4. Do not deploy on Fridays.", "\n4. Announce the release in the team channel.", 1), "skill_revision_flips_negation"},
	} {
		if got := revisionWorse(base, tc.next, lessons); got != tc.want {
			t.Error(name, got)
		}
	}
}

func TestSkillNotHelping(t *testing.T) {
	for _, tc := range []struct {
		uses, failed, baseUses, baseFailed int
		want                               bool
	}{
		{0, 0, 0, 0, false}, // never used: no signal
		{4, 4, 0, 0, false}, // too few uses without a version to compare with
		{6, 5, 0, 0, true},  // mostly fails
		{12, 5, 0, 0, false},
		{4, 3, 5, 1, true},  // fails more than the version before
		{4, 3, 5, 4, false}, // the version before was no better
		{4, 3, 2, 0, false}, // the version before was hardly used
	} {
		if got := skillNotHelping(tc.uses, tc.failed, tc.baseUses, tc.baseFailed); got != tc.want {
			t.Error(tc, got)
		}
	}
}

// again runs the scope's dream once more and returns its report.
func (s dreamModelSeed) again(t *testing.T) DreamRunItem {
	t.Helper()
	before, _ := s.f.svc.ListDreamRuns(testCtx, s.f.human, scopeA, 20)
	job, err := s.f.svc.Repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.f.svc.runDream(testCtx, s.worker, job); err != nil {
		t.Fatal(err)
	}
	after, err := s.f.svc.ListDreamRuns(testCtx, s.f.human, scopeA, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after.Items {
		if !slices.ContainsFunc(before.Items, func(b DreamRunItem) bool { return b.ID == item.ID }) {
			return item
		}
	}
	t.Fatal("no new dream run")
	return DreamRunItem{}
}

func (s dreamModelSeed) skillState(t *testing.T) (active string, status func(string) string) {
	t.Helper()
	skill, err := s.f.svc.Repo.Get(testCtx, colSkills, scopeA, "review")
	if err != nil {
		t.Fatal(err)
	}
	return skill.ActiveVersion, func(id string) string { return s.status(t, colSkillVersions, id) }
}

// used records n uses of the version that ended with status, each in a run of its own.
func (s dreamModelSeed) used(t *testing.T, v Record, status string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		s.usedIn(t, v, fmt.Sprintf("%s-%s-%d", v.ID[len(v.ID)-12:], status, i), time.Now().UTC(), status)
	}
}

// usedIn records one run that started at the given time and reported the version's use once per
// status: as a runtime agent's ingested run on Postgres (the agent's scope, as production records
// them), as stored use events on the fake.
func (s dreamModelSeed) usedIn(t *testing.T, v Record, run string, at time.Time, statuses ...string) {
	t.Helper()
	at = at.Truncate(time.Millisecond)
	if _, ok := s.f.svc.Repo.(*Postgres); !ok {
		for i, status := range statuses {
			recordUse(t, s.f, v, status, fmt.Sprintf("use-%s-%d", run, i), "use-trace-"+run, at)
		}
		return
	}
	skill, _ := s.f.svc.Repo.Get(testCtx, colSkills, scopeA, v.SkillID)
	scope := scopeA
	scope.AgentID = "codex-runtime"
	events := []TraceEventInput{}
	for i, status := range statuses {
		raw, _ := json.Marshal(SkillUsage{SkillID: v.SkillID, Version: slices.Index(skillHistory(skill), v.ID) + 1, VersionID: v.ID, ContentHash: v.Analysis.ContentHash, Runtime: "codex", Status: status, StartedAt: at, CompletedAt: at})
		events = append(events, TraceEventInput{ID: fmt.Sprintf("use-event-%s-%d", run, i), Kind: skillUsageEventKind, OccurredAt: at, Payload: raw})
	}
	if _, err := s.f.svc.IngestCompletedTrace(testCtx, s.f.agent, TraceIngestInput{Scope: scope, TraceID: "use-trace-" + run, ThreadID: "use", AgentRun: CompletedAgentRunInput{ID: "use-run-" + run, TaskType: "skill_run", Outcome: "success", StartedAt: at, FinishedAt: at, Text: "skill usage"}, Events: events}); err != nil {
		t.Fatal("use ingest", err)
	}
}

// One run that reports many failures, failures long ago and cancelled uses do not retire a skill;
// failures of several recent runs do, and a cancelled use is no success to weigh against them.
func checkDreamCountsRunsNotEvents(t *testing.T, s dreamModelSeed) {
	promo, _ := s.changeOf(s.run(t).Run, "promote_skill")
	v1, err := s.f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, promo.After)
	if err != nil {
		t.Fatal(err)
	}
	s.usedIn(t, v1, "burst", time.Now().UTC(), "failure", "failure", "failure", "failure", "failure", "failure")
	for i := 0; i < 6; i++ {
		s.usedIn(t, v1, fmt.Sprintf("old-%d", i), time.Now().UTC().Add(-2*skillWatchPeriod), "failure")
	}
	s.used(t, v1, "cancelled", 3)
	if c, i := s.changeOf(s.again(t).Run, "demote_skill"); i >= 0 {
		t.Fatal("demoted on one run's reports, old failures or cancelled uses", c)
	}
	s.used(t, v1, "failure", 4)
	if _, i := s.changeOf(s.again(t).Run, "demote_skill"); i < 0 {
		t.Fatal("five failed runs did not retire the skill; cancelled uses counted as successes")
	}
}

// The version a failing revision replaced does not come back when a person could not roll back to
// it either (here: it expired); the skill is retired instead.
func TestDreamRetiresWhenBaseCannotComeBack(t *testing.T) {
	s := newDreamModelSeed(t, nil)
	v1, v2 := revisedSkill(t, s, new(string))
	past := time.Now().Add(-time.Hour)
	v1.ExpiresAt = &past
	if err := s.f.svc.Repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		v1.Revision++
		return tx.Replace(ctx, colSkillVersions, v1, v1.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	s.used(t, v2, "failure", 3)
	c, i := s.changeOf(s.again(t).Run, "demote_skill")
	if active, status := s.skillState(t); i < 0 || c.After != "" || active != "" || status(v1.ID) != "superseded" {
		t.Fatal("expired base came back", c, active, status(v1.ID))
	}
}

// revisedSkill promotes the review skill (v1, three good uses), then revises it from three new
// lessons (v2). The model adds *added as a step from then on.
func revisedSkill(t *testing.T, s dreamModelSeed, added *string) (Record, Record) {
	t.Helper()
	promo, _ := s.changeOf(s.run(t).Run, "promote_skill")
	v1, _ := s.f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, promo.After)
	s.used(t, v1, "success", 3)
	_, draft := draftFixture()
	*added = "List the changed files in the summary."
	dreamModelServer(t, s.f, func(system, user string) string {
		if !strings.Contains(system, "For an existing skill") {
			return ""
		}
		step := "2. 실패하면 변경 제안을 철회한다."
		return structuredText(skillDraftPatch{Edits: []skillDraftEdit{{OldText: step, NewText: step + "\n3. " + *added}}, Analysis: draft.Analysis})
	})
	for i := 0; i < 3; i++ {
		learnedLesson(t, s.f, "review", "list the changed files in the summary", false)
	}
	revised := s.again(t)
	rev, i := s.changeOf(revised.Run, "promote_skill")
	if i < 0 || rev.Before != v1.ID {
		t.Fatal("no revision", revised.Run.Changes, revised.Run.Skipped)
	}
	v2, _ := s.f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, rev.After)
	v1, _ = s.f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, v1.ID)
	return v1, v2
}

// A skill the dream made whose uses mostly fail is retired, and is not drafted again.
func checkDreamRetiresFailingSkill(t *testing.T, s dreamModelSeed) {
	promo, _ := s.changeOf(s.run(t).Run, "promote_skill")
	v1, err := s.f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, promo.After)
	if err != nil {
		t.Fatal(err)
	}
	s.used(t, v1, "success", 1)
	s.used(t, v1, "failure", 4)
	if c, i := s.changeOf(s.again(t).Run, "demote_skill"); i >= 0 {
		t.Fatal("demoted on four failures", c)
	}
	s.used(t, v1, "validation_failed", 1)
	item := s.again(t)
	c, i := s.changeOf(item.Run, "demote_skill")
	active, status := s.skillState(t)
	if i < 0 || c.Before != v1.ID || c.After != "" || active != "" || status(v1.ID) != "rejected" || strings.Contains(c.Reason, v1.Analysis.Description) {
		t.Fatal("not retired", item.Run.Changes, item.Run.Skipped, active)
	}
	if _, i = s.changeOf(s.again(t).Run, "promote_skill"); i >= 0 {
		t.Fatal("drafted the retired skill again")
	}
}

// A revision the dream applied that fails more often than the version it replaced is rolled back; a
// person can undo that, and the dream then leaves the version alone.
func checkDreamRollsBackWorseRevision(t *testing.T, s dreamModelSeed) {
	added := ""
	v1, v2 := revisedSkill(t, s, &added)
	// Lessons waiting in the run that rolls back: revising the skill in that same run would leave
	// the roll back impossible to undo.
	added = "Ask a second person to approve the release."
	for i := 0; i < 3; i++ {
		learnedLesson(t, s.f, "skill:review", "ask a second person to approve the release", false)
	}
	s.used(t, v2, "failure", 3)
	back := s.again(t)
	c, i := s.changeOf(back.Run, "demote_skill")
	active, status := s.skillState(t)
	if i < 0 || c.Before != v2.ID || c.After != v1.ID || active != v1.ID || status(v1.ID) != statusActive || status(v2.ID) != "rejected" {
		t.Fatal("not rolled back", back.Run.Changes, back.Run.Skipped, active)
	}
	if _, i = s.changeOf(back.Run, "promote_skill"); i >= 0 {
		t.Fatal("revised the skill in the run that rolled it back", back.Run.Changes)
	}
	out, err := s.f.svc.UndoDream(testCtx, s.f.human, back.ID, DreamUndoInput{Scope: scopeA})
	active, status = s.skillState(t)
	if err != nil || len(out.Conflicts) != 0 || active != v2.ID || status(v2.ID) != statusActive || status(v1.ID) != "superseded" {
		t.Fatal("undo", out, err, active, status(v1.ID), status(v2.ID))
	}
	if c, i = s.changeOf(s.again(t).Run, "demote_skill"); i >= 0 {
		t.Fatal("demoted a version a person put back", c)
	}
}

func TestDreamRetiresFailingSkill(t *testing.T) {
	checkDreamRetiresFailingSkill(t, newDreamModelSeed(t, nil))
}
func TestDreamRollsBackWorseRevision(t *testing.T) {
	checkDreamRollsBackWorseRevision(t, newDreamModelSeed(t, nil))
}
func TestDreamCountsRunsNotEvents(t *testing.T) {
	checkDreamCountsRunsNotEvents(t, newDreamModelSeed(t, nil))
}
func TestPostgresDreamSkillOutcomes(t *testing.T) {
	checkDreamCountsRunsNotEvents(t, newDreamModelSeed(t, isolatedPostgres(t)))
	checkDreamRetiresFailingSkill(t, newDreamModelSeed(t, isolatedPostgres(t)))
	checkDreamRollsBackWorseRevision(t, newDreamModelSeed(t, isolatedPostgres(t)))
}
