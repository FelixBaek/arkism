package memory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
)

// AdoptRestoredBlobs only opens a quarantined restore into a different DB UUID.
// The operator must keep all writers stopped until deletion replay completes.
func (s *Service) AdoptRestoredBlobs(ctx context.Context, token string) error {
	actor, err := s.authorize(ctx, token, "restore:blobs", blobScope(), "storage", "")
	if err != nil {
		return err
	}
	if actor.Role != "service" {
		return ErrDenied
	}
	if s.Repo == nil || s.BlobDirectory == "" {
		return ErrUnavailable
	}
	return (blobStore{dir: s.BlobDirectory}).locked(ctx, func(b *lockedBlobs) error {
		pending, err := openPrivateBlob(b.root, ".restore-pending", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(pending, maxBackupManifest+1))
		pending.Close()
		if err != nil {
			return err
		}
		if len(raw) > maxBackupManifest {
			return ErrInvalid
		}
		var m blobBackupManifest
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF || m.Version != 1 || !sourceHashPattern.MatchString(m.SourceIdentity) || !sourceHashPattern.MatchString(m.SourceRootID) || !sourceHashPattern.MatchString(m.Database.Digest) || m.Database.Bytes < 1 || m.Files == nil || len(m.Files) > maxBackupFiles {
			return ErrInvalid
		}
		files := map[string]int64{}
		for _, f := range m.Files {
			if !sourceHashPattern.MatchString(f.Digest) || f.Bytes < 1 || f.Bytes > b.maxBytes || files[f.Digest] != 0 {
				return ErrInvalid
			}
			files[f.Digest] = f.Bytes
		}
		identity, err := s.Repo.BlobStorageIdentity(ctx)
		if err != nil {
			return err
		}
		if !sourceHashPattern.MatchString(identity) {
			return ErrUnavailable
		}
		if identity == m.SourceIdentity {
			return ErrDenied
		}
		origin, err := s.Repo.Get(ctx, colBlobs, blobScope(), "storage-binding-"+m.SourceIdentity)
		if err != nil {
			return err
		}
		if origin.Kind != "blob_root_binding" || origin.Status != statusActive || origin.BlobRootID != m.SourceRootID {
			return ErrUnavailable
		}
		directory, err := b.root.Open(".")
		if err != nil {
			return err
		}
		defer directory.Close()
		seen := 0
		for {
			entries, e := directory.ReadDir(64)
			if e != nil && !errors.Is(e, io.EOF) {
				return e
			}
			for _, entry := range entries {
				name := entry.Name()
				if name == ".lock" || name == ".restore-pending" || name == ".database" {
					continue
				}
				if size := files[name]; size > 0 {
					if e := b.verify(name, name, size); e != nil {
						return e
					}
					seen++
					continue
				}
				// A retry may find the unfinished binding publication's private probe.
				suffix, ok := strings.CutPrefix(name, ".binding-")
				decoded, e := hex.DecodeString(suffix)
				if !ok || e != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != suffix {
					return ErrInvalid
				}
				file, e := openPrivateBlob(b.root, name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
				if e != nil {
					return e
				}
				info, e := file.Stat()
				file.Close()
				if e != nil {
					return e
				}
				if info.Size() > 130 {
					return ErrInvalid
				}
			}
			if errors.Is(e, io.EOF) {
				break
			}
			if e = ctx.Err(); e != nil {
				return e
			}
		}
		if seen != len(files) {
			return ErrUnavailable
		}
		if err = s.Repo.ValidateBlobRestore(ctx, files); err != nil {
			return err
		}
		d, err := s.authorize(ctx, token, "restore:blobs", blobScope(), "storage", "")
		if err != nil {
			return err
		}
		if d.Role != "service" || d.PrincipalID != actor.PrincipalID {
			return ErrDenied
		}
		if err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return audit(ctx, tx, blobScope(), actor.PrincipalID, "restore:blobs", "storage")
		}); err != nil {
			return err
		}
		review, ok := s.Repo.(restoreReviewStore)
		if !ok {
			return ErrUnavailable
		}
		if err = review.RequireRestoreReview(ctx, ContentHash(string(raw))); err != nil {
			return err
		}
		rootID := ""
		_, err = b.root.Lstat(".database")
		if errors.Is(err, os.ErrNotExist) {
			// Never replace another live root's DB registration.
			_, err = s.Repo.Get(ctx, colBlobs, blobScope(), "storage-binding-"+identity)
			if !errors.Is(err, ErrNotFound) {
				if err == nil {
					return ErrConflict
				}
				return err
			}
			rootID = ContentHash(ID())
			if err = b.publishBinding(identity, rootID, true); err != nil {
				return err
			}
		} else if err == nil {
			rootID, err = b.bind(identity)
			if err != nil {
				return err
			}
		} else {
			return err
		}
		if err = bindBlobRoot(ctx, s.Repo, identity, rootID); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = b.root.Remove(".restore-pending"); err != nil {
			return err
		}
		return directory.Sync()
	})
}
