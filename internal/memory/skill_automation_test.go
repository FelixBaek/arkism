package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"testing"
	"time"
)

func (x fakeTx) SkillTrialSourceIDs(_ context.Context, scope Scope, id string) ([]string, error) {
	out := []string{}
	for _, r := range x.store["sources"] {
		var u SkillUsage
		if r.Scope == scope && r.Kind == "note" && json.Unmarshal([]byte(r.Text), &u) == nil && u.TrialID == id {
			out = append(out, r.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func skillAutomationFixture(t *testing.T, supplied ...*fixture) (*fixture, Record, Record, ChangeInput) {
	t.Helper()
	f := newFixture(t)
	if len(supplied) > 0 {
		f = supplied[0]
	}
	comparison, proposal := comparisonRecordFixture(t, f)
	recorded, e := f.svc.RecordSkillComparison(testCtx, "comparison-service", comparison)
	if e != nil {
		t.Fatal(e)
	}
	f.auth.actors["learner"] = actor("learner", "service", true, append(allVerbs, "auto_learn")...)
	f.svc.SkillAutomationPolicies = []SkillAutomationPolicy{{PrincipalID: "learner", Scope: scopeA, SkillIDs: []string{proposal.Skill.ID}, Runtimes: []string{"codex"}, ExpiresAt: time.Now().Add(2 * time.Hour), MaxTrialSeconds: 3600, MinTrialRuns: 2}}
	approved, e := f.svc.Transition(testCtx, "learner", colSkillVersions, proposal.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved", ValidationID: recorded.Evaluation.ID})
	if e != nil {
		t.Fatal("auto approve", e)
	}
	trial, e := f.svc.StartSkillTrial(testCtx, "learner", SkillTrialInput{Scope: scopeA, SkillID: proposal.Skill.ID, EventID: "trial", Candidate: Ref{Collection: colSkillVersions, ID: approved.ID, Revision: approved.Revision}, Runtime: "codex", EndsAt: time.Now().Add(30 * time.Minute)})
	if e != nil {
		t.Fatal("trial", e)
	}
	in := ChangeInput{Scope: scopeA, VersionID: approved.ID, ExpectedRevision: proposal.Skill.Revision, TrialID: trial.ID}
	sources := []SourceRef{}
	for i := 0; i < 2; i++ {
		now := time.Now()
		u := SkillUsage{TrialID: trial.ID, SkillID: proposal.Skill.ID, VersionID: approved.ID, Version: proposal.Version, Runtime: "codex", ContentHash: approved.Analysis.ContentHash, Status: "success", StartedAt: now, CompletedAt: now}
		raw, _ := json.Marshal(u)
		source := Record{ID: fmt.Sprint("trial-source-", i), Scope: scopeA, Kind: "note", Status: "active", Revision: 1, Text: string(raw), TraceID: fmt.Sprint("trial-trace-", i), CreatedBy: "runtime", CreatedByType: "agent"}
		if _, postgres := f.svc.Repo.(*Postgres); postgres {
			time.Sleep(2 * time.Millisecond)
			end := time.Now().UTC().Truncate(time.Millisecond)
			start := trial.CreatedAt.Truncate(time.Millisecond).Add(time.Millisecond)
			runtimeScope := scopeA
			runtimeScope.AgentID = "codex-runtime"
			f.svc.SkillAutomationPolicies[0].EvidenceAgents = []string{"codex-runtime"}
			u.StartedAt = start
			u.CompletedAt = end
			raw, _ = json.Marshal(u)
			_, e = f.svc.IngestCompletedTrace(testCtx, f.agent, TraceIngestInput{Scope: runtimeScope, TraceID: source.TraceID, ThreadID: "trial", AgentRun: CompletedAgentRunInput{ID: fmt.Sprint("trial-run-", i), TaskType: "trial", Outcome: "success", StartedAt: start, FinishedAt: end, Text: "Bounded trial execution"}, Events: []TraceEventInput{{ID: source.ID, Kind: skillUsageEventKind, OccurredAt: end, Payload: raw}}})
			if e != nil {
				t.Fatal("trial execution ingest", e)
			}
			pass := true
			ref := Ref{Collection: "sources", ID: source.ID, Revision: 1}
			observed := f.create(f.human2, "evaluations", CreateInput{Scope: runtimeScope, Text: "Independent bounded execution check", Target: &ref, Sources: []SourceRef{{SourceID: source.ID, Locator: "trace"}}, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &pass}})
			in.ScopedTrialEvidence = append(in.ScopedTrialEvidence, ScopedTrialEvidence{Scope: runtimeScope, Source: ref, Evaluation: Ref{Collection: "evaluations", ID: observed.ID, Revision: observed.Revision}})
		} else {
			if e = f.svc.Repo.Insert(testCtx, "sources", source); e != nil {
				t.Fatal(e)
			}
			if e = f.svc.Repo.Insert(testCtx, "execution_traces", Record{ID: source.TraceID, Scope: scopeA, Status: "active", Revision: 1}); e != nil {
				t.Fatal(e)
			}
		}
		if _, postgres := f.svc.Repo.(*Postgres); !postgres {
			in.TrialEvidence = append(in.TrialEvidence, Ref{Collection: "sources", ID: source.ID, Revision: 1})
			sources = append(sources, SourceRef{SourceID: source.ID, Locator: "trace"})
		} else {
			sources = approved.Sources
		}
	}
	pass := true
	target := Ref{Collection: colSkillVersions, ID: approved.ID, Revision: approved.Revision}
	ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "Independent trial execution check", Target: &target, Sources: sources, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &pass, ComparisonReport: comparison.Result.Report}})
	in.ValidationID = ev.ID
	return f, approved, trial, in
}

func TestSkillAutomationPolicy(t *testing.T) {
	f, candidate, _, in := skillAutomationFixture(t)
	raw, _ := json.Marshal(f.svc.SkillAutomationPolicies)
	if _, e := ParseSkillAutomationPolicies(string(raw)); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"[]", "null", string(raw) + " {}", `[{"unknown":1}]`} {
		if _, e := ParseSkillAutomationPolicies(bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid policy", e)
		}
	}
	for _, role := range []string{"agent", "pipeline"} {
		f.auth.actors[role] = actor("learner", role, true, append(allVerbs, "auto_learn")...)
		if _, e := f.svc.ApplySkill(testCtx, role, candidate.SkillID, in, false); !errors.Is(e, ErrDenied) {
			t.Fatal("role allowed", role, e)
		}
	}
	f.svc.SkillAutomationPolicies[0].Scope.AgentID = "other"
	if _, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false); !errors.Is(e, ErrDenied) {
		t.Fatal("scope widened", e)
	}
}
func TestSkillAutomationTrialPromotion(t *testing.T) {
	f, candidate, _, in := skillAutomationFixture(t)
	got, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false)
	if e != nil || got.ActiveVersion != candidate.ID {
		t.Fatal("promotion", e, got.ActiveVersion)
	}
}
func TestSkillAutomationPromotionRejectsMissingOrStaleEvidence(t *testing.T) {
	for _, kind := range []string{"missing", "cherry_pick", "same_trace", "failed", "deleted", "self", "expired_policy", "trial_bound"} {
		t.Run(kind, func(t *testing.T) {
			f, candidate, trial, in := skillAutomationFixture(t)
			switch kind {
			case "missing":
				in.TrialID = ""
			case "cherry_pick":
				in.TrialEvidence = in.TrialEvidence[:1]
			case "expired_policy":
				f.svc.SkillAutomationPolicies[0].ExpiresAt = time.Now().Add(-time.Second)
			case "self":
				ev, _ := f.svc.Repo.Get(testCtx, "evaluations", scopeA, in.ValidationID)
				ev.Evaluation.EvalType = "automated_check"
				self := f.create("learner", "evaluations", CreateInput{Scope: scopeA, Text: ev.Text, Target: ev.Target, Sources: ev.Sources, Evaluation: ev.Evaluation})
				in.ValidationID = self.ID
			case "trial_bound":
				trial.SkillTrial.Candidate.Revision++
				f.svc.Repo.Replace(testCtx, colSkillTrials, trial, trial.Revision)
			default:
				source, _ := f.svc.Repo.Get(testCtx, "sources", scopeA, in.TrialEvidence[1].ID)
				if kind == "same_trace" {
					source.TraceID = "trial-trace-0"
				}
				if kind == "deleted" {
					source.ContentErased = true
				}
				if kind == "failed" {
					var usage SkillUsage
					json.Unmarshal([]byte(source.Text), &usage)
					usage.Status = "failure"
					raw, _ := json.Marshal(usage)
					source.Text = string(raw)
				}
				f.svc.Repo.Replace(testCtx, "sources", source, source.Revision)
			}
			if _, e := f.svc.ApplySkill(testCtx, "learner", candidate.SkillID, in, false); e == nil {
				t.Fatal("unsafe promotion accepted")
			}
			parent, _ := f.svc.Repo.Get(testCtx, colSkills, scopeA, candidate.SkillID)
			if parent.ActiveVersion != "" {
				t.Fatal("baseline changed")
			}
		})
	}
}

func TestPostgresSkillAutomationActualTrialEvidence(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	f, candidate, trial, in := skillAutomationFixture(t, f)
	p := f.svc.Repo.(*Postgres)
	runtimeScope := scopeA
	runtimeScope.AgentID = "codex-runtime"
	ids, e := p.SkillTrialSourceIDs(testCtx, runtimeScope, trial.ID)
	if e != nil || len(ids) != 2 {
		t.Fatal("trial source query", ids, e)
	}
	other := scopeA
	other.AgentID = "another-agent"
	ids, e = p.SkillTrialSourceIDs(testCtx, other, trial.ID)
	if e != nil || len(ids) != 0 {
		t.Fatal("trial scope", ids, e)
	}
	app := restrictedPostgres(t, p)
	var role string
	if e = app.q().QueryRow(testCtx, "SELECT current_user").Scan(&role); e != nil {
		t.Fatal(e)
	}
	if _, e = p.q().Exec(testCtx, "GRANT SELECT ON source_events,execution_traces TO "+pgx.Identifier{role}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	if e = p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "learner", Role: "service", Token: "learner", OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "apply:skills", "auto_learn:skills", "read:sources", "read:execution_traces", "read:evaluations"}}); e != nil {
		t.Fatal(e)
	}
	f.svc.Repo = app
	f.svc.Auth, e = NewPostgresAuthorizer(app)
	if e != nil {
		t.Fatal(e)
	}
	bound := ContextWithPostgresToken(testCtx, "learner")
	lease, e := f.svc.ClaimSkillTrialCheck(bound, "learner", trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: candidate.SkillID})
	if e != nil {
		t.Fatal("claim under application RLS", e)
	}
	got, e := f.svc.ApplySkill(bound, "learner", candidate.SkillID, in, false)
	if e != nil || got.ActiveVersion != candidate.ID {
		t.Fatal("promote canonical trial under application RLS", e)
	}
	if e = f.svc.FinishSkillTrialCheck(bound, "learner", trial.ID, SkillTrialCheckFinish{Scope: scopeA, SkillID: candidate.SkillID, ExpectedRevision: lease.Revision, LeaseToken: lease.LeaseToken, Outcome: "checked"}); e != nil {
		t.Fatal("finish under application RLS", e)
	}

}
