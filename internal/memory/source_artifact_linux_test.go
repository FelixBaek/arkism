package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Build the already validated/authorized inputs expected by the private
// coordinator. This does not substitute for a future HTTP ingestion test.
func sourceFileProposal(in CreateInput, actor Decision) Record {
	now := time.Now().UTC()
	r := Record{ID: ID(), Scope: in.Scope, Kind: in.Kind, Text: in.Text, Status: statusActive, Revision: 1, OriginProduct: in.OriginProduct, SourceRef: in.SourceRef, CollectedAt: in.CollectedAt, Sensitivity: in.Sensitivity, Inspection: &Inspection{RulesVersion: inspectionVersion}, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
	r.Sources = []SourceRef{sourceReference(r, "original")}
	return r
}

func exerciseSourceArtifactRegistration(t *testing.T, repo Repository) {
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.BlobDirectory = blobTestDir(t)
	in := sourceInput(CreateInput{Scope: scopeA, Text: "source with file", Sensitivity: "internal", IdempotencyKey: "first-file"})
	body := "shared original file"
	file := sourceArtifact{Kind: "tool_output", Data: artifactData(body)}
	ingest := func(in CreateInput, file sourceArtifact, body string) (Record, error) {
		d, e := f.svc.authorize(testCtx, f.human, "write:sources", in.Scope, "", structuredText(in))
		if e != nil {
			return Record{}, e
		}
		var result Record
		e = f.svc.withBlobs(testCtx, func(b *lockedBlobs) error {
			var err error
			result, err = b.ingestSource(f.svc, f.human, in, sourceFileProposal(in, d), d, file, strings.NewReader(body))
			return err
		})
		return result, e
	}
	first, err := ingest(in, file, body)
	if err != nil || first.Duplicate || first.ContentHash != ContentHash(in.Text) {
		t.Fatal("first file source", err)
	}
	again, err := ingest(in, file, body)
	if err != nil || structuredText(first) != structuredText(again) {
		t.Fatal("file receipt changed response", err)
	}
	in.IdempotencyKey = "duplicate-file"
	duplicate, err := ingest(in, file, body)
	if err != nil || !duplicate.Duplicate || duplicate.ID != first.ID {
		t.Fatal("file source duplicate", err)
	}
	for _, changed := range []sourceArtifact{
		{Kind: file.Kind, Data: artifactData("different original file")},
		{Kind: "code", Data: file.Data},
		{Kind: file.Kind, Data: artifactMetadata{Digest: file.Data.Digest, Bytes: file.Data.Bytes, MIME: "application/json"}},
	} {
		payload := body
		if changed.Data.Digest != file.Data.Digest {
			payload = "different original file"
		}
		if _, err = ingest(in, changed, payload); !errors.Is(err, ErrConflict) {
			t.Fatal("changed file reused receipt", err)
		}
	}
	if _, err = os.Stat(filepath.Join(f.svc.BlobDirectory, ContentHash("different original file"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected file not collected", err)
	}
	keyless := in
	keyless.IdempotencyKey = ""
	for _, changed := range []sourceArtifact{
		{Kind: "code", Data: file.Data},
		{Kind: file.Kind, Data: artifactMetadata{Digest: file.Data.Digest, Bytes: file.Data.Bytes, MIME: "application/json"}},
	} {
		if _, err = ingest(keyless, changed, body); !errors.Is(err, ErrConflict) {
			t.Fatal("keyless duplicate changed artifact metadata", err)
		}
	}
	if count, e := repo.BlobReferences(testCtx, file.Data.Digest); e != nil || count != 1 {
		t.Fatal("keyless conflict changed reference count", count, e)
	}
	// A text-only source must not collide with a source containing the same text
	// plus a file. Their legacy content_hash values intentionally remain equal.
	textIn := in
	textIn.IdempotencyKey = "text-only"
	plain, err := f.svc.Create(testCtx, f.human, "sources", textIn)
	if err != nil || plain.ID == first.ID {
		t.Fatal("file/text dedup identities mixed", err)
	}
	alternate := in
	alternate.IdempotencyKey = ""
	alternateBody := "second actual file"
	alternateFile := sourceArtifact{Kind: file.Kind, Data: artifactData(alternateBody)}
	alternateSource, err := ingest(alternate, alternateFile, alternateBody)
	if err != nil || alternateSource.ID == first.ID || alternateSource.ID == plain.ID {
		t.Fatal("distinct file identity collapsed", err)
	}
	// Read permission is still required to return an existing source. A denied
	// response must never cause cleanup to unlink its committed file.
	f.auth.actors[f.human].verbs["read"] = false
	if _, err = ingest(in, file, body); !errors.Is(err, ErrDenied) {
		t.Fatal("write-only replay returned existing source", err)
	}
	f.auth.actors[f.human].verbs["read"] = true
	other := in
	other.Scope = scopeB
	second, err := ingest(other, file, body)
	if err != nil || second.ID == first.ID {
		t.Fatal("cross-scope file source", err)
	}
	fileOnly := in
	fileOnly.Text = ""
	fileOnly.IdempotencyKey = "file-only"
	only, err := ingest(fileOnly, file, body)
	if err != nil || only.ID == first.ID || len(only.Sources) != 1 || only.Sources[0].Span != nil {
		t.Fatal("file-only source has an invalid empty text span", err)
	}
	blob, err := repo.Get(testCtx, colBlobs, blobScope(), file.Data.Digest)
	if err != nil || blob.Blob == nil || blob.Blob.References != 3 {
		t.Fatal("shared file count", err)
	}
	if n, e := repo.BlobReferences(testCtx, file.Data.Digest); e != nil || n != 3 {
		t.Fatal("shared actual references", n, e)
	}
	if err = f.svc.withBlobs(testCtx, func(b *lockedBlobs) error { return b.verify(file.Data.Digest, file.Data.Digest, file.Data.Bytes) }); err != nil {
		t.Fatal("stored digest", err)
	}
	// A missing reference is corruption, not a request to resurrect it.
	a, err := repo.Get(testCtx, colArtifacts, in.Scope, artifactID(in.Scope, first.ID, file.Data.Digest))
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(testCtx, colArtifacts, in.Scope, a.ID, a.Revision); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first-file", "new-file-key"} {
		in.IdempotencyKey = key
		if _, err = ingest(in, file, body); !errors.Is(err, ErrUnavailable) {
			t.Fatal("missing reference recreated", err)
		}
	}
	if _, err = repo.Get(testCtx, colArtifacts, in.Scope, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing artifact recreated", err)
	}
}

type sourceFinalAuditFailure struct{ Repository }
type sourceFinalAuditTx struct{ Tx }

func (x sourceFinalAuditTx) Insert(ctx context.Context, col string, r Record) error {
	if col == "audit_events" && r.Kind == "create:sources" {
		return ErrUnavailable
	}
	return x.Tx.Insert(ctx, col, r)
}
func (r sourceFinalAuditFailure) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, sourceFinalAuditTx{tx}) })
}

func exerciseSourceArtifactFailure(t *testing.T, repo Repository) {
	f := newFixture(t)
	f.svc.Repo = repo
	store := blobStore{dir: blobTestDir(t)}
	in := sourceInput(CreateInput{Scope: scopeA, Text: "atomic file source", Sensitivity: "internal", IdempotencyKey: "atomic-file"})
	d, err := f.svc.authorize(testCtx, f.human, "write:sources", in.Scope, "", structuredText(in))
	if err != nil {
		t.Fatal(err)
	}
	body := "atomic original file"
	file := sourceArtifact{Kind: "tool_output", Data: artifactData(body)}
	proposal := sourceFileProposal(in, d)
	if err = store.locked(testCtx, func(b *lockedBlobs) error {
		for _, mutate := range []func(*Record){
			func(r *Record) { r.Text = "uninspected replacement" },
			func(r *Record) { r.Sensitivity = "public" },
			func(r *Record) { r.Kind = "other" },
			func(r *Record) { r.Inspection = nil },
		} {
			bad := proposal
			mutate(&bad)
			if _, e := b.ingestSource(f.svc, f.human, in, bad, d, file, strings.NewReader(body)); !errors.Is(e, ErrInvalid) {
				t.Fatal("invalid proposal accepted", e)
			}
			if _, e := os.Stat(filepath.Join(store.dir, file.Data.Digest)); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("invalid proposal published bytes", e)
			}
		}
		f.svc.Repo = sourceFinalAuditFailure{repo}
		if _, e := b.ingestSource(f.svc, f.human, in, proposal, d, file, strings.NewReader(body)); !errors.Is(e, ErrUnavailable) {
			t.Fatal("final audit failure ignored", e)
		}
		checks := []struct {
			col, id string
			scope   Scope
		}{
			{"sources", proposal.ID, in.Scope},
			{colArtifacts, artifactID(in.Scope, proposal.ID, file.Data.Digest), in.Scope},
			{colBlobs, file.Data.Digest, blobScope()},
			{"jobs", "source-receipt-" + ContentHash(structuredText(in.Scope)+"\n"+d.PrincipalID+"\n"+in.IdempotencyKey), in.Scope},
			{"jobs", "source-digest-" + ContentHash(structuredText(in.Scope)+"\nsource-file-v1\n"+ContentHash(in.Text)+"\n"+file.Data.Digest), in.Scope},
		}
		for _, check := range checks {
			if _, e := repo.Get(testCtx, check.col, check.scope, check.id); !errors.Is(e, ErrNotFound) {
				t.Fatal("partial metadata after rollback", check.col, e)
			}
		}
		if _, e := os.Stat(filepath.Join(store.dir, file.Data.Digest)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("rollback left unreferenced file", e)
		}
		f.svc.Repo = artifactCommitUnknown{repo}
		if _, e := b.ingestSource(f.svc, f.human, in, proposal, d, file, strings.NewReader(body)); !errors.Is(e, ErrUnavailable) {
			t.Fatal("ambiguous commit not propagated", e)
		}
		if e := b.verify(file.Data.Digest, file.Data.Digest, file.Data.Bytes); e != nil {
			t.Fatal("ambiguous committed bytes deleted", e)
		}
		f.svc.Repo = repo
		again, e := b.ingestSource(f.svc, f.human, in, sourceFileProposal(in, d), d, file, strings.NewReader(body))
		if e != nil || again.ID != proposal.ID || again.Duplicate {
			t.Fatal("ambiguous commit replay", e)
		}
		blob, e := repo.Get(testCtx, colBlobs, blobScope(), file.Data.Digest)
		if e != nil || blob.Blob == nil || blob.Blob.References != 1 {
			t.Fatal("ambiguous commit duplicate refcount", e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceArtifactRegistration(t *testing.T) {
	exerciseSourceArtifactRegistration(t, newFakeRepo())
}
func TestSourceArtifactFailure(t *testing.T) { exerciseSourceArtifactFailure(t, newFakeRepo()) }
