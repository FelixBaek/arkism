package memory

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func queuedPrivacyFixture(t *testing.T) (*Postgres, *fixture, Record, PrivacyDeletionStatus) {
	t.Helper()
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	f.auth.actors[f.human].verbs["hold"] = true
	f.auth.actors[f.human].verbs["release"] = true
	source := f.source()
	if err := p.Insert(testCtx, "memories", Record{ID: "deleted-memory", Scope: scopeA, Revision: 1, Status: statusActive, Text: "memory"}); err != nil {
		t.Fatal(err)
	}
	evaluation := Record{ID: "retained-evaluation", Scope: scopeA, Revision: 1, Text: "retained-body", Sources: refs(source), Target: &Ref{Collection: "memories", ID: "deleted-memory", Revision: 1}}
	if err := p.Insert(testCtx, "evaluations", evaluation); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(testCtx, f.human, "memories", "deleted-memory", ChangeInput{Scope: scopeA, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	request, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "source-request", TargetType: "source_event", TargetIDs: []string{source.ID}, Reason: "owner_request"})
	if err != nil {
		t.Fatal(err)
	}
	evaluation, err = p.Get(testCtx, "evaluations", scopeA, evaluation.ID)
	if err != nil || !evaluation.PrivacyQuarantined {
		t.Fatal("fixture quarantine missing", err)
	}
	return p, f, evaluation, request
}

func TestPostgresPrivacyQueuedErasureRespectsHold(t *testing.T) {
	p, f, evaluation, request := queuedPrivacyFixture(t)
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	after, err := p.Get(testCtx, "evaluations", scopeA, evaluation.ID)
	if err != nil || after.Text != evaluation.Text || after.Revision != evaluation.Revision {
		t.Fatal("queued erasure bypassed hold", err)
	}
	jobID := deletionJobID("memories", scopeA, "deleted-memory")
	for attempt := 0; attempt <= maxJobAttempts; attempt++ {
		job, err := p.Get(testCtx, colJobs, scopeA, jobID)
		if err != nil || job.Status != JobStatusPending || job.Attempts != 0 || job.LeaseToken != "" || job.RunAt == nil || !job.RunAt.After(time.Now()) || job.SourceMemoryErasure.DeletedRecords["evaluations"] != 0 {
			t.Fatal("held batch consumed attempts/progress", err)
		}
		job.RunAt = nil
		job.Revision++
		if err := p.Replace(testCtx, colJobs, job, job.Revision-1); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
			t.Fatal(err)
		}
	}
	held, err := f.svc.GetPrivacyDeletion(testCtx, f.human, request.ID, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReleasePrivacyDeletionHold(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: held.Revision, Reason: "release", PolicyRef: "owner_hold"}); err != nil {
		t.Fatal(err)
	}
	job, err := p.Get(testCtx, colJobs, scopeA, jobID)
	if err != nil {
		t.Fatal(err)
	}
	job.RunAt = nil
	job.Revision++
	if err := p.Replace(testCtx, colJobs, job, job.Revision-1); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(testCtx, "evaluations", scopeA, evaluation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("released batch did not resume", err)
	}
}

func TestPostgresPrivacyQueuedErasureHoldRace(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		p, f, evaluation, request := queuedPrivacyFixture(t)
		var holdErr, workerErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, holdErr = f.svc.HoldPrivacyDeletion(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"})
		}()
		go func() { defer wg.Done(); <-start; workerErr = f.svc.WorkOnce(testCtx, f.work) }()
		close(start)
		wg.Wait()
		if holdErr != nil && !errors.Is(holdErr, ErrConflict) {
			t.Fatal("unexpected hold failure", holdErr)
		}
		if workerErr != nil && !errors.Is(workerErr, ErrConflict) {
			t.Fatal("unexpected worker failure", workerErr)
		}
		row, err := p.Get(testCtx, "evaluations", scopeA, evaluation.ID)
		if holdErr == nil && (err != nil || row.Text != evaluation.Text) {
			t.Fatal("hold committed with erasure", err)
		}
		if holdErr != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal("hold rejected without erasure", err)
		}
	}
}

func TestPostgresPrivacyHoldRejectsAlreadyErasedImpact(t *testing.T) {
	p, f, evaluation, request := queuedPrivacyFixture(t)
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(testCtx, "evaluations", scopeA, evaluation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("fixture was not erased", err)
	}
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: scopeA, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"}); !errors.Is(err, ErrConflict) {
		t.Fatal("hold claimed already erased data was retained", err)
	}
}
