package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type changedBlobIdentity struct {
	Repository
	identity string
}

func (r *changedBlobIdentity) BlobStorageIdentity(context.Context) (string, error) {
	return r.identity, nil
}

func TestBlobBindingReadiness(t *testing.T) {
	repo := &changedBlobIdentity{Repository: newFakeRepo(), identity: ContentHash("first database")}
	svc := &Service{Repo: repo, BlobDirectory: blobTestDir(t)}
	probe := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		Handler(svc).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		if w.Code != want {
			t.Fatalf("readiness %d: %s", w.Code, w.Body.String())
		}
	}
	probe(200)
	marker := filepath.Join(svc.BlobDirectory, ".database")
	data, err := os.ReadFile(marker)
	if err != nil || !strings.HasPrefix(string(data), repo.identity+"\n") || len(data) != 130 {
		t.Fatal("missing identity", err)
	}
	probe(200)
	repo.identity = ContentHash("second database")
	probe(503)
	repo.identity = ContentHash("first database")
	probe(200)
	if err := os.Chmod(marker, 0644); err != nil {
		t.Fatal(err)
	}
	probe(503)
	if err := os.Chmod(marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	probe(503)
	svc.BlobDirectory = ""
	probe(200) // No file capability is implied by text-only readiness.
}

func TestBlobBindingRefusesUnownedContents(t *testing.T) {
	for _, name := range []string{"unbound_file", "symlink", "hardlink", "oversized_marker"} {
		t.Run(name, func(t *testing.T) {
			svc := &Service{Repo: newFakeRepo(), BlobDirectory: blobTestDir(t)}
			marker := filepath.Join(svc.BlobDirectory, ".database")
			external := filepath.Join(blobTestDir(t), "external")
			if err := os.WriteFile(external, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "unbound_file":
				if err := os.WriteFile(filepath.Join(svc.BlobDirectory, ContentHash("orphan")), []byte("orphan"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(external, marker); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(external, marker); err != nil {
					t.Fatal(err)
				}
			case "oversized_marker":
				id, _ := svc.Repo.BlobStorageIdentity(testCtx)
				if err := os.WriteFile(marker, []byte(id+"\nextra"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.CheckBlobStorage(testCtx); err == nil {
				t.Fatal("unsafe root accepted")
			}
			if data, err := os.ReadFile(external); err != nil || string(data) != "preserve" {
				t.Fatal("external file touched", err)
			}
			if name == "unbound_file" {
				if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("populated root claimed", err)
				}
			}
		})
	}
}

func TestBlobBindingConcurrentDatabases(t *testing.T) {
	dir := blobTestDir(t)
	a, z := &Service{Repo: newFakeRepo(), BlobDirectory: dir}, &Service{Repo: newFakeRepo(), BlobDirectory: dir}
	results := make(chan error, 2)
	for _, svc := range []*Service{a, z} {
		go func() { results <- svc.CheckBlobStorage(testCtx) }()
	}
	successes, denials := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrUnavailable) {
			denials++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || denials != 1 {
		t.Fatalf("binding race: successes=%d denials=%d", successes, denials)
	}
}

func TestBlobBindingRejectsSecondRoot(t *testing.T) {
	repo := newFakeRepo()
	a, z := &Service{Repo: repo, BlobDirectory: blobTestDir(t)}, &Service{Repo: repo, BlobDirectory: blobTestDir(t)}
	if err := a.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	if err := z.CheckBlobStorage(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("same database accepted second root", err)
	}
	if err := a.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
}

func TestBlobBindingRecoversInterruptedMarker(t *testing.T) {
	svc := &Service{Repo: newFakeRepo(), BlobDirectory: blobTestDir(t)}
	stale := filepath.Join(svc.BlobDirectory, ".binding-"+ID())
	if err := os.WriteFile(stale, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale marker not removed", err)
	}
	// An ambiguous DB response keeps the published root token for a safe retry.
	other := &Service{Repo: artifactCommitUnknown{newFakeRepo()}, BlobDirectory: blobTestDir(t)}
	if err := other.CheckBlobStorage(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	other.Repo = other.Repo.(artifactCommitUnknown).Repository
	if err := other.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
}

func TestBlobBindingSharedRoot(t *testing.T) {
	repo, dir := newFakeRepo(), blobTestDir(t)
	results := make(chan error, 2)
	for range 2 {
		svc := &Service{Repo: repo, BlobDirectory: dir}
		go func() { results <- svc.CheckBlobStorage(testCtx) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestBlobBindingPreservesUnsafeTemporaryFiles(t *testing.T) {
	for _, mode := range []string{"upload", "long_name", "uppercase", "bad_hex", "permissions", "symlink", "hardlink", "oversized", "multiple"} {
		t.Run(mode, func(t *testing.T) {
			svc := &Service{Repo: newFakeRepo(), BlobDirectory: blobTestDir(t)}
			name := ".binding-" + ID()
			switch mode {
			case "upload":
				name = ".upload-" + ID()
			case "long_name":
				name = ".binding-" + ContentHash("marker")
			case "uppercase":
				name = ".binding-" + strings.Repeat("A", 32)
			case "bad_hex":
				name = ".binding-" + strings.Repeat("g", 32)
			}
			target := filepath.Join(svc.BlobDirectory, name)
			external := filepath.Join(blobTestDir(t), "external")
			if err := os.WriteFile(external, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			payload := []byte("preserve")
			if mode == "oversized" {
				payload = make([]byte, 131)
			}
			var err error
			switch mode {
			case "symlink":
				err = os.Symlink(external, target)
			case "hardlink":
				err = os.Link(external, target)
			default:
				err = os.WriteFile(target, payload, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "permissions" {
				if err := os.Chmod(target, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "multiple" {
				if err := os.WriteFile(filepath.Join(svc.BlobDirectory, ".binding-"+ID()), []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.CheckBlobStorage(testCtx); err == nil {
				t.Fatal("unsafe temporary file accepted")
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != string(payload) {
				t.Fatal("temporary file changed", err)
			}
			if data, err := os.ReadFile(external); err != nil || string(data) != "preserve" {
				t.Fatal("external file changed", err)
			}
			if _, err := os.Lstat(filepath.Join(svc.BlobDirectory, ".database")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsafe root bound", err)
			}
		})
	}
}
