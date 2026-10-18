package capture

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func privateRecallFixture(t *testing.T) (cwd, transcript string, posts *[]map[string]any) {
	t.Helper()
	standing := stableFake(t)
	_, posts, _ = sessionOutcomeEnv(t)
	fakeStableValidation(t, standing)
	cwd = t.TempDir()
	transcript = filepath.Join(t.TempDir(), "session.jsonl")
	// A record beyond the metadata inspector's bound would make a whole-file
	// inspection fail. Recall may only append refs; the idle drain must refuse it.
	raw := `{"metadata":"` + strings.Repeat("x", archiveMetadataRecordBytes) + `"}` + "\n"
	if err := os.WriteFile(transcript, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	item := memoryHit("approved-memory", 3, "")
	item["kind"], item["text"] = "user", "approved visible memory"
	*standing = []any{item}
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{item}, true, nil }
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "0")
	t.Setenv("ARKISM_RECENT_SESSIONS", "0")
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_WORK_AGENT", "")
	t.Setenv("ARKISM_AGENT", "")
	old := hookTranscript
	t.Cleanup(func() { hookTranscript = old })
	return
}

func TestPrivateRecallHookDefersWholeTranscriptInspectionUntilIdle(t *testing.T) {
	for _, hook := range []string{"SessionStart", "UserPromptSubmit"} {
		t.Run(hook, func(t *testing.T) {
			cwd, transcript, posts := privateRecallFixture(t)
			event := map[string]any{"hook_event_name": hook, "session_id": "recall-session", "cwd": cwd, "transcript_path": transcript,
				"prompt": "승인된 기억 전달과 처리 경계를 확인합니다."}
			output := hookText(t, dumps(event))
			if !strings.Contains(output, "approved visible memory") {
				t.Fatal("normal recall context changed", output)
			}
			ledgers, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
			if len(ledgers) != 1 {
				t.Fatal("recall inspected the oversized transcript instead of only appending refs", ledgers)
			}
			raw, err := os.ReadFile(ledgers[0])
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil || row["cwd"] != cwd || row["transcript"] != transcript || len(raw) > 4096 {
				t.Fatal("explicit source context not preserved in the reference-only ledger", err)
			}
			if hook == "SessionStart" {
				event["source"] = "compact"
				if hookText(t, dumps(event)) != output {
					t.Fatal("cached stable context bytes changed")
				}
			}
			timeNow = func() float64 { return float64(time.Now().UnixNano())/1e9 + sessionIdle + 1 }
			sessionTraces()
			if len(*posts) != 0 {
				t.Fatal("the idle drain skipped full metadata inspection", *posts)
			}
		})
	}
}

func TestPrivateRecallLegacyCallerRetainsFullInspectionFallback(t *testing.T) {
	_, transcript, _ := privateRecallFixture(t)
	hookTranscript = transcript
	ledgerRecall("legacy-no-cwd", []map[string]any{memoryHit("memory", 1, "")})
	ledgers, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(ledgers) != 0 {
		t.Fatal("unknown legacy context bypassed transcript inspection", ledgers)
	}
}

func TestPrivateRecallFastPathChecksWorkingDirectoryAndOriginalParent(t *testing.T) {
	for _, policy := range []string{"paused", "cwd_excluded", "cwd_ignore", "source_ignore"} {
		t.Run(policy, func(t *testing.T) {
			cwd, transcript, _ := privateRecallFixture(t)
			switch policy {
			case "paused":
				privateTestPause(t)
			case "cwd_excluded":
				raw, _ := json.Marshal(map[string]any{"exclude": []string{cwd}})
				if err := os.WriteFile(captureSettingsPath(), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "cwd_ignore":
				if err := os.WriteFile(filepath.Join(cwd, captureIgnoreFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "source_ignore":
				if err := os.WriteFile(filepath.Join(filepath.Dir(transcript), captureIgnoreFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			event := map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "blocked-recall", "cwd": cwd,
				"transcript_path": transcript, "prompt": "승인된 기억 전달과 처리 경계를 확인합니다."}
			if code := hookMain(strings.NewReader(dumps(event)), &output); code != 0 {
				t.Fatal(code)
			}
			ledgers, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
			if len(ledgers) != 0 {
				t.Fatal("excluded recall created a ledger", ledgers)
			}
		})
	}
}
