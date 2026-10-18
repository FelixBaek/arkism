package host

import (
	"errors"
	"fmt"
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
	start := now()
	deadline := start.Add(atomicWriteRetryBudget)
	delay := time.Millisecond
	attempts := 0
	for {
		err := rename(oldpath, newpath)
		attempts++
		if err == nil {
			return nil
		}
		if !atomicWriteWindowsMayRetry(err) {
			return atomicWriteWindowsFailure(err, "not_retryable", attempts, start, now())
		}
		if atomicWriteWindowsPermanent(oldpath, newpath) {
			return atomicWriteWindowsFailure(err, "permanent", attempts, start, now())
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return atomicWriteWindowsFailure(err, "budget", attempts, start, now())
		}
		sleep(min(delay, remaining))
		if !now().Before(deadline) {
			return atomicWriteWindowsFailure(err, "budget", attempts, start, now())
		}
		delay = min(2*delay, 16*time.Millisecond)
	}
}

// Diagnostics add fixed labels and numbers only; the original error remains
// reachable with errors.Is/As. An error pointer is no longer returned directly.
type atomicWriteReplaceError struct {
	cause    error
	reason   string
	attempts int
	elapsed  time.Duration
	errno    syscall.Errno
}

func (err *atomicWriteReplaceError) Error() string {
	return fmt.Sprintf("atomic_write_replace reason=%s attempts=%d elapsed_ms=%d errno=%d: %v", err.reason, err.attempts, err.elapsed.Milliseconds(), err.errno, err.cause)
}

func (err *atomicWriteReplaceError) Unwrap() error { return err.cause }

func atomicWriteWindowsFailure(cause error, reason string, attempts int, start, end time.Time) error {
	var errno syscall.Errno
	errors.As(cause, &errno)
	return &atomicWriteReplaceError{cause: cause, reason: reason, attempts: attempts, elapsed: max(end.Sub(start), 0), errno: errno}
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
