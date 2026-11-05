//go:build unix

package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDocumentConverterStopsItsChildren: a timed-out converter's background
// child is stopped with it, and the private folder is removed.
func TestDocumentConverterStopsItsChildren(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "convert")
	body := "#!/bin/sh\npwd > " + dir + "/workdir\nsleep 300 &\necho $! > " + dir + "/child\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMORY_DOCUMENT_CONVERTER", script)
	t.Setenv("MEMORY_DOCUMENT_CONVERTER_EXTENSIONS", ".doc")
	defer func(d time.Duration) { converterTimeout = d }(converterTimeout)
	converterTimeout = 300 * time.Millisecond
	if _, err := documentText("a.doc", []byte("x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timed out converter: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "child"))
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("child pid", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("converter child survived the timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
	work, _ := os.ReadFile(filepath.Join(dir, "workdir"))
	if _, err = os.Stat(strings.TrimSpace(string(work))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("converter folder kept", string(work), err)
	}
}
