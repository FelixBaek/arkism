package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func proposalFixture(t *testing.T, f *fixture) ProposeSkillInput {
	t.Helper()
	in := ProposeSkillInput{Skill: ImportSkillInput{Scope: scopeA, SkillID: "lesson-review", Path: "/skills/lesson-review/SKILL.md", Content: "# Review\nVerify the result before reporting success.", Analysis: detailedSkillAnalysis(), ComparisonPlan: comparisonPlan()}, Generator: &Generator{Model: "test-model", PromptVersion: "v1", PipelineVersion: "v1"}}
	for i := 0; i < 3; i++ {
		src := f.source()
		runInput := completedRunInput(CreateInput{Scope: scopeA, Text: "learning run", Sources: refs(src)})
		runInput.Run.Outcome = "failed"
		run := f.create(f.agent, "agent_runs", runInput)
		passed := false
		target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
		ev := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "independent check", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
		lesson, err := f.svc.Learn(testCtx, f.agent, LearningInput{Scope: scopeA, Run: target, Evaluation: Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}, TaskType: "review", Lesson: "verify the result", VerificationMethod: "result check"})
		if err != nil {
			t.Fatal(err)
		}
		in.Lessons = append(in.Lessons, lesson.Lesson)
	}
	return in
}
func TestSkillProposal(t *testing.T) { checkSkillProposal(t, newFixture(t)) }
func TestPostgresSkillProposal(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillProposal(t, f)
}
func checkSkillProposal(t *testing.T, f *fixture) {
	t.Helper()
	in := proposalFixture(t, f)
	raw, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", "/v1/skills/propose", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+f.agent)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got ImportSkillResult
	if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if got.Record.Status != "candidate" || got.Record.ApprovedBy != "" || got.Skill.ActiveVersion != "" || got.Record.Generator == nil || got.Record.CreatedByType != "agent" || len(got.Record.Sources) != 2 {
		t.Fatal("proposal provenance/activation", got.Record)
	}
	source, e := f.svc.Repo.Get(testCtx, "sources", scopeA, got.Record.Sources[0].SourceID)
	if e != nil || source.TrustLevel != "agent_proposed" {
		t.Fatal("source provenance", e)
	}
	slices.Reverse(in.Lessons)
	again, e := f.svc.ProposeSkill(testCtx, f.agent, in)
	if e != nil || !again.Reused || again.Record.ID != got.Record.ID {
		t.Fatal("retry", e)
	}
	if _, e = f.svc.ImportSkill(testCtx, f.agent, in.Skill); !errors.Is(e, ErrDenied) {
		t.Fatal("import authority widened", e)
	}
	if _, e = f.svc.Transition(testCtx, f.agent, "skill_versions", got.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved"}); !errors.Is(e, ErrDenied) {
		t.Fatal("agent approval", e)
	}
	passed := true
	target := Ref{Collection: "skill_versions", ID: got.Record.ID, Revision: got.Record.Revision}
	ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "independent comparison", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed, ComparisonReport: &SkillComparisonReport{PlanSHA256: got.ComparisonPlanSHA256, BaselineScore: .5, CandidateScore: .8, CandidateCost: 1, CandidateDurationMS: 500, RegressionPassed: true, SafetyPassed: true}}})
	approved, e := f.svc.Transition(testCtx, f.human, "skill_versions", got.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Record.Revision, Status: "approved", ValidationID: ev.ID})
	if e != nil {
		t.Fatal("approve", e)
	}
	retry, e := f.svc.ProposeSkill(testCtx, f.agent, in)
	if e != nil || !retry.Reused || retry.Record.Status != "approved" {
		t.Fatal("approved retry", e)
	}
	parent, e := f.svc.ApplySkill(testCtx, f.human, in.Skill.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Skill.Revision, VersionID: approved.ID}, false)
	if e != nil || parent.ActiveVersion != approved.ID {
		t.Fatal("apply", e)
	}
	active, e := f.svc.Repo.Get(testCtx, "skill_versions", scopeA, approved.ID)
	if e != nil || checkActiveSkill(testCtx, f.svc.Repo, active) != nil {
		t.Fatal("active", e)
	}
	lesson, e := f.svc.Repo.Get(testCtx, "memory_candidates", scopeA, in.Lessons[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	previous := lesson.Revision
	lesson.Revision++
	lesson.Status = "rejected"
	if e = f.svc.Repo.Replace(testCtx, "memory_candidates", lesson, previous); e != nil {
		t.Fatal(e)
	}
	if e = checkActiveSkill(testCtx, f.svc.Repo, active); e == nil {
		t.Fatal("withdrawn lesson still routes")
	}
	if _, e = f.svc.ProposeSkill(testCtx, f.agent, in); e == nil {
		t.Fatal("withdrawn lesson reused")
	}
}
func TestSkillProposalRejectsInvalidEvidence(t *testing.T) {
	for _, name := range []string{"missing_plan", "missing_generator", "few", "duplicate", "stale", "different_lesson", "denied_read", "denied_parent_read", "source_revoked"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			in := proposalFixture(t, f)
			switch name {
			case "missing_plan":
				in.Skill.ComparisonPlan = nil
			case "missing_generator":
				in.Generator = nil
			case "few":
				in.Lessons = in.Lessons[:2]
			case "duplicate":
				in.Lessons[2] = in.Lessons[0]
			case "stale":
				in.Lessons[0].Revision++
			case "denied_parent_read":
				f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:skills"}
			case "denied_read":
				f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:evaluations"}
			case "different_lesson", "source_revoked":
				row, e := f.repo.Get(testCtx, "memory_candidates", scopeA, in.Lessons[0].ID)
				if e != nil {
					t.Fatal(e)
				}
				if name == "different_lesson" {
					row.Text = "different lesson"
					if e = f.repo.Replace(testCtx, "memory_candidates", row, row.Revision); e != nil {
						t.Fatal(e)
					}
				} else {
					src, e := f.repo.Get(testCtx, "sources", scopeA, row.Sources[0].SourceID)
					if e != nil {
						t.Fatal(e)
					}
					src.Status = "expired"
					if e = f.repo.Replace(testCtx, "sources", src, src.Revision); e != nil {
						t.Fatal(e)
					}
				}
			}
			if _, e := f.svc.ProposeSkill(testCtx, f.agent, in); e == nil {
				t.Fatal("invalid proposal accepted")
			}
			if len(f.repo.rows("skill_versions")) != 0 || len(f.repo.rows("skills")) != 0 {
				t.Fatal("invalid proposal left writes")
			}
		})
	}
}

func TestSkillProposalErasure(t *testing.T) { checkSkillProposalErasure(t, newFixture(t)) }
func TestPostgresSkillProposalErasure(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillProposalErasure(t, f)
}
func checkSkillProposalErasure(t *testing.T, f *fixture) {
	t.Helper()
	in := proposalFixture(t, f)
	got, e := f.svc.ProposeSkill(testCtx, f.agent, in)
	if e != nil {
		t.Fatal(e)
	}
	pinned, e := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{in.Skill.SkillID + "@1", in.Skill.SkillID + "@latest-approved"}})
	if e != nil || pinned.Items[0].Instructions != in.Skill.Content || pinned.Items[0].ValidationStatus != "candidate" || pinned.Items[1].Error != "no_approved_version" {
		t.Fatal("candidate routing", pinned, e)
	}
	lesson := in.Lessons[0]
	if e = f.svc.Delete(testCtx, f.human, lesson.Collection, lesson.ID, ChangeInput{Scope: scopeA, ExpectedRevision: lesson.Revision}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.Get(testCtx, f.human, "sources", scopeA, got.Record.Sources[0].SourceID); e == nil {
		t.Fatal("erased evidence still exposes generated source")
	}
	for i := 0; i < 10; i++ {
		e = f.svc.WorkOnce(testCtx, erasureWorker(f))
		if errors.Is(e, ErrNotFound) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, ref := range []Ref{{Collection: "skill_versions", ID: got.Record.ID}, {Collection: "sources", ID: got.Record.Sources[0].SourceID}} {
		if _, e = f.svc.Repo.Get(testCtx, ref.Collection, scopeA, ref.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("derived content remains", ref, e)
		}
	}
	if _, e = f.svc.Repo.Get(testCtx, "sources", scopeA, got.Record.Sources[1].SourceID); e != nil {
		t.Fatal("original source lost", e)
	}
}

func TestSkillProposalRechecksBeforeWrite(t *testing.T) {
	f := newFixture(t)
	in := proposalFixture(t, f)
	f.svc.Repo = evaluationChangeRepo{Repository: f.repo, change: func() {
		row, e := f.repo.Get(testCtx, "memory_candidates", scopeA, in.Lessons[0].ID)
		if e != nil {
			t.Fatal(e)
		}
		prev := row.Revision
		row.Revision++
		row.Status = "rejected"
		if e = f.repo.Replace(testCtx, "memory_candidates", row, prev); e != nil {
			t.Fatal(e)
		}
	}}
	if _, e := f.svc.ProposeSkill(testCtx, f.agent, in); !errors.Is(e, ErrConflict) {
		t.Fatal("changed evidence accepted", e)
	}
	if len(f.repo.rows("skill_versions")) != 0 {
		t.Fatal("partial candidate write")
	}
}
func TestSkillProposalMetadataAndPrivacy(t *testing.T) {
	f := newFixture(t)
	in := proposalFixture(t, f)
	first, e := f.svc.ProposeSkill(testCtx, f.agent, in)
	if e != nil {
		t.Fatal(e)
	}
	changed := *in.Skill.Analysis
	changed.Description = "Check every claimed result."
	in.Skill.Analysis = &changed
	second, e := f.svc.ProposeSkill(testCtx, f.agent, in)
	if e != nil || second.Record.ID == first.Record.ID {
		t.Fatal("changed analysis reused", e)
	}
	modified := second.Record
	modified.Relations = nil
	if sameProvenance(second.Record, modified) {
		t.Fatal("proposal evidence can be removed")
	}
	modified = second.Record
	modified.Kind = importedSkillBaselineKind
	if sameProvenance(second.Record, modified) {
		t.Fatal("proposal kind can be bypassed")
	}
	in.Skill.Content += "\nContact test@example.com."
	private, e := f.svc.ProposeSkill(testCtx, f.agent, in)
	if e != nil || private.Record.Sensitivity != "private" {
		t.Fatal("content privacy downgraded", e)
	}
}
