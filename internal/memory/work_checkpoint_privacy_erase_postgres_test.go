package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func inPrivacyTx(t *testing.T, p *Postgres, fn func(context.Context, *Postgres) error) error {
	t.Helper()
	return p.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx.(*Postgres)) })
}

// Erasing work checkpoints leaves a tombstone plus a receipt; verification
// refuses anything less, and erasure refuses rows that were never quarantined.
func TestPostgresWorkCheckpointPrivacyEraseAndVerifyGuards(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	for _, id := range []string{"wp.erase", "wp.keep"} {
		if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, id, "event", 0, workCheckpointPackage())); err != nil {
			t.Fatal(err)
		}
	}
	impact, err := p.DiscoverPrivacyImpact(testCtx, scopeA, "work_checkpoint", []string{"wp.erase"}, 10)
	if err != nil || len(impact.Records) != 2 || impact.Records[0].ID != "wp.erase" || impact.Records[1].Collection != colWorkCheckpointReceipts {
		t.Fatalf("selected impact = %+v %v", impact, err)
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scopeA, "work_checkpoint", []string{"wp.erase", "wp.keep"}, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("selected impact over limit was truncated", err)
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scopeA, "work_checkpoint", []string{"wp.missing"}, 10); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing checkpoint", err)
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scopeB, "work_checkpoint", []string{"wp.erase"}, 10); !errors.Is(err, ErrNotFound) {
		t.Fatal("checkpoint visible from another scope", err)
	}
	if err := inPrivacyTx(t, p, func(ctx context.Context, pp *Postgres) error {
		return eraseWorkPrivacyImpact(ctx, pp, scopeA, impact, "privacy-deletion-x")
	}); !errors.Is(err, ErrConflict) {
		t.Fatal("erased a checkpoint that was never quarantined", err)
	}
	if row, err := p.Get(testCtx, colWorkCheckpoints, scopeA, "wp.erase"); err != nil || row.ContentErased || row.WorkCheckpoint == nil {
		t.Fatal("refused erase changed the checkpoint", err)
	}
	if _, err := p.Get(testCtx, colJobs, scopeA, workDeletionID(scopeA, "wp.erase")); !errors.Is(err, ErrNotFound) {
		t.Fatal("refused erase left a deletion receipt", err)
	}
	deletion, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "erase", TargetType: "work_checkpoint", TargetIDs: []string{"wp.erase"}, Reason: "owner_request"})
	if err != nil {
		t.Fatal(err)
	}
	impact = deletion.PrivacyDeletion.Impact
	verify := func() error {
		return inPrivacyTx(t, p, func(ctx context.Context, pp *Postgres) error { return verifyWorkPrivacyImpact(ctx, pp, scopeA, impact) })
	}
	if err := verify(); !errors.Is(err, ErrConflict) {
		t.Fatal("verified a quarantined but readable checkpoint", err)
	}
	for i := 0; i < 2; i++ { // the second pass meets the tombstone and is a no-op
		if err := inPrivacyTx(t, p, func(ctx context.Context, pp *Postgres) error {
			return eraseWorkPrivacyImpact(ctx, pp, scopeA, impact, deletion.ID)
		}); err != nil {
			t.Fatal(err)
		}
	}
	row, err := p.Get(testCtx, colWorkCheckpoints, scopeA, "wp.erase")
	if err != nil || !row.ContentErased || row.WorkCheckpoint != nil || row.Status != "deleted" || row.RequestHash != "" {
		t.Fatalf("tombstone = %+v %v", row, err)
	}
	if keep, err := p.Get(testCtx, colWorkCheckpoints, scopeA, "wp.keep"); err != nil || keep.ContentErased || keep.PrivacyQuarantined || keep.WorkCheckpoint == nil {
		t.Fatal("unrelated checkpoint touched", err)
	}
	if err := verify(); err != nil {
		t.Fatal("clean verify", err)
	}
	receiptID := workDeletionID(scopeA, "wp.erase")
	receipt, err := p.Get(testCtx, colJobs, scopeA, receiptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(testCtx, colJobs, scopeA, receiptID, receipt.Revision); err != nil {
		t.Fatal(err)
	}
	if err := verify(); !errors.Is(err, ErrNotFound) {
		t.Fatal("verified without a deletion receipt", err)
	}
	forged := receipt
	forged.Target = &Ref{Collection: colWorkCheckpoints, ID: "wp.keep", Revision: 1}
	if err := p.Insert(testCtx, colJobs, forged); err != nil {
		t.Fatal(err)
	}
	if err := verify(); !errors.Is(err, ErrConflict) {
		t.Fatal("verified with a receipt for another checkpoint", err)
	}
	missing := PrivacyImpact{Records: []Ref{{Collection: colWorkCheckpoints, ID: "wp.never", Revision: 1}}}
	if err := inPrivacyTx(t, p, func(ctx context.Context, pp *Postgres) error {
		return verifyWorkPrivacyImpact(ctx, pp, scopeA, missing)
	}); !errors.Is(err, ErrConflict) {
		t.Fatal("absence accepted as proof of deletion", err)
	}
}

func TestPostgresWorkCheckpointPrivacyRejectsForeignKindAndFencesSubject(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := p.Insert(testCtx, colWorkCheckpoints, Record{ID: "wp.foreign", Scope: scopeA, Kind: "note", Status: statusActive, Revision: 1, Text: "not a checkpoint", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scopeA, "work_checkpoint", []string{"wp.foreign"}, 10); !errors.Is(err, ErrConflict) {
		t.Fatal("non-checkpoint row selected as work", err)
	}
	subject := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "a"}
	fence := func(s Scope) error {
		return inPrivacyTx(t, p, func(ctx context.Context, pp *Postgres) error { return pp.FenceWorkSubject(ctx, s) })
	}
	if err := fence(subject); err != nil {
		t.Fatal("no deletion, fence closed", err)
	}
	pd := &PrivacyDeletion{State: privacyQuarantined, TargetType: "subject", TargetIDs: []string{"u"}, RequestHash: ContentHash("s"), LegalHoldStatus: privacyHoldNone}
	job := Record{ID: "privacy-deletion-subject", Scope: subject, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, PrivacyDeletion: pd}
	if err := p.Insert(testCtx, colJobs, job); err != nil {
		t.Fatal(err)
	}
	if err := fence(subject); !errors.Is(err, ErrDenied) {
		t.Fatal("new work accepted for a subject being deleted", err)
	}
	other := subject
	other.UserID = "v"
	if err := fence(other); err != nil {
		t.Fatal("another subject fenced", err)
	}
	job.PrivacyDeletion.State, job.Revision = privacyCancelled, 2
	if err := p.Replace(testCtx, colJobs, job, 1); err != nil {
		t.Fatal(err)
	}
	if err := fence(subject); err != nil {
		t.Fatal("cancelled deletion still fences", err)
	}
	if err := inPrivacyTx(t, p, func(ctx context.Context, pp *Postgres) error { return pp.FenceWorkSubject(ctx, Scope{}) }); !errors.Is(err, ErrInvalid) {
		t.Fatal("fence with bad scope", err)
	}
}
