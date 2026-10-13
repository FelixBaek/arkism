package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestPostgresPrivacyOverlappingHoldBlocksClaim(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	f.auth.actors[f.human].verbs["hold"] = true
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "shared", "event", 0, workCheckpointPackage())); err != nil {
		t.Fatal(err)
	}
	requests := []PrivacyDeletionStatus{}
	for _, key := range []string{"first", "second"} {
		r, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: key, TargetType: "work_checkpoint", TargetIDs: []string{"shared"}, Reason: "owner_request"})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, requests[0].ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: requests[0].Revision, Reason: "retain", PolicyRef: "owner_hold"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		claim, err := f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: scopeA})
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if claim.HoldStatus != privacyHoldHeld {
			t.Fatal("overlapping hold bypassed by another deletion request")
		}
		if _, err = f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, claim.DeletionRequestID, PrivacyRuntimeReceiptInput{Scope: scopeA, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken, Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Held: true}}); err != nil {
			t.Fatal(err)
		}
	}
	row, err := p.Get(testCtx, colWorkCheckpoints, scopeA, "shared")
	if err != nil || row.ContentErased || row.WorkCheckpoint == nil {
		t.Fatal("held work lost", err)
	}
	if _, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "third", TargetType: "work_checkpoint", TargetIDs: []string{"shared"}, Reason: "owner_request"}); !errors.Is(err, ErrConflict) {
		t.Fatal("new overlapping request accepted", err)
	}
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "unrelated", "event", 0, workCheckpointPackage())); err != nil {
		t.Fatal(err)
	}
	unrelated, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "unrelated", TargetType: "work_checkpoint", TargetIDs: []string{"unrelated"}, Reason: "owner_request"})
	if err != nil {
		t.Fatal(err)
	}
	// If the initial blocked candidate came first, the held quarantine-only
	// request may still need its own claim before unrelated purge can proceed.
	for i := 0; i < 2; i++ {
		claim, err := f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: scopeA})
		if err != nil {
			t.Fatal("unrelated queue blocked", err)
		}
		if claim.DeletionRequestID == unrelated.ID {
			return
		}
		if claim.HoldStatus != privacyHoldHeld {
			t.Fatal("held target claimed for purge")
		}
		if _, err := f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, claim.DeletionRequestID, PrivacyRuntimeReceiptInput{Scope: scopeA, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken, Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Held: true}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("unrelated request not claimed")
}

func TestPostgresPrivacyOverlappingHoldClaimRace(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	f.auth.actors[f.human].verbs["hold"] = true
	for attempt := 0; attempt < 12; attempt++ {
		scope := scopeA
		scope.ProjectID = fmt.Sprintf("overlap-race-%d", attempt)
		workID := fmt.Sprintf("shared-%d", attempt)
		if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scope, workID, "event", 0, workCheckpointPackage())); err != nil {
			t.Fatal(err)
		}
		a, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "hold", TargetType: "work_checkpoint", TargetIDs: []string{workID}, Reason: "owner_request"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "claim", TargetType: "work_checkpoint", TargetIDs: []string{workID}, Reason: "owner_request"}); err != nil {
			t.Fatal(err)
		}
		var holdErr, claimErr error
		var claim PrivacyRuntimeAction
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, holdErr = f.svc.HoldPrivacyDeletion(testCtx, f.human, a.ID, PrivacyHoldInput{Scope: scope, ExpectedRevision: a.Revision, Reason: "retain", PolicyRef: "owner_hold"})
		}()
		go func() {
			defer wg.Done()
			<-start
			claim, claimErr = f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: scope})
		}()
		close(start)
		wg.Wait()
		if holdErr == nil && claimErr == nil && claim.HoldStatus != privacyHoldHeld {
			t.Fatal("hold and overlapping purge claim both committed")
		}
		if holdErr != nil && !errors.Is(holdErr, ErrConflict) {
			t.Fatal("unexpected hold error", holdErr)
		}
		if claimErr != nil && !errors.Is(claimErr, ErrNotFound) && !errors.Is(claimErr, ErrConflict) {
			t.Fatal("unexpected claim error", claimErr)
		}
		if holdErr != nil && claimErr != nil {
			t.Fatal("neither side progressed", holdErr, claimErr)
		}
	}
}

func TestPostgresPrivacyOverlappingPredicate(t *testing.T) {
	p := isolatedPostgres(t)
	for _, mode := range []string{"source", "trace", "thread", "run", "record", "subject", "other-subject", "disjoint", "other-collection", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			scope := scopeA
			scope.ProjectID = "overlap-" + mode
			r := recoveryLedgerRequest(scope, mode)
			r.PrivacyDeletion.State, r.PrivacyDeletion.LegalHoldStatus = privacyHeld, privacyHoldHeld
			impact := PrivacyImpact{}
			targetType := "work_checkpoint"
			blocked := true
			switch mode {
			case "source":
				impact.SourceEventIDs = []string{"same"}
			case "trace":
				impact.TraceIDs = []string{"same"}
			case "thread":
				impact.ThreadIDs = []string{"same"}
			case "run":
				impact.ExpectedRunIDs = []string{"same"}
			case "record", "disjoint", "other-collection", "foreign":
				impact.Records = []Ref{{Collection: colWorkCheckpoints, ID: "same", Revision: 1}}
			case "subject":
				targetType = "subject"
			case "other-subject":
				r.PrivacyDeletion.TargetType = "subject"
			}
			r.PrivacyDeletion.Impact = impact
			if mode == "record" {
				impact.Records = []Ref{{Collection: colWorkCheckpoints, ID: "same", Revision: 9}}
			}
			if mode == "disjoint" {
				impact.Records = []Ref{{Collection: colWorkCheckpoints, ID: "different", Revision: 1}}
				blocked = false
			}
			if mode == "other-collection" {
				impact.Records = []Ref{{Collection: "sources", ID: "same", Revision: 1}}
				blocked = false
			}
			if mode == "foreign" {
				r.Scope.UserID = "another"
				blocked = false
			}
			if err := p.Insert(testCtx, colJobs, r); err != nil {
				t.Fatal(err)
			}
			err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				return checkPrivacyOverlap(ctx, tx, scope, "new", targetType, impact, false)
			})
			if blocked && !errors.Is(err, ErrConflict) || !blocked && err != nil {
				t.Fatal("wrong overlap result", err)
			}
		})
	}
}

func TestPostgresPrivacyOverlappingLateHoldAndRestoredAdvance(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	f.auth.actors[f.human].verbs["hold"] = true
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "shared", "event", 0, workCheckpointPackage())); err != nil {
		t.Fatal(err)
	}
	requests := []PrivacyDeletionStatus{}
	for _, key := range []string{"first", "second"} {
		r, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: key, TargetType: "work_checkpoint", TargetIDs: []string{"shared"}, Reason: "owner_request"})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	claim, err := f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: scopeA})
	if err != nil {
		t.Fatal(err)
	}
	other := requests[0]
	if other.ID == claim.DeletionRequestID {
		other = requests[1]
	}
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, other.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: other.Revision, Reason: "retain", PolicyRef: "owner_hold"}); !errors.Is(err, ErrConflict) {
		t.Fatal("late cross-request hold accepted", err)
	}
	// Simulate an inconsistent old backup: one request held, another already
	// past runtime purge. Canonical erasure must still refuse the held impact.
	r, err := p.Get(testCtx, colJobs, scopeA, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.PrivacyDeletion.State, r.PrivacyDeletion.LegalHoldStatus = privacyHeld, privacyHoldHeld
	r.Revision++
	if err := p.Replace(testCtx, colJobs, r, r.Revision-1); err != nil {
		t.Fatal(err)
	}
	advanced, err := f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, claim.DeletionRequestID, PrivacyRuntimeReceiptInput{Scope: scopeA, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken, Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Verified: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdvancePrivacyDeletion(testCtx, f.work, claim.DeletionRequestID, PrivacyRevisionInput{Scope: scopeA, ExpectedRevision: advanced.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatal("canonical purge bypassed restored hold", err)
	}
	work, err := p.Get(testCtx, colWorkCheckpoints, scopeA, "shared")
	if err != nil || work.ContentErased || work.WorkCheckpoint == nil {
		t.Fatal("held work erased", err)
	}
}

func TestPostgresPrivacyOverlappingSourceAndWork(t *testing.T) {
	for _, firstType := range []string{"source_event", "work_checkpoint"} {
		t.Run(firstType, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := deletionReplayFixture(t, p)
			f.auth.actors[f.human].verbs["hold"] = true
			source := f.source()
			pkg := workCheckpointPackage()
			pkg.SourceRefs = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
			if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "dependent-work", "event", 0, pkg)); err != nil {
				t.Fatal(err)
			}
			ids := map[string]string{"source_event": source.ID, "work_checkpoint": "dependent-work"}
			a, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "held", TargetType: firstType, TargetIDs: []string{ids[firstType]}, Reason: "owner_request"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.svc.HoldPrivacyDeletion(testCtx, f.human, a.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: a.Revision, Reason: "retain", PolicyRef: "owner_hold"}); err != nil {
				t.Fatal(err)
			}
			secondType := "source_event"
			if firstType == secondType {
				secondType = "work_checkpoint"
			}
			if _, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "overlap", TargetType: secondType, TargetIDs: []string{ids[secondType]}, Reason: "owner_request"}); !errors.Is(err, ErrConflict) {
				t.Fatal("cross-type hold bypassed", err)
			}
		})
	}
}
