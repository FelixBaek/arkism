package skills

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const pcNow = 1_791_000_000.0

func pcTemp(t *testing.T) string {
	root := t.TempDir()
	oldState, oldLink := pcState, pcLinkDir
	pcState, pcLinkDir = root, filepath.Join(root, "arkism-pc-link")
	t.Cleanup(func() { pcState, pcLinkDir = oldState, oldLink })
	return root
}

func pcMetricsFile(t *testing.T, root, origin string, rows ...host.Obj) string {
	d := filepath.Join(root, "arkism-capture-pc-"+origin)
	os.MkdirAll(filepath.Join(d, "spool"), 0o755)
	var lines []string
	for _, r := range rows {
		lines = append(lines, host.MustDumps(r, host.DumpOpts{}))
	}
	os.WriteFile(filepath.Join(d, "metrics.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	return d
}

func ev(t float64, event string) host.Obj {
	return host.Obj{{Key: "ts", Value: unixISO(t)}, {Key: "event", Value: event}}
}

func pcRow(t *testing.T, now float64) host.Obj {
	rows := pcReport(now)
	if len(rows) == 0 {
		t.Fatal("no row")
	}
	return rows[0].(host.Obj)
}

func get(o host.Obj, k string) any { v, _ := o.Get(k); return v }

func TestPCNoTraceMeansNoRow(t *testing.T) {
	pcTemp(t)
	if len(pcReport(pcNow)) != 0 {
		t.Fatal("row without trace")
	}
}

func TestPCRecentSuccessIsNotFailing(t *testing.T) {
	root := pcTemp(t)
	pcMetricsFile(t, root, "codex", ev(pcNow-90, "writeback_failure"), ev(pcNow-60, "turn_completed"))
	row := pcRow(t, pcNow)
	if get(row, "agent") != "codex" || get(row, "failing") != false || get(row, "last_seen") != unixISO(pcNow-60) {
		t.Fatal(row)
	}
}

func TestPCLocalSkipDoesNotClearFailure(t *testing.T) {
	root := pcTemp(t)
	pcMetricsFile(t, root, "codex", ev(pcNow-60, "recall_degraded"), append(ev(pcNow-30, "turn_completed"), host.KV{Key: "status", Value: "skipped_no_cue"}))
	if get(pcRow(t, pcNow), "failing") != true {
		t.Fatal("skip cleared failure")
	}
}

func TestPCRetriedRememberClearsFailure(t *testing.T) {
	root := pcTemp(t)
	for _, e := range []string{"explicit_remember", "user_corrected_fact"} {
		pcMetricsFile(t, root, "codex", ev(pcNow-60, "writeback_failure"), ev(pcNow-30, e))
		if get(pcRow(t, pcNow), "failing") != false {
			t.Fatal(e)
		}
	}
}

func TestPCLastEventFailureIsFailing(t *testing.T) {
	root := pcTemp(t)
	pcMetricsFile(t, root, "zcode", ev(pcNow-90, "recall"), ev(pcNow-30, "hook_error"))
	if get(pcRow(t, pcNow), "failing") != true {
		t.Fatal("not failing")
	}
}

func TestPCPendingCountsOldSpoolFilesByEnqueueTime(t *testing.T) {
	root := pcTemp(t)
	d := pcMetricsFile(t, root, "claude_code", ev(pcNow-5, "recall"))
	for i := range 250 {
		os.WriteFile(filepath.Join(d, "spool", strconv.FormatInt(int64((pcNow-10)*1e9)+int64(i), 10)+"-new.json"), []byte("{}"), 0o644)
	}
	os.WriteFile(filepath.Join(d, "spool", strconv.FormatInt(int64((pcNow-3600)*1e9), 10)+"-old.json"), []byte("{}"), 0o644)
	entries, _ := os.ReadDir(filepath.Join(d, "spool"))
	for _, e := range entries {
		os.Chtimes(filepath.Join(d, "spool", e.Name()), time.Unix(pcNow, 0), time.Unix(pcNow, 0))
	}
	if get(pcRow(t, pcNow), "pending") != 1 {
		t.Fatal(pcRow(t, pcNow))
	}
}

func TestPCSuccessAfterErrorIsNotThrottled(t *testing.T) {
	pcTemp(t)
	pcMark("codex", false, pcNow)
	pcMark("codex", true, pcNow+1)
	if get(pcRow(t, pcNow+2), "failing") != true {
		t.Fatal("error lost")
	}
	pcMark("codex", false, pcNow+2)
	if get(pcRow(t, pcNow+3), "failing") != false {
		t.Fatal("success swallowed")
	}
}

func TestPCMarkIsThrottledAndErrorFlipsFailing(t *testing.T) {
	pcTemp(t)
	pcMark("gemini", false, pcNow)
	pcMark("gemini", false, pcNow+5)
	if m := host.ReadJSONFile(filepath.Join(pcLinkDir, "gemini.json"), 1<<20); m["last_ok"] != unixISO(pcNow) {
		t.Fatal(m)
	}
	if get(pcRow(t, pcNow+6), "failing") != false {
		t.Fatal("failing")
	}
	pcMark("gemini", true, pcNow+60)
	if get(pcRow(t, pcNow+61), "failing") != true {
		t.Fatal("not failing")
	}
	pcMark("gemini", false, pcNow+120)
	if get(pcRow(t, pcNow+121), "failing") != false {
		t.Fatal("still failing")
	}
}

func TestPCUnknownOriginIsIgnored(t *testing.T) {
	pcTemp(t)
	pcMark("../etc", false, pcNow)
	if _, err := os.Stat(pcLinkDir); err == nil {
		t.Fatal("link dir created")
	}
}
