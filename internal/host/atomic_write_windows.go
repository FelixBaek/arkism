package host

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// These Win32 errors can also describe temporary sharing/delete-pending states.
// An ambiguous access denial is not proof of a transient failure: it is returned
// unchanged if this short retry budget expires. No ACL or attributes are changed.
const (
	atomicWriteAccessDenied     syscall.Errno = 5
	atomicWriteSharingViolation syscall.Errno = 32
	atomicWriteLockViolation    syscall.Errno = 33
	atomicWriteRetryBudget                    = 500 * time.Millisecond
)

func replaceAtomicWrite(oldpath, newpath string) error {
	return replaceAtomicWriteWindows(oldpath, newpath, os.Rename, time.Now, time.Sleep)
}

// The retry budget bounds additional attempts and sleeps, not kernel-call or
// scheduler latency. Every attempt uses the same exclusively created source.
func replaceAtomicWriteWindows(oldpath, newpath string, rename func(string, string) error, now func() time.Time, sleep func(time.Duration)) error {
	deadline := now().Add(atomicWriteRetryBudget)
	delay := time.Millisecond
	for {
		err := rename(oldpath, newpath)
		if err == nil || !atomicWriteWindowsMayRetry(err) || atomicWriteWindowsPermanent(oldpath, newpath) {
			return err
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return err
		}
		sleep(min(delay, remaining))
		if !now().Before(deadline) {
			return err
		}
		delay = min(2*delay, 16*time.Millisecond)
	}
}

func atomicWriteWindowsMayRetry(err error) bool {
	return errors.Is(err, atomicWriteAccessDenied) || errors.Is(err, atomicWriteSharingViolation) || errors.Is(err, atomicWriteLockViolation)
}

func atomicWriteWindowsPermanent(oldpath, newpath string) bool {
	info, err := os.Lstat(oldpath)
	if errors.Is(err, os.ErrNotExist) || err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o222 == 0) {
		return true
	}
	info, err = os.Lstat(newpath)
	// On Windows Go maps FILE_ATTRIBUTE_READONLY to mode 0444. Lstat does not
	// follow a destination symlink: renaming replaces that entry, not its target.
	return err == nil && (info.IsDir() || info.Mode().IsRegular() && info.Mode().Perm()&0o222 == 0)
}
