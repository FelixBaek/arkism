package backup

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeDocker puts a `docker` shell script first on PATH.
func fakeDocker(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunDockerCapturesOrStreams(t *testing.T) {
	fakeDocker(t, `echo "out:$*"; echo "err" >&2; [ "$1" != fail ]`)
	out, err := runDocker(context.Background(), time.Minute, dockerIO{capture: true}, "inspect", "x")
	if err != nil || out != "out:inspect x\n" {
		t.Fatalf("capture = %q %v", out, err)
	}
	var streamed strings.Builder
	if out, err := runDocker(context.Background(), time.Minute, dockerIO{stdout: &streamed, stdin: strings.NewReader("")}, "start", "a"); err != nil || out != "" || streamed.String() != "out:start a\n" {
		t.Fatalf("stream = %q %q %v", out, streamed.String(), err)
	}
	if _, err := runDocker(context.Background(), time.Minute, dockerIO{capture: true}, "fail"); err == nil || !strings.HasPrefix(err.Error(), "docker fail:") {
		t.Fatalf("failing docker = %v", err)
	}
}

func TestPsqlSnapshotExportsAndCloses(t *testing.T) {
	// The fake psql answers the first statement with a snapshot id, then waits for stdin to close.
	fakeDocker(t, `read line; echo "00000003-0000001B-1"; cat >/dev/null`)
	s, err := openSnapshot("db")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.export(context.Background())
	if err != nil || id != "00000003-0000001B-1" {
		t.Fatalf("export = %q %v", id, err)
	}
	s.close()

	fakeDocker(t, `read line; echo "ERROR: no"; cat >/dev/null`)
	s, err = openSnapshot("db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.export(context.Background()); err == nil || err.Error() != "snapshot export failed" {
		t.Fatalf("bad answer = %v", err)
	}
	s.close()

	fakeDocker(t, `cat >/dev/null`)
	s, err = openSnapshot("db")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.export(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled export = %v", err)
	}
	s.close()
	if _, err := s.export(context.Background()); err == nil {
		t.Fatal("export after close should fail to write")
	}
}

func TestWaitReadyPollsUntilOK(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	if err := waitReady(context.Background(), srv.URL); err != nil || n.Load() != 2 {
		t.Fatalf("waitReady = %v after %d", err, n.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitReady(ctx, "http://127.0.0.1:1/readyz"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
	if err := waitReady(context.Background(), "::bad"); err == nil {
		t.Fatal("bad url should fail")
	}
}

func TestCopyTreeKeepsLinksAndRefusesSpecialFiles(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "sub", "a"), "A")
	if err := os.Chmod(filepath.Join(src, "sub", "a"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/a", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	_ = os.Chtimes(filepath.Join(src, "sub", "a"), old, old)
	dst := filepath.Join(root, "out", "dst")
	if err := copyTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(dst, "link")); err != nil || target != "sub/a" {
		t.Fatalf("link = %q %v", target, err)
	}
	info, err := os.Stat(filepath.Join(dst, "sub", "a"))
	if err != nil || info.Mode().Perm() != 0o640 || !info.ModTime().Equal(old) {
		t.Fatalf("copied file = %v %v", info, err)
	}
	if err := copyTree(context.Background(), src, dst); err == nil {
		t.Fatal("an existing destination should fail")
	}
	if err := copyTree(context.Background(), filepath.Join(root, "missing"), filepath.Join(root, "d2")); err == nil {
		t.Fatal("a missing source should fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyTree(ctx, src, filepath.Join(root, "d3")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled copy = %v", err)
	}
	fifoDir := filepath.Join(root, "fifo")
	_ = os.Mkdir(fifoDir, 0o700)
	if err := syscall.Mkfifo(filepath.Join(fifoDir, "p"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(context.Background(), fifoDir, filepath.Join(root, "d4")); err == nil || !strings.Contains(err.Error(), "special file") {
		t.Fatalf("fifo = %v", err)
	}
	if err := copyFile(filepath.Join(root, "nope"), filepath.Join(root, "x")); err == nil {
		t.Fatal("copying a missing file should fail")
	}
	if err := copyFile(filepath.Join(src, "sub", "a"), filepath.Join(root, "no", "dir", "x")); err == nil {
		t.Fatal("copying into a missing directory should fail")
	}
	if err := copyStat(filepath.Join(root, "nope"), dst, true); err == nil {
		t.Fatal("stat of a missing file should fail")
	}
	if err := copyStat(filepath.Join(root, "nope"), dst, false); err == nil {
		t.Fatal("lstat of a missing file should fail")
	}
	if err := copyStat(src, filepath.Join(root, "nope"), true); err == nil {
		t.Fatal("utime of a missing target should fail")
	}
}

func TestCaptureRequiresRunningContainers(t *testing.T) {
	restoreFakes(t)
	ready = func(context.Context, string) error { return nil }
	answer, fail := "false\n", error(nil)
	docker = func(context.Context, time.Duration, dockerIO, ...string) (string, error) { return answer, fail }
	if err := capture(context.Background(), "db", "", "", "", []string{"api"}, "", io.Discard); err == nil || !strings.Contains(err.Error(), "running containers") {
		t.Fatalf("stopped container = %v", err)
	}
	fail = errors.New("no docker")
	if err := capture(context.Background(), "db", "", "", "", []string{"api"}, "", io.Discard); !errors.Is(err, fail) {
		t.Fatalf("inspect error = %v", err)
	}
	ready = func(context.Context, string) error { return errors.New("not ready") }
	if err := capture(context.Background(), "db", "", "", "", nil, "", io.Discard); err == nil || err.Error() != "not ready" {
		t.Fatalf("not ready = %v", err)
	}
}

func TestSnapshotMainFlagsAndFailure(t *testing.T) {
	if code, _, errOut := run(t, []string{"backup-snapshot", "--container", "db"}); code != 2 || !strings.Contains(errOut, "--blob-root") {
		t.Fatalf("missing flags = %d %q", code, errOut)
	}
	restoreFakes(t)
	mask := syscall.Umask(0o022) // snapshotMain sets the process umask; put it back afterwards
	syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(mask) })
	ready = func(context.Context, string) error { return errors.New("down") }
	code, _, errOut := run(t, []string{"backup-snapshot", "--container", "db", "--blob-root", "b", "--output", "o", "--snapshot-root", "s"})
	if code != 1 || !strings.Contains(errOut, "backup snapshot failed: down") {
		t.Fatalf("failed capture = %d %q", code, errOut)
	}
}

func TestBundleMainUsageAndFailures(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"backup-bundle"}, 2, "usage:"},
		{[]string{"backup-bundle", "pack"}, 2, "usage:"},
		{[]string{"backup-bundle", "create", "--blob-root", dir}, 2, "--database-archive"},
		{[]string{"backup-bundle", "extract", "--input", "x"}, 2, "--output"},
		{[]string{"backup-bundle", "extract", "--input", filepath.Join(dir, "missing.zip"), "--output", filepath.Join(dir, "out")}, 1, "quarantined"},
		{[]string{"backup-bundle", "create", "--writers-stopped", "--database-archive", filepath.Join(dir, "missing.dump"), "--blob-root", dir}, 1, "quarantined"},
	} {
		code, _, errOut := run(t, tc.args)
		if code != tc.code || !strings.Contains(errOut, tc.err) {
			t.Errorf("%v = %d %q", tc.args, code, errOut)
		}
	}
}
