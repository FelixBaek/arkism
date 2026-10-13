package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func generatedCandidate(scope Scope, refs []SourceRef) CreateInput {
	return candidateInput(CreateInput{Scope: scope, Text: "automatic proposal", Sources: refs, Generator: &Generator{Model: "test-model", PromptVersion: "v1", PipelineVersion: "v1"}})
}
func TestCandidateDailyQuota(t *testing.T) {
	f := newFixture(t)
	f.svc.CandidateDailyLimit = 2
	src := f.source()
	in := generatedCandidate(scopeA, refs(src))
	for i := 0; i < 2; i++ {
		if _, e := f.svc.Create(testCtx, f.agent, "memory_candidates", in); e != nil {
			t.Fatal(e)
		}
	}
	out, e := f.svc.Create(testCtx, f.human, "memory_candidates", in)
	if !errors.Is(e, ErrCandidateQuota) || out.ID != "" {
		t.Fatal("generated human bypass", out, e)
	}
	if code, status := errorResponse(e); status != 429 || code != "candidate_daily_limit" {
		t.Fatal(code, status)
	}
	// Human-written proposals do not consume the automatic extraction budget.
	in.Generator = nil
	if _, e = f.svc.Create(testCtx, f.human, "memory_candidates", in); e != nil {
		t.Fatal(e)
	}
	if len(f.repo.rows("memory_candidates")) != 3 {
		t.Fatal("unexpected candidate count")
	}
	// Deleting a generated candidate does not refund the day's budget.
	for _, r := range f.repo.rows("memory_candidates") {
		if r.CreatedByType == "agent" {
			if e = f.svc.Delete(testCtx, f.human, "memory_candidates", r.ID, ChangeInput{Scope: r.Scope, ExpectedRevision: r.Revision}); e != nil {
				t.Fatal(e)
			}
			break
		}
	}
	if _, e = f.svc.Create(testCtx, f.agent, "memory_candidates", generatedCandidate(scopeA, refs(src))); !errors.Is(e, ErrCandidateQuota) {
		t.Fatal("delete refunded quota", e)
	}
	// A new project's automated writes share the same deployment budget.
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeB, Text: "other source"}))
	if _, e = f.svc.Create(testCtx, f.agent, "memory_candidates", generatedCandidate(scopeB, refs(other))); !errors.Is(e, ErrCandidateQuota) {
		t.Fatal("scope bypass", e)
	}
}
func TestCandidateQuotaRollbackAndConfiguration(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	in := generatedCandidate(scopeA, refs(src))
	f.svc.CandidateDailyLimit = 0
	if out, e := f.svc.Create(testCtx, f.agent, "memory_candidates", in); !errors.Is(e, ErrUnavailable) || out.ID != "" {
		t.Fatal(out, e)
	}
	f.svc.CandidateDailyLimit = 1
	f.repo.failAudit = true
	if _, e := f.svc.Create(testCtx, f.agent, "memory_candidates", in); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	for _, r := range f.repo.rows("jobs") {
		if r.Kind == "candidate_quota" {
			t.Fatal("quota survived rollback")
		}
	}
	f.repo.failAudit = false
	if _, e := f.svc.Create(testCtx, f.agent, "memory_candidates", in); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.Create(testCtx, f.agent, "memory_candidates", in); !errors.Is(e, ErrCandidateQuota) {
		t.Fatal(e)
	}
}
func TestCandidateQuotaUTCDayAndNoRefund(t *testing.T) {
	f := newFixture(t)
	at := time.Date(2026, 9, 20, 23, 59, 59, 0, time.UTC)
	for _, stamp := range []time.Time{at, at.Add(time.Second)} {
		if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return reserveCandidate(ctx, tx, scopeA, stamp, 1) }); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return reserveCandidate(ctx, tx, scopeA, at.In(time.FixedZone("KST", 9*3600)), 1)
	}); !errors.Is(e, ErrCandidateQuota) {
		t.Fatal("timezone reset budget", e)
	}
	// Raising and then lowering the configured ceiling does not reset usage.
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return reserveCandidate(ctx, tx, scopeA, at, 2) }); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return reserveCandidate(ctx, tx, scopeA, at, 1) }); !errors.Is(e, ErrCandidateQuota) {
		t.Fatal(e)
	}
}
