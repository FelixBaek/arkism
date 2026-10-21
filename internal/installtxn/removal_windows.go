package installtxn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

// RemoveInstalledPayload removes only receipt-bound, unchanged program bytes.
// Unknown files and nonempty directories remain. Its helper runs outside Live.
func RemoveInstalledPayload(live, version string) error {
	return removeInstalledPayload(live, version, CheckInstalled)
}
func removeInstalledPayload(live, version string, check func(string, string) error) error {
	if err := helperOutsidePayload(live); err != nil {
		return err
	}
	if err := check(live, version); err != nil {
		return err
	}
	l, err := paths(live)
	if err != nil {
		return err
	}
	release, err := openLayout(l)
	if err != nil {
		return err
	}
	defer release()
	if pending, err := directoryExists(l.pending); err != nil || pending {
		if err != nil {
			return err
		}
		return errors.New("uninstall_pending_transaction_requires_repair")
	}
	manifest, err := installedReceipt(l, version)
	if err != nil {
		return err
	}
	return removePayloadFiles(l.live, manifest)
}
func installedReceipt(l layout, version string) (Manifest, error) {
	entries, err := os.ReadDir(l.versions)
	if err != nil {
		return Manifest{}, err
	}
	var found []Manifest
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "version-") {
			continue
		}
		path := filepath.Join(l.versions, entry.Name())
		if err = ordinary(path, true); err != nil {
			return Manifest{}, err
		}
		var j journal
		if err = readJSON(filepath.Join(path, "journal.json"), &j); err != nil {
			return Manifest{}, err
		}
		if err = j.validate(l); err != nil {
			return Manifest{}, err
		}
		if entry.Name() != "version-"+j.ID {
			return Manifest{}, errors.New("uninstall_receipt_directory_mismatch")
		}
		if j.Phase != "committed" {
			continue
		}
		var metadata struct{ Version string }
		if err = json.Unmarshal(j.Metadata, &metadata); err != nil {
			return Manifest{}, err
		}
		if metadata.Version == version {
			found = append(found, j.New)
		}
	}
	if len(found) == 0 {
		return Manifest{}, errors.New("uninstall_owned_receipt_missing")
	}
	// Repeated local builds may use a version label twice. Select only a receipt
	// whose remaining files match; do not guess from the latest directory name.
	for _, candidate := range found {
		if err = verifyRemainingPayload(l.live, candidate); err == nil {
			return candidate, nil
		}
	}
	return Manifest{}, errors.New("uninstall_payload_no_longer_matches_receipt")
}
func verifyRemainingPayload(live string, manifest Manifest) error {
	files, releases, err := preparePayloadRemoval(live, manifest)
	closeRemoval(files, releases)
	return err
}
func closeRemoval(files []*linkFile, releases []func()) {
	for _, file := range files {
		file.close()
	}
	for i := len(releases) - 1; i >= 0; i-- {
		releases[i]()
	}
}
func preparePayloadRemoval(live string, manifest Manifest) (files []*linkFile, releases []func(), err error) {
	if err = manifest.Validate(); err != nil {
		return nil, nil, err
	}
	if exists, inspectErr := directoryExists(live); inspectErr != nil || !exists {
		return nil, nil, inspectErr
	}
	for _, relative := range manifest.Directories {
		if _, inspectErr := directoryExists(filepath.Join(live, filepath.FromSlash(relative))); inspectErr != nil {
			return nil, nil, inspectErr
		}
	}
	for _, entry := range manifest.Files {
		path := filepath.Join(live, filepath.FromSlash(entry.Path))
		if _, inspectErr := os.Lstat(path); errors.Is(inspectErr, os.ErrNotExist) {
			continue
		} else if inspectErr != nil {
			return files, releases, inspectErr
		}
		release, pinErr := pinAncestors(filepath.Dir(path))
		if pinErr != nil {
			return files, releases, pinErr
		}
		releases = append(releases, release)
		file, openErr := openRemovalFile(path, entry)
		if openErr != nil {
			return files, releases, openErr
		}
		files = append(files, file)
	}
	return files, releases, nil
}
func openRemovalFile(path string, expected File) (*linkFile, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if err = metadataTag(handle, false); err != nil {
		file.Close()
		return nil, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, expected.Size+1))
	if err != nil || size != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		file.Close()
		return nil, errors.New("uninstall_modified_payload_preserved: " + path)
	}
	return &linkFile{file: file, path: path}, nil
}
func removePayloadFiles(live string, manifest Manifest) error {
	files, releases, err := preparePayloadRemoval(live, manifest)
	defer func() { closeRemoval(files, releases) }()
	if err != nil {
		return err
	}
	for _, file := range files {
		if err = file.remove(); err != nil {
			return err
		}
	}
	closeRemoval(nil, releases)
	releases = nil
	directories := append([]string{}, manifest.Directories...)
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, relative := range directories {
		if err = removeEmptyPayloadDirectory(filepath.Join(live, filepath.FromSlash(relative))); err != nil {
			return err
		}
	}
	return removeEmptyPayloadDirectory(live)
}
func removeEmptyPayloadDirectory(path string) error {
	exists, err := directoryExists(path)
	if err != nil || !exists {
		return err
	}
	// Remove, never RemoveAll: even a late unknown file prevents directory removal.
	if err = os.Remove(path); err != nil && !errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
		return err
	}
	return nil
}
