package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func blobRestoreFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.svc.BlobDirectory = blobTestDir(t)
	f.auth.actors[f.work] = actor("restorer", "service", false, "restore")
	data := artifactData("restored bytes")
	source := artifactSource(t, f.repo, scopeA)
	a := Record{ID: artifactID(scopeA, source.ID, data.Digest), Scope: scopeA, Kind: "code", Status: statusActive, Revision: 1, Artifact: &data, ArtifactGeneration: ContentHash(ID()), Target: &Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, Sources: []SourceRef{{SourceID: source.ID, Locator: "original"}}, Sensitivity: source.Sensitivity}
	for col, rows := range map[string][]Record{
		colArtifacts: {a}, colBlobs: {{ID: data.Digest, Scope: blobScope(), Kind: "blob", Status: statusActive, Revision: 1, Blob: &blobMetadata{Bytes: data.Bytes, References: 1}}, {ID: "storage-binding-" + ContentHash("old-db"), Scope: blobScope(), Kind: "blob_root_binding", Status: statusActive, Revision: 1, BlobRootID: ContentHash("old-root")}},
	} {
		for _, r := range rows {
			if e := f.repo.Insert(testCtx, col, r); e != nil {
				t.Fatal(e)
			}
		}
	}
	m := blobBackupManifest{Version: 1, SourceIdentity: ContentHash("old-db"), SourceRootID: ContentHash("old-root"), Database: backupFile{ContentHash("dump"), 4}, Files: []backupFile{{data.Digest, data.Bytes}}}
	raw, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(f.svc.BlobDirectory, ".restore-pending"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(f.svc.BlobDirectory, data.Digest), []byte("restored bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	return f, data.Digest
}
func TestBlobRestore(t *testing.T) {
	f, digest := blobRestoreFixture(t)
	if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, ".binding-"+ID()), make([]byte, 130), 0600); e != nil {
		t.Fatal(e)
	}
	if e := f.svc.CheckBlobStorage(testCtx); !errors.Is(e, ErrUnavailable) {
		t.Fatal("quarantine ignored", e)
	}
	if e := f.svc.AdoptRestoredBlobs(testCtx, f.work); e != nil {
		t.Fatal(e)
	}
	if e := f.svc.CheckBlobStorage(testCtx); e != nil {
		t.Fatal("adopted root unavailable", e)
	}
	if _, e := os.Stat(filepath.Join(f.svc.BlobDirectory, ".restore-pending")); !os.IsNotExist(e) {
		t.Fatal("quarantine remains", e)
	}
	data, e := os.ReadFile(filepath.Join(f.svc.BlobDirectory, digest))
	if e != nil || string(data) != "restored bytes" {
		t.Fatal("bytes changed", e)
	}
	if f.svc.AdoptRestoredBlobs(testCtx, f.work) == nil {
		t.Fatal("normal root adopted again")
	}
}
func TestBlobRestoreRefuses(t *testing.T) {
	for _, scenario := range []string{"human", "denied", "audit", "corrupt", "missing", "unexpected", "symlink", "count", "missing_blob", "other_root", "old_db", "revoked", "large_binding", "upload"} {
		t.Run(scenario, func(t *testing.T) {
			f, digest := blobRestoreFixture(t)
			path := filepath.Join(f.svc.BlobDirectory, digest)
			switch scenario {
			case "large_binding", "upload":
				name := ".binding-" + ID()
				if scenario == "upload" {
					name = ".upload-" + ID()
				}
				if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, name), make([]byte, 131), 0600); e != nil {
					t.Fatal(e)
				}
			case "human":
				f.auth.actors[f.work] = actor("human", "human", false, "restore")
			case "denied":
				f.auth.actors[f.work] = actor("restorer", "service", false)
			case "revoked":
				f.svc.Auth = &blobGCPolicy{Authorizer: f.auth}
			case "audit":
				f.svc.Repo = artifactAuditFailure{f.repo}
			case "corrupt":
				if e := os.WriteFile(path, []byte("wrong bytes"), 0600); e != nil {
					t.Fatal(e)
				}
			case "missing":
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
			case "unexpected":
				if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, "extra"), nil, 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Rename(path, filepath.Join(f.svc.BlobDirectory, "alias")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink("alias", path); e != nil {
					t.Fatal(e)
				}
			case "count":
				r, e := f.repo.Get(testCtx, colBlobs, blobScope(), digest)
				if e != nil {
					t.Fatal(e)
				}
				r.Blob.References = 2
				r.Revision++
				if e = f.repo.Replace(testCtx, colBlobs, r, 1); e != nil {
					t.Fatal(e)
				}
			case "missing_blob":
				if e := f.repo.Delete(testCtx, colBlobs, blobScope(), digest, 1); e != nil {
					t.Fatal(e)
				}
			case "other_root":
				identity, e := f.repo.BlobStorageIdentity(testCtx)
				if e != nil {
					t.Fatal(e)
				}
				if e = bindBlobRoot(testCtx, f.repo, identity, ContentHash("another-root")); e != nil {
					t.Fatal(e)
				}
			case "old_db":
				f.svc.Repo = &changedBlobIdentity{Repository: f.repo, identity: ContentHash("old-db")}
			}
			if f.svc.AdoptRestoredBlobs(testCtx, f.work) == nil {
				t.Fatal("invalid restore opened")
			}
			if _, e := os.Stat(filepath.Join(f.svc.BlobDirectory, ".restore-pending")); e != nil {
				t.Fatal("quarantine removed", e)
			}
			if f.svc.CheckBlobStorage(testCtx) == nil {
				t.Fatal("failed restore became ready")
			}
		})
	}
}

type restoreCommitUnknown struct {
	Repository
	calls int
}

func (r *restoreCommitUnknown) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.calls++
	e := r.Repository.Transaction(ctx, fn)
	if e == nil && r.calls == 2 {
		return ErrUnavailable
	}
	return e
}
func TestBlobRestoreAmbiguousBinding(t *testing.T) {
	f, _ := blobRestoreFixture(t)
	f.svc.Repo = &restoreCommitUnknown{Repository: f.repo}
	if e := f.svc.AdoptRestoredBlobs(testCtx, f.work); !errors.Is(e, ErrUnavailable) {
		t.Fatal("unknown commit accepted", e)
	}
	if _, e := os.Stat(filepath.Join(f.svc.BlobDirectory, ".restore-pending")); e != nil {
		t.Fatal(e)
	}
	f.svc.Repo = f.repo
	if e := f.svc.AdoptRestoredBlobs(testCtx, f.work); e != nil {
		t.Fatal("binding retry failed", e)
	}
}

func (r *restoreCommitUnknown) RequireRestoreReview(ctx context.Context, digest string) error {
	return r.Repository.(restoreReviewStore).RequireRestoreReview(ctx, digest)
}
func (r *restoreCommitUnknown) CheckRestorePublication(ctx context.Context) error {
	return r.Repository.(restoreReviewStore).CheckRestorePublication(ctx)
}

// Review persistence failure must never remove the extraction quarantine.
type restoreReviewFailure struct{ Repository }

func (r restoreReviewFailure) RequireRestoreReview(context.Context, string) error {
	return ErrUnavailable
}
func (r restoreReviewFailure) CheckRestorePublication(context.Context) error { return ErrUnavailable }
func TestBlobRestoreReviewPersistence(t *testing.T) {
	f, _ := blobRestoreFixture(t)
	f.svc.Repo = restoreReviewFailure{f.repo}
	if err := f.svc.AdoptRestoredBlobs(testCtx, f.work); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.svc.BlobDirectory, ".restore-pending")); err != nil {
		t.Fatal("quarantine lost on gate failure", err)
	}
	f.svc.Repo = f.repo
	if err := f.svc.AdoptRestoredBlobs(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CheckRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("adoption published unreviewed restore", err)
	}
	f.svc.BlobDirectory = ""
	if err := f.svc.CheckRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("omitted file root bypassed gate", err)
	}
}

// TestBlobRestoreIntoObjectStorage: a manifest that names stored objects is
// adopted only when object storage holds each at its size; files the bundle
// carried are uploaded there.
func TestBlobRestoreIntoObjectStorage(t *testing.T) {
	for _, scenario := range []string{"object", "missing_object", "no_storage", "local_upload", "upload_fails"} {
		t.Run(scenario, func(t *testing.T) { blobRestoreObjectScenario(t, scenario) })
	}
}

func blobRestoreObjectScenario(t *testing.T, scenario string) {
	f, digest := blobRestoreFixture(t)
	fake := &fakeObjects{objects: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scenario == "upload_fails" && r.Method == http.MethodPut {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer server.Close()
	objects, err := NewS3BlobStore(server.URL, "bucket", "us-east-1", "blobs/", "AK", "SK", "")
	if err != nil {
		t.Fatal(err)
	}
	if scenario != "no_storage" {
		f.svc.BlobObjects = objects
	}
	if scenario != "local_upload" && scenario != "upload_fails" {
		writeObjectOnlyRestoreManifest(t, f, digest)
	}
	if scenario == "object" {
		fake.objects["/bucket/blobs/"+digest] = []byte("restored bytes")
	}
	checkBlobRestoreObjectScenario(t, scenario, f, fake, digest, f.svc.AdoptRestoredBlobs(testCtx, f.work))
}

// writeObjectOnlyRestoreManifest leaves the referenced bytes in object storage, not in the bundle.
func writeObjectOnlyRestoreManifest(t *testing.T, f *fixture, digest string) {
	t.Helper()
	pending := filepath.Join(f.svc.BlobDirectory, ".restore-pending")
	m := blobBackupManifest{Version: 1, SourceIdentity: ContentHash("old-db"), SourceRootID: ContentHash("old-root"), Database: backupFile{ContentHash("dump"), 4}, Files: []backupFile{}, Objects: []backupFile{{digest, 14}}}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(pending, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.svc.BlobDirectory, digest)); err != nil {
		t.Fatal(err)
	}
}

func checkBlobRestoreObjectScenario(t *testing.T, scenario string, f *fixture, fake *fakeObjects, digest string, err error) {
	t.Helper()
	switch scenario {
	case "object":
		if err != nil {
			t.Fatal(err)
		}
	case "local_upload":
		if err != nil || !fake.has(digest) {
			t.Fatal("not uploaded", err)
		}
		if _, err = os.Stat(filepath.Join(f.svc.BlobDirectory, digest)); !os.IsNotExist(err) {
			t.Fatal("local copy kept beside object storage", err)
		}
	case "upload_fails":
		// A failed upload keeps the local copy and the pending marker for a retry.
		_, local := os.Stat(filepath.Join(f.svc.BlobDirectory, digest))
		_, marker := os.Stat(filepath.Join(f.svc.BlobDirectory, ".restore-pending"))
		if err == nil || local != nil || marker != nil {
			t.Fatal("upload failure lost data", err, local, marker)
		}
	default:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal("adopted without the objects", err)
		}
	}
}
