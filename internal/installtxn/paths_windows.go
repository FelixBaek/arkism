// Package installtxn publishes a complete Windows payload and keeps the prior
// directory until publication and its metadata have committed. The installer
// adapter supplies metadata and process ownership checks; this package never
// recursively deletes a previous installation or a recovery workspace.
package installtxn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type layout struct{ live, versions, pending, incoming, previous, failed, owner string }
type ownership struct {
	Format    int
	Live, SID string
}

func paths(live string) (layout, error) {
	clean := filepath.Clean(live)
	volume := filepath.VolumeName(clean)
	if clean != live || !filepath.IsAbs(clean) || len(volume) != 2 || volume[1] != ':' || filepath.Dir(clean) == clean || strings.ContainsAny(clean, `*?<>"|`) {
		return layout{}, errors.New("install_path_invalid")
	}
	for _, part := range strings.Split(clean[len(volume)+1:], `\`) {
		if err := component(part); err != nil {
			return layout{}, err
		}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return layout{}, err
	}
	versions := clean + ".versions"
	pending := filepath.Join(versions, "pending")
	return layout{clean, versions, pending, filepath.Join(pending, "incoming"), filepath.Join(pending, "previous"), filepath.Join(pending, "failed"), user.User.Sid.String()}, nil
}
func component(value string) error {
	if value == "" || value == "." || value == ".." || strings.TrimRight(value, " .") != value || strings.ContainsAny(value, `<>:"/\|?*`) {
		return errors.New("install_component_invalid")
	}
	for _, r := range value {
		if r < 32 {
			return errors.New("install_component_invalid")
		}
	}
	stem := strings.ToUpper(strings.Split(value, ".")[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		return errors.New("install_device_name")
	}
	return nil
}
func ordinary(path string, directory bool) error {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return fmt.Errorf("install_path_not_ordinary: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !directory && !info.Mode().IsRegular() {
		return errors.New("install_file_not_regular")
	}
	return nil
}
func directoryExists(path string) (bool, error) {
	if err := ordinary(path, true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Pin existing ancestors against rename for the duration of the operation.
// Payload roots are checked separately because the transaction must rename them.
func pinAncestors(path string) (func(), error) {
	var handles []windows.Handle
	closeAll := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			windows.CloseHandle(handles[i])
		}
	}
	var chain []string
	for p := path; ; p = filepath.Dir(p) {
		chain = append(chain, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		ptr, err := windows.UTF16PtrFromString(chain[i])
		if err != nil {
			closeAll()
			return nil, err
		}
		h, err := windows.CreateFile(ptr, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			closeAll()
			return nil, err
		}
		handles = append(handles, h)
		var info windows.ByHandleFileInformation
		if err = windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			closeAll()
			return nil, errors.New("install_ancestor_not_ordinary")
		}
	}
	return closeAll, nil
}
func privateDirectory(path, sid string) error {
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		return err
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.CreateDirectory(ptr, &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor})
}
func openLayout(l layout) (func(), error) {
	release, err := pinAncestors(filepath.Dir(l.live))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) { release(); return nil, err }
	created := false
	if err = privateDirectory(l.versions, l.owner); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return fail(err)
	}
	if err = ordinary(l.versions, true); err != nil {
		return fail(err)
	}
	pinned, err := pinAncestors(l.versions)
	if err != nil {
		return fail(err)
	}
	oldRelease := release
	release = func() { pinned(); oldRelease() }
	// A protected current-user/system DACL prevents another account replacing state.
	descriptor, err := windows.GetNamedSecurityInfo(l.versions, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fail(err)
	}
	expected, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + l.owner + ")")
	if err != nil {
		return fail(err)
	}
	if descriptor.String() != expected.String() {
		return fail(errors.New("install_versions_acl_changed"))
	}
	marker := filepath.Join(l.versions, "owner.json")
	if created {
		if err = writeNewJSON(marker, ownership{1, l.live, l.owner}); err != nil {
			return fail(err)
		}
	}
	var owner ownership
	if err = readJSON(marker, &owner); err != nil {
		return fail(err)
	}
	if owner != (ownership{1, l.live, l.owner}) {
		return fail(errors.New("install_versions_owner_mismatch"))
	}
	lockPath := filepath.Join(l.versions, "transaction.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail(err)
	}
	if err = ordinary(lockPath, false); err != nil {
		lock.Close()
		return fail(err)
	}
	if err = windows.LockFileEx(windows.Handle(lock.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped)); err != nil {
		lock.Close()
		return fail(errors.New("install_transaction_busy"))
	}
	return func() { lock.Close(); release() }, nil
}
func writeNewJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
