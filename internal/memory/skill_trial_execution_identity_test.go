package memory

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTrialExecutionIdentity(t *testing.T) {
	t0 := time.Now().UTC()
	first := SkillUsage{Runtime: "codex", StartedAt: t0, CompletedAt: t0.Add(time.Second)}
	cases := []struct {
		name, firstOrigin, secondOrigin string
		next                            time.Duration
		valid                           bool
	}{
		{"copied_legacy", "", "", 0, false}, {"known_origin_copied_to_missing", "one", "", 0, false}, {"missing_copied_to_known", "", "one", 0, false},
		{"same_upstream_even_when_retimed", "one", "one", time.Second, false}, {"independent_upstream_same_window", "one", "two", 0, true},
		{"independent_legacy_rerun", "", "", time.Second, true}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTrialExecutions()
			if !s.add("t1", "r1", c.firstOrigin, first) {
				t.Fatal("first")
			}
			u := first
			u.StartedAt = u.StartedAt.Add(c.next)
			u.CompletedAt = u.CompletedAt.Add(c.next)
			if s.add("t2", "r2", c.secondOrigin, u) != c.valid {
				t.Fatal(c.name)
			}
		})
	}
}
func TestPostgresIndependentTrialExecutions(t *testing.T) {
	for _, kind := range []string{"same_upstream", "copied_legacy", "independent_upstream", "independent_legacy"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = isolatedPostgres(t)
			comparison, proposal := comparisonRecordFixture(t, f)
			var first TraceIngestInput
			adjust := func(i int, in *TraceIngestInput) {
				if kind == "same_upstream" {
					in.LangSmithRunID = "origin-one"
				}
				if kind == "independent_upstream" {
					in.LangSmithRunID = fmt.Sprint("origin-", i)
				}
				if i == 0 {
					first = *in
					return
				}
				if kind == "copied_legacy" || kind == "independent_upstream" {
					in.AgentRun.StartedAt = first.AgentRun.StartedAt
					in.AgentRun.FinishedAt = first.AgentRun.FinishedAt
					in.Events[0].OccurredAt = first.Events[0].OccurredAt
					in.Events[0].Payload = first.Events[0].Payload
				}
			}
			_, candidate, trial, apply := skillAutomationFromComparison(t, f, comparison, proposal, adjust)
			token := testPrincipalToken("learner")
			observe, e := f.svc.ObserveSkillTrial(testCtx, token, trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
			rejected := kind == "same_upstream" || kind == "copied_legacy"
			if rejected && !errors.Is(e, ErrConflict) || !rejected && (e != nil || observe.State != "ready_for_comparison") {
				t.Fatalf("observe %s: %s %v", kind, observe.State, e)
			}
			_, e = f.svc.ApplySkill(testCtx, token, candidate.SkillID, apply, false)
			if rejected && !errors.Is(e, ErrConflict) || !rejected && e != nil {
				t.Fatalf("apply %s: %v", kind, e)
			}
		})
	}
}
