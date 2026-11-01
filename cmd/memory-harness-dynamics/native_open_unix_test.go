//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNativeReadOpenDoesNotBlockOnReplacedFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		file, openErr := openReadFile(root, "fifo")
		if openErr == nil {
			openErr = file.Close()
		}
		done <- openErr
	}()
	select {
	case openErr := <-done:
		if openErr != nil {
			t.Fatal(openErr)
		}
	case <-time.After(time.Second):
		// Release a blocking reader before failing, avoiding an orphaned test.
		writer, releaseErr := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if releaseErr == nil {
			_ = writer.Close()
		}
		t.Fatal("FIFO replacement blocked read before type verification")
	}
	if _, err = readAt(root, "fifo"); err == nil {
		t.Fatal("non-regular file accepted by actual readAt")
	}
}
