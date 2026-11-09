package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSkillDraftMustBeUsable(t *testing.T) {
	in, good := draftFixture()
	noAvoid, noSteps := good, good
	noAvoid.Analysis.AvoidWhen = nil
	noSteps.Content = "# 결과 확인\n결과를 잘 확인한다."
	if err := normalizeSkillDraft(in, &good); err != nil {
		t.Fatal("usable draft refused", err)
	}
	for name, d := range map[string]SkillDraft{"no avoid_when": noAvoid, "no steps": noSteps} {
		if err := normalizeSkillDraft(in, &d); !errors.Is(err, ErrDenied) {
			t.Fatal(name, "accepted", err)
		}
	}
}

func TestRevisionWorse(t *testing.T) {
	base := "# Deploy\n1. Run the tests.\n2. Tag the release.\n3. Watch the error rate for 10 minutes."
	lessons := []string{"watch the error rate for 30 minutes, 10 was too short", "announce the release in the team channel after tagging"}
	for name, tc := range map[string]struct{ next, want string }{
		"replaces a value":  {strings.Replace(base, "10 minutes", "30 minutes", 1), ""},
		"adds a step":       {base + "\n4. Announce the release in the team channel.", ""},
		"drops steps":       {"# Deploy\n1. Run the tests.", "skill_revision_drops_steps"},
		"invents a step":    {base + "\n4. Rotate the database credentials and purge the cache every night.", "skill_revision_unfaithful"},
		"rewrites it whole": {"# Shipping\nBe careful and thoughtful whenever software goes out.", "skill_revision_drops_steps"},
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

// used records n uses of the version that ended with status: as a runtime agent's ingested runs on
// Postgres, as stored use events on the fake.
func (s dreamModelSeed) used(t *testing.T, v Record, status string, n int) {
	t.Helper()
	if _, ok := s.f.svc.Repo.(*Postgres); !ok {
		recordedUses(t, s.f, v, status, n)
		return
	}
	skill, _ := s.f.svc.Repo.Get(testCtx, colSkills, scopeA, v.SkillID)
	scope := scopeA
	scope.AgentID = "codex-runtime"
	for i := 0; i < n; i++ {
		at := time.Now().UTC().Truncate(time.Millisecond)
		id := fmt.Sprintf("%s-%s-%d", v.ID[len(v.ID)-12:], status, i)
		raw, _ := json.Marshal(SkillUsage{SkillID: v.SkillID, Version: slices.Index(skillHistory(skill), v.ID) + 1, VersionID: v.ID, ContentHash: v.Analysis.ContentHash, Runtime: "codex", Status: status, StartedAt: at, CompletedAt: at})
		if _, err := s.f.svc.IngestCompletedTrace(testCtx, s.f.agent, TraceIngestInput{Scope: scope, TraceID: "use-trace-" + id, ThreadID: "use", AgentRun: CompletedAgentRunInput{ID: "use-run-" + id, TaskType: "skill_run", Outcome: "success", StartedAt: at, FinishedAt: at, Text: "skill usage"}, Events: []TraceEventInput{{ID: "use-event-" + id, Kind: skillUsageEventKind, OccurredAt: at, Payload: raw}}}); err != nil {
			t.Fatal("use ingest", err)
		}
	}
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
	promo, _ := s.changeOf(s.run(t).Run, "promote_skill")
	v1, _ := s.f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, promo.After)
	s.used(t, v1, "success", 3)
	_, draft := draftFixture()
	dreamModelServer(t, s.f, func(system, user string) string {
		if !strings.Contains(system, "For an existing skill") {
			return ""
		}
		step := "2. 실패하면 변경 제안을 철회한다."
		return structuredText(skillDraftPatch{Edits: []skillDraftEdit{{OldText: step, NewText: step + "\n3. List the changed files in the summary."}}, Analysis: draft.Analysis})
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
	s.used(t, v2, "failure", 3)
	back := s.again(t)
	c, i := s.changeOf(back.Run, "demote_skill")
	active, status := s.skillState(t)
	if i < 0 || c.Before != v2.ID || c.After != v1.ID || active != v1.ID || status(v1.ID) != statusActive || status(v2.ID) != "rejected" {
		t.Fatal("not rolled back", back.Run.Changes, back.Run.Skipped, active)
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
func TestPostgresDreamSkillOutcomes(t *testing.T) {
	checkDreamRetiresFailingSkill(t, newDreamModelSeed(t, isolatedPostgres(t)))
	checkDreamRollsBackWorseRevision(t, newDreamModelSeed(t, isolatedPostgres(t)))
}
