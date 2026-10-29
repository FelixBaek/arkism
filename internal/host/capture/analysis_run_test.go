package capture

import (
	"errors"
	"os"
	"testing"
)

func readAnalysis(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(analysisPath())
	if err != nil {
		t.Fatal(err)
	}
	v, err := loadsBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func TestDrainRecordsWhatItDidWithTheSpool(t *testing.T) {
	sessionUnblockSpool(t, "session_archive_private_skipped")
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	got := readAnalysis(t)
	// the private batch is dropped with its child; the other session waits for its batch
	for k, want := range map[string]float64{"jobs": 2, "done": 0, "dropped": 1, "waiting": 1, "failed": 0} {
		if n, _ := num(got[k]); n != want {
			t.Fatal(k, got)
		}
	}
	if got["reason"] != "session_archive_private_skipped" || got["last_done_at"] != nil {
		t.Fatal(got)
	}
	before, _ := os.ReadFile(analysisPath())
	if err := drain(false); err != nil { // the waiting job is not due yet: nothing touched, the last run stays
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(analysisPath()); string(after) != string(before) {
		t.Fatal("an idle drain overwrote the last run", string(after))
	}
}

func TestAnalysisRunKeepsTheLastFinishedJob(t *testing.T) {
	capFake(t)
	if err := saveAnalysisRun(&analysisRun{jobs: 2, done: 2, candidates: 0, reasons: map[string]int{}}, nil); err != nil {
		t.Fatal(err)
	}
	done := readAnalysis(t)
	if done["last_done_at"] == nil || !eq(done["last_done_candidates"], 0) {
		t.Fatal("a run that finished jobs with no candidates must still mark them done", done)
	}
	if err := saveAnalysisRun(&analysisRun{jobs: 1, failed: 1, reasons: map[string]int{"401": 1}}, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	got := readAnalysis(t)
	if got["last_done_at"] != done["last_done_at"] || got["reason"] != "401" || got["error"] == "" {
		t.Fatal("a failed run must keep the last finished job and say why", got)
	}
}
