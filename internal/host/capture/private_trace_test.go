package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func privateTraceFixture(t *testing.T, codex bool) (spool, ledger, transcript, project string, posts *[]map[string]any) {
	t.Helper()
	capFake(t)
	spool, posts, _ = sessionOutcomeEnv(t)
	project = t.TempDir()
	transcript = filepath.Join(t.TempDir(), "session.jsonl")
	rows := []map[string]any{{"type": "session_meta", "timestamp": host.NowISO(), "payload": map[string]any{"cwd": project}}}
	text := "visible <private>synthetic-reply <private>inner</private>hidden-tail</private> end"
	command := "git commit -m 'visible <private>synthetic-command <private>inner</private>command-tail</private> end'"
	if codex {
		rows = append(rows,
			map[string]any{"type": "response_item", "timestamp": host.NowISO(), "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "짧게 답해"}}}},
			map[string]any{"type": "response_item", "timestamp": host.NowISO(), "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}},
			map[string]any{"type": "response_item", "timestamp": host.NowISO(), "payload": map[string]any{"type": "custom_tool_call", "input": command}})
	} else {
		rows = append(rows,
			map[string]any{"type": "user", "timestamp": host.NowISO(), "message": map[string]any{"role": "user", "content": "짧게 답해"}},
			map[string]any{"type": "assistant", "timestamp": host.NowISO(), "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}, map[string]any{"type": "tool_use", "input": map[string]any{"command": command}}}}})
	}
	var raw strings.Builder
	for _, row := range rows {
		raw.WriteString(dumps(row) + "\n")
	}
	if err := os.WriteFile(transcript, []byte(raw.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	before := hookTranscript
	hookTranscript = transcript
	t.Cleanup(func() { hookTranscript = before })
	ledgerRecall("private-trace-session", []map[string]any{memoryHit("memory", 1, "")})
	paths, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.jsonl"))
	if len(paths) != 1 {
		t.Fatal("expected an actual recall ledger", paths)
	}
	ledger = paths[0]
	timeNow = func() float64 { return float64(time.Now().UnixNano())/1e9 + sessionIdle + 1 }
	return
}

func TestPrivateTraceLedgerNeverSendsMarkedAssistantOrToolText(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(map[bool]string{false: "claude", true: "codex"}[codex], func(t *testing.T) {
			spool, ledger, _, _, posts := privateTraceFixture(t, codex)
			post := capPost
			capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
				for _, secret := range []string{"synthetic-reply", "hidden-tail", "synthetic-command", "command-tail"} {
					if strings.Contains(dumps(body), secret) {
						t.Fatal("private behavior reached the actual transport boundary")
					}
				}
				return post(path, body, d)
			}
			if _, err := recordSession(ledger, spool); err != nil || len(*posts) != 1 {
				t.Fatal(err, *posts)
			}
			events := (*posts)[0]["events"].([]any)
			if len(events) != 2 {
				t.Fatal("redacted behavior lost", events)
			}
			behavior := events[1].(map[string]any)["payload"].(map[string]any)
			if behavior["incomplete"] != true || len(behavior["commits"].([]any)) != 1 || len(behavior["replies"].([]any)) != 1 || privatePayload(behavior) {
				t.Fatal("redacted behavior was treated as a complete observation", behavior)
			}
		})
	}
}

func TestPrivateTracePolicyPreservesOpenLedgerUntilResume(t *testing.T) {
	for _, policy := range []string{"paused", "source_ignore", "project_ignore", "project_exclude"} {
		t.Run(policy, func(t *testing.T) {
			spool, ledger, transcript, project, posts := privateTraceFixture(t, true)
			before, _ := os.ReadFile(ledger)
			ignore := ""
			switch policy {
			case "paused":
				privateTestPause(t)
			case "source_ignore":
				ignore = filepath.Join(filepath.Dir(transcript), captureIgnoreFile)
			case "project_ignore":
				ignore = filepath.Join(project, captureIgnoreFile)
			case "project_exclude":
				data, _ := json.Marshal(map[string]any{"exclude": []string{project}})
				if err := os.WriteFile(captureSettingsPath(), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if ignore != "" {
				if err := os.WriteFile(ignore, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for range 3 {
				sessionTraces()
				after, err := os.ReadFile(ledger)
				claims, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.claimed"))
				receipts, _ := filepath.Glob(filepath.Join(spool, "*.json"))
				if err != nil || string(before) != string(after) || len(claims) != 0 || len(receipts) != 0 || len(*posts) != 0 {
					t.Fatal("blocked trace consumed ledger/claim/receipt", err, claims, receipts)
				}
			}
			if ignore != "" {
				if err := os.Remove(ignore); err != nil {
					t.Fatal(err)
				}
			}
			privateTestResume(t)
			sessionTraces()
			if len(*posts) != 1 || exists(ledger) {
				t.Fatal("trace did not resume", *posts)
			}
		})
	}
}

func TestPrivateTracePolicyAfterPostDefersReceiptAndReplaysStableID(t *testing.T) {
	spool, ledger, _, _, posts := privateTraceFixture(t, true)
	post := capPost
	pause := true
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		response, err := post(path, body, d)
		if pause {
			privateTestPause(t)
		}
		return response, err
	}
	sessionTraces()
	receipts, _ := filepath.Glob(filepath.Join(spool, "*.json"))
	claims, _ := filepath.Glob(filepath.Join(capState, sessionLedgerName, "*.claimed"))
	if len(*posts) != 1 || exists(ledger) || len(claims) != 1 || len(receipts) != 0 {
		t.Fatal("post-time pause consumed a receipt or claim", *posts, claims, receipts)
	}
	claimBefore, _ := os.ReadFile(claims[0])
	sessionTraces()
	claimAfter, _ := os.ReadFile(claims[0])
	if len(*posts) != 1 || string(claimBefore) != string(claimAfter) {
		t.Fatal("paused claim changed")
	}
	pause = false
	privateTestResume(t)
	sessionTraces()
	receipts, _ = filepath.Glob(filepath.Join(spool, "*.json"))
	if len(*posts) != 2 || dumps((*posts)[0]) != dumps((*posts)[1]) || len(receipts) != 1 || exists(claims[0]) {
		t.Fatal("trace did not replay its exact stable request", *posts, receipts)
	}
}

func TestPrivateTraceMissingTranscriptRemainsReferenceOnly(t *testing.T) {
	spool, ledger, transcript, _, posts := privateTraceFixture(t, true)
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	if _, err := recordSession(ledger, spool); err != nil || len(*posts) != 1 || len((*posts)[0]["events"].([]any)) != 1 {
		t.Fatal("missing transcript lost its reference-only trace", err, *posts)
	}
}

func TestPrivateTraceRejectsLedgerIdentityBeforeTransport(t *testing.T) {
	for _, key := range []string{"origin", "session", "scope", "refs"} {
		t.Run(key, func(t *testing.T) {
			spool, ledger, _, _, posts := privateTraceFixture(t, true)
			raw, err := os.ReadFile(ledger)
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				t.Fatal(err)
			}
			marked := "id<private>synthetic-identity</private>"
			switch key {
			case "scope":
				row[key].(map[string]any)["user_id"] = marked
			case "refs":
				row[key].([]any)[0].(map[string]any)["id"] = marked
			default:
				row[key] = marked
			}
			if err := os.WriteFile(ledger, []byte(dumps(row)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := recordSession(ledger, spool); err == nil || len(*posts) != 0 {
				t.Fatal("private ledger identity reached transport", err, *posts)
			}
		})
	}
}

func TestPrivateCanonicalReceiptChecksIdentityAndPolicyBeforeSpool(t *testing.T) {
	for _, mode := range []string{"private_id", "private_session", "private_timestamp", "paused", "repository_ignore"} {
		t.Run(mode, func(t *testing.T) {
			capFake(t)
			scope := traceScopeEnv(t)
			t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
			ref := map[string]any{"collection": "execution_traces", "id": "trace", "revision": 1}
			event := map[string]any{"scope": scope, "session_id": "session", "occurred_at": host.NowISO(),
				"receipt": map[string]any{"trace": ref, "run": ref, "source_events": []any{}}}
			switch mode {
			case "private_id":
				ref["id"] = "id<private>synthetic-identity</private>"
			case "private_session":
				event["session_id"] = "session<private>synthetic-session</private>"
			case "private_timestamp":
				event["occurred_at"] = "timestamp<private>synthetic-timestamp</private>"
			case "paused":
				privateTestPause(t)
			case "repository_ignore":
				project := t.TempDir()
				event["repository"] = project
				if err := os.WriteFile(filepath.Join(project, captureIgnoreFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := EnqueueSessionTrace(event); !pending(err) {
				t.Fatal(err)
			}
			files, _ := filepath.Glob(filepath.Join(spoolDir(), "*.json"))
			if len(files) != 0 {
				t.Fatal("blocked canonical receipt created a spool", files)
			}
		})
	}
}
