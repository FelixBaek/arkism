//go:build integration && linux

package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntegrationFileIngestHTTP(t *testing.T) { exerciseFileIngestHTTP(t, isolatedPostgres(t)) }
func TestIntegrationArtifactRead(t *testing.T)   { exerciseArtifactRead(t, isolatedPostgres(t)) }

func TestIntegrationArtifactReadDeletionFence(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.BlobDirectory = blobTestDir(t)
	out, err := f.svc.IngestFile(testCtx, f.human, FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}, []byte("fenced original download"))
	if err != nil {
		t.Fatal(err)
	}
	race := &memoryFenceRaceRepo{Repository: repo, reached: make(chan struct{}), release: make(chan struct{})}
	reader := &Service{Repo: race, Auth: f.auth, BlobDirectory: f.svc.BlobDirectory}
	ctx, cancel := context.WithTimeout(testCtx, 10*time.Second)
	defer cancel()
	type result struct {
		body []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		body, e := reader.ReadArtifact(ctx, f.human, scopeA, out.Source.ID, out.Artifact.ID)
		done <- result{body, e}
	}()
	released := false
	defer func() {
		if !released {
			close(race.release)
		}
	}()
	select {
	case <-race.reached:
	case <-ctx.Done():
		t.Fatal("reader never reached source fence")
	}
	if err = f.svc.Delete(ctx, f.human, "sources", out.Source.ID, ChangeInput{Scope: scopeA, ExpectedRevision: out.Source.Revision}); err != nil {
		t.Fatal(err)
	}
	close(race.release)
	released = true
	select {
	case got := <-done:
		if !errors.Is(got.err, ErrNotFound) || got.body != nil {
			t.Fatal("read survived committed deletion", got.err)
		}
	case <-ctx.Done():
		t.Fatal("reader did not finish")
	}
}

func TestIntegrationArtifactReferences(t *testing.T) {
	exerciseArtifactReferences(t, isolatedPostgres(t))
}
func TestIntegrationArtifactFailures(t *testing.T) { exerciseArtifactFailures(t, isolatedPostgres(t)) }
func TestIntegrationArtifactSourceFence(t *testing.T) {
	exerciseArtifactSourceFence(t, isolatedPostgres(t))
}
func TestIntegrationSourceArtifactRegistration(t *testing.T) {
	exerciseSourceArtifactRegistration(t, isolatedPostgres(t))
}
func TestIntegrationSourceArtifactFailure(t *testing.T) {
	exerciseSourceArtifactFailure(t, isolatedPostgres(t))
}

// Simulate the previous uploader losing its response while its transaction is
// still in flight. GC must conflict with that writer, never unlink its file.
func TestIntegrationArtifactGCFencesPendingCommit(t *testing.T) {
	repo := isolatedPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := blobStore{dir: blobTestDir(t)}
	source := artifactSource(t, repo, scopeA)
	data := artifactData("pending upload")
	if err := store.locked(ctx, func(b *lockedBlobs) error {
		_, _, e := b.put(strings.NewReader("pending upload"), data.Digest)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	prepared, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	go func() {
		done <- repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			_, err := attachArtifactMetadata(ctx, tx, source.Scope, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, "tool_output", data, artifactActor(source.Scope))
			if err != nil {
				return err
			}
			once.Do(func() { close(prepared) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-prepared:
	case err := <-done:
		t.Fatal("writer failed before prepare", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	observed := &artifactCountBarrier{Repository: repo, release: release}
	err := store.locked(ctx, func(b *lockedBlobs) error { return b.collect(observed, data.Digest) })
	if !errors.Is(err, ErrConflict) {
		t.Fatal("GC did not preserve pending reference", err)
	}
	if err = <-done; err != nil {
		t.Fatal("pending commit failed", err)
	}
	if n, e := repo.BlobReferences(ctx, data.Digest); e != nil || n != 1 {
		t.Fatal("pending reference lost", n, e)
	}
	if err = store.locked(ctx, func(b *lockedBlobs) error { return b.verify(data.Digest, data.Digest, data.Bytes) }); err != nil {
		t.Fatal(err)
	}
}

type artifactCountBarrier struct {
	Repository
	release chan struct{}
	once    sync.Once
}
type artifactCountTx struct {
	Tx
	parent *artifactCountBarrier
}

func (r *artifactCountBarrier) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, artifactCountTx{tx, r}) })
}
func (x artifactCountTx) BlobReferences(ctx context.Context, digest string) (int64, error) {
	n, e := x.Tx.BlobReferences(ctx, digest)
	if e == nil && n == 0 {
		x.parent.once.Do(func() { close(x.parent.release) })
	}
	return n, e
}

func TestIntegrationArtifactConcurrentAttachDetach(t *testing.T) {
	repo := isolatedPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := blobStore{dir: blobTestDir(t)}
	first, second := artifactSource(t, repo, scopeA), artifactSource(t, repo, scopeB)
	var old Record
	if err := store.locked(ctx, func(b *lockedBlobs) error { old = attachTestArtifact(t, b, repo, first, "shared"); return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- store.locked(ctx, func(b *lockedBlobs) error { return b.detach(repo, old, "tester") }) }()
	go func() {
		done <- store.locked(ctx, func(b *lockedBlobs) error {
			_, e := b.attach(repo, second.Scope, Ref{Collection: "sources", ID: second.ID, Revision: second.Revision}, "tool_output", artifactData("shared"), artifactActor(second.Scope), strings.NewReader("shared"))
			return e
		})
	}()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	data := artifactData("shared")
	if n, e := repo.BlobReferences(ctx, data.Digest); e != nil || n != 1 {
		t.Fatal("concurrent reference loss", n, e)
	}
	if err := store.locked(ctx, func(b *lockedBlobs) error { return b.verify(data.Digest, data.Digest, data.Bytes) }); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationBlobBinding(t *testing.T) {
	a, z := isolatedPostgres(t), isolatedPostgres(t)
	first, err := a.BlobStorageIdentity(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := z.BlobStorageIdentity(testCtx)
	if err != nil || first == second {
		t.Fatal("database identity not distinct", err)
	}
	svc := &Service{Repo: a, BlobDirectory: blobTestDir(t)}
	if err := svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	svc.Repo = z
	if err := svc.CheckBlobStorage(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("mixed database accepted", err)
	}
	svc.Repo = a
	if err := svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	svc.BlobDirectory = blobTestDir(t)
	if err := svc.CheckBlobStorage(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("second root for same DB accepted", err)
	}
}

func TestIntegrationBlobBindingConcurrentRoots(t *testing.T) {
	repo := isolatedPostgres(t)
	results := make(chan error, 2)
	for range 2 {
		svc := &Service{Repo: repo, BlobDirectory: blobTestDir(t)}
		go func() { results <- svc.CheckBlobStorage(testCtx) }()
	}
	success, denied := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrConflict) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("two roots: success=%d denied=%d", success, denied)
	}
}

func TestIntegrationBlobGC(t *testing.T) { exerciseBlobGC(t, isolatedPostgres(t)) }
