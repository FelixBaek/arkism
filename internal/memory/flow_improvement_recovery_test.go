package memory

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type improvementSaveFailure struct {
	Repository
	fail  atomic.Bool
	stage string
}
type improvementSaveFailureTx struct {
	Tx
	owner *improvementSaveFailure
}

func (r *improvementSaveFailure) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, improvementSaveFailureTx{tx, r}) })
}
func (tx improvementSaveFailureTx) Replace(ctx context.Context, col string, r Record, revision int64) error {
	stage := tx.owner.stage
	if stage == "" {
		stage = "comparison_required"
	}
	if col == colJobs && r.Kind == flowImprovementKind && r.FlowImprovement.Pending == nil && r.FlowImprovement.Stage == stage && tx.owner.fail.CompareAndSwap(true, false) {
		return ErrUnavailable
	}
	return tx.Tx.Replace(ctx, col, r, revision)
}

func TestFlowImprovementRecoversOnlyExactRecordedComparison(t *testing.T) {
	for _, altered := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "altered"}[altered], func(t *testing.T) {
			f, _, start, _ := improvementFixture(t, false)
			v, e := f.svc.StartFlowImprovement(testCtx, f.agent, start)
			if e != nil {
				t.Fatal(e)
			}
			v = advanceImprovementFixture(t, f, v, f.agent, "generate", "generate", nil)
			comparison := improvementComparison(t, f, v)
			repo := &improvementSaveFailure{Repository: f.svc.Repo, stage: "approval_required"}
			repo.fail.Store(true)
			f.svc.Repo = repo
			in := FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "comparison", Action: "compare", Comparison: &comparison}
			if _, e = f.svc.AdvanceFlowImprovement(testCtx, "flow-checker", v.ID, in); !errors.Is(e, ErrUnavailable) {
				t.Fatal(e)
			}
			if altered {
				id := "comparison-" + ContentHash(structuredText(scopeA)+"\nflow-checker\nservice\n"+improvementEvent(v.ID, in.ActionID)) + "-source"
				source, e := f.repo.Get(testCtx, "sources", scopeA, id)
				if e != nil {
					t.Fatal(e)
				}
				source.Text = "An arbitrary success summary"
				if e = f.repo.Replace(testCtx, "sources", source, source.Revision); e != nil {
					t.Fatal(e)
				}
			}
			v, e = f.svc.GetFlowImprovement(testCtx, f.human, v.ID, scopeA)
			if e != nil {
				t.Fatal(e)
			}
			in.ExpectedRevision, in.Recover = v.Revision, true
			out, e := f.svc.AdvanceFlowImprovement(testCtx, "flow-checker", v.ID, in)
			if altered {
				if e == nil || out.Improvement.Stage != "comparison_required" {
					t.Fatal("altered comparison accepted", out, e)
				}
			} else if e != nil || out.Improvement.Stage != "approval_required" || out.Improvement.Pending != nil {
				t.Fatal(out, e)
			}
		})
	}
}

func TestFlowImprovementRecoversCommittedGenerationWithoutAnotherModelCall(t *testing.T) {
	f, _, start, _ := improvementFixture(t, false)
	v, e := f.svc.StartFlowImprovement(testCtx, f.agent, start)
	if e != nil {
		t.Fatal(e)
	}
	repo := &improvementSaveFailure{Repository: f.svc.Repo}
	repo.fail.Store(true)
	f.svc.Repo = repo
	in := FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "one-generation", Action: "generate"}
	if _, e = f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, in); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	f.svc.Drafter = nil
	v, e = f.svc.GetFlowImprovement(testCtx, f.human, v.ID, scopeA)
	if e != nil || v.Improvement.Pending == nil {
		t.Fatal(v, e)
	}
	if _, e = f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("uncertain work replayed", e)
	}
	in.ExpectedRevision = v.Revision
	in.Recover = true
	v, e = f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, in)
	if e != nil || v.Improvement.Stage != "comparison_required" || v.Improvement.Pending != nil {
		t.Fatal(v, e)
	}
	if got := len(f.repo.rows(colSkillVersions)); got != 1 {
		t.Fatal("duplicate candidate", got)
	}
}

func TestFlowImprovementConcurrentAdvanceReservesOneGeneration(t *testing.T) {
	f, _, start, _ := improvementFixture(t, false)
	v, e := f.svc.StartFlowImprovement(testCtx, f.agent, start)
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.svc.Drafter = generationHookDrafter{SkillDrafter: f.svc.Drafter, hook: func() { close(entered); <-release }}
	in := FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "one-generation", Action: "generate"}
	done := make(chan error, 1)
	go func() { _, err := f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, in); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}
	_, second := f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, in)
	close(release)
	if !errors.Is(second, ErrConflict) {
		t.Fatal("duplicate concurrent request", second)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if len(f.repo.rows(colSkillVersions)) != 1 {
		t.Fatal("duplicate candidate")
	}
}

func TestFlowImprovementCurrentAuthorityAndSourceRevisionAreRequired(t *testing.T) {
	for _, mode := range []string{"source_revision", "source_erased", "read_denied", "analysis_denied"} {
		t.Run(mode, func(t *testing.T) {
			f, _, start, _ := improvementFixture(t, false)
			v, e := f.svc.StartFlowImprovement(testCtx, f.agent, start)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "source_revision", "source_erased":
				r, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, start.SourceRun.ID)
				prev := r.Revision
				r.Revision++
				r.ContentErased = mode == "source_erased"
				if e = f.repo.Replace(testCtx, colCrewRuns, r, prev); e != nil {
					t.Fatal(e)
				}
			case "read_denied":
				f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:crew"}
			case "analysis_denied":
				f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "analyze:skills"}
			}
			if _, e = f.svc.AdvanceFlowImprovement(testCtx, f.agent, v.ID, FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "generate", Action: "generate"}); e == nil {
				t.Fatal("current authority/binding bypassed")
			}
			if len(f.repo.rows(colSkillVersions)) != 0 {
				t.Fatal("unauthorized generation")
			}
		})
	}
}

func TestFlowImprovementObservationRecoveryKeepsCanonicalPromotion(t *testing.T) {
	f, candidate, trial, input := skillAutomationFixture(t)
	if _, e := f.svc.ApplySkill(testCtx, testPrincipalToken("learner"), candidate.SkillID, input, false); e != nil {
		t.Fatal(e)
	}
	state := FlowImprovement{Stage: "trial_running", Trial: recordRef(colSkillTrials, trial)}
	r := Record{Scope: scopeA, SkillID: candidate.SkillID}
	if e := f.svc.recoverImprovementObservation(testCtx, testPrincipalToken("learner"), r, &state, "observe"); e != nil {
		t.Fatal(e)
	}
	if state.Stage != "monitoring" || state.ObservationState != "promoted" {
		t.Fatal("canonical promotion reset to trial", state)
	}
}

func TestFlowImprovementRecoveryMissingReceiptPreservesClaim(t *testing.T) {
	f, h, start, _ := improvementFixture(t, false)
	var modelCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { modelCalls.Add(1); w.WriteHeader(503) }))
	t.Cleanup(server.Close)
	f.svc.Drafter = &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "test"}}
	view, err := f.svc.StartFlowImprovement(testCtx, f.agent, start)
	if err != nil {
		t.Fatal(err)
	}
	in := FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: view.Revision, Action: "generate", ActionID: "live-generation"}
	row, err := f.svc.readFlowImprovement(testCtx, f.agent, view.ID, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := f.svc.improvementActionActor(testCtx, f.agent, row, in.Action)
	if err != nil {
		t.Fatal(err)
	}
	row, _, err = f.svc.claimImprovement(testCtx, f.agent, row, in, actor)
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved := func(t *testing.T, row Record, wantState string) {
		t.Helper()
		in.Recover = true
		in.ExpectedRevision = row.Revision
		_, err := f.svc.AdvanceFlowImprovement(testCtx, f.agent, view.ID, in)
		code, status := errorResponse(err)
		if !errors.Is(err, errGenerationReceiptMissing) || code != "generation_receipt_missing" || status != 409 {
			t.Fatal(err, code, status)
		}
		body := h.call("POST", "/v1/flow/improvements/"+view.ID+"/advance", f.agent, in, http.StatusConflict, nil)
		if !bytes.Contains(body, []byte("generation_receipt_missing")) || modelCalls.Load() != 0 {
			t.Fatal("HTTP status body or generation count", modelCalls.Load())
		}
		observed, err := f.svc.GetFlowImprovement(testCtx, f.agent, view.ID, scopeA)
		if err != nil || observed.Revision != row.Revision || observed.Improvement.Pending == nil || observed.Improvement.Pending.State != wantState || len(observed.Improvement.Receipts) != 0 {
			t.Fatal(wantState, observed, err)
		}
		competing := in
		competing.Recover = false
		competing.ActionID = "other-generation"
		if _, err = f.svc.AdvanceFlowImprovement(testCtx, f.agent, view.ID, competing); !errors.Is(err, ErrConflict) {
			t.Fatal("competing generation", err)
		}
	}
	t.Run("running", func(t *testing.T) { assertPreserved(t, row, "running") })
	row, err = f.svc.finishImprovement(testCtx, row, cloneImprovement(row.FlowImprovement), ErrUnavailable)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("unknown", func(t *testing.T) { assertPreserved(t, row, "unknown") })
}
