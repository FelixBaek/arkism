//go:build windows

package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
			err := replaceAtomicWriteWindows(oldpath, newpath, func(sourcePath, destinationPath string) error {
				if sourcePath != oldpath || destinationPath != newpath {
					t.Fatal("retry changed its source or destination")
				}
				attempts++
				if attempts < 4 {
					return &os.LinkError{Op: "rename", Old: sourcePath, New: destinationPath, Err: code}
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
	var diagnostic *atomicWriteReplaceError
	var link *os.LinkError
	if !errors.As(err, &diagnostic) || !errors.As(err, &link) || link != original || !errors.Is(err, original) || !errors.Is(err, atomicWriteSharingViolation) {
		t.Fatal("diagnostic wrapper lost the original typed cause", err)
	}
	if diagnostic.reason != "budget" || diagnostic.attempts != attempts || diagnostic.elapsed != atomicWriteRetryBudget || diagnostic.errno != atomicWriteSharingViolation || !strings.HasPrefix(err.Error(), "atomic_write_replace reason=budget attempts=") {
		t.Fatal("bounded failure diagnostics changed", err)
	}
	if clock.Sub(start) != atomicWriteRetryBudget || attempts < 2 {
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
				if !errors.Is(err, permanent) || attempts != 2 || sleeps != 1 {
					t.Fatal("permanent follow-up error was retried")
				}
			} else if !errors.Is(err, code) || attempts != 1 || sleeps != 0 {
				t.Fatal("known permanent error consumed a retry budget", err, attempts, sleeps)
			}
			assertAtomicWriteTerminalDiagnostic(t, err, mode, attempts, clock.Sub(time.Unix(1, 0)), code)

		})
	}
}

func assertAtomicWriteTerminalDiagnostic(t *testing.T, err error, mode string, attempts int, elapsed time.Duration, code syscall.Errno) {
	t.Helper()
	var diagnostic *atomicWriteReplaceError
	if !errors.As(err, &diagnostic) {
		t.Fatal("missing fixed terminal diagnostics", err)
	}
	reason := "permanent"
	if mode == "errno" || mode == "after_transient" {
		reason = "not_retryable"
	}
	if mode == "after_transient" {
		code = 0
	}
	if diagnostic.reason != reason || diagnostic.attempts != attempts || diagnostic.elapsed != elapsed || diagnostic.errno != code {
		t.Fatal("terminal reason/count/errno changed", err)
	}
}
