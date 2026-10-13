package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const maxCompareBytes = 512 * 1024 * 1024

// CompareFiles hashes regular files beneath the caller's checkout; it never follows a saved path outside it.
func CompareFiles(cwd string, files []any) ([]any, error) {
	results := []any{}
	root, err := unix.Open(cwd, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: cwd, Err: err}
	}
	defer unix.Close(root)
	for _, raw := range files {
		item, _ := raw.(map[string]any)
		path, isStr := item["path"].(string)
		expected, hashStr := item["sha256"].(string)
		parts, ok := pathParts(path)
		if !isStr || !ok || !hashStr || !hex64.MatchString(expected) {
			p := item["path"]
			if p == nil {
				p = ""
			}
			results = append(results, map[string]any{"path": p, "state": "invalid_path_or_hash"})
			continue
		}
		results = append(results, map[string]any{"path": path, "state": compareOne(root, parts, expected)})
	}
	return results, nil
}

func compareOne(root int, parts []string, expected string) string {
	dir, err := unix.Dup(root)
	if err != nil {
		return "unreadable_or_symlink"
	}
	defer func() { unix.Close(dir) }()
	for _, component := range parts[:len(parts)-1] {
		child, err := unix.Openat(dir, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return openState(err)
		}
		unix.Close(dir)
		dir = child
	}
	last := parts[len(parts)-1]
	fd, err := unix.Openat(dir, last, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return openState(err)
	}
	f := os.NewFile(uintptr(fd), last)
	defer f.Close()
	var before, after, current unix.Stat_t
	if unix.Fstat(fd, &before) != nil {
		return "unreadable_or_symlink"
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return "not_regular"
	}
	if before.Size > maxCompareBytes {
		return "too_large"
	}
	digest := sha256.New()
	total, err := io.Copy(digest, io.LimitReader(f, maxCompareBytes+1))
	if err != nil {
		return "unreadable_or_symlink"
	}
	if unix.Fstat(fd, &after) != nil || unix.Fstatat(dir, last, &current, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return "unreadable_or_symlink"
	}
	if total > maxCompareBytes || stamp(before) != stamp(after) || stamp(after) != stamp(current) {
		return "changed_during_read"
	}
	if hex.EncodeToString(digest.Sum(nil)) == expected {
		return "match"
	}
	return "changed"
}

type statStamp struct {
	dev, ino     uint64
	size         int64
	mtime, ctime int64
}

func stamp(s unix.Stat_t) statStamp {
	return statStamp{uint64(s.Dev), uint64(s.Ino), s.Size, s.Mtim.Nano(), s.Ctim.Nano()}
}

func openState(err error) string {
	if errors.Is(err, unix.ENOENT) {
		return "missing"
	}
	return "unreadable_or_symlink"
}
