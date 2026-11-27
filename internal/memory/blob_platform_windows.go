package memory

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const blobOpenFlags = 0

func privateBlobRoot(root *os.Root, expected os.FileInfo) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	actual, err := dir.Stat()
	if err != nil {
		return err
	}
	if !actual.IsDir() || !os.SameFile(expected, actual) {
		return ErrInvalid
	}
	return privateWindowsBlob(dir, true)
}

func privateBlobFile(file *os.File) error { return privateWindowsBlob(file, false) }

func privateWindowsBlob(file *os.File, directory bool) error {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory ||
		(!directory && info.NumberOfLinks != 1) {
		return ErrInvalid
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(sd)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return ErrInvalid
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return ErrInvalid
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if sid.Equals(owner) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
				continue
			}
		}
		return ErrInvalid
	}
	return nil
}

func tryBlobLock(file *os.File) (bool, error) {
	var offset windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &offset)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return err == nil, err
}

// Windows does not support flushing directory handles. File contents are
// flushed before publication and metadata changes use root-relative operations.
func syncBlobDirectory(_ *os.File) error { return nil }
