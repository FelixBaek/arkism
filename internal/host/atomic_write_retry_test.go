//go:build windows

package host

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func atomicWriteRetryFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	oldpath, newpath := filepath.Join(dir, "exclusive-source"), filepath.Join(dir, "state")
	for _, path := range []string{oldpath, newpath} {
		if err := os.WriteFile(path, []byte("owned sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return oldpath, newpath
}

func TestAtomicWriteRetryTransientThenSuccess(t *testing.T) {
	for _, code := range []syscall.Errno{atomicWriteAccessDenied, atomicWriteSharingViolation, atomicWriteLockViolation} {
		t.Run(code.Error(), func(t *testing.T) {
			oldpath, newpath := atomicWriteRetryFixture(t)
			clock := time.Unix(1, 0)
			attempts := 0
			err := replaceAtomicWriteWindows(oldpath, newpath, func(old, new string) error {
				if old != oldpath || new != newpath {
					t.Fatal("retry changed its source or destination")
				}
				attempts++
				if attempts < 4 {
					return &os.LinkError{Op: "rename", Old: old, New: new, Err: code}
				}
				return nil
			}, func() time.Time { return clock }, func(delay time.Duration) { clock = clock.Add(delay) })
			if err != nil || attempts != 4 || clock.Sub(time.Unix(1, 0)) != 7*time.Millisecond {
				t.Fatal("transient retry did not preserve the bounded backoff", err, attempts)
			}
		})
	}
}

func TestAtomicWriteRetryBudgetPreservesFailure(t *testing.T) {
	oldpath, newpath := atomicWriteRetryFixture(t)
	start := time.Unix(1, 0)
	clock := start
	attempts := 0
	original := &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: atomicWriteSharingViolation}
	err := replaceAtomicWriteWindows(oldpath, newpath, func(string, string) error { attempts++; return original },
		func() time.Time { return clock }, func(delay time.Duration) {
			if delay <= 0 || clock.Add(delay).After(start.Add(atomicWriteRetryBudget)) {
				t.Fatal("retry slept beyond its budget")
			}
			clock = clock.Add(delay)
		})
	if err != original || clock.Sub(start) != atomicWriteRetryBudget || attempts < 2 {
		t.Fatal("retry lost the exact failure or its finite budget", err, attempts)
	}
	for _, path := range []string{oldpath, newpath} {
		if string(atomicWriteRead(t, path)) != "owned sentinel" {
			t.Fatal("failed retry modified a file")
		}
	}
}

func TestAtomicWriteRetryPermanentErrorsDoNotWait(t *testing.T) {
	for _, mode := range []string{"errno", "directory", "readonly", "source_missing", "after_transient"} {
		t.Run(mode, func(t *testing.T) {
			oldpath, newpath := atomicWriteRetryFixture(t)
			code := atomicWriteAccessDenied
			switch mode {
			case "errno":
				code = syscall.Errno(112) // ERROR_DISK_FULL is never retried.
			case "directory":
				os.Remove(newpath)
				if err := os.Mkdir(newpath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "readonly":
				if err := os.Chmod(newpath, 0o400); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(newpath, 0o600) })
			case "source_missing":
				os.Remove(oldpath)
			}
			attempts, sleeps := 0, 0
			clock := time.Unix(1, 0)
			permanent := errors.New("fixed non-retryable failure")
			err := replaceAtomicWriteWindows(oldpath, newpath, func(string, string) error {
				attempts++
				if mode == "after_transient" && attempts > 1 {
					return permanent
				}
				return code
			}, func() time.Time { return clock }, func(delay time.Duration) { sleeps++; clock = clock.Add(delay) })
			if mode == "after_transient" {
				if err != permanent || attempts != 2 || sleeps != 1 {
					t.Fatal("permanent follow-up error was retried")
				}
			} else if !errors.Is(err, code) || attempts != 1 || sleeps != 0 {
				t.Fatal("known permanent error consumed a retry budget", err, attempts, sleeps)
			}
		})
	}
}
