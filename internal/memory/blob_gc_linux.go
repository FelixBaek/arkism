package memory

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
)

// CollectBlobGarbage never detaches artifacts: live references always win.
// CollectBlobGarbage is internal, global storage housekeeping only. Never
// exposed by scoped CRUD.
func (s *Service) CollectBlobGarbage(ctx context.Context, token string) (BlobGCResult, error) {
	var result BlobGCResult
	actor, err := s.authorize(ctx, token, "maintain:blobs", blobScope(), "storage", "")
	if err != nil {
		return result, err
	}
	if actor.Role != "service" {
		return result, ErrDenied
	}
	runID := "blob-gc-" + ID()
	permit := func() error {
		d, e := s.authorize(ctx, token, "maintain:blobs", blobScope(), "storage", "")
		if e != nil {
			return e
		}
		if d.Role != "service" || d.PrincipalID != actor.PrincipalID {
			return ErrDenied
		}
		return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return audit(ctx, tx, blobScope(), actor.PrincipalID, "maintain:blobs", runID)
		})
	}
	err = s.withBlobs(ctx, func(b *lockedBlobs) error {
		// shortcut: one streaming pass under the shared flock; schedule off-peak.
		// Shorter lock batches need a measured inventory/resume contract first.
		directory, e := b.root.Open(".")
		if e != nil {
			return e
		}
		defer directory.Close()
		checkFile := func(name string, limit int64) error {
			file, e := openPrivateBlob(b.root, name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if e != nil {
				return e
			}
			defer file.Close()
			info, e := file.Stat()
			if e != nil {
				return e
			}
			if info.Size() > limit {
				return ErrUnavailable
			}
			return nil
		}
		collect := func(digest string) error {
			count, e := s.Repo.BlobReferences(ctx, digest)
			if e != nil {
				return e
			}
			if count > 0 {
				blob, e := s.Repo.Get(ctx, colBlobs, blobScope(), digest)
				if e != nil || blob.Kind != "blob" || blob.Status != statusActive || blob.Blob == nil || blob.Blob.References != count {
					return ErrUnavailable
				}
				result.ReferencedFiles++
				return nil
			}
			if e = permit(); e != nil {
				return e
			}
			if e = b.collect(s.Repo, digest); e != nil {
				return e
			}
			result.CollectedBlobs++
			return nil
		}
		for {
			if e = ctx.Err(); e != nil {
				return e
			}
			entries, readErr := directory.ReadDir(64)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for _, entry := range entries {
				name := entry.Name()
				if name == ".lock" || name == ".database" {
					continue
				}
				result.ScannedFiles++
				if sourceHashPattern.MatchString(name) {
					if e = checkFile(name, b.maxBytes); e != nil {
						return e
					}
					if e = collect(name); e != nil {
						return e
					}
					continue
				}
				limit := temporaryBlobLimit(name, b.maxBytes)
				if limit == 0 {
					return ErrUnavailable
				}
				if e = checkFile(name, limit); e != nil {
					return e
				}
				if e = permit(); e != nil {
					return e
				}
				if e = b.root.Remove(name); e != nil {
					return e
				}
				if e = directory.Sync(); e != nil {
					return e
				}
				result.TemporaryFilesRemoved++
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
		}
		return collectPendingBlobs(ctx, s.Repo, collect)
	})
	return result, err
}

// temporaryBlobLimit is the size bound of an upload or binding probe name, or 0 for any other name.
func temporaryBlobLimit(name string, maxBytes int64) int64 {
	limit := int64(0)
	suffix, upload := strings.CutPrefix(name, ".upload-")
	if upload {
		limit = maxBytes + 1
	} else {
		var binding bool
		suffix, binding = strings.CutPrefix(name, ".binding-")
		if binding {
			limit = 130
		}
	}
	raw, decodeErr := hex.DecodeString(suffix)
	if limit == 0 || decodeErr != nil || len(raw) != 16 || hex.EncodeToString(raw) != suffix {
		return 0
	}
	return limit
}

// A crash after unlink can leave no directory entry but a pending DB row.
func collectPendingBlobs(ctx context.Context, repo Repository, collect func(string) error) error {
	after := ""
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		ids, e := repo.BlobGCBatch(ctx, after)
		if e != nil {
			return e
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if !sourceHashPattern.MatchString(id) || id <= after {
				return ErrUnavailable
			}
			if e = collect(id); e != nil {
				return e
			}
			after = id
		}
	}
}
