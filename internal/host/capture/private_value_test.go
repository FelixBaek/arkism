package capture

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func privateValueFixtures() map[string]string {
	return map[string]string{
		"escaped":           `before \u003cprivate>synthetic-secret\u003c/private> after`,
		"recursive_escape":  `before \\u003cprivate\\u003esynthetic-secret\\u003c/private\\u003e after`,
		"unfinished_tag":    "before <private synthetic-secret",
		"unfinished_prefix": "before <private ",
	}
}

func TestPrivateValueKeepsOtherSessionRecordsBeforeAuthorization(t *testing.T) {
	for name, marked := range privateValueFixtures() {
		for _, role := range []string{"user", "assistant", "tool"} {
			t.Run(name+"/"+role, func(t *testing.T) {
				capFake(t)
				const safe = "Keep <PrivateRoute> and <privateer> components; answer in Korean."
				records := []any{
					map[string]any{"source_id": "private-source", "revision": 1, "role": role, "text": marked},
					map[string]any{"source_id": "safe-source", "revision": 1, "role": "user", "text": safe},
				}
				previous := map[string]any{"goal": marked, "constraints": []string{marked, safe}}
				prior := []any{host.Obj{{Key: "source_id", Value: "prior-source"}, {Key: "quote", Value: marked}}}
				before := dumps([]any{records, previous, prior})
				var authorized, sent string
				stop := errors.New("synthetic model boundary")
				llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) {
					sent = user
					return nil, stop
				}
				_, err := sessionPrepare(map[string]any{}, "session", records, 0, 2, 0, previous, prior, "", func(content string) error {
					authorized = content
					return nil
				})
				if !errors.Is(err, stop) || sent == "" || sent != authorized {
					t.Fatalf("safe session did not reach its authorized model: %v", err)
				}
				if privatePayload(sent) || strings.Contains(sent, "synthetic-secret") {
					t.Fatal("marked content crossed the model boundary")
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(sent), &payload); err != nil {
					t.Fatal(err)
				}
				data := payload["data"].(map[string]any)
				rows := data["records"].([]any)
				if len(rows) != 2 || autosaveGet(rows[0], "text") != privateMark || autosaveGet(rows[0], "role") != role || autosaveGet(rows[0], "source_id") != "private-source" || autosaveGet(rows[1], "text") != safe {
					t.Fatal("value masking changed other records or provenance")
				}
				state := data["previous_state"].(map[string]any)
				if state["goal"] != privateMark || !eq(state["constraints"], []any{privateMark, safe}) || autosaveGet(data["prior_evidence"].([]any)[0], "quote") != privateMark {
					t.Fatal("state/evidence values were not masked independently")
				}
				if dumps([]any{records, previous, prior}) != before {
					t.Fatal("caller records/state/evidence mutated")
				}
			})
		}
	}
}

func TestPrivateValueKeepsSafeLegacyDialogueInSameQueue(t *testing.T) {
	for name, marked := range privateValueFixtures() {
		t.Run(name, func(t *testing.T) {
			capFake(t)
			t.Setenv("ARKISM_SESSION_PROCESSOR", "")
			const safe = "앞으로 한국어로 답해. Keep <PrivateRoute> and <privateer> components."
			job := map[string]any{"origin": "codex", "session_id": "session", "event_id": "event", "type": "turn_completed",
				"users": []any{
					map[string]any{"text": marked, "at": "2026-10-18T00:00:00Z", "notes": []string{marked, safe}},
					map[string]any{"text": safe, "at": "2026-10-18T00:00:01Z"},
				}}
			evPrepareUsers = func(_ any, users []any, _ string) ([]any, error) { return users, nil }
			var query, sent string
			search = func(text, _ string, _ int, _ float64) ([]any, bool, error) {
				query = text
				return nil, false, nil
			}
			stop := errors.New("synthetic model boundary")
			llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) { sent = user; return nil, stop }
			persisted := false
			err := process(job, func() error { persisted = true; return nil })
			if !errors.Is(err, stop) || !persisted || query != safe || !strings.Contains(sent, safe) {
				t.Fatalf("safe dialogue did not reach search/model: %v, persisted=%v", err, persisted)
			}
			if privatePayload([]string{query, sent, dumps(job)}) || strings.Contains(sent, "synthetic-secret") {
				t.Fatal("marked queue value crossed the search/model boundary")
			}
			users := job["users"].([]any)
			if autosaveGet(users[0], "text") != privateMark || autosaveGet(users[1], "text") != safe || !eq(autosaveGet(users[0], "notes"), []string{privateMark, safe}) {
				t.Fatal("queue masking changed other values")
			}
		})
	}
}

func TestPrivateValueDoesNotRewriteIdentityOrKeys(t *testing.T) {
	for _, field := range []string{"source_id", "key"} {
		t.Run(field, func(t *testing.T) {
			capFake(t)
			row := map[string]any{"source_id": "source", "revision": 1, "role": "user", "text": "safe"}
			want := "session_private_identity"
			if field == "key" {
				row[`\u003cprivate>synthetic-secret`] = "safe"
				want = "session_private_content"
			} else {
				row[field] = `\u003cprivate>synthetic-secret`
			}
			before := dumps(row)
			llm = func(string, string, string, time.Duration, string, string) (any, error) {
				t.Fatal("private identity/key reached a model")
				return nil, nil
			}
			_, err := sessionPrepare(map[string]any{}, "session", []any{row}, 0, 1, 0, nil, nil, "", func(string) error {
				t.Fatal("private identity/key reached authorization")
				return nil
			})
			if errorCode(err) != want || dumps(row) != before {
				t.Fatal(err, "identity/key changed")
			}
		})
	}
}
