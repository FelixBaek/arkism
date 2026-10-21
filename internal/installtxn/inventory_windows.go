package installtxn

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// Manifest is the exact inventory supplied by the trusted installer payload.
// It includes directories, so neither empty directories nor extra files vanish
// silently during verification. Hashes authenticate bytes only when the caller
// obtains the expected inventory from an authenticated package.
// File identifies the expected relative name, byte length and SHA256 of one payload file.
type File struct {
	Path   string
	Size   int64
	SHA256 string
}
type Manifest struct {
	Directories []string
	Files       []File
}

func relativePath(path string) error {
	if path == "" || strings.Contains(path, `\`) {
		return errors.New("install_manifest_path_invalid")
	}
	for _, part := range strings.Split(path, "/") {
		if err := component(part); err != nil {
			return err
		}
	}
	return nil
}
func (m Manifest) Validate() error { return m.validate(false) }
func (m Manifest) validate(allowEmpty bool) error {
	if len(m.Files) == 0 && !allowEmpty || len(m.Files)+len(m.Directories) > 20000 {
		return errors.New("install_manifest_size_invalid")
	}
	seen := map[string]bool{}
	var total int64
	add := func(path string) error {
		if err := relativePath(path); err != nil {
			return err
		}
		fold := strings.ToLower(path)
		if seen[fold] {
			return errors.New("install_manifest_path_collision")
		}
		seen[fold] = true
		return nil
	}
	for _, dir := range m.Directories {
		if err := add(dir); err != nil {
			return err
		}
	}
	for _, file := range m.Files {
		if err := add(file.Path); err != nil {
			return err
		}
		hash, err := hex.DecodeString(file.SHA256)
		if err != nil || len(hash) != sha256.Size || file.SHA256 != strings.ToLower(file.SHA256) || file.Size < 0 || file.Size > 512<<20 {
			return errors.New("install_manifest_file_invalid")
		}
		total += file.Size
		if total > 8<<30 {
			return errors.New("install_manifest_total_limit")
		}
	}
	dirs := map[string]bool{".": true}
	for _, dir := range m.Directories {
		dirs[strings.ToLower(dir)] = true
	}
	for path := range seen {
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path)))
		if !dirs[parent] {
			return errors.New("install_manifest_parent_missing")
		}
	}
	return nil
}
func (m Manifest) normalized() Manifest {
	out := Manifest{Directories: append([]string{}, m.Directories...), Files: append([]File{}, m.Files...)}
	sort.Strings(out.Directories)
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out
}

// Inventory reads ordinary files only and does not traverse reparse points.
func Inventory(root string) (Manifest, error) { return inventory(root, false) }
func inventory(root string, allowEmpty bool) (Manifest, error) {
	var result Manifest
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ordinary(path, entry.IsDir()); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if err = relativePath(relative); err != nil {
			return err
		}
		if len(result.Directories)+len(result.Files) >= 20000 {
			return errors.New("install_inventory_limit")
		}
		if entry.IsDir() {
			result.Directories = append(result.Directories, relative)
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, io.LimitReader(file, (512<<20)+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		total += size
		if size > 512<<20 || total > 8<<30 {
			return errors.New("install_inventory_limit")
		}
		result.Files = append(result.Files, File{relative, size, hex.EncodeToString(hash.Sum(nil))})
		if len(result.Directories)+len(result.Files) > 20000 {
			return errors.New("install_inventory_limit")
		}
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	if err = result.validate(allowEmpty); err != nil {
		return Manifest{}, err
	}
	return result.normalized(), nil
}
func verify(root string, expected Manifest) error {
	actual, err := Inventory(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected.normalized()) {
		return fmt.Errorf("install_payload_mismatch: %s", root)
	}
	return nil
}
func stage(source, target string, expected Manifest) error {
	release, err := pinAncestors(source)
	if err != nil {
		return err
	}
	defer release()
	if err = verify(source, expected); err != nil {
		return err
	}
	if err = os.Mkdir(target, 0700); err != nil {
		return err
	}
	dirs := append([]string{}, expected.Directories...)
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) < len(dirs[j]) })
	for _, dir := range dirs {
		if err = os.Mkdir(filepath.Join(target, filepath.FromSlash(dir)), 0700); err != nil {
			return err
		}
	}
	for _, entry := range expected.Files {
		from := filepath.Join(source, filepath.FromSlash(entry.Path))
		to := filepath.Join(target, filepath.FromSlash(entry.Path))
		if err = copyFile(from, to, entry); err != nil {
			return err
		}
	}
	return verify(target, expected)
}
func copyFile(source, target string, expected File) error {
	if err := ordinary(source, false); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, expected.Size+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if size != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return errors.New("install_copy_changed")
	}
	return nil
}

func verifyBaseline(root string, expected Manifest) error {
	actual, err := inventory(root, true)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected.normalized()) {
		return errors.New("install_baseline_changed")
	}
	return nil
}
