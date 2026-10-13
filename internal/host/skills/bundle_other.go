//go:build !unix

package skills

// Windows (and other non-Unix) skill bundle capture. Same rules as bundle_unix.go, by path instead of
// by directory handle: a linked folder or file (symlink or junction) is refused, a file that changes
// while it is read is refused, and nothing outside the skill's root is read.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// linked reports a symlink or junction (Windows reports a junction as an irregular file).
func linked(info fs.FileInfo) bool {
	return info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
}

// plainDirectory checks that every folder from root down to relative is a real folder.
func plainDirectory(root, relative string) error {
	current := root
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return &os.PathError{Op: "open", Path: relative, Err: err}
		}
		if linked(info) {
			return errors.New("skill_bundle_symlink")
		}
		if !info.IsDir() {
			return errors.New("skill_bundle_directory_changed")
		}
	}
	return nil
}

// readFile reads a whole regular file: raw is nil when over limit (limit < 0 keeps all).
func readFile(file string, limit int) (string, int, []byte, error) {
	before, err := os.Lstat(file)
	if err != nil {
		return "", 0, nil, &os.PathError{Op: "open", Path: file, Err: err}
	}
	if linked(before) || !before.Mode().IsRegular() {
		return "", 0, nil, errors.New("skill_bundle_not_regular")
	}
	f, err := os.Open(file)
	if err != nil {
		return "", 0, nil, err
	}
	defer f.Close()
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
	after, err := f.Stat()
	if err != nil {
		return "", 0, nil, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(size) != after.Size() {
		return "", 0, nil, errors.New("skill_bundle_source_changed")
	}
	if limit >= 0 && size > limit {
		head = nil
	} else if head == nil {
		head = []byte{}
	}
	return hex.EncodeToString(h.Sum(nil)), size, head, nil
}

// captureBundle collects a skill's text companions; see bundle_unix.go for the rules.
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
	skillRel, sourceRel = filepath.ToSlash(skillRel), filepath.ToSlash(sourceRel)
	starts := []string{skillRel}
	if root != skillDir {
		for _, d := range sharedDirs {
			if !strings.HasPrefix(skillRel, d+"/") && skillRel != d {
				starts = append(starts, d)
			}
		}
	}
	entries, found := map[string]captureEntry{}, false
	for _, start := range starts {
		if start != skillRel {
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(start)))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, err
			}
			if linked(info) {
				return nil, errors.New("skill_bundle_symlink")
			}
			if !info.IsDir() {
				continue
			}
		}
		if err := plainDirectory(root, start); err != nil {
			if errors.Is(err, fs.ErrNotExist) && start == skillRel {
				return nil, err
			}
			if err.Error() == "skill_bundle_symlink" {
				return nil, err
			}
			return nil, errors.New("skill_bundle_directory_changed")
		}
		err := walkFolder(filepath.Join(root, filepath.FromSlash(start)), start, ".", func(rel, file string) error {
			if !validPath(rel) {
				return errors.New("invalid_skill_bundle_path")
			}
			sha, size, raw, err := readFile(file, maxFileBytes)
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
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, errors.New("skill_bundle_source_missing")
	}
	return buildBundle(entries)
}

// walkFolder visits the files under dir in sorted order: files first, then folders.
func walkFolder(dir, start, base string, visit func(rel, file string) error) error {
	items, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name())
	}
	slices.Sort(names)
	var dirs, files []string
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if excluded(name) {
			continue
		}
		if linked(info) {
			// A link to a folder counts as a folder and is refused; a link to a file fails when read.
			if target, err := os.Stat(filepath.Join(dir, name)); err == nil && target.IsDir() {
				return errors.New("skill_bundle_symlink")
			}
			files = append(files, name)
			continue
		}
		if info.IsDir() {
			dirs = append(dirs, name)
		} else {
			files = append(files, name)
		}
	}
	for _, name := range files {
		if err := visit(path.Join(start, base, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	for _, name := range dirs {
		if err := walkFolder(filepath.Join(dir, name), start, path.Join(base, name), visit); err != nil {
			return err
		}
	}
	return nil
}

// readLocal is (sha256, bytes) of one file under root without following any link.
func readLocal(root, relative string) (string, []byte, error) {
	abs, _ := filepath.Abs(root)
	parent, name := path.Dir(relative), path.Base(relative)
	if parent == "." {
		parent = ""
	}
	if err := plainDirectory(abs, parent); err != nil {
		return "", nil, err
	}
	sha, _, raw, err := readFile(filepath.Join(abs, filepath.FromSlash(parent), name), -1)
	return sha, raw, err
}
