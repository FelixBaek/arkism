package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func privacyWorkerFixture(t *testing.T) (*Service, *privacyAdvanceRepo, string) {
	t.Helper()
	base := newFakeRepo()
	repo := &privacyAdvanceRepo{fakeRepo: base, nextID: "privacy-fairness"}
	now := time.Now().UTC()
	pd := &PrivacyDeletion{State: privacyPurgingDerived, RequestHash: ContentHash("fairness"), LegalHoldStatus: privacyHoldNone}
	pd.RuntimeReceipt = &PrivacyRuntimeReceipt{RequestHash: pd.RequestHash, Quarantined: true, Verified: true}
	if err := repo.Insert(testCtx, colJobs, Record{ID: repo.nextID, Scope: scopeA, Kind: privacyDeletionKind, Status: JobStatusPending, Revision: 1, PrivacyDeletion: pd, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Stale index jobs occur after a target was deleted. A busy queue must not
	// prevent the separate canonical deletion phases from making progress.
	for _, id := range []string{"embedding-a", "embedding-b"} {
		if err := repo.Insert(testCtx, colJobs, Record{ID: id, Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, Target: &Ref{Collection: "memories", ID: "already-deleted", Revision: 1}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{Repo: repo, Auth: &fakeAuth{actors: map[string]fakeActor{"worker": actor("worker", "service", false, "purge")}}}
	return svc, repo, "worker"
}

func TestWorkOnceAdvancesPrivacyWhileIndexQueueIsBusy(t *testing.T) {
	svc, repo, token := privacyWorkerFixture(t)
	if err := svc.WorkOnce(testCtx, token); err != nil {
		t.Fatal(err)
	}
	row, err := repo.Get(testCtx, colJobs, scopeA, repo.nextID)
	if err != nil || row.PrivacyDeletion.State != privacyVerifying || row.Revision != 2 {
		t.Fatalf("busy index queue starved privacy phase: state=%s revision=%d error=%v", row.PrivacyDeletion.State, row.Revision, err)
	}
	done := 0
	for _, r := range repo.rows(colJobs) {
		if r.Kind == "embedding" && r.Status == JobStatusDone {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("canonical privacy phase starved ordinary work: finished=%d", done)
	}
}

type privacyLookupFailure struct{ *privacyAdvanceRepo }

type privacyFinishFailure struct{ *privacyAdvanceRepo }

func (privacyFinishFailure) FinishJob(context.Context, Record, bool) error {
	return ErrNotFound
}

func TestWorkOnceDoesNotHideJobFinishFailureAfterPrivacyProgress(t *testing.T) {
	svc, repo, token := privacyWorkerFixture(t)
	svc.Repo = privacyFinishFailure{repo}
	if err := svc.WorkOnce(testCtx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("privacy progress hid ordinary job completion failure: %v", err)
	}
}

func (privacyLookupFailure) NextPrivacyAdvance(context.Context, time.Time) (Record, error) {
	return Record{}, ErrUnavailable
}

func TestWorkOncePrivacyFailureDoesNotBlockIndexQueue(t *testing.T) {
	svc, repo, token := privacyWorkerFixture(t)
	svc.Repo = privacyLookupFailure{repo}
	if err := svc.WorkOnce(testCtx, token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("privacy failure must remain visible: %v", err)
	}
	done := 0
	for _, r := range repo.rows(colJobs) {
		if r.Kind == "embedding" && r.Status == JobStatusDone {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("privacy failure blocked ordinary queue: finished=%d", done)
	}
}

func TestPostgresWorkOnceAdvancesPrivacyWhileIndexQueueIsBusy(t *testing.T) {
	p := isolatedPostgres(t)
	svc, fixture, token := privacyWorkerFixture(t)
	for _, row := range fixture.rows(colJobs) {
		if err := p.Insert(testCtx, colJobs, row); err != nil {
			t.Fatal(err)
		}
	}
	svc.Repo = p
	if err := svc.WorkOnce(testCtx, token); err != nil {
		t.Fatal(err)
	}
	row, err := p.Get(testCtx, colJobs, scopeA, fixture.nextID)
	if err != nil || row.PrivacyDeletion.State != privacyVerifying {
		t.Fatalf("privacy did not advance with queued embeddings: %+v %v", row.PrivacyDeletion, err)
	}
	done := 0
	for _, row := range pgCollection(t, p, colJobs) {
		if row.Kind == "embedding" && row.Status == JobStatusDone {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("ordinary queue did not progress: done=%d", done)
	}
}
