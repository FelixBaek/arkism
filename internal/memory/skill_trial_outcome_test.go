package memory

import (
	"errors"
	"testing"
)

func TestSkillTrialOutcomeProducer(t *testing.T) {
	f, candidate, trial, in := skillAutomationFixture(t)
	f.auth.actors["checker"] = actor("independent-trial-checker", "service", true, allVerbs...)
	input := SkillTrialOutcomeInput{Scope: scopeA, TrialScope: scopeA, TrialID: trial.ID, EventID: "checked-once", Source: in.TrialEvidence[0], Evidence: []Ref{in.TrialEvidence[0]}, CheckerSHA256: ContentHash("fixed independent checker"), Passed: true}
	result, e := f.svc.RecordSkillTrialOutcome(testCtx, "checker", input)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.svc.RecordSkillTrialOutcome(testCtx, "checker", input)
	if e != nil || again != result {
		t.Fatal("replay", again, e)
	}
	input.Passed = false
	if _, e = f.svc.RecordSkillTrialOutcome(testCtx, "checker", input); !errors.Is(e, ErrConflict) {
		t.Fatal("changed replay", e)
	}
	input.Passed = true
	input.EventID = "second"
	input.Source = in.TrialEvidence[1]
	input.Evidence = []Ref{in.TrialEvidence[1]}
	if _, e = f.svc.RecordSkillTrialOutcome(testCtx, "learner", input); !errors.Is(e, ErrDenied) {
		t.Fatal("manager self-check", e)
	}
	if _, e = f.svc.RecordSkillTrialOutcome(testCtx, "checker", input); e != nil {
		t.Fatal(e)
	}
	out, e := f.svc.ObserveSkillTrial(testCtx, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil || out.State != "ready_for_comparison" {
		t.Fatal(out, e)
	}
	source, _ := f.repo.Get(testCtx, "sources", scopeA, input.Source.ID)
	source.ContentErased = true
	if e = f.repo.Replace(testCtx, "sources", source, source.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.RecordSkillTrialOutcome(testCtx, "checker", input); e == nil {
		t.Fatal("deleted evidence replayed")
	}
}
