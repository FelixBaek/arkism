package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// A private, single-host directory shared by the API and worker. Its parent
// directories must also be operator-controlled; never replace .lock while live.
type blobStore struct {
	dir      string
	maxBytes int64 // Zero uses the 200 MiB default.
}
type lockedBlobs struct {
	root     *os.Root
	ctx      context.Context
	maxBytes int64
}

func (s blobStore) locked(ctx context.Context, fn func(*lockedBlobs) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	limit := s.maxBytes
	if limit == 0 {
		limit = 200 << 20
	}
	if limit < 1 || limit > 200<<20 {
		return ErrInvalid
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return ErrInvalid
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return ErrInvalid
	}
	lock, err := openPrivateBlob(root, ".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	// shortcut: one flock serializes all file/metadata mutations on this host;
	// use per-digest locks only if measured contention requires it.
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err = ctx.Err(); err != nil {
		return err
	}
	return fn(&lockedBlobs{root: root, ctx: ctx, maxBytes: limit})
}

// os.Root may resolve in-root symlinks itself even with O_NOFOLLOW. Check
// the directory entry too; all legitimate writers serialize on this root.
func openPrivateBlob(root *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil && !(errors.Is(err, os.ErrNotExist) && flags&os.O_CREATE != 0) {
		return nil, err
	}
	if err == nil && !before.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	file, err := root.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	if err = privateBlobFile(file); err == nil {
		var current, opened os.FileInfo
		current, err = root.Lstat(name)
		if err == nil {
			opened, err = file.Stat()
		}
		if err == nil && (!current.Mode().IsRegular() || !os.SameFile(current, opened)) {
			err = ErrInvalid
		}
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func privateBlobFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return ErrInvalid
	}
	return nil
}

// put is deliberately not a DB operation. The caller must hold locked through
// metadata commit/cleanup; a failed or ambiguous commit must never expose a file
// by digest alone. Only authorized source metadata may make bytes readable.
// input must be a local file or deadline-bounded reader: context cannot interrupt
// an arbitrary Reader blocked inside Read. HTTP staging belongs before this lock.
func (b *lockedBlobs) put(input io.Reader, expected string) (digest string, size int64, err error) {
	if expected != "" && !sourceHashPattern.MatchString(expected) {
		return "", 0, ErrInvalid
	}
	if err = b.ctx.Err(); err != nil {
		return "", 0, err
	}
	name := ".upload-" + ID()
	file, err := b.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		file.Close()
		if e := b.root.Remove(name); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	hash := sha256.New()
	size, err = io.Copy(io.MultiWriter(file, hash), io.LimitReader(blobContextReader{b.ctx, input}, b.maxBytes+1))
	if err != nil {
		return "", 0, err
	}
	if size == 0 {
		return "", 0, ErrInvalid
	}
	if size > b.maxBytes {
		return "", 0, errBlobTooLarge
	}
	digest = hex.EncodeToString(hash.Sum(nil))
	if expected != "" && expected != digest {
		return "", 0, errBlobDigestMismatch
	}
	if err = file.Sync(); err != nil {
		return "", 0, err
	}
	if err = file.Close(); err != nil {
		return "", 0, err
	}
	if err = b.verify(name, digest, size); err != nil {
		return "", 0, err
	}
	if err = b.ctx.Err(); err != nil {
		return "", 0, err
	}
	if _, err = b.root.Lstat(digest); err == nil {
		if err = b.verify(digest, digest, size); err != nil {
			return "", 0, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err = b.root.Rename(name, digest); err != nil {
			return "", 0, err
		}
	} else {
		return "", 0, err
	}
	// Retry also syncs the directory: a prior rename may have succeeded before
	// its fsync failed. A retry must not mistake that for durable publication.
	directory, err := b.root.Open(".")
	if err != nil {
		return "", 0, err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return "", 0, err
	}
	return digest, size, nil
}

func (b *lockedBlobs) verify(name, digest string, size int64) error {
	file, err := openPrivateBlob(b.root, name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(blobContextReader{b.ctx, file}, b.maxBytes+1))
	if err != nil {
		return err
	}
	if count != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return ErrInvalid
	}
	return nil
}

type blobContextReader struct {
	ctx   context.Context
	input io.Reader
}

func (r blobContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(p)
}
