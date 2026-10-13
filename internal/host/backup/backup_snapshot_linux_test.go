package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func restoreFakes(t *testing.T) {
	d, r, n, c := docker, ready, newSnapshot, chown
	t.Cleanup(func() { docker, ready, newSnapshot, chown = d, r, n, c })
}

type fakeSession struct {
	events *[]string
	fail   string
}

func (s fakeSession) export(context.Context) (string, error) {
	*s.events = append(*s.events, "export")
	if s.fail == "export" {
		return "", errors.New("export")
	}
	return "00000001-00000001-1", nil
}

func (s fakeSession) close() { *s.events = append(*s.events, "close") }

// exercise runs capture with docker, readiness and chown faked; ready after a
// writer restart changes the live blob, which the snapshot copy must not see.
func exercise(t *testing.T, fail string) []string {
	t.Helper()
	var events []string
	root := t.TempDir()
	blobs := privateDir(t, filepath.Join(root, "blobs"))
	write(t, filepath.Join(blobs, "file"), "before")
	restoreFakes(t)
	newSnapshot = func(string) (snapshotSession, error) {
		events = append(events, "session")
		return fakeSession{&events, fail}, nil
	}
	docker = func(_ context.Context, _ time.Duration, dio dockerIO, args ...string) (string, error) {
		event := args[0]
		if strings.Contains(strings.Join(args, " "), "pg_dump") {
			event = "dump"
		}
		events = append(events, event)
		if event == fail {
			return "", errors.New("docker failed")
		}
		if event == "dump" {
			io.WriteString(dio.stdout, "database snapshot")
		}
		return "true\n", nil
	}
	ready = func(context.Context, string) error {
		events = append(events, "ready")
		if slices.Contains(events, "start") {
			write(t, filepath.Join(blobs, "file"), "after")
		}
		return nil
	}
	chown = func(string, int, int) error { return nil }
	err := capture(context.Background(), "db", blobs, filepath.Join(root, "db.dump"), filepath.Join(root, "snapshot"),
		[]string{"api", "worker"}, "unused", io.Discard)
	if (err != nil) != (fail != "") {
		t.Fatal(fail, err)
	}
	if fail == "" {
		if b, _ := os.ReadFile(filepath.Join(root, "snapshot", "file")); string(b) != "before" {
			t.Fatalf("snapshot copy %q", b)
		}
		if b, _ := os.ReadFile(filepath.Join(blobs, "file")); string(b) != "after" {
			t.Fatalf("live blob %q", b)
		}
		if info, _ := os.Stat(filepath.Join(root, "snapshot")); info.Mode().Perm() != 0o700 {
			t.Fatal("snapshot root mode", info.Mode())
		}
		if b, _ := os.ReadFile(filepath.Join(root, "db.dump")); string(b) != "database snapshot" {
			t.Fatalf("dump %q", b)
		}
	}
	return events
}

func TestDumpRunsAfterWritersRestartWithIndependentBlobCopy(t *testing.T) {
	events := exercise(t, "")
	if slices.Index(events, "export") > slices.Index(events, "start") || slices.Index(events, "start") > slices.Index(events, "dump") {
		t.Fatal(events)
	}
	if events[len(events)-1] != "close" {
		t.Fatal(events)
	}
}

func TestExportFailureRestartsWriters(t *testing.T) {
	events := exercise(t, "export")
	if !slices.Contains(events, "start") || slices.Contains(events, "dump") || events[len(events)-1] != "close" {
		t.Fatal(events)
	}
}

func TestPartialStopFailureStillAttemptsRestart(t *testing.T) {
	if events := exercise(t, "stop"); !slices.Contains(events, "start") {
		t.Fatal(events)
	}
}

func TestDumpFailureLeavesWritersRunning(t *testing.T) {
	events := exercise(t, "dump")
	count := 0
	for _, e := range events {
		if e == "start" {
			count++
		}
	}
	if count != 1 || events[len(events)-1] != "close" {
		t.Fatal(events)
	}
}

// A signal cancels the step in flight, but the restart runs on its own context.
func TestCancelledCaptureStillRestartsWriters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var restartCtx context.Context
	restoreFakes(t)
	ready = func(context.Context, string) error { return nil }
	docker = func(c context.Context, _ time.Duration, _ dockerIO, args ...string) (string, error) {
		switch args[0] {
		case "stop":
			cancel()
			return "", c.Err()
		case "start":
			restartCtx = c
		}
		return "true\n", nil
	}
	root := t.TempDir()
	capture(ctx, "db", root, filepath.Join(root, "out"), filepath.Join(root, "snap"), []string{"api"}, "unused", io.Discard)
	if restartCtx == nil || restartCtx.Err() != nil {
		t.Fatal("writers not restarted on a live context")
	}
}
