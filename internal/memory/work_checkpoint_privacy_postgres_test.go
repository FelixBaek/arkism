package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestPostgresWorkCheckpointPrivacyLifecycle(t *testing.T) {
	for _, target := range []string{"source_event", "derived_memory", "subject", "work_checkpoint"} {
		t.Run(target, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := newFixture(t)
			f.svc.Repo = p
			f.auth.actors[f.human] = actor("human-1", "human", true, append(allVerbs, "hold", "release", "export")...)
			f.auth.actors[f.work] = actor("worker-1", "service", true, append(allVerbs, "purge", "erase")...)
			scope := scopeA
			scope.UserID = "human-1"
			source := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scope, Text: "private-work-marker"}))
			pkg := workCheckpointPackage()
			pkg.Goal = "private-work-marker"
			if target == "source_event" || target == "work_checkpoint" {
				pkg.SourceRefs = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
			}
			if target == "derived_memory" {
				candidate := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scope, Text: "derived-private-work", Sources: refs(source)}))
				approved, err := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scope, ExpectedRevision: candidate.Revision, Action: "approve"})
				if err != nil {
					t.Fatal(err)
				}
				pkg.SourceRefs = []Ref{{Collection: "memories", ID: approved.ID, Revision: approved.Revision}}
			}
			input := workCheckpointPGInput(scope, "private-work", "event", 0, pkg)
			if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, input); err != nil {
				t.Fatal(err)
			}
			foreign := input
			foreign.Scope = scopeB
			foreign.ID = "foreign-work"
			foreign.Package.SourceRefs = nil
			if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, foreign); err != nil {
				t.Fatal(err)
			}
			targetID := source.ID
			if target == "work_checkpoint" {
				targetID = input.ID
			}
			if target == "subject" {
				targetID = scope.UserID
			}
			requestType := target
			if target == "derived_memory" {
				requestType = "source_event"
			}
			if target == "work_checkpoint" {
				request := PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "role-denied", TargetType: target, TargetIDs: []string{input.ID}, Reason: "owner_request"}
				for _, token := range []string{f.agent, f.work} {
					if _, err := f.svc.RequestPrivacyDeletion(testCtx, token, request); !errors.Is(err, ErrDenied) {
						t.Fatalf("nonhuman delete: %v", err)
					}
				}
				request.IdempotencyKey = "stale"
				request.ExpectedRevision = 99
				if _, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, request); !errors.Is(err, ErrConflict) {
					t.Fatalf("stale delete: %v", err)
				}
				request.Scope = scopeB
				request.ExpectedRevision = 0
				if _, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, request); !errors.Is(err, ErrNotFound) {
					t.Fatalf("foreign target delete: %v", err)
				}
			}
			deletion, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "erase-work", TargetType: requestType, TargetIDs: []string{targetID}, Reason: "owner_request", RetentionPolicyRef: retentionExternal})
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, ref := range deletion.PrivacyDeletion.Impact.Records {
				counts[ref.Collection]++
			}
			if counts[colWorkCheckpoints] != 1 || counts[colWorkCheckpointReceipts] != 1 {
				t.Fatalf("missing work/receipt impact: %v", counts)
			}
			if err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				return verifyWorkPrivacyImpact(ctx, tx, scope, deletion.PrivacyDeletion.Impact)
			}); !errors.Is(err, ErrConflict) {
				t.Fatalf("verifier accepted retained work: %v", err)
			}
			held, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, deletion.ID, PrivacyHoldInput{Scope: scope, ExpectedRevision: deletion.Revision, Reason: "retain_for_review", PolicyRef: "owner_hold"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scope, ID: input.ID}); err == nil {
				t.Fatal("quarantined work readable")
			}
			if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, input); err == nil {
				t.Fatal("quarantined replay succeeded")
			}
			if _, err := f.svc.AdvancePrivacyDeletion(testCtx, f.work, deletion.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: held.Revision}); err == nil {
				t.Fatal("hold allowed purge")
			}
			retained, err := p.Get(testCtx, colWorkCheckpoints, scope, input.ID)
			if err != nil || retained.WorkCheckpoint == nil || retained.ContentErased {
				t.Fatal("hold lost content", err)
			}
			_, err = f.svc.ReleasePrivacyDeletionHold(testCtx, f.human, deletion.ID, PrivacyHoldInput{Scope: scope, ExpectedRevision: held.Revision, Reason: "review_finished", PolicyRef: "owner_release"})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: scope})
			if err != nil {
				t.Fatal(err)
			}
			// This fixture creates no external runtime sessions.
			status, err := f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, deletion.ID, PrivacyRuntimeReceiptInput{Scope: scope, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken, Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Verified: true, ExpectedRunIDs: claim.ExpectedRunIDs}})
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 100 && status.PrivacyDeletion.State != privacyWaiting; n++ {
				if status.PrivacyDeletion.State == privacyPurgingDerived || status.PrivacyDeletion.State == privacyVerifying {
					status, err = f.svc.AdvancePrivacyDeletion(testCtx, f.work, deletion.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision})
					if err != nil {
						t.Fatal(err)
					}
				}
				if err = f.svc.WorkOnce(testCtx, f.work); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
				status, err = f.svc.GetPrivacyDeletion(testCtx, f.human, deletion.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
			}
			if status.PrivacyDeletion.State != privacyWaiting {
				t.Fatalf("purge unfinished: %s", status.PrivacyDeletion.State)
			}
			for _, ref := range deletion.PrivacyDeletion.Impact.Records {
				if ref.Collection != colWorkCheckpoints && ref.Collection != colWorkCheckpointReceipts {
					continue
				}
				row, err := p.Get(testCtx, ref.Collection, scope, ref.ID)
				if err != nil || !row.ContentErased || row.WorkCheckpoint != nil || row.Target != nil {
					t.Fatalf("unpurged %s: %v", ref.Collection, err)
				}
				var doc string
				if err = p.q().QueryRow(testCtx, `SELECT doc::text FROM memory_records WHERE collection=$1 AND id=$2 AND user_id=$3`, ref.Collection, ref.ID, scope.UserID).Scan(&doc); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(doc, "private-work-marker") || strings.Contains(doc, "request_hash") || strings.Contains(doc, "receipt_target_id") {
					t.Fatal("private fields remain in document")
				}
			}
			if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, input); err == nil {
				t.Fatal("identical pre-deletion request succeeded")
			}
			for _, event := range []string{"event", "new-event"} {
				retry := input
				retry.EventID = event
				retry.Package.SourceRefs = nil
				if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, retry); err == nil {
					t.Fatal("erased work resurrected")
				}
			}
			if target == "subject" {
				fresh := input
				fresh.ID = "new-work"
				fresh.Package.SourceRefs = nil
				if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, fresh); !errors.Is(err, ErrDenied) {
					t.Fatalf("subject recreated work: %v", err)
				}
			}
			ledger, err := f.svc.ExportDeletions(testCtx, f.human, scope)
			if err != nil {
				t.Fatal("ledger export", err)
			}
			found := false
			for _, ref := range ledger.Targets {
				if ref.Collection == colWorkCheckpoints && ref.ID == input.ID {
					found = true
				}
			}
			if !found {
				t.Fatal("work missing from deletion ledger")
			}
			if target == "work_checkpoint" {
				preserved, err := p.Get(testCtx, "sources", scope, source.ID)
				if err != nil || !live(preserved) || preserved.Text != "private-work-marker" {
					t.Fatal("individual work deletion changed original", err)
				}
			}
			if _, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scopeB, ID: foreign.ID}); err != nil {
				t.Fatal("foreign scope changed", err)
			}
		})
	}
}

func TestPostgresWorkCheckpointSubjectDeletionRace(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	scope := scopeA
	scope.UserID = "human-1"
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var saveErr, deleteErr error
	var deletion PrivacyDeletionStatus
	go func() {
		defer wg.Done()
		<-start
		_, saveErr = f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scope, "racing-work", "event", 0, workCheckpointPackage()))
	}()
	go func() {
		defer wg.Done()
		<-start
		deletion, deleteErr = f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "race-delete", TargetType: "subject", TargetIDs: []string{scope.UserID}, Reason: "owner_request", RetentionPolicyRef: retentionExternal})
	}()
	close(start)
	wg.Wait()
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if saveErr != nil && !errors.Is(saveErr, ErrDenied) {
		t.Fatal(saveErr)
	}
	if saveErr == nil {
		found := false
		for _, ref := range deletion.PrivacyDeletion.Impact.Records {
			if ref.Collection == colWorkCheckpoints && ref.ID == "racing-work" {
				found = true
			}
		}
		if !found {
			t.Fatal("concurrent work escaped subject discovery")
		}
	}
	if _, err := f.svc.GetWorkCheckpoint(testCtx, f.agent, WorkCheckpointGetInput{Scope: scope, ID: "racing-work"}); err == nil {
		t.Fatal("concurrent work readable after subject deletion")
	}
}

func finishWorkOnlyPrivacy(t *testing.T, f *fixture, scope Scope, id string) {
	t.Helper()
	claim, err := f.svc.ClaimPrivacyDeletion(testCtx, f.work, PrivacyScopeInput{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if claim.DeletionRequestID != id || len(claim.ExpectedRunIDs) != 0 || len(claim.SourceEventIDs) != 0 {
		t.Fatal("fixture unexpectedly has external runtime evidence")
	}
	status, err := f.svc.RecordPrivacyRuntimeReceipt(testCtx, f.work, id, PrivacyRuntimeReceiptInput{Scope: scope, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken, Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Verified: true}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		status, err = f.svc.AdvancePrivacyDeletion(testCtx, f.work, id, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision})
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.PrivacyDeletion.State != privacyWaiting {
		t.Fatal("work deletion did not reach backup boundary", status.PrivacyDeletion.State)
	}
}
