package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func blobGCFixture(t *testing.T, repo Repository) *fixture {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.BlobDirectory = blobTestDir(t)
	f.auth.actors[f.work] = actor("storage-maintainer", "service", false, "maintain")
	if err := f.svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	return f
}
func exerciseBlobGC(t *testing.T, repo Repository) {
	f := blobGCFixture(t, repo)
	body := []byte("shared gc survivor")
	var sources []FileIngestResult
	for _, scope := range []Scope{scopeA, scopeB} {
		out, e := f.svc.IngestFile(testCtx, f.human, FileIngestInput{Source: sourceInput(CreateInput{Scope: scope}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}, body)
		if e != nil {
			t.Fatal(e)
		}
		sources = append(sources, out)
	}
	for i := range 70 {
		if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, ".upload-"+ID()), []byte("abandoned bytes"), 0600); e != nil {
			t.Fatal(e)
		}
		// Metadata-only rows exercise pagination after unlink and process loss.
		row := Record{ID: ContentHash(fmt.Sprint("missing", i)), Scope: blobScope(), Kind: "blob", Status: JobStatusPending, Revision: 1, Blob: &blobMetadata{}}
		if i%2 == 0 {
			row.Status = statusActive
		}
		if e := repo.Insert(testCtx, colBlobs, row); e != nil {
			t.Fatal(e)
		}
	}
	for i := range 3 {
		orphan := fmt.Sprint("orphan", i)
		if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, ContentHash(orphan)), []byte(orphan), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, ".binding-"+ID()), []byte("partial marker"), 0600); e != nil {
		t.Fatal(e)
	}
	result, e := f.svc.CollectBlobGarbage(testCtx, f.work)
	if e != nil || result.ReferencedFiles != 1 || result.CollectedBlobs != 73 || result.TemporaryFilesRemoved != 71 {
		t.Fatal("garbage collection", result, e)
	}
	for _, out := range sources {
		got, e := f.svc.ReadArtifact(testCtx, f.human, out.Source.Scope, out.Source.ID, out.Artifact.ID)
		if e != nil || string(got) != string(body) {
			t.Fatal("shared file lost", e)
		}
	}
	entries, e := os.ReadDir(f.svc.BlobDirectory)
	if e != nil || len(entries) != 3 {
		t.Fatal("residue remains", len(entries), e)
	}
	result, e = f.svc.CollectBlobGarbage(testCtx, f.work)
	if e != nil || result.CollectedBlobs != 0 || result.TemporaryFilesRemoved != 0 {
		t.Fatal("replay not idempotent", result, e)
	}
}
func TestBlobGC(t *testing.T) { exerciseBlobGC(t, newFakeRepo()) }

func TestBlobGCRefuses(t *testing.T) {
	for _, scenario := range []string{"denied", "human", "agent", "pipeline", "wrong_scope", "revoked", "changed_actor", "audit", "unknown_commit", "wrong_root", "unknown", "symlink", "hardlink", "permissions", "directory", "large_temp", "malformed_temp", "positive_count"} {
		t.Run(scenario, func(t *testing.T) {
			f := blobGCFixture(t, newFakeRepo())
			originalRoot := f.svc.BlobDirectory
			name := ContentHash("orphan")
			if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, name), []byte("orphan"), 0600); e != nil {
				t.Fatal(e)
			}
			token := f.work
			switch scenario {
			case "denied":
				token = "unknown"
			case "human", "agent", "pipeline":
				f.auth.actors[f.work] = actor("operator", scenario, false, "maintain")
			case "wrong_scope":
				a := f.auth.actors[f.work]
				a.wrongScope = true
				f.auth.actors[f.work] = a
			case "revoked", "changed_actor":
				f.svc.Auth = &blobGCPolicy{Authorizer: f.auth, change: scenario == "changed_actor"}
			case "audit":
				f.svc.Repo = artifactAuditFailure{f.svc.Repo}
			case "unknown_commit":
				f.svc.Repo = artifactCommitUnknown{f.svc.Repo}
			case "wrong_root":
				f.svc.BlobDirectory = blobTestDir(t)
			case "positive_count":
				e := f.svc.Repo.Insert(testCtx, colBlobs, Record{ID: name, Scope: blobScope(), Kind: "blob", Status: statusActive, Revision: 1, Blob: &blobMetadata{Bytes: 6, References: 1}})
				if e != nil {
					t.Fatal(e)
				}
			default:
				path := filepath.Join(f.svc.BlobDirectory, name)
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				name = ".upload-" + ID()
				path = filepath.Join(f.svc.BlobDirectory, name)
				switch scenario {
				case "unknown":
					name = "unknown-file"
					path = filepath.Join(f.svc.BlobDirectory, name)
				case "malformed_temp":
					name = ".upload-ABC"
					path = filepath.Join(f.svc.BlobDirectory, name)
				}
				if scenario == "directory" {
					if e := os.Mkdir(path, 0700); e != nil {
						t.Fatal(e)
					}
				} else if scenario == "symlink" {
					if e := os.Symlink(".database", path); e != nil {
						t.Fatal(e)
					}
				} else if scenario == "hardlink" {
					if e := os.Link(filepath.Join(f.svc.BlobDirectory, ".database"), path); e != nil {
						t.Fatal(e)
					}
				} else {
					if e := os.WriteFile(path, []byte("residue"), 0600); e != nil {
						t.Fatal(e)
					}
					if scenario == "permissions" {
						if e := os.Chmod(path, 0644); e != nil {
							t.Fatal(e)
						}
					}
					if scenario == "large_temp" {
						if e := os.Truncate(path, (200<<20)+2); e != nil {
							t.Fatal(e)
						}
					}
				}
			}
			_, e := f.svc.CollectBlobGarbage(testCtx, token)
			if e == nil {
				t.Fatal("unsafe collection accepted")
			}
			if _, e = os.Lstat(filepath.Join(originalRoot, name)); e != nil {
				t.Fatal("refused item lost", e)
			}
		})
	}
}

type blobGCPolicy struct {
	Authorizer
	calls  int
	change bool
}

func (p *blobGCPolicy) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := p.Authorizer.Authorize(ctx, token, in)
	p.calls++
	if p.calls > 1 {
		if p.change {
			d.PrincipalID = "different-maintainer"
		} else {
			d.Allowed = false
		}
	}
	return d, e
}

func TestBlobGCWaitsForWriter(t *testing.T) {
	f := blobGCFixture(t, newFakeRepo())
	ctx, cancel := context.WithTimeout(testCtx, 100*time.Millisecond)
	defer cancel()
	err := f.svc.withBlobs(testCtx, func(b *lockedBlobs) error {
		name := ".upload-" + ID()
		if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, name), []byte("in progress"), 0600); e != nil {
			return e
		}
		if _, e := f.svc.CollectBlobGarbage(ctx, f.work); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal("ignored writer lock/cancellation", e)
		}
		if _, e := b.root.Stat(name); e != nil {
			t.Fatal("in-flight temp lost", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// No age threshold: releasing the shared lock proves the writer is gone.
	result, err := f.svc.CollectBlobGarbage(testCtx, f.work)
	if err != nil || result.TemporaryFilesRemoved != 1 {
		t.Fatal(result, err)
	}
}

func TestBlobGCAuditOmitsNames(t *testing.T) {
	f := blobGCFixture(t, newFakeRepo())
	name := ContentHash("orphan")
	if e := os.WriteFile(filepath.Join(f.svc.BlobDirectory, name), []byte("orphan"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.CollectBlobGarbage(testCtx, f.work); e != nil {
		t.Fatal(e)
	}
	for _, r := range f.repo.store["audit_events"] {
		if strings.Contains(structuredText(r), name) || strings.Contains(structuredText(r), "orphan") {
			t.Fatal("file identity leaked to audit")
		}
	}
}
