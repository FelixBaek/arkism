package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A reader without FILE_SHARE_DELETE prevents replacement until it closes. This
// fixture owns every file and handle; it does not change ACLs or attributes.
func atomicWriteHoldDestination(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestAtomicWriteWindowsReleasedDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	foreign := filepath.Join(dir, ".state.json.tmp")
	for _, file := range []string{path, foreign} {
		if err := os.WriteFile(file, []byte("preserved sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handle := atomicWriteHoldDestination(t, path)
	defer func() {
		if handle != windows.InvalidHandle {
			windows.CloseHandle(handle)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- AtomicWrite(path, "complete replacement") }()
	select {
	case err := <-done:
		t.Fatal("replacement returned while its destination was held", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = windows.InvalidHandle
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("released destination was not replaced", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("released destination replacement did not finish")
	}
	if string(atomicWriteRead(t, path)) != "complete replacement" || string(atomicWriteRead(t, foreign)) != "preserved sentinel" {
		t.Fatal("released replacement was torn or changed a foreign file")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("released replacement retained its temporary")
	}
}

func TestAtomicWriteWindowsHeldDestinationFailsBoundedly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	foreign := filepath.Join(dir, ".state.json.tmp")
	for _, file := range []string{path, foreign} {
		if err := os.WriteFile(file, []byte("preserved sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handle := atomicWriteHoldDestination(t, path)
	defer windows.CloseHandle(handle)
	start := time.Now()
	err := AtomicWrite(path, "must not replace a held destination")
	elapsed := time.Since(start)
	if !errors.Is(err, atomicWriteAccessDenied) && !errors.Is(err, atomicWriteSharingViolation) && !errors.Is(err, atomicWriteLockViolation) {
		t.Fatal("held destination did not preserve its Windows error", err)
	}
	// Fake-clock core tests enforce exactly 500ms of retry budget. This native
	// bound includes kernel/scheduler overhead and detects an unbounded loop.
	if elapsed < atomicWriteRetryBudget/2 || elapsed > 2*time.Second {
		t.Fatal("held destination retry did not respect its finite budget", elapsed)
	}
	for _, file := range []string{path, foreign} {
		if string(atomicWriteRead(t, file)) != "preserved sentinel" {
			t.Fatal("held replacement changed existing state")
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("failed held replacement retained its temporary")
	}
}

func TestAtomicWriteWindowsReadOnlyDestinationDoesNotWait(t *testing.T) {
	oldpath, newpath := atomicWriteRetryFixture(t)
	if err := os.Chmod(newpath, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(newpath, 0o600) })
	sleeps := 0
	err := replaceAtomicWriteWindows(oldpath, newpath, os.Rename, time.Now, func(time.Duration) { sleeps++ })
	if err == nil || sleeps != 0 || string(atomicWriteRead(t, newpath)) != "owned sentinel" || string(atomicWriteRead(t, oldpath)) != "owned sentinel" {
		t.Fatal("read-only destination was modified or retried", err, sleeps)
	}
}
