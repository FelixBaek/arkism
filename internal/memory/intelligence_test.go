package memory

import (
	"errors"
	"testing"
)

var twinGenerator = &Generator{Model: "arkism-local", PromptVersion: "twin-v1", PipelineVersion: "twin-v1"}

func TestDeepTwinClaimsGrowOnlyFromEvidenceAndHumanPromotion(t *testing.T) {
	f := newFixture(t)
	runs := []Ref{}
	for i := 0; i < 3; i++ {
		run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "report run", Sources: refs(f.source())}))
		runs = append(runs, Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision})
	}
	claim := func(text string, confidence float64, evidence []Ref) Record {
		return f.create(f.agent, "memory_candidates", CreateInput{Scope: scopeA, Kind: "user", Text: text, Tags: []string{"twin:preference"},
			Confidence: &confidence, Sources: refs(f.source()), Relations: evidence, Generated: true, Generator: twinGenerator})
	}
	one := claim("prefers Korean reports", 0.9, runs[:1])
	three := claim("prefers minimal reports", 0.9, runs)
	low := claim("prefers tables", 0.5, runs)
	f.create(f.agent, "memory_candidates", CreateInput{Scope: scopeA, Kind: "user", Text: "untagged user fact", Sources: refs(f.source()), Relations: runs, Generated: true, Generator: twinGenerator})

	page, err := f.svc.ListTwinClaims(testCtx, f.human, scopeA, nil)
	if err != nil || len(page.Items) != 3 || page.Authority != "data_only_untrusted" {
		t.Fatalf("claims: %+v %v", page, err)
	}
	status := map[string]string{}
	for _, c := range page.Items {
		status[c.ID] = c.Status
		if c.Type != "preference" || len(c.EvidenceRefs) == 0 || c.LastConfirmed != nil {
			t.Fatalf("claim shape: %+v", c)
		}
	}
	if status[one.ID] != "candidate" || status[three.ID] != "inferred" || status[low.ID] != "candidate" {
		t.Fatalf("one run must stay candidate, three runs infer: %v", status)
	}
	specialist := scopeA
	specialist.AgentID = "designer"
	if page, _ = f.svc.ListTwinClaims(testCtx, f.human, specialist, nil); len(page.Items) != 0 {
		t.Fatal("twin is per user, not per specialist")
	}

	improve := func(id string, dry bool) (ImprovementDecision, error) {
		return f.svc.Improve(testCtx, f.human, ImprovementInput{Scope: scopeA, Target: improvementTwinClaim, ID: id, DryRun: dry})
	}
	if d, _ := improve(one.ID, false); d.Promoted || d.Reason != "insufficient_distinct_runs" {
		t.Fatalf("single observation promoted: %+v", d)
	}
	if d, _ := improve(low.ID, false); d.Promoted || d.Reason != "low_confidence" {
		t.Fatalf("low confidence promoted: %+v", d)
	}
	if d, _ := improve(three.ID, true); d.Promoted || !d.Promotable || d.Reason != "dry_run" {
		t.Fatalf("dry run: %+v", d)
	}
	if _, err = f.svc.Improve(testCtx, f.agent, ImprovementInput{Scope: scopeA, Target: improvementTwinClaim, ID: three.ID}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent promoted a twin claim", err)
	}
	if d, err := improve(three.ID, false); err != nil || !d.Promoted || d.DistinctRuns != 3 {
		t.Fatalf("promote: %+v %v", d, err)
	}
	page, _ = f.svc.ListTwinClaims(testCtx, f.human, scopeA, []string{"confirmed"})
	if len(page.Items) != 1 || page.Items[0].ID != three.ID || page.Items[0].LastConfirmed == nil || len(page.Items[0].EvidenceRefs) != 3 {
		t.Fatalf("confirmed claim keeps its evidence chain: %+v", page.Items)
	}
	if _, err = f.svc.ListTwinClaims(testCtx, f.human, scopeA, []string{"approved"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown status accepted")
	}
}

func TestReflectionsGroupRepeatedLessonsAndValidateSkillImprovement(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		src := f.source()
		input := completedRunInput(CreateInput{Scope: scopeA, Text: "etsy run", Sources: refs(src)})
		input.Run.Outcome = "failed"
		run := f.create(f.agent, "agent_runs", input)
		passed := false
		target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
		ev := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "check", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
		if _, err := f.svc.Learn(testCtx, f.agent, LearningInput{Scope: scopeA, Run: target, Evaluation: Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision},
			TaskType: "skill:etsy-listing", Lesson: "Titles exceed 140 characters.", FailureReason: "title_too_long", VerificationMethod: "listing validator"}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			page, err := f.svc.ListReflections(testCtx, f.human, scopeA, "skill:etsy-listing")
			if err != nil || len(page.Items) != 1 || page.Items[0].DistinctRuns != 1 || page.Items[0].Status != "candidate" {
				t.Fatalf("first reflection: %+v %v", page, err)
			}
			d, _ := f.svc.Improve(testCtx, f.human, ImprovementInput{Scope: scopeA, Target: improvementSkillVersio, ID: "skill:etsy-listing"})
			if d.Promotable || d.Reason != "insufficient_distinct_runs" {
				t.Fatalf("one failure became a skill improvement: %+v", d)
			}
		}
	}
	page, err := f.svc.ListReflections(testCtx, f.human, scopeA, "skill:etsy-listing")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("reflections: %+v %v", page, err)
	}
	r := page.Items[0]
	if r.DistinctRuns != 3 || r.Confidence != 0.875 || r.Interpretation != "Titles exceed 140 characters." || r.Observation == "" || len(r.EvidenceRefs) < 3 {
		t.Fatalf("grouped reflection: %+v", r)
	}
	if other, _ := f.svc.ListReflections(testCtx, f.human, scopeA, "skill:other"); len(other.Items) != 0 {
		t.Fatal("subject filter leaked")
	}
	d, err := f.svc.Improve(testCtx, f.human, ImprovementInput{Scope: scopeA, Target: improvementSkillVersio, ID: "skill:etsy-listing"})
	if err != nil || !d.Promotable || d.Promoted || d.Reason != "skill_review_required" || d.Next == "" {
		t.Fatalf("skill improvement must hand off to skill review: %+v %v", d, err)
	}
	if len(f.repo.rows("memories")) != 0 {
		t.Fatal("reflection auto-promoted")
	}
}
