package memory

import (
	"context"
	"errors"
	"time"
)

// Collection UUID survives restarts but changes on an ordinary database restore.
// Do not use UUID-preserving clones against the same local file directory.

func bindBlobRoot(ctx context.Context, repo Repository, identity, rootID string) error {
	if !sourceHashPattern.MatchString(identity) || !sourceHashPattern.MatchString(rootID) {
		return ErrInvalid
	}
	id := "storage-binding-" + identity
	return repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		saved, err := tx.Get(ctx, colBlobs, blobScope(), id)
		if err == nil {
			if saved.Kind != "blob_root_binding" || saved.Status != statusActive || saved.BlobRootID != rootID {
				return ErrUnavailable
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := time.Now().UTC()
		return tx.Insert(ctx, colBlobs, Record{ID: id, Scope: blobScope(), Kind: "blob_root_binding", Status: statusActive, Revision: 1, CreatedBy: "memoryd", CreatedByType: "service", CreatedAt: now, UpdatedAt: now, BlobRootID: rootID})
	})
}
