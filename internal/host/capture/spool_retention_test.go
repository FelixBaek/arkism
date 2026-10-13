package capture

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func touch(t *testing.T, path string, at float64) {
	t.Helper()
	tm := time.Unix(0, int64(at*1e9))
	if err := os.Chtimes(path, tm, tm); err != nil {
		t.Fatal(err)
	}
}

func TestSpoolBoundaryRestoreLegacyCorruptionAndUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	now := timeNow()
	for _, c := range []struct {
		name  string
		body  string
		mtime float64
	}{
		{"restored", `{"arkism_queued_at": ` + pyRepr(now-spoolTTL) + `}`, now},
		{"recent", `{"arkism_queued_at": ` + pyRepr(now-spoolTTL+1) + `}`, now - spoolTTL - 1},
		{"legacy", `{}`, now - spoolTTL},
		{"broken", `{`, now - spoolTTL},
	} {
		path := filepath.Join(root, c.name+".json")
		os.WriteFile(path, []byte(c.body), 0o644)
		touch(t, path, c.mtime)
	}
	os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("keep"), 0o644)
	os.Mkdir(filepath.Join(root, "child"), 0o755)
	os.WriteFile(filepath.Join(root, "child", "old.json"), []byte("{}"), 0o644)
	n, err := cleanupSpool(root, pcTime, now)
	if err != nil || n != 3 {
		t.Fatalf("removed %d, %v", n, err)
	}
	for _, keep := range []string{"recent.json", "unrelated.txt", "child/old.json"} {
		if !exists(filepath.Join(root, keep)) {
			t.Errorf("%s removed", keep)
		}
	}
}

func TestSpoolSymlinkTargetAndAliasAreNotCleaned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink privilege not assumed")
	}
	root := t.TempDir()
	spool := filepath.Join(root, "spool")
	os.Mkdir(spool, 0o755)
	original := filepath.Join(root, "original.json")
	os.WriteFile(original, []byte("{}"), 0o644)
	touch(t, original, 1)
	os.Symlink(original, filepath.Join(spool, "link.json"))
	if n, err := cleanupSpool(spool, pcTime, -1); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if !exists(original) {
		t.Fatal("symlink target removed")
	}
	alias := filepath.Join(root, "alias")
	os.Symlink(spool, alias)
	if _, err := cleanupSpool(alias, pcTime, -1); pyType(err) != "ValueError" {
		t.Fatalf("alias: %v", err)
	}
}

func TestSpoolQueuedAtRejectsInvalidTimes(t *testing.T) {
	now := timeNow()
	for _, v := range []any{nil, true, math.NaN(), json.Number("-1"), now + 120, "2026-01-01T00:00:00"} {
		if _, err := queuedAt(v, now); err == nil || err.Error() != "capture_queue_time_invalid" {
			t.Errorf("%v: %v", v, err)
		}
	}
	if at, err := queuedAt(now+30, now); err != nil || at != now {
		t.Errorf("future within 60s caps at now: %v %v", at, err)
	}
	if at, err := queuedAt("2026-01-01T00:00:00Z", now); err != nil || at != 1767225600 {
		t.Errorf("aware ISO: %v %v", at, err)
	}
	if got := spoolISO(1767225600.25); got != "2026-01-01T00:00:00.250000Z" {
		t.Errorf("iso %s", got)
	}
}
