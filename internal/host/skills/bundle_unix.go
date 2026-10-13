//go:build unix

package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// openDirectory opens every ancestor without following links, so a walk from the handle cannot escape.
func openDirectory(dir string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, &os.PathError{Op: "open", Path: dir, Err: err}
		}
		fd = next
	}
	return fd, nil
}

func openRelative(root int, relative string) (int, error) {
	fd, err := unix.Dup(root)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, &os.PathError{Op: "open", Path: relative, Err: err}
		}
		fd = next
	}
	return fd, nil
}

type stamp struct{ size, mtime, ctime int64 }

func stampOf(s unix.Stat_t) stamp {
	return stamp{s.Size, s.Mtim.Nano(), s.Ctim.Nano()}
}

// readAt reads a whole regular file without following links: raw is nil when over limit (limit < 0 keeps all).
func readAt(dir int, name string, limit int) (string, int, []byte, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", 0, nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return "", 0, nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", 0, nil, errors.New("skill_bundle_not_regular")
	}
	h, size, buf := sha256.New(), 0, make([]byte, 1<<20)
	var head []byte
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if limit < 0 || size <= limit {
				head = append(head, buf[:n]...)
			}
			size += n
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, nil, err
		}
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return "", 0, nil, err
	}
	if stampOf(before) != stampOf(after) || int64(size) != after.Size {
		return "", 0, nil, errors.New("skill_bundle_source_changed")
	}
	if limit >= 0 && size > limit {
		head = nil
	} else if head == nil {
		head = []byte{}
	}
	return hex.EncodeToString(h.Sum(nil)), size, head, nil
}

// captureBundle collects a skill's text companions (Linux/server collector): linked, special or
// changing companions are refused. Paths are relative to the nearest plugin root within the
// inventory (its shared folders plus this skill's folder), otherwise to the skill folder.
func captureBundle(sourcePath, inventoryRoot, expectedHash string) (map[string]any, error) {
	source, _ := filepath.Abs(sourcePath)
	boundary := filepath.Dir(source)
	if inventoryRoot != "" {
		boundary, _ = filepath.Abs(inventoryRoot)
	}
	if !isUnder(source, boundary) {
		return nil, errors.New("skill_bundle_outside_root")
	}
	skillDir := filepath.Dir(source)
	root := skillDir
	for folder := skillDir; ; folder = filepath.Dir(folder) {
		if folder != boundary && !isUnder(folder, boundary) {
			break
		}
		if isDir(filepath.Join(folder, ".claude-plugin")) {
			root = folder
			break
		}
		if folder == filepath.Dir(folder) {
			break
		}
	}
	skillRel, _ := filepath.Rel(root, skillDir)
	sourceRel, _ := filepath.Rel(root, source)
	starts := []string{skillRel}
	if root != skillDir {
		for _, d := range sharedDirs {
			if !strings.HasPrefix(skillRel, d+"/") && skillRel != d {
				starts = append(starts, d)
			}
		}
	}
	entries, found := map[string]captureEntry{}, false
	rootFD, err := openDirectory(root)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	for _, start := range starts {
		if start != skillRel {
			var st unix.Stat_t
			if err := unix.Fstatat(rootFD, start, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if errors.Is(err, unix.ENOENT) {
					continue
				}
				return nil, err
			}
			if st.Mode&unix.S_IFMT == unix.S_IFLNK {
				return nil, errors.New("skill_bundle_symlink")
			}
			if st.Mode&unix.S_IFMT != unix.S_IFDIR {
				continue
			}
		}
		startFD, err := openRelative(rootFD, start)
		if err != nil {
			if errors.Is(err, unix.ENOENT) && start == skillRel {
				return nil, err
			}
			return nil, errors.New("skill_bundle_directory_changed")
		}
		err = walkBundle(startFD, start, ".", func(rel string, dir int, name string) error {
			if !validPath(rel) {
				return errors.New("invalid_skill_bundle_path")
			}
			sha, size, raw, err := readAt(dir, name, maxFileBytes)
			if err != nil {
				return err
			}
			if rel == sourceRel {
				found = true
				if expectedHash != "" && sha != expectedHash {
					return errors.New("skill_bundle_source_changed")
				}
				return nil
			}
			entries[rel] = captureEntry{sha, size, raw}
			return nil
		})
		unix.Close(startFD)
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, errors.New("skill_bundle_source_missing")
	}
	return buildBundle(entries)
}

// walkBundle visits files under dir (an open handle; closed by the caller) in sorted order.
func walkBundle(dir int, start, base string, visit func(rel string, dir int, name string) error) error {
	dup, err := unix.Dup(dir)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(dup), "dir")
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return err
	}
	slices.Sort(names)
	var dirs, files []string
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		kind := st.Mode & unix.S_IFMT
		if kind == unix.S_IFLNK {
			// Like os.walk's is_dir(): a link to a folder counts as a folder.
			var target unix.Stat_t
			if unix.Fstatat(dir, name, &target, 0) == nil && target.Mode&unix.S_IFMT == unix.S_IFDIR {
				kind = unix.S_IFDIR
			}
		}
		if kind == unix.S_IFDIR {
			if !excluded(name) {
				dirs = append(dirs, name)
			}
		} else if !excluded(name) {
			files = append(files, name)
		}
	}
	for _, name := range dirs {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return errors.New("skill_bundle_symlink")
		}
	}
	for _, name := range files {
		if err := visit(path.Join(start, base, name), dir, name); err != nil {
			return err
		}
	}
	for _, name := range dirs {
		sub, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return errors.New("skill_bundle_directory_changed")
		}
		err = walkBundle(sub, start, path.Join(base, name), visit)
		unix.Close(sub)
		if err != nil {
			return err
		}
	}
	return nil
}

// readLocal is (sha256, bytes) of one file under root without following any link.
func readLocal(root, relative string) (string, []byte, error) {
	abs, _ := filepath.Abs(root)
	fd, err := openDirectory(abs)
	if err != nil {
		return "", nil, err
	}
	defer unix.Close(fd)
	parent, name := path.Dir(relative), path.Base(relative)
	if parent == "." {
		parent = ""
	}
	dir, err := openRelative(fd, parent)
	if err != nil {
		return "", nil, err
	}
	defer unix.Close(dir)
	sha, _, raw, err := readAt(dir, name, -1)
	return sha, raw, err
}
