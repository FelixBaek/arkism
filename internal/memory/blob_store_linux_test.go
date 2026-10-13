package memory

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func blobTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBlobStore(t *testing.T) {
	dir := blobTestDir(t)
	store := blobStore{dir: dir, maxBytes: 16}
	ctx := context.Background()
	put := func(body, expected string) error {
		return store.locked(ctx, func(b *lockedBlobs) error {
			digest, size, err := b.put(strings.NewReader(body), expected)
			if err == nil && (digest != ContentHash(body) || size != int64(len(body))) {
				t.Error("incorrect digest or size")
			}
			return err
		})
	}
	for range 2 {
		if err := put("hello", ContentHash("hello")); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected lock and one blob: %v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ContentHash("hello")))
	if err != nil || string(data) != "hello" {
		t.Fatal("stored bytes differ")
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatal("unsafe file mode")
		}
	}
	if err := put("world", ContentHash("other")); !errors.Is(err, errBlobDigestMismatch) {
		t.Fatal(err)
	}
	if err := put(strings.Repeat("x", 17), ""); !errors.Is(err, errBlobTooLarge) {
		t.Fatal(err)
	}
	for _, tc := range []struct{ body, digest string }{{"", ""}, {strings.Repeat("x", 17), ""}, {"world", ContentHash("other")}, {"world", "../bad"}} {
		if err := put(tc.body, tc.digest); err == nil {
			t.Fatal("accepted invalid write")
		}
	}
	if err := store.locked(ctx, func(b *lockedBlobs) error {
		_, _, err := b.put(io.MultiReader(strings.NewReader("partial"), failingBlobReader{}), "")
		return err
	}); err == nil {
		t.Fatal("accepted read failure")
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("failed write left a file")
	}
	if err := os.WriteFile(filepath.Join(dir, ContentHash("hello")), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := put("hello", ""); err == nil {
		t.Fatal("accepted corrupt existing blob")
	}
	data, _ = os.ReadFile(filepath.Join(dir, ContentHash("hello")))
	if string(data) != "other" {
		t.Fatal("overwrote corrupted blob")
	}
}

type failingBlobReader struct{}

func (failingBlobReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestBlobStoreBoundaries(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "permissions", "lock_symlink", "root_permissions", "root_symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := blobTestDir(t)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("hello"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, ContentHash("hello"))
			root := dir
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, target)
			case "hardlink":
				err = os.Link(outside, target)
			case "directory":
				err = os.Mkdir(target, 0700)
			case "permissions":
				err = os.WriteFile(target, []byte("hello"), 0644)
			case "lock_symlink":
				err = os.Symlink(outside, filepath.Join(dir, ".lock"))
			case "root_permissions":
				err = os.Chmod(dir, 0755)
			case "root_symlink":
				root = filepath.Join(t.TempDir(), "link")
				err = os.Symlink(dir, root)
			}
			if err != nil {
				t.Fatal(err)
			}
			store := blobStore{dir: root, maxBytes: 16}
			if err := store.locked(context.Background(), func(b *lockedBlobs) error { _, _, err := b.put(strings.NewReader("hello"), ""); return err }); err == nil {
				t.Fatal("accepted unsafe filesystem entry")
			}
			data, _ := os.ReadFile(outside)
			if string(data) != "hello" {
				t.Fatal("changed external file")
			}
		})
	}
}

func TestBlobStoreLock(t *testing.T) {
	dir := blobTestDir(t)
	store := blobStore{dir: dir, maxBytes: 16}
	if err := store.locked(context.Background(), func(b *lockedBlobs) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		err := store.locked(ctx, func(*lockedBlobs) error { t.Error("entered held lock"); return nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock cancellation: %v", err)
		}
		command := exec.Command(os.Args[0], "-test.run=^TestBlobStoreChildLock$")
		command.Env = append(os.Environ(), "MEMORY_BLOB_LOCK_TEST="+dir)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("child lock: %s %v", output, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := store.locked(context.Background(), func(b *lockedBlobs) error { _, _, err := b.put(strings.NewReader("hello"), ""); return err }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("concurrent writes duplicated blob")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.locked(ctx, func(*lockedBlobs) error { t.Error("entered canceled lock"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestBlobStoreChildLock(t *testing.T) {
	dir := os.Getenv("MEMORY_BLOB_LOCK_TEST")
	if dir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	store := blobStore{dir: dir, maxBytes: 16}
	if err := store.locked(ctx, func(*lockedBlobs) error { return errors.New("entered parent's lock") }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("process exclusion: %v", err)
	}
}

func TestBlobStoreLimitConfig(t *testing.T) {
	for _, limit := range []int64{0, -1, (200 << 20) + 1} {
		store := blobStore{dir: blobTestDir(t), maxBytes: limit}
		err := store.locked(context.Background(), func(b *lockedBlobs) error {
			_, _, err := b.put(strings.NewReader("hello"), "")
			return err
		})
		if limit == 0 && err != nil || limit != 0 && !errors.Is(err, ErrInvalid) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
}

func TestBlobStoreRejectsInternalAliases(t *testing.T) {
	for _, name := range []string{".lock", ".database"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.svc.BlobDirectory = blobTestDir(t)
			if err := f.svc.CheckBlobStorage(testCtx); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.svc.BlobDirectory, name)
			if err := os.Rename(path, filepath.Join(f.svc.BlobDirectory, "alias")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("alias", path); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.CheckBlobStorage(testCtx); err == nil {
				t.Fatal("in-root alias accepted", name)
			}
		})
	}
}
