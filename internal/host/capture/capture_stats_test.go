package capture

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func writeMetrics(t *testing.T, rows ...string) {
	t.Helper()
	if err := os.WriteFile(metricsFile(), []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureStatsCountsTheLocalMetrics(t *testing.T) {
	capFake(t)
	var out bytes.Buffer
	if err := stats(&out); err != nil || !strings.Contains(out.String(), `"capture_events": 0`) { // no metrics yet
		t.Fatal(err, out.String())
	}
	writeMetrics(t, `{"event": "recall", "captured": 2}`, `{"event": "recall"}`, `{"event": "writeback_failure"}`, `{"event": "hook_error"}`,
		`{"status": "skipped_no_cue"}`, `{"status": "no_claims"}`, `{"status": "candidate", "latency_ms": 30}`,
		`{"status": "accepted", "latency_ms": 10}`, `{"status": "rejected", "reason": "secret_detected", "latency_ms": 20}`,
		`{"status": "weird"}`, `{"event":"capture_copies_expired","count":3}`, `{"event":"capture_expiry_receipt_failed"}`, `{"event":"session_validation_withheld"}`)
	for _, d := range []string{spoolDir(), failedDir()} {
		os.MkdirAll(d, 0o700)
	}
	os.WriteFile(filepath.Join(spoolDir(), "a.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(spoolDir(), "b.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(failedDir(), "c.json"), []byte("{}"), 0o600)
	out.Reset()
	if err := stats(&out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	want := map[string]any{"capture_events": 4.0, "candidates": 1.0, "accepted": 1.0, "merged": 0.0, "rejected": 1.0, "privacy_rejected": 1.0,
		"skipped_no_cue": 1.0, "no_claims": 1.0, "writeback_failures": 1.0, "hook_errors": 1.0, "recalls": 2.0, "recall_hits_from_capture": 2.0,
		"spool_pending": 2.0, "failed_jobs": 1.0, "capture_latency_ms_p50": 20.0,
		"transport_copies_expired": 3.0, "expiry_receipt_failures": 1.0, "session_validation_withheld": 1.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	writeMetrics(t, `{"event": "recall"}`, `not json`)
	if err := stats(&out); err == nil {
		t.Fatal("bad metric line accepted")
	}
}

func TestCaptureUtilityCountsTextOverlapWithoutClaimingActualUse(t *testing.T) {
	capFake(t)
	projects := reflectDirs(t)
	settledPath := writeSession(t, projects, "s1", row(userRow, "배포해"), row(aiRow, "운영은 SQLite라서 임베딩도 Gemini로 맞췄습니다"))
	touch(t, settledPath, timeNow()-2*settled)
	writeSession(t, projects, "s3", row(userRow, "아직 진행 중"), row(aiRow, "작업 중"))
	writeMetrics(t,
		`{"event": "recall", "hook": "UserPromptSubmit", "session": "s1", "ids": ["m1", "m2", "m3", "m4", "m1"], "ts": "2026-10-02"}`,
		`{"event": "recall", "hook": "UserPromptSubmit", "session": "s2", "ids": ["m1"], "ts": "2026-10-02"}`,
		`{"event": "recall", "hook": "UserPromptSubmit", "session": "s3", "ids": ["m1"], "ts": "2026-10-02"}`,
		`{"event": "recall", "hook": "SessionStart", "session": "s1", "ids": ["m9"], "ts": "2026-10-02"}`,
		`{"event": "recall", "hook": "UserPromptSubmit", "session": "old", "ids": ["m9"], "ts": "2026-09-01"}`)
	texts := map[string]string{"m1": "운영 Orbit은 SQLite 전용이고 임베딩은 Gemini다.", "m2": "커밋 메시지는 한국어로 쓴다."}
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		switch id := pathID(path); id {
		case "m3":
			return nil, &host.HTTPError{Code: 404}
		case "m4":
			return nil, &host.HTTPError{Code: 503}
		default:
			return map[string]any{"text": texts[id]}, nil
		}
	}
	counts, sessions, err := utilityCounts("2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	// m1 overlaps in s1 and is unread in s2; s3 is busy; m3 is gone; m4 could not be read.
	want := map[string][3]int{"m1": {1, 1, 1}, "m2": {1, 0, 0}, "m3": {1, 0, 0}}
	if sessions != 3 || !reflect.DeepEqual(counts, want) {
		t.Fatal(sessions, counts)
	}

	var out bytes.Buffer
	if err := utility(1, &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	review := got["review_unused"].([]any)
	var ids []string
	for _, r := range review {
		ids = append(ids, r.(map[string]any)["id"].(string))
	}
	// the old row counts here: utility reads every session
	if got["sessions"] != 4.0 || got["memory_injections"] != 3.0 || got["used"] != 1.0 || got["use_rate"] != 0.333 ||
		!reflect.DeepEqual(ids, []string{"m2", "m3"}) {
		t.Fatal(got)
	}
	if got["measurement_basis"] != "answer_text_overlap" || got["actual_use_status"] != "not_measured" ||
		got["actual_used"] != nil || got["text_overlap_count"] != got["used"] ||
		got["text_overlap_rate"] != got["use_rate"] || got["unread_session_pairs"] != 2.0 ||
		!reflect.DeepEqual(got["review_without_text_overlap"], review) {
		t.Fatal("overlap and unavailable transcripts must not claim actual use", got)
	}
	if review[0].(map[string]any)["text"] != texts["m2"] {
		t.Fatal(review)
	}
}

func TestCaptureUtilityWithNothingRecalledHasNoRate(t *testing.T) {
	capFake(t)
	reflectDirs(t)
	var out bytes.Buffer
	if err := utility(3, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"use_rate": null`) || !strings.Contains(out.String(), `"review_unused": []`) {
		t.Fatal(out.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["actual_used"] != nil || got["actual_use_status"] != "not_measured" || got["text_overlap_rate"] != nil {
		t.Fatal("absence of observations is not zero actual use", got)
	}
}

func TestCaptureUtilityRepeatingAnIgnoredRuleDoesNotEstablishActualUse(t *testing.T) {
	capFake(t)
	projects := reflectDirs(t)
	path := writeSession(t, projects, "ignored-rule", row(userRow, "진행해"),
		row(aiRow, "I ignored the rule never delete production database and deleted it."))
	touch(t, path, timeNow()-2*settled)
	writeMetrics(t, `{"event":"recall","hook":"UserPromptSubmit","session":"ignored-rule","ids":["rule-1"]}`)
	capGet = func(_ string, _ time.Duration) (map[string]any, error) {
		return map[string]any{"text": "never delete production database"}, nil
	}
	var out bytes.Buffer
	if err := utility(1, &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["text_overlap_count"] != 1.0 || got["measurement_basis"] != "answer_text_overlap" ||
		got["actual_use_status"] != "not_measured" || got["actual_used"] != nil {
		t.Fatal("repeating a rule is not evidence it was followed", got)
	}
}

func TestCaptureBackfillSendsOnlyRowsWithText(t *testing.T) {
	capFake(t)
	dir := t.TempDir()
	note := filepath.Join(dir, "feedback.md")
	os.WriteFile(note, []byte("결론부터 쓴다"), 0o600)
	touch(t, note, 1760000000.25)
	plan := filepath.Join(dir, "plan.json")
	os.WriteFile(plan, []byte(`[{"path": `+strconv.Quote(note)+`, "action": "IMPORT", "kind": "preference", "subject": "보고", "text": "결론부터 쓴다"},
		{"path": "x.md", "action": "SKIP"}, {"path": "y.md", "action": "MERGE", "text": ""}]`), 0o600)
	var sent any
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/capture" {
			t.Fatal(path)
		}
		sent = body
		return mustLoad(t, `{"items": [{"status": "candidate", "reason": "review_required", "candidate_id": "n1"}]}`), nil
	}
	var out bytes.Buffer
	if err := backfill(plan, &out); err != nil {
		t.Fatal(err)
	}
	var report []map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err, out.String())
	}
	digest := sha256Hex([]byte("결론부터 쓴다"))
	if len(report) != 3 || report[0]["sha256"] != digest || report[0]["occurred_at"] != "2025-10-09T08:53:20.250Z" ||
		report[0]["candidate_id"] != "n1" || report[1]["action"] != "SKIP" || len(report[1]) != 2 || report[2]["action"] != "MERGE" {
		t.Fatal(report)
	}
	claim := field(field(sent, "claims").([]any)[0], "reason")
	if !strings.HasPrefix(str(claim), "source=claude_memory path=feedback.md sha256="+digest[:16]+" trace=claude-memory-backfill-") {
		t.Fatal(claim)
	}
	if id := field(field(sent, "event"), "id"); id != "claude-memory:feedback.md@"+digest[:16] {
		t.Fatal(id)
	}

	for body, want := range map[string]string{`[{"path": "x"}]`: "KeyError", `{"a": 1}`: "TypeError", `[{"path": ` + strconv.Quote(filepath.Join(dir, "gone")) + `, "action": "IMPORT", "text": "t"}]`: "URLError"} {
		os.WriteFile(plan, []byte(body), 0o600)
		if err := backfill(plan, &out); pyType(err) != want {
			t.Fatal(body, pyType(err), err)
		}
	}
	if err := backfill(filepath.Join(dir, "none.json"), &out); err == nil {
		t.Fatal("missing plan accepted")
	}
}

func TestCaptureMainDispatchesCommands(t *testing.T) {
	capFake(t)
	reflectDirs(t)
	capGet = func(string, time.Duration) (map[string]any, error) { return map[string]any{"items": []any{}}, nil }
	for _, args := range [][]string{{"stats"}, {"trial"}, {"review"}, {"review", "--dry-run"}, {"utility"}} {
		var out, errOut bytes.Buffer
		if code := captureMain(args, strings.NewReader(""), &out, &errOut); code != 0 {
			t.Fatal(args, code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := captureMain(nil, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(out.String(), strings.TrimSpace(captureDoc)[:20]) {
		t.Fatal(code, out.String())
	}
	if code := captureMain([]string{"backfill", filepath.Join(t.TempDir(), "none.json")}, strings.NewReader(""), &out, &errOut); code != 1 ||
		!strings.HasPrefix(errOut.String(), "URLError: ") {
		t.Fatal(code, errOut.String())
	}
}
