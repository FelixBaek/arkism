package capture

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func spoolJobs(t *testing.T, dir string) []map[string]any {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []map[string]any
	for _, p := range paths {
		raw, _ := os.ReadFile(p)
		out = append(out, mustLoad(t, string(raw)))
	}
	return out
}

func TestSpoolServerExpiredAndOriginalPCTime(t *testing.T) {
	dir := capFake(t)
	t.Setenv("ARKISM_CLIENT", "pc")
	now := timeNow()
	if _, err := promptCapture(map[string]any{"prompt": "앞으로 짧게", "session_id": "s", pcTime: now - 30}); err != nil {
		t.Fatal(err)
	}
	jobs := spoolJobs(t, filepath.Join(dir, "spool"))
	if at, ok := spoolTimestamp(jobs[0]["queued_at"]); len(jobs) != 1 || !ok || math.Abs(at-(now-30)) > 1e-3 {
		t.Fatal(jobs)
	}
	skipped, err := promptCapture(map[string]any{"prompt": "앞으로 짧게", "session_id": "s2", pcTime: now - spoolTTL})
	if !skipped || err != nil || len(spoolJobs(t, filepath.Join(dir, "spool"))) != 1 {
		t.Fatal(skipped, err)
	}
}

func TestSpoolServerExpiredAckDoesNotRecall(t *testing.T) {
	dir := capFake(t)
	t.Setenv("ARKISM_CLIENT", "pc")
	t.Setenv("ARKISM_CAPTURE_ACK", "1")
	t.Setenv("ARKISM_CAPTURE_CHILD", "")
	capOnPrompt = true
	capAgentOff = func(string) bool { return false }
	keep := selSelected
	t.Cleanup(func() { selSelected = keep })
	selSelected = func(any) (string, error) { t.Fatal("work read"); return "", nil }
	var out bytes.Buffer
	event := `{"hook_event_name": "UserPromptSubmit", "prompt": "기억", "arkism_queued_at": ` + pyRepr(timeNow()-spoolTTL) + `}`
	if hookMain(strings.NewReader(event), &out) != 0 || out.String() != `{"arkismDelivery": {"accepted": true}}`+"\n" {
		t.Fatalf("%q", out.String())
	}
	if len(spoolJobs(t, filepath.Join(dir, "spool"))) != 0 {
		t.Fatal("enqueued")
	}
}

func TestSpoolServerServerCleanupAndLegacyRetryPreserveFirstTime(t *testing.T) {
	dir := capFake(t)
	spool, failed := filepath.Join(dir, "spool"), filepath.Join(dir, "failed")
	os.MkdirAll(spool, 0o700)
	os.MkdirAll(failed, 0o700)
	now := timeNow()
	for _, p := range []string{filepath.Join(spool, "expired.json"), filepath.Join(failed, "expired.json")} {
		os.WriteFile(p, []byte(`{"queued_at": "`+archiveISO(now-spoolTTL)+`"}`), 0o600)
	}
	if err := drain(false); err != nil { // nothing left to process
		t.Fatal(err)
	}
	if len(spoolJobs(t, spool))+len(spoolJobs(t, failed)) != 0 {
		t.Fatal("expired copies kept")
	}
	legacy := filepath.Join(spool, "legacy.json")
	os.WriteFile(legacy, []byte(`{"session_id":"s"}`), 0o600) // no users: every attempt fails
	touch(t, legacy, now-60)
	info, _ := os.Stat(legacy)
	initial := mtime(info)
	for range capMaxAttempts {
		if err := drain(false); err != nil {
			t.Fatal(err)
		}
	}
	jobs := spoolJobs(t, failed)
	if at, ok := spoolTimestamp(or(jobs, []map[string]any{{}}).([]map[string]any)[0]["queued_at"]); len(jobs) != 1 || !ok || math.Abs(at-initial) > 1e-3 {
		t.Fatal(jobs)
	}
	keep := timeNow
	t.Cleanup(func() { timeNow = keep })
	timeNow = func() float64 { return initial + spoolTTL + 1 }
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(failed, "legacy.json")) {
		t.Fatal("failed copy kept")
	}
}
