package memory

import (
	"errors"
	"slices"
	"testing"
)

func TestPostgresPrivacyQuarantineBlocksGenericDelete(t *testing.T) {
	for _, col := range []string{"memory_candidates", "memories", "evaluations"} {
		t.Run(col, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := deletionReplayFixture(t, p)
			f.auth.actors[f.human].verbs["hold"] = true
			source := f.source()
			id := "held-" + col
			if err := p.Insert(testCtx, col, Record{ID: id, Scope: scopeA, Revision: 1, Status: statusActive, Text: "retained-body", Sources: refs(source)}); err != nil {
				t.Fatal(err)
			}
			request, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "delete-source", TargetType: "source_event", TargetIDs: []string{source.ID}, Reason: "owner_request"})
			if err != nil {
				t.Fatal(err)
			}
			row, err := p.Get(testCtx, col, scopeA, id)
			if err != nil || !row.PrivacyQuarantined {
				t.Fatal("fixture was not quarantined", err)
			}
			if err := f.svc.Delete(testCtx, f.human, col, id, ChangeInput{Scope: scopeA, ExpectedRevision: row.Revision}); !errors.Is(err, ErrConflict) {
				t.Fatal("generic delete bypassed pending quarantine", err)
			}
			if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"}); err != nil {
				t.Fatal(err)
			}
			jobs := len(pgCollection(t, p, colJobs))
			if err := f.svc.Delete(testCtx, f.human, col, id, ChangeInput{Scope: scopeA, ExpectedRevision: row.Revision}); !errors.Is(err, ErrConflict) {
				t.Fatal("generic delete bypassed quarantine/hold", err)
			}
			after, err := p.Get(testCtx, col, scopeA, id)
			if err != nil || after.Revision != row.Revision || after.Text != row.Text || len(pgCollection(t, p, colJobs)) != jobs {
				t.Fatal("held data or jobs changed", err)
			}
			if err := p.Insert(testCtx, col, Record{ID: "unrelated", Scope: scopeA, Revision: 1, Status: statusActive, Text: "ordinary-record"}); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.Delete(testCtx, f.human, col, "unrelated", ChangeInput{Scope: scopeA, ExpectedRevision: 1}); err != nil {
				t.Fatal("ordinary delete blocked", err)
			}
		})
	}
}

func TestPostgresPrivacyQuarantinedProposalPreservesMemory(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	source := f.source()
	candidate := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "paired-private", Sources: refs(source)}))
	memory, err := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := p.Get(testCtx, "memory_candidates", scopeA, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	proposal.PrivacyQuarantined = true
	proposal.Revision++
	if err := p.Replace(testCtx, "memory_candidates", proposal, proposal.Revision-1); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(testCtx, f.human, "memories", memory.ID, ChangeInput{Scope: scopeA, ExpectedRevision: memory.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatal("paired delete bypassed proposal quarantine", err)
	}
	for col, row := range map[string]Record{"memories": memory, "memory_candidates": proposal} {
		after, err := p.Get(testCtx, col, scopeA, row.ID)
		if err != nil || after.Revision != row.Revision || after.Text != row.Text {
			t.Fatal("paired data changed", col, err)
		}
	}
}

func deletionReplayFixture(t *testing.T, p *Postgres) *fixture {
	f := newFixture(t)
	f.svc.Repo = p
	f.auth.actors[f.human] = actor("human-1", "human", true, append(allVerbs, "export")...)
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "erase", "purge", "embed")
	return f
}
func drainDeletionReplayFixture(t *testing.T, f *fixture, p *Postgres) {
	t.Helper()
	for {
		claim, err := f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: scopeA})
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(claim.ExpectedRunIDs) != 0 {
			t.Fatal("fixture has unverified external runtime state")
		}
		_, err = f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, claim.DeletionRequestID, PrivacyRuntimeReceiptInput{Scope: scopeA, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken, Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Verified: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		pending := false
		for _, row := range pgCollection(t, p, colJobs) {
			if row.Kind == privacyDeletionKind && row.PrivacyDeletion.State != privacyWaiting {
				pending = true
				if row.PrivacyDeletion.State != privacyPurgingDerived && row.PrivacyDeletion.State != privacyVerifying {
					t.Fatal("unexpected privacy state", row.PrivacyDeletion.State)
				}
				if _, err := f.svc.AdvancePrivacyDeletion(testCtx, f.work, row.ID, PrivacyRevisionInput{Scope: scopeA, ExpectedRevision: row.Revision}); err != nil {
					t.Fatal(err)
				}
			}
			if slices.Contains(erasureJobKinds, row.Kind) && row.Status != JobStatusDone {
				pending = true
				if row.Status == JobStatusFailed {
					t.Fatal("erasure job failed")
				}
			}
		}
		if !pending {
			return
		}
		if err := f.svc.WorkOnce(testCtx, f.work); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	t.Fatal("mixed deletion did not finish")
}
