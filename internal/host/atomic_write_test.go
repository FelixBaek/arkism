package host

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func atomicWriteRead(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAtomicWriteKeepsForeignTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	foreign := filepath.Join(dir, ".state.json.tmp")
	if err := os.WriteFile(path, []byte("previous state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("foreign sentinel"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(foreign)
	if err := AtomicWrite(path, "complete replacement"); err != nil {
		t.Fatal(err)
	}
	if string(atomicWriteRead(t, path)) != "complete replacement" {
		t.Fatal("replacement was not published")
	}
	if string(atomicWriteRead(t, foreign)) != "foreign sentinel" {
		t.Fatal("a foreign temporary file was overwritten")
	}
	after, _ := os.Stat(foreign)
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("a foreign temporary file was replaced or chmodded")
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("replacement is not private")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("temporary file leaked after success")
	}
}

func TestAtomicWriteDoesNotFollowTemporaryAliases(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir, outside := t.TempDir(), filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("outside remains intact"), 0o640); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(outside)
			alias := filepath.Join(dir, ".state.json.tmp")
			link := os.Link
			if kind == "symlink" {
				link = os.Symlink
			}
			if err := link(outside, alias); err != nil {
				if runtime.GOOS == "windows" && kind == "symlink" {
					t.Skip("fixture needs permission to create a symbolic link")
				}
				t.Fatal(err)
			}
			path := filepath.Join(dir, "state.json")
			if err := AtomicWrite(path, "new memory state"); err != nil {
				t.Fatal(err)
			}
			if string(atomicWriteRead(t, outside)) != "outside remains intact" {
				t.Fatal("temporary alias allowed an external overwrite")
			}
			after, _ := os.Stat(outside)
			if !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("temporary alias changed external file identity or permissions")
			}
			if string(atomicWriteRead(t, alias)) != "outside remains intact" || string(atomicWriteRead(t, path)) != "new memory state" {
				t.Fatal("publication consumed a foreign alias")
			}
		})
	}
}

func TestAtomicWriteConcurrentReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	const writers = 64
	values := make([]string, writers)
	for index := range values {
		values[index] = fmt.Sprintf("%03d:", index) + string(bytes.Repeat([]byte{byte('A' + index%26)}, 8192))
	}
	start := make(chan struct{})
	results := make(chan error, writers)
	var wait sync.WaitGroup
	for _, value := range values {
		wait.Go(func() {
			<-start
			results <- AtomicWrite(path, value)
		})
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent replacement failed", err)
		}
	}
	actual := string(atomicWriteRead(t, path))
	complete := false
	for _, value := range values {
		complete = complete || actual == value
	}
	if !complete {
		t.Fatal("published state contains an incomplete or mixed write")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatal("temporary files remain after concurrent replacement")
	}
}

func TestAtomicWriteFailurePreservesDestinationAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(path, "existing")
	foreign := filepath.Join(dir, ".state.json.tmp")
	for _, file := range []string{child, foreign} {
		if err := os.WriteFile(file, []byte("preserved sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := AtomicWrite(path, "replacement must fail"); err == nil {
		t.Fatal("a nonempty directory was replaced")
	}
	for _, file := range []string{child, foreign} {
		if string(atomicWriteRead(t, file)) != "preserved sentinel" {
			t.Fatal("failed publication modified a preexisting file")
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("temporary file leaked after failed publication")
	}
}

func TestAtomicWriteLongDestinationName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, strings.Repeat("s", 235)+".json")
	if err := os.WriteFile(path, []byte("previous state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, "new memory state"); err != nil {
		t.Fatal("a long supported destination name was rejected", err)
	}
	if string(atomicWriteRead(t, path)) != "new memory state" {
		t.Fatal("long destination was not replaced")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary file leaked for a long destination")
	}
}
