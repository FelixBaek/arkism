package memory

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// ingestSource accepts only previously authorized/inspected local input.
// Source registration and the artifact reference share one transaction.
func (b *lockedBlobs) ingestSource(s *Service, token string, in CreateInput, proposed Record, actor Decision, file sourceArtifact, input io.Reader) (Record, error) {
	if err := checkSourceArtifact(in, proposed, actor, file); err != nil {
		return Record{}, err
	}
	return b.publishArtifact(s.Repo, file.Data, input, func() (Record, error) {
		return s.createSource(b.ctx, token, in, proposed, actor, &file)
	})
}

func (b *lockedBlobs) publishArtifact(repo Repository, data artifactMetadata, input io.Reader, register func() (Record, error)) (Record, error) {
	blob, err := repo.Get(b.ctx, colBlobs, blobScope(), data.Digest)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	if err == nil && blob.Status == JobStatusPending {
		if err = b.collect(repo, data.Digest); err != nil {
			return Record{}, err
		}
	}
	// Every published byte passes the full-content secret scan on the way in;
	// a blob cannot land in the store uninspected.
	_, size, err := b.put(newFullScanReader(input), data.Digest)
	if err == nil && size != data.Bytes {
		err = ErrInvalid
	}
	var result Record
	if err == nil {
		result, err = register()
	}
	if err != nil {
		// A successful GC fence also serializes any earlier ambiguous commit. If the
		// DB is unavailable or the file has references, preserve bytes for later GC.
		_ = b.collect(repo, data.Digest)
		return Record{}, err
	}
	return result, nil
}

// collect first commits a pending marker, including for a metadata-free orphan.
// Its write to the same digest fences any pending registration transaction. Only
// after that commit is confirmed may bytes be unlinked. Pending forbids attach.
func (b *lockedBlobs) collect(repo Repository, digest string) error {
	if !sourceHashPattern.MatchString(digest) {
		return ErrInvalid
	}
	var marker Record
	err := repo.Transaction(b.ctx, func(ctx context.Context, tx Tx) error {
		count, e := tx.BlobReferences(ctx, digest)
		if e != nil {
			return e
		}
		if count != 0 {
			return ErrConflict
		}
		if e := tx.CheckArtifactPrivacyHold(ctx, digest); e != nil {
			return e
		}
		marker, e = tx.Get(ctx, colBlobs, blobScope(), digest)
		now := time.Now().UTC()
		if errors.Is(e, ErrNotFound) {
			marker = Record{ID: digest, Scope: blobScope(), Kind: "blob", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "memoryd", CreatedByType: "service", Blob: &blobMetadata{}}
			return tx.Insert(ctx, colBlobs, marker)
		}
		if e != nil {
			return e
		}
		if marker.Kind != "blob" || marker.Blob == nil || marker.Blob.References != 0 || (marker.Status != statusActive && marker.Status != JobStatusPending) {
			return ErrUnavailable
		}
		marker.Status = JobStatusPending
		marker.Revision++
		marker.UpdatedAt = now
		return tx.Replace(ctx, colBlobs, marker, marker.Revision-1)
	})
	if err != nil {
		return err
	}
	info, err := b.root.Lstat(digest)
	if err == nil {
		if !info.Mode().IsRegular() {
			return ErrUnavailable
		}
		file, e := openPrivateBlob(b.root, digest, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return e
		}
		file.Close()
		if err = b.root.Remove(digest); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory, err := b.root.Open(".")
	if err != nil {
		return err
	}
	err = directory.Sync()
	directory.Close()
	if err != nil {
		return err
	}
	return repo.Transaction(b.ctx, func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, colBlobs, blobScope(), digest)
		if e != nil {
			return e
		}
		if current.Status != JobStatusPending || current.Revision != marker.Revision || current.Blob == nil || current.Blob.References != 0 {
			return ErrConflict
		}
		return tx.Delete(ctx, colBlobs, blobScope(), digest, current.Revision)
	})
}
