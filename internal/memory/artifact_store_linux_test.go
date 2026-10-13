package memory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func artifactSource(t *testing.T, repo Repository, scope Scope) Record {
	t.Helper()
	r := Record{ID: ID(), Scope: scope, Kind: "document", Text: "synthetic original", Status: statusActive, Revision: 1, CreatedAt: time.Now().UTC().Truncate(time.Millisecond), CreatedBy: "tester", CreatedByType: "human", Sensitivity: "internal"}
	if err := repo.Insert(testCtx, "sources", r); err != nil {
		t.Fatal(err)
	}
	return r
}
func artifactActor(scope Scope) Decision {
	return Decision{Scope: scope, PrincipalID: "tester", Role: "service", Allowed: true}
}

func artifactData(body string) artifactMetadata {
	return artifactMetadata{Digest: ContentHash(body), MIME: "text/plain", Bytes: int64(len(body))}
}
func attachTestArtifact(t *testing.T, b *lockedBlobs, repo Repository, source Record, body string) Record {
	t.Helper()
	r, err := b.attach(repo, source.Scope, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, "tool_output", artifactData(body), artifactActor(source.Scope), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func exerciseArtifactReferences(t *testing.T, repo Repository) {
	t.Helper()
	store := blobStore{dir: blobTestDir(t)}
	first, second := artifactSource(t, repo, scopeA), artifactSource(t, repo, scopeB)
	data := artifactData(`{"shared":true}`)
	err := store.locked(testCtx, func(b *lockedBlobs) error {
		a := attachTestArtifact(t, b, repo, first, `{"shared":true}`)
		duplicate := attachTestArtifact(t, b, repo, first, `{"shared":true}`)
		if a.ID != duplicate.ID || structuredText(a) != structuredText(duplicate) || a.ArtifactGeneration != duplicate.ArtifactGeneration {
			t.Fatal("duplicate artifact ID changed")
		}
		z, e := b.attach(repo, second.Scope, Ref{Collection: "sources", ID: second.ID, Revision: second.Revision}, "tool_output", artifactMetadata{Digest: data.Digest, MIME: "application/json", Bytes: data.Bytes}, artifactActor(second.Scope), strings.NewReader(`{"shared":true}`))
		if e != nil {
			t.Fatal("foreign scope MIME affected attachment", e)
		}
		if a.ID == z.ID {
			t.Fatal("cross-scope artifact identity shared")
		}
		blob, e := repo.Get(testCtx, colBlobs, blobScope(), data.Digest)
		if e != nil || blob.Blob == nil || blob.Blob.References != 2 {
			t.Fatalf("reference count: %+v %v", blob.Blob, e)
		}
		if n, e := repo.BlobReferences(testCtx, data.Digest); e != nil || n != 2 {
			t.Fatal("actual reference count", n, e)
		}
		if e = b.verify(data.Digest, data.Digest, data.Bytes); e != nil {
			return e
		}
		entries, e := os.ReadDir(store.dir)
		if e != nil || len(entries) != 2 {
			t.Fatal("expected one file and lock", len(entries), e)
		}
		if _, e = repo.Get(testCtx, colArtifacts, scopeB, a.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("artifact scope leak", e)
		}
		for _, col := range []string{colBlobs, colArtifacts} {
			svc := &Service{Repo: repo}
			if _, e = svc.Get(testCtx, "token", col, blobScope(), data.Digest); !errors.Is(e, ErrInvalid) {
				t.Fatal("public internal metadata read", e)
			}
			if e = svc.Delete(testCtx, "token", col, data.Digest, ChangeInput{Scope: blobScope(), ExpectedRevision: 1}); !errors.Is(e, ErrInvalid) {
				t.Fatal("public internal metadata delete", e)
			}
		}
		f := newFixture(t)
		f.svc.Repo = repo
		preview, e := f.svc.PreviewDeletion(testCtx, f.human, "sources", first.ID, ChangeInput{Scope: first.Scope, ExpectedRevision: first.Revision})
		if e != nil || preview.DirectReferences[colArtifacts] != 1 {
			t.Fatal("scoped artifact preview", preview, e)
		}
		f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: a.ID}
		denied, e := f.svc.PreviewDeletion(testCtx, f.human, "sources", first.ID, ChangeInput{Scope: first.Scope, ExpectedRevision: first.Revision})
		if !errors.Is(e, ErrDenied) || denied.DirectReferences != nil {
			t.Fatal("preview ignored artifact read permission", e)
		}
		if e = b.collect(repo, data.Digest); !errors.Is(e, ErrConflict) {
			t.Fatal("collected live references", e)
		}
		stale := a
		stale.Revision++
		if e = b.detach(repo, stale, "tester"); !errors.Is(e, ErrConflict) {
			t.Fatal("detached stale revision", e)
		}
		if e = b.detach(repo, a, "tester"); e != nil {
			return e
		}
		if e = b.verify(data.Digest, data.Digest, data.Bytes); e != nil {
			t.Fatal("first detach removed shared file", e)
		}
		if e = b.detach(repo, z, "tester"); e != nil {
			return e
		}
		if _, e = os.Stat(filepath.Join(store.dir, data.Digest)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("last reference file remains", e)
		}
		if _, e = repo.Get(testCtx, colBlobs, blobScope(), data.Digest); !errors.Is(e, ErrNotFound) {
			t.Fatal("blob metadata remains", e)
		}
		if e = b.detach(repo, z, "tester"); e != nil {
			return e
		}
		// Re-upload after zero-reference GC is a new live reference, not a tombstone.
		reuploaded, e := b.attach(repo, second.Scope, *z.Target, z.Kind, *z.Artifact, artifactActor(second.Scope), strings.NewReader(`{"shared":true}`))
		if e != nil {
			t.Fatal(e)
		}
		if reuploaded.ArtifactGeneration == z.ArtifactGeneration {
			t.Fatal("artifact generation reused")
		}
		if e = b.detach(repo, z, "tester"); !errors.Is(e, ErrConflict) {
			t.Fatal("old deletion removed re-upload", e)
		}
		return b.verify(data.Digest, data.Digest, data.Bytes)
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestArtifactReferences(t *testing.T) { exerciseArtifactReferences(t, newFakeRepo()) }

// The wrapper injects transaction failures at real repository boundaries too.
type artifactAuditFailure struct{ Repository }
type artifactAuditTx struct{ Tx }

func (x artifactAuditTx) CheckPrivacyOverlap(ctx context.Context, scope Scope, id, targetType string, impact PrivacyImpact, holding bool) error {
	return checkPrivacyOverlap(ctx, x.Tx, scope, id, targetType, impact, holding)
}

func (x artifactAuditTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "audit_events" {
		return ErrUnavailable
	}
	return x.Tx.Insert(ctx, col, r)
}
func (r artifactAuditFailure) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, artifactAuditTx{tx}) })
}

type artifactCommitUnknown struct{ Repository }

func (r artifactCommitUnknown) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if err := r.Repository.Transaction(ctx, fn); err != nil {
		return err
	}
	return ErrUnavailable
}

func exerciseArtifactFailures(t *testing.T, repo Repository) {
	t.Helper()
	store := blobStore{dir: blobTestDir(t)}
	source := artifactSource(t, repo, scopeA)
	ref := Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}
	data := artifactData("file rollback")
	err := store.locked(testCtx, func(b *lockedBlobs) error {
		if _, e := b.attach(artifactAuditFailure{repo}, source.Scope, ref, "tool_output", data, artifactActor(source.Scope), strings.NewReader("file rollback")); !errors.Is(e, ErrUnavailable) {
			t.Fatal("audit failure hidden", e)
		}
		if n, e := repo.BlobReferences(testCtx, data.Digest); e != nil || n != 0 {
			t.Fatal("partial artifact transaction", n, e)
		}
		if _, e := repo.Get(testCtx, colBlobs, blobScope(), data.Digest); !errors.Is(e, ErrNotFound) {
			t.Fatal("partial blob transaction", e)
		}
		if _, e := os.Stat(filepath.Join(store.dir, data.Digest)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("failed attach file not collected", e)
		}
		if _, e := b.attach(artifactCommitUnknown{repo}, source.Scope, ref, "tool_output", data, artifactActor(source.Scope), strings.NewReader("file rollback")); !errors.Is(e, ErrUnavailable) {
			t.Fatal("unknown commit hidden", e)
		}
		if e := b.verify(data.Digest, data.Digest, data.Bytes); e != nil {
			t.Fatal("uncertain commit deleted referenced file", e)
		}
		saved := attachTestArtifact(t, b, repo, source, "file rollback")
		if n, e := repo.BlobReferences(testCtx, data.Digest); e != nil || n != 1 {
			t.Fatal("unknown commit retry duplicated", n, e)
		}
		if e := b.detach(artifactAuditFailure{repo}, saved, "tester"); !errors.Is(e, ErrUnavailable) {
			t.Fatal("detach audit failure hidden", e)
		}
		if n, e := repo.BlobReferences(testCtx, data.Digest); e != nil || n != 1 {
			t.Fatal("detach partially committed", n, e)
		}
		// Metadata corruption must preserve bytes, even if a counter claims zero.
		blob, e := repo.Get(testCtx, colBlobs, blobScope(), data.Digest)
		if e != nil {
			return e
		}
		meta := *blob.Blob
		meta.References = 0
		blob.Blob = &meta
		blob.Revision++
		if e = repo.Replace(testCtx, colBlobs, blob, blob.Revision-1); e != nil {
			return e
		}
		if e = b.collect(repo, data.Digest); !errors.Is(e, ErrConflict) {
			t.Fatal("count mismatch collected file", e)
		}
		if _, e = b.attach(repo, source.Scope, ref, "tool_output", data, artifactActor(source.Scope), strings.NewReader("file rollback")); !errors.Is(e, ErrUnavailable) {
			t.Fatal("corrupt duplicate accepted", e)
		}
		meta.References = 1
		blob.Blob = &meta
		blob.Revision++
		if e = repo.Replace(testCtx, colBlobs, blob, blob.Revision-1); e != nil {
			return e
		}
		// A file permission failure happens after the pending marker commits.
		if e = os.Chmod(filepath.Join(store.dir, data.Digest), 0400); e != nil {
			return e
		}
		if e = b.detach(repo, saved, "tester"); e == nil {
			t.Fatal("unsafe file unlinked")
		}
		pending, e := repo.Get(testCtx, colBlobs, blobScope(), data.Digest)
		if e != nil || pending.Status != JobStatusPending || pending.Blob.References != 0 {
			t.Fatal("missing recoverable GC marker", e)
		}
		if e = os.Chmod(filepath.Join(store.dir, data.Digest), 0600); e != nil {
			return e
		}
		if e = repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			_, err := attachArtifactMetadata(ctx, tx, source.Scope, ref, "tool_output", data, artifactActor(source.Scope))
			return err
		}); !errors.Is(e, ErrUnavailable) {
			t.Fatal("pending accepted a new reference", e)
		}
		resumed := attachTestArtifact(t, b, repo, source, "file rollback")
		if resumed.ArtifactGeneration == saved.ArtifactGeneration {
			t.Fatal("GC resume reused deleted generation")
		}
		if e = b.detach(repo, resumed, "tester"); e != nil {
			return e
		}
		// Missing metadata is fenced before an orphan is removed.
		if _, _, e = b.put(strings.NewReader("orphan"), ""); e != nil {
			return e
		}
		if e = b.collect(artifactCommitUnknown{repo}, ContentHash("orphan")); !errors.Is(e, ErrUnavailable) {
			t.Fatal("uncertain GC commit hidden", e)
		}
		if e = b.verify(ContentHash("orphan"), ContentHash("orphan"), int64(len("orphan"))); e != nil {
			t.Fatal("GC unlinked without confirmed fence", e)
		}
		if e = b.collect(repo, ContentHash("orphan")); e != nil {
			return e
		}
		if _, e = os.Stat(filepath.Join(store.dir, ContentHash("orphan"))); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("orphan survived", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestArtifactFailures(t *testing.T) { exerciseArtifactFailures(t, newFakeRepo()) }

func exerciseArtifactSourceFence(t *testing.T, repo Repository) {
	t.Helper()
	store := blobStore{dir: blobTestDir(t)}
	source := artifactSource(t, repo, scopeA)
	source.Text = ""
	if e := repo.Replace(testCtx, "sources", source, source.Revision); e != nil {
		t.Fatal(e)
	}
	source, e := repo.Get(testCtx, "sources", scopeA, source.ID)
	if e != nil {
		t.Fatal(e)
	}
	err := store.locked(testCtx, func(b *lockedBlobs) error {
		emptyOriginal := attachTestArtifact(t, b, repo, source, "file-only original")
		if emptyOriginal.Sources[0].Span != nil {
			t.Fatal("artifact invented source text span")
		}
		for _, r := range []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision + 1}, {Collection: "sources", ID: "missing", Revision: 1}} {
			if _, e := b.attach(repo, scopeA, r, "tool_output", artifactData("file"), artifactActor(source.Scope), strings.NewReader("file")); e == nil {
				t.Fatal("invalid source attached")
			}
		}
		if _, e := b.attach(repo, scopeB, Ref{Collection: "sources", ID: source.ID, Revision: 1}, "tool_output", artifactData("file"), artifactActor(scopeB), strings.NewReader("file")); e == nil {
			t.Fatal("cross-scope source attached")
		}
		past := time.Now().Add(-time.Hour)
		source.ExpiresAt = &past
		source.Revision++
		if e := repo.Replace(testCtx, "sources", source, source.Revision-1); e != nil {
			return e
		}
		if _, e := b.attach(repo, scopeA, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, "tool_output", artifactData("file"), artifactActor(source.Scope), strings.NewReader("file")); !errors.Is(e, ErrConflict) {
			t.Fatal("expired source attached", e)
		}
		if e := repo.Delete(testCtx, "sources", scopeA, source.ID, source.Revision); e != nil {
			return e
		}
		if _, e := b.attach(repo, scopeA, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, "tool_output", artifactData("file"), artifactActor(source.Scope), strings.NewReader("file")); !errors.Is(e, ErrNotFound) {
			t.Fatal("deleted source attached", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestArtifactSourceFence(t *testing.T) { exerciseArtifactSourceFence(t, newFakeRepo()) }

func TestArtifactActorAndMimeBoundary(t *testing.T) {
	repo := newFakeRepo()
	source := artifactSource(t, repo, scopeA)
	store := blobStore{dir: blobTestDir(t)}
	err := store.locked(testCtx, func(b *lockedBlobs) error {
		ref := Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}
		data := artifactData("inspected")
		for _, actor := range []Decision{{Allowed: true, Scope: scopeA, PrincipalID: "tester", Role: "agent"}, {Allowed: true, Scope: scopeB, PrincipalID: "tester", Role: "service"}, {Scope: scopeA, PrincipalID: "tester", Role: "service"}} {
			if _, e := b.attach(repo, scopeA, ref, "tool_output", data, actor, strings.NewReader("inspected")); !errors.Is(e, ErrDenied) {
				t.Fatal("invalid actor accepted", e)
			}
		}
		actor := artifactActor(scopeA)
		actor.Role = "pipeline"
		bad := data
		bad.MIME = "application/zip"
		if _, e := b.attach(repo, scopeA, ref, "tool_output", bad, actor, strings.NewReader("inspected")); !errors.Is(e, ErrInvalid) {
			t.Fatal("unsupported MIME accepted", e)
		}
		if _, e := b.attach(repo, scopeA, ref, "audio", data, actor, strings.NewReader("inspected")); !errors.Is(e, ErrInvalid) {
			t.Fatal("kind/MIME mismatch accepted", e)
		}
		if entries, e := os.ReadDir(store.dir); e != nil || len(entries) != 1 {
			t.Fatal("denied request wrote a file", e)
		}
		saved, e := b.attach(repo, scopeA, ref, "tool_output", data, actor, strings.NewReader("inspected"))
		if e != nil || saved.CreatedByType != "pipeline" {
			t.Fatal("actor provenance lost", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// attach and detach drive publishArtifact/collect directly in tests; production
// registers artifacts through ingestSource and erases through the source saga.
func (b *lockedBlobs) attach(repo Repository, scope Scope, source Ref, kind string, data artifactMetadata, actor Decision, input io.Reader) (Record, error) {
	if !validArtifactActor(actor, scope) {
		return Record{}, ErrDenied
	}
	if !validArtifact(kind, data) {
		return Record{}, ErrInvalid
	}
	return b.publishArtifact(repo, data, input, func() (Record, error) {
		var result Record
		err := repo.Transaction(b.ctx, func(ctx context.Context, tx Tx) error {
			var e error
			result, e = attachArtifactMetadata(ctx, tx, scope, source, kind, data, actor)
			return e
		})
		return result, err
	})
}

func (b *lockedBlobs) detach(repo Repository, expected Record, actor string) error {
	if !validArtifactRecord(expected) {
		return ErrInvalid
	}
	if err := repo.Transaction(b.ctx, func(ctx context.Context, tx Tx) error { return detachArtifactMetadata(ctx, tx, expected, actor) }); err != nil {
		return err
	}
	err := b.collect(repo, expected.Artifact.Digest)
	if errors.Is(err, ErrConflict) {
		return nil
	} // Other sources still reference it.
	return err
}
