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
		return s.adoptRestoredBlobs(ctx, b, token, actor)
	})
}

func (s *Service) adoptRestoredBlobs(ctx context.Context, b *lockedBlobs, token string, actor Decision) error {
	raw, files, m, err := readRestoreManifest(b)
	if err != nil {
		return err
	}
	identity, err := s.restoreTargetIdentity(ctx, m)
	if err != nil {
		return err
	}
	directory, err := b.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = verifyRestoredDirectory(ctx, b, directory, files); err != nil {
		return err
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
	rootID, err := s.restoredRootID(ctx, b, identity)
	if err != nil {
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
}

func readRestoreManifest(b *lockedBlobs) ([]byte, map[string]int64, blobBackupManifest, error) {
	var m blobBackupManifest
	pending, err := openPrivateBlob(b.root, ".restore-pending", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, m, err
	}
	raw, err := io.ReadAll(io.LimitReader(pending, maxBackupManifest+1))
	pending.Close()
	if err != nil {
		return nil, nil, m, err
	}
	if len(raw) > maxBackupManifest {
		return nil, nil, m, ErrInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF || !validRestoreManifest(m) {
		return nil, nil, m, ErrInvalid
	}
	files := map[string]int64{}
	for _, f := range m.Files {
		if !sourceHashPattern.MatchString(f.Digest) || f.Bytes < 1 || f.Bytes > b.maxBytes || files[f.Digest] != 0 {
			return nil, nil, m, ErrInvalid
		}
		files[f.Digest] = f.Bytes
	}
	return raw, files, m, nil
}

func validRestoreManifest(m blobBackupManifest) bool {
	return m.Version == 1 && sourceHashPattern.MatchString(m.SourceIdentity) && sourceHashPattern.MatchString(m.SourceRootID) && sourceHashPattern.MatchString(m.Database.Digest) && m.Database.Bytes >= 1 && m.Files != nil && len(m.Files) <= maxBackupFiles
}

func (s *Service) restoreTargetIdentity(ctx context.Context, m blobBackupManifest) (string, error) {
	identity, err := s.Repo.BlobStorageIdentity(ctx)
	if err != nil {
		return "", err
	}
	if !sourceHashPattern.MatchString(identity) {
		return "", ErrUnavailable
	}
	if identity == m.SourceIdentity {
		return "", ErrDenied
	}
	origin, err := s.Repo.Get(ctx, colBlobs, blobScope(), "storage-binding-"+m.SourceIdentity)
	if err != nil {
		return "", err
	}
	if origin.Kind != "blob_root_binding" || origin.Status != statusActive || origin.BlobRootID != m.SourceRootID {
		return "", ErrUnavailable
	}
	return identity, nil
}

func verifyRestoredDirectory(ctx context.Context, b *lockedBlobs, directory *os.File, files map[string]int64) error {
	seen := 0
	for {
		entries, e := directory.ReadDir(64)
		if e != nil && !errors.Is(e, io.EOF) {
			return e
		}
		for _, entry := range entries {
			counted, err := verifyRestoredEntry(b, entry.Name(), files)
			if err != nil {
				return err
			}
			if counted {
				seen++
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
	return nil
}

func verifyRestoredEntry(b *lockedBlobs, name string, files map[string]int64) (bool, error) {
	if name == ".lock" || name == ".restore-pending" || name == ".database" {
		return false, nil
	}
	if size := files[name]; size > 0 {
		if e := b.verify(name, name, size); e != nil {
			return false, e
		}
		return true, nil
	}
	// A retry may find the unfinished binding publication's private probe.
	suffix, ok := strings.CutPrefix(name, ".binding-")
	decoded, e := hex.DecodeString(suffix)
	if !ok || e != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != suffix {
		return false, ErrInvalid
	}
	file, e := openPrivateBlob(b.root, name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return false, e
	}
	info, e := file.Stat()
	file.Close()
	if e != nil {
		return false, e
	}
	if info.Size() > 130 {
		return false, ErrInvalid
	}
	return false, nil
}

func (s *Service) restoredRootID(ctx context.Context, b *lockedBlobs, identity string) (string, error) {
	_, err := b.root.Lstat(".database")
	if errors.Is(err, os.ErrNotExist) {
		// Never replace another live root's DB registration.
		_, err = s.Repo.Get(ctx, colBlobs, blobScope(), "storage-binding-"+identity)
		if !errors.Is(err, ErrNotFound) {
			if err == nil {
				return "", ErrConflict
			}
			return "", err
		}
		rootID := ContentHash(ID())
		if err = b.publishBinding(identity, rootID, true); err != nil {
			return "", err
		}
		return rootID, nil
	} else if err == nil {
		return b.bind(identity)
	}
	return "", err
}
