package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArtifactErasure(t *testing.T) { exerciseArtifactErasure(t, newFakeRepo()) }

func exerciseArtifactErasure(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	token := erasureWorker(f)
	store := blobStore{dir: blobTestDir(t)}
	f.svc.BlobDirectory = store.dir
	if err := f.svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	a, z := artifactSource(t, repo, scopeA), artifactSource(t, repo, scopeB)
	var artifacts []Record
	if err := store.locked(testCtx, func(b *lockedBlobs) error {
		artifacts = []Record{attachTestArtifact(t, b, repo, a, "worker shared file"), attachTestArtifact(t, b, repo, z, "worker shared file")}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, artifacts[0].Artifact.Digest)
	claim := func() Record {
		t.Helper()
		job, err := repo.ClaimJob(testCtx, time.Now(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	run := func(job Record) {
		t.Helper()
		if err := f.svc.eraseSourceMemories(testCtx, token, job); err != nil {
			t.Fatal(err)
		}
	}
	for i, source := range []Record{a, z} {
		if err := f.svc.Delete(testCtx, f.human, "sources", source.ID, ChangeInput{Scope: source.Scope, ExpectedRevision: source.Revision}); err != nil {
			t.Fatal(err)
		}
		job := claim()
		f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: artifacts[i].ID}
		if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, ErrDenied) {
			t.Fatal("row denial", err)
		}
		f.svc.Auth = f.auth
		f.svc.Repo = artifactAuditFailure{repo}
		if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, ErrUnavailable) {
			t.Fatal("detach audit", err)
		}
		f.svc.Repo = repo
		count, err := repo.BlobReferences(testCtx, artifacts[i].Artifact.Digest)
		if err != nil || count != int64(2-i) {
			t.Fatal("failed detach changed references", count, err)
		}
		run(job)
		status, err := f.svc.SourceDeletionStatus(testCtx, f.human, source.Scope, source.ID)
		if err != nil || status.PendingArtifactCleanup != 1 || status.Status != JobStatusPending || status.DeletedRecords[colArtifacts] != 1 || status.FullErasureVerified {
			t.Fatalf("pending status %+v %v", status, err)
		}
		raw, _ := json.Marshal(status)
		if strings.Contains(string(raw), artifacts[i].ID) || strings.Contains(string(raw), artifacts[i].Artifact.Digest) {
			t.Fatal("status exposed file identity")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("detach unlinked file", err)
		}
		job = claim()
		f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: artifacts[i].ID}
		if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, ErrDenied) {
			t.Fatal("cleanup row denial", err)
		}
		f.svc.Auth = f.auth
		if i == 1 {
			f.svc.BlobDirectory = ""
			if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, ErrUnavailable) {
				t.Fatal("missing root", err)
			}
			f.svc.BlobDirectory = blobTestDir(t)
			if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, ErrUnavailable) {
				t.Fatal("wrong root accepted", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("wrong root removed original", err)
			}
			savedRootJob, err := repo.Get(testCtx, "jobs", job.Scope, job.ID)
			if err != nil || len(savedRootJob.SourceMemoryErasure.PendingArtifacts) != 1 {
				t.Fatal("wrong root lost intent", err)
			}
			f.svc.BlobDirectory = store.dir
			f.svc.Repo = &changedBlobIdentity{Repository: repo, identity: ContentHash("wrong database")}
			if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, ErrUnavailable) {
				t.Fatal("wrong database accepted", err)
			}
			f.svc.Repo = repo
			for _, failure := range []error{ErrConflict, ErrUnavailable} {
				f.svc.Repo = &artifactGCFailure{Repository: repo, failure: failure}
				if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, failure) {
					t.Fatal("GC failure acknowledged", err)
				}
				saved, err := repo.Get(testCtx, "jobs", job.Scope, job.ID)
				if err != nil || len(saved.SourceMemoryErasure.PendingArtifacts) != 1 {
					t.Fatal("GC failure lost intent", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal("uncertain GC unlinked file", err)
				}
			}
			f.svc.Repo = repo
			stale := job
			stale.Revision++
			if err := f.svc.eraseSourceMemories(testCtx, token, stale); !errors.Is(err, ErrConflict) {
				t.Fatal("stale lease", err)
			}
			if err := os.Chmod(path, 0400); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.eraseSourceMemories(testCtx, token, job); err == nil {
				t.Fatal("bad file permission accepted")
			}
			saved, err := repo.Get(testCtx, "jobs", job.Scope, job.ID)
			if err != nil || len(saved.SourceMemoryErasure.PendingArtifacts) != 1 {
				t.Fatal("failure lost cleanup intent", err)
			}
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			f.svc.Repo = artifactAuditFailure{repo}
			if err := f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(err, ErrUnavailable) {
				t.Fatal("ack audit", err)
			}
			f.svc.Repo = repo
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("expected file GC before failed ack", err)
			}
			saved, err = repo.Get(testCtx, "jobs", job.Scope, job.ID)
			if err != nil || len(saved.SourceMemoryErasure.PendingArtifacts) != 1 {
				t.Fatal("failed ack lost intent", err)
			}
		}
		run(job)
		run(claim())
		status, err = f.svc.SourceDeletionStatus(testCtx, f.human, source.Scope, source.ID)
		if err != nil || status.Status != JobStatusDone || status.PendingArtifactCleanup != 0 || status.FullErasureVerified {
			t.Fatalf("done %+v %v", status, err)
		}
		if i == 0 {
			if data, err := os.ReadFile(path); err != nil || string(data) != "worker shared file" {
				t.Fatal("shared file erased", err)
			}
			if _, err := repo.Get(testCtx, colArtifacts, z.Scope, artifacts[1].ID); err != nil {
				t.Fatal("other scope artifact erased", err)
			}
		}
	}
	if _, err := repo.Get(testCtx, colBlobs, blobScope(), artifacts[0].Artifact.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("final blob not collected", err)
	}
}

func TestArtifactErasureMultipleFiles(t *testing.T) {
	f := newFixture(t)
	source := f.source()
	token := erasureWorker(f)
	f.svc.BlobDirectory = blobTestDir(t)
	if err := f.svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	if err := (blobStore{dir: f.svc.BlobDirectory}).locked(testCtx, func(b *lockedBlobs) error {
		attachTestArtifact(t, b, f.repo, source, "first file")
		attachTestArtifact(t, b, f.repo, source, "second file")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(testCtx, f.human, "sources", source.ID, ChangeInput{Scope: source.Scope, ExpectedRevision: source.Revision}); err != nil {
		t.Fatal(err)
	}
	for tick, want := range []int{2, 1, 0, 0} {
		if err := f.svc.WorkOnce(testCtx, token); err != nil {
			t.Fatal(err)
		}
		status, err := f.svc.SourceDeletionStatus(testCtx, f.human, source.Scope, source.ID)
		if err != nil || status.PendingArtifactCleanup != want || (status.Status == JobStatusDone) != (tick == 3) {
			t.Fatalf("tick %d: %+v %v", tick, status, err)
		}
		assertErasureStatusKeys(t, status)
	}
}

// Fail the GC reference query, independent of binding/lease transaction count.
type artifactGCFailure struct {
	Repository
	failure error
}
type artifactGCTx struct {
	Tx
	failure error
}

func (r *artifactGCFailure) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, artifactGCTx{tx, r.failure}) })
}
func (x artifactGCTx) BlobReferences(context.Context, string) (int64, error) { return 0, x.failure }

// Check the actual response's closed-object keys against the published contract.
func assertErasureStatusKeys(t *testing.T, status SourceErasureStatus) {
	t.Helper()
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	props := schemas["SourceErasureStatus"].(map[string]any)["properties"].(map[string]any)
	records := props["deleted_records"].(map[string]any)["properties"].(map[string]any)
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	for key := range response {
		if _, ok := props[key]; !ok {
			t.Errorf("undocumented status field %s", key)
		}
	}
	for key := range status.DeletedRecords {
		if _, ok := records[key]; !ok {
			t.Errorf("undocumented deleted_records field %s", key)
		}
	}
}
