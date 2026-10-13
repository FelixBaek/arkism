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

func (s *Service) withBlobs(ctx context.Context, fn func(*lockedBlobs) error) error {
	if s.Repo == nil || s.BlobDirectory == "" {
		return ErrUnavailable
	}
	return (blobStore{dir: s.BlobDirectory}).locked(ctx, func(b *lockedBlobs) error {
		if _, err := b.root.Lstat(".restore-pending"); err == nil {
			return ErrUnavailable
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		identity, err := s.Repo.BlobStorageIdentity(ctx)
		if err != nil {
			return err
		}
		rootID, err := b.bind(identity)
		if err != nil {
			return err
		}
		if err = bindBlobRoot(ctx, s.Repo, identity, rootID); err != nil {
			return err
		}
		return fn(b)
	})
}

func (s *Service) CheckBlobStorage(ctx context.Context) error {
	if s.BlobDirectory == "" {
		return nil
	}
	return s.withBlobs(ctx, func(*lockedBlobs) error { return nil })
}

// Bind only an empty root. A matching marker still gets a write/fsync probe so
// readiness detects a read-only filesystem before destructive work is attempted.
func (b *lockedBlobs) bind(identity string) (string, error) {
	rootID := ContentHash(ID())
	if !sourceHashPattern.MatchString(identity) {
		return "", ErrInvalid
	}
	marker, err := openPrivateBlob(b.root, ".database", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	fresh := errors.Is(err, os.ErrNotExist)
	if !fresh {
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(marker, 131))
		marker.Close()
		if err != nil {
			return "", err
		}
		parts := strings.Split(string(data), "\n")
		if len(parts) != 3 || parts[0] != identity || !sourceHashPattern.MatchString(parts[1]) || parts[2] != "" {
			return "", ErrUnavailable
		}
		rootID = parts[1]
	}
	directory, err := b.root.Open(".")
	if err != nil {
		return "", err
	}
	defer directory.Close()
	if fresh {
		entries, e := directory.ReadDir(3)
		if e != nil && !errors.Is(e, io.EOF) {
			return "", e
		}
		// A first-bind crash can leave one private temporary marker, never file data.
		if len(entries) > 2 {
			return "", ErrUnavailable
		}
		var stale string
		for _, entry := range entries {
			if entry.Name() == ".lock" {
				continue
			}
			suffix, ok := strings.CutPrefix(entry.Name(), ".binding-")
			raw, e := hex.DecodeString(suffix)
			if !ok || e != nil || len(raw) != 16 || hex.EncodeToString(raw) != suffix {
				return "", ErrUnavailable
			}
			stale = entry.Name()
		}
		if stale != "" {
			file, e := openPrivateBlob(b.root, stale, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if e != nil {
				return "", e
			}
			info, statErr := file.Stat()
			file.Close()
			if statErr != nil {
				return "", statErr
			}
			if info.Size() > 130 {
				return "", ErrUnavailable
			}
			if e = b.root.Remove(stale); e != nil {
				return "", e
			}
		}
	}

	if err = b.publishBinding(identity, rootID, fresh); err != nil {
		return "", err
	}
	return rootID, nil
}

func (b *lockedBlobs) publishBinding(identity, rootID string, fresh bool) error {
	directory, err := b.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()

	name := ".binding-" + ID()
	probe, err := b.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer b.root.Remove(name)
	_, err = probe.WriteString(identity + "\n" + rootID + "\n")
	if err == nil {
		err = probe.Sync()
	}
	closeErr := probe.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = b.ctx.Err(); err != nil {
		return err
	}
	if fresh {
		err = b.root.Rename(name, ".database")
	} else {
		err = b.root.Remove(name)
	}
	if err != nil {
		return err
	}
	if err = directory.Sync(); err != nil {
		return err
	}
	return nil
}
