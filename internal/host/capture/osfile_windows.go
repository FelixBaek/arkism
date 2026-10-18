//go:build windows

package capture

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// tryLock locks byte 0 exclusively without waiting (msvcrt.locking LK_NBLCK); a held lock is errBusy.
func tryLock(f *os.File) error {
	var ol windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errBusy
	}
	return err
}

// waitLock blocks until byte 0 is locked exclusively.
func waitLock(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol)
}

// openNoFollow opens path read-only and refuses a reparse point (symlink).
func openNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.ELOOP}
	}
	return os.Open(path)
}

func filetimeNs(ft windows.Filetime) int64 { return ft.Nanoseconds() }

func infoStamp(h windows.Handle) (fileStamp, error) {
	var d windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &d); err != nil {
		return fileStamp{}, err
	}
	// Python reports the volume serial as st_dev and the file index as st_ino on Windows.
	return fileStamp{Dev: uint64(d.VolumeSerialNumber), Ino: uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow),
		Nlink: uint64(d.NumberOfLinks), Size: int64(d.FileSizeHigh)<<32 | int64(d.FileSizeLow),
		MtimeNs: filetimeNs(d.LastWriteTime), CtimeNs: filetimeNs(d.CreationTime)}, nil
}

// pathStamp is os.stat (follow) or os.lstat identity of path.
func pathStamp(path string, follow bool) (fileStamp, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fileStamp{}, err
	}
	flags := uint32(windows.FILE_FLAG_BACKUP_SEMANTICS)
	if !follow {
		flags |= windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return fileStamp{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	defer windows.CloseHandle(h)
	return infoStamp(h)
}

// handleStamp is os.fstat of an open file.
func handleStamp(f *os.File) (fileStamp, error) { return infoStamp(windows.Handle(f.Fd())) }

// detach starts cmd without a console window, outside the caller's process group.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}

// killGroup ends the process (Windows has no process groups to signal here).
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// syncDir is a no-op: Windows cannot fsync a directory handle (Python skipped it too).
func syncDir(string) error { return nil }

// EvalSymlinks expands 8.3 names and filesystem letter case on Windows. Those
// spellings are normal paths. Reparse points (including junctions) remain aliases
// and are refused at every existing component before comparing file identities.
func resolvedPathEqual(path, canonical string) bool {
	for candidate := path; ; candidate = filepath.Dir(candidate) {
		attributes, err := captureWindowsAttributes(candidate)
		if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return false
		}
		if parent := filepath.Dir(candidate); parent == candidate {
			break
		}
	}
	original, err := os.Lstat(path)
	if err != nil {
		return false
	}
	actual, err := os.Lstat(canonical)
	return err == nil && os.SameFile(original, actual)
}

func captureWindowsAttributes(path string) (uint32, error) {
	native, err := captureWindowsPath(path)
	if err != nil {
		return 0, err
	}
	name, err := windows.UTF16PtrFromString(native)
	if err != nil {
		return 0, err
	}
	if filepath.Dir(path) != path {
		return windows.GetFileAttributes(name)
	}
	// GetFileAttributes cannot inspect a UNC share root. Inspect roots through
	// a handle without following reparse points; failures still reject the path.
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return 0, err
	}
	return info.FileAttributes, nil
}

func evalCanonicalPath(path string) (string, error) {
	native, err := captureWindowsPath(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(native)
}

// Native attribute and path-resolution APIs need the same long-path spelling
// that Go's os package supplies internally. Keep UNC shares and device prefixes.
func captureWindowsPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(absolute, `\\?\`) || strings.HasPrefix(absolute, `\\.\`) {
		return absolute, nil
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + absolute[2:], nil
	}
	return `\\?\` + absolute, nil
}
