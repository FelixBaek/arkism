package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var artifactHoldScope = Scope{WorkspaceID: "ws-a", ProjectID: "proj-a", UserID: "human-1"}

// Model an older/restored standalone erasure queue without a parent privacy request.
type legacyArtifactQueueRepo struct{ Repository }

func (legacyArtifactQueueRepo) allowLegacySourceDeleteForTest() {}

func privacyArtifactFixture(t *testing.T) (*Postgres, *fixture, Record, Record, PrivacyDeletionStatus) {
	t.Helper()
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	f.auth.actors[f.human].verbs["hold"] = true
	f.auth.actors[f.human].verbs["release"] = true
	f.svc.BlobDirectory = blobTestDir(t)
	if err := f.svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal("fixture step 1", err)
	}
	source := artifactSource(t, p, artifactHoldScope)
	var artifact Record
	if err := f.svc.withBlobs(testCtx, func(b *lockedBlobs) error {
		artifact = attachTestArtifact(t, b, p, source, "held file body")
		return nil
	}); err != nil {
		t.Fatal("fixture step 2", err)
	}
	f.svc.Repo = legacyArtifactQueueRepo{p}
	if err := f.svc.Delete(testCtx, f.human, "sources", source.ID, ChangeInput{Scope: artifactHoldScope, ExpectedRevision: source.Revision}); err != nil {
		t.Fatal("fixture step 3", err)
	}
	f.svc.Repo = p
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal("fixture step 4", err)
	}
	job, err := p.Get(testCtx, colJobs, artifactHoldScope, sourceMemoryErasureID(artifactHoldScope, source.ID))
	if err != nil || len(job.SourceMemoryErasure.PendingArtifacts) != 1 {
		t.Fatal("missing cleanup intent", err)
	}
	request, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: artifactHoldScope, IdempotencyKey: "artifact-subject", TargetType: "subject", TargetIDs: []string{artifactHoldScope.UserID}, Reason: "owner_request"})
	if err != nil {
		t.Fatal("fixture step 5", err)
	}
	return p, f, artifact, job, request
}

func TestPostgresPrivacyArtifactHeldGC(t *testing.T) {
	p, f, artifact, job, request := privacyArtifactFixture(t)
	held, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: artifactHoldScope, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"})
	if err != nil {
		t.Fatal(err)
	}
	// The orphan collector must honor the same fence as the queued worker.
	err = f.svc.withBlobs(testCtx, func(b *lockedBlobs) error { return b.collect(p, artifact.Artifact.Digest) })
	if !errors.Is(err, ErrConflict) {
		t.Fatal("generic GC bypassed hold", err)
	}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(f.svc.BlobDirectory, artifact.Artifact.Digest)); err != nil || string(body) != "held file body" {
		t.Fatal("held file removed", err)
	}
	saved, err := p.Get(testCtx, colJobs, artifactHoldScope, job.ID)
	if err != nil || saved.Status != JobStatusPending || saved.Attempts != 0 || saved.LeaseToken != "" || saved.RunAt == nil || !saved.RunAt.After(time.Now()) || len(saved.SourceMemoryErasure.PendingArtifacts) != 1 {
		t.Fatal("held cleanup advanced", err)
	}
	if _, err := f.svc.ReleasePrivacyDeletionHold(testCtx, f.human, held.ID, PrivacyHoldInput{Scope: artifactHoldScope, ExpectedRevision: held.Revision, Reason: "release", PolicyRef: "owner_hold"}); err != nil {
		t.Fatal(err)
	}
	saved.RunAt = nil
	saved.Revision++
	if err := p.Replace(testCtx, colJobs, saved, saved.Revision-1); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.svc.BlobDirectory, artifact.Artifact.Digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("released file survived", err)
	}
}

// Stop after the actual GC marker commit, before collect can unlink the file.
type artifactMarkerBarrier struct {
	Repository
	digest  string
	reached chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (r *artifactMarkerBarrier) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if err := r.Repository.Transaction(ctx, fn); err != nil {
		return err
	}
	marker, err := r.Repository.Get(ctx, colBlobs, blobScope(), r.digest)
	if err == nil && marker.Status == JobStatusPending {
		r.once.Do(func() { close(r.reached); <-r.resume })
	}
	return nil
}
func TestPostgresPrivacyArtifactHoldAfterMarker(t *testing.T) {
	p, f, artifact, _, request := privacyArtifactFixture(t)
	barrier := &artifactMarkerBarrier{Repository: p, digest: artifact.Artifact.Digest, reached: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- f.svc.withBlobs(testCtx, func(b *lockedBlobs) error { return b.collect(barrier, barrier.digest) })
	}()
	defer func() {
		close(barrier.resume)
		if err := <-done; err != nil {
			t.Error("GC completion", err)
		}
	}()
	select {
	case <-barrier.reached:
	case err := <-done:
		done <- err
		t.Fatal("GC did not commit marker", err)
	case <-time.After(10 * time.Second):
		t.Fatal("marker timeout")
	}
	if _, err := os.Stat(filepath.Join(f.svc.BlobDirectory, barrier.digest)); err != nil {
		t.Fatal("barrier did not precede unlink", err)
	}
	if _, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: artifactHoldScope, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"}); !errors.Is(err, ErrConflict) {
		t.Fatal("hold accepted during irreversible GC", err)
	}
}

func TestPostgresPrivacyArtifactHoldMarkerRace(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		p, f, artifact, _, request := privacyArtifactFixture(t)
		barrier := &artifactMarkerBarrier{Repository: p, digest: artifact.Artifact.Digest, reached: make(chan struct{}), resume: make(chan struct{})}
		start := make(chan struct{})
		holdDone, gcDone := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := f.svc.HoldPrivacyDeletion(testCtx, f.human, request.ID, PrivacyHoldInput{Scope: artifactHoldScope, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"})
			holdDone <- err
		}()
		go func() {
			<-start
			gcDone <- f.svc.withBlobs(testCtx, func(b *lockedBlobs) error { return b.collect(barrier, barrier.digest) })
		}()
		close(start)
		holdErr := <-holdDone
		close(barrier.resume)
		gcErr := <-gcDone
		if holdErr == nil {
			if !errors.Is(gcErr, ErrConflict) {
				t.Fatal("hold and GC both succeeded", gcErr)
			}
			if _, err := os.Stat(filepath.Join(f.svc.BlobDirectory, barrier.digest)); err != nil {
				t.Fatal("held bytes lost", err)
			}
		} else if !errors.Is(holdErr, ErrConflict) || gcErr != nil {
			t.Fatal("unexpected race outcomes", holdErr, gcErr)
		}
	}
}

func TestPostgresPrivacyArtifactHoldScopeAndProvenance(t *testing.T) {
	p, _, artifact, job, request := privacyArtifactFixture(t)
	row, err := p.Get(testCtx, colJobs, artifactHoldScope, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind string
		impact     PrivacyImpact
		foreign    bool
		blocked    bool
	}{
		{name: "subject", kind: "subject", blocked: true},
		{name: "source", kind: "source_event", impact: PrivacyImpact{SourceEventIDs: []string{job.Target.ID}}, blocked: true},
		{name: "artifact", kind: "source_event", impact: PrivacyImpact{Records: []Ref{{Collection: colArtifacts, ID: artifact.ID, Revision: 99}}}, blocked: true},
		{name: "target", kind: "source_event", impact: PrivacyImpact{Records: []Ref{*job.Target}}, blocked: true},
		{name: "wrong-collection", kind: "source_event", impact: PrivacyImpact{Records: []Ref{{Collection: "memories", ID: artifact.ID, Revision: 1}}}},
		{name: "unrelated", kind: "source_event", impact: PrivacyImpact{SourceEventIDs: []string{"unrelated"}}},
		{name: "foreign-subject", kind: "subject", foreign: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := row
			privacy := *row.PrivacyDeletion
			privacy.TargetType, privacy.Impact, privacy.State, privacy.LegalHoldStatus = tc.kind, tc.impact, privacyHeld, privacyHoldHeld
			candidate.PrivacyDeletion = &privacy
			if tc.foreign {
				candidate.Scope.ProjectID = "other-project"
			}
			if err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				if err := tx.Delete(ctx, colJobs, row.Scope, row.ID, row.Revision); err != nil {
					return err
				}
				return tx.Insert(ctx, colJobs, candidate)
			}); err != nil {
				t.Fatal(err)
			}
			err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				return tx.CheckArtifactPrivacyHold(ctx, artifact.Artifact.Digest)
			})
			if tc.blocked && !errors.Is(err, ErrConflict) || !tc.blocked && err != nil {
				t.Fatal("incorrect overlap", err)
			}
			if err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
				if err := tx.Delete(ctx, colJobs, candidate.Scope, candidate.ID, candidate.Revision); err != nil {
					return err
				}
				return tx.Insert(ctx, colJobs, row)
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresPrivacyArtifactHoldAfterMarkerRestricted(t *testing.T) {
	p, f, artifact, _, request := privacyArtifactFixture(t)
	const token = "artifact-human-without-storage-permission"
	if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "human-1", Role: roleHuman, Token: token, OrganizationID: artifactHoldScope.WorkspaceID, ProjectID: artifactHoldScope.ProjectID, Actions: []string{"db:read", "db:write"}}); err != nil {
		t.Fatal(err)
	}
	app := restrictedPostgres(t, p)
	ctx := ContextWithPostgresToken(testCtx, token)
	if _, err := app.Get(ctx, colBlobs, blobScope(), artifact.Artifact.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("fixture exposed private blob", err)
	}
	holdService := &Service{Repo: app, Auth: f.auth}
	barrier := &artifactMarkerBarrier{Repository: p, digest: artifact.Artifact.Digest, reached: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- f.svc.withBlobs(testCtx, func(b *lockedBlobs) error { return b.collect(barrier, barrier.digest) })
	}()
	defer func() {
		close(barrier.resume)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-barrier.reached:
	case err := <-done:
		done <- err
		t.Fatal("no marker", err)
	case <-time.After(10 * time.Second):
		t.Fatal("marker timeout")
	}
	if _, err := holdService.HoldPrivacyDeletion(ctx, f.human, request.ID, PrivacyHoldInput{Scope: artifactHoldScope, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"}); !errors.Is(err, ErrConflict) {
		t.Fatal("restricted hold missed hidden pending marker", err)
	}
}

func TestPostgresPrivacyArtifactRestrictedPermissions(t *testing.T) {
	p, f, artifact, job, request := privacyArtifactFixture(t)
	const humanToken = "artifact-scoped-human-token"
	const workerToken = "artifact-storage-other-project-token"
	for _, grant := range []PrincipalGrant{
		{PrincipalID: "human-1", Role: roleHuman, Token: humanToken, OrganizationID: artifactHoldScope.WorkspaceID, ProjectID: artifactHoldScope.ProjectID, Actions: []string{"db:read", "db:write"}},
		{PrincipalID: "storage-worker", Role: roleService, Token: workerToken, OrganizationID: "other-org", ProjectID: "other-project", Actions: []string{"db:read", "db:write", "storage:internal"}},
	} {
		if err := p.ProvisionPrincipal(testCtx, grant); err != nil {
			t.Fatal(err)
		}
	}
	app := restrictedPostgres(t, p)
	ctx := ContextWithPostgresToken(testCtx, humanToken)
	holdService := &Service{Repo: app, Auth: f.auth}
	if _, err := holdService.HoldPrivacyDeletion(ctx, f.human, request.ID, PrivacyHoldInput{Scope: artifactHoldScope, ExpectedRevision: request.Revision, Reason: "retain", PolicyRef: "owner_hold"}); err != nil {
		t.Fatal("scoped human cannot hold unstarted cleanup", err)
	}
	if _, err := app.Get(ctx, colBlobs, blobScope(), artifact.Artifact.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("private blob became visible", err)
	}
	for _, authCtx := range []context.Context{ctx, ContextWithPostgresToken(testCtx, "")} {
		var result bool
		if err := app.q().QueryRow(authCtx, `SELECT artifact_gc_held($1)`, artifact.Artifact.Digest).Scan(&result); err == nil {
			t.Fatal("global GC inspection allowed without storage permission")
		}
		if err := app.q().QueryRow(authCtx, `SELECT artifact_cleanup_pending($1,$2,$3,$4,$5,$6)`, "other-org", "other-project", artifactHoldScope.UserID, "", job.ID, artifact.Artifact.Digest).Scan(&result); err == nil {
			t.Fatal("foreign marker inspection allowed")
		}
	}
	workerCtx := ContextWithPostgresToken(testCtx, workerToken)
	if _, err := app.Get(workerCtx, colJobs, artifactHoldScope, request.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("worker fixture can read foreign privacy request", err)
	}
	if err := app.Transaction(workerCtx, func(ctx context.Context, tx Tx) error {
		return tx.CheckArtifactPrivacyHold(ctx, artifact.Artifact.Digest)
	}); !errors.Is(err, ErrConflict) {
		t.Fatal("global collector missed held foreign file", err)
	}
}
