package capture

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestPrivateModelRefusesUnredactableKeysBeforeAuthorization(t *testing.T) {
	capFake(t)
	request, _ := sessionFixture(t)
	records := request["records"].([]any)
	records[0].(map[string]any)["tool_payload"] = map[string]any{"<private>synthetic-key</private>": "public"}
	_, err := sessionPrepare(map[string]any{}, "s", records, 0, 1, 0, nil, nil, "", func(string) error {
		t.Fatal("private key entered transfer authorization")
		return nil
	})
	if errorCode(err) != "session_private_content" {
		t.Fatal(err)
	}
}

func TestPrivateModelRefusesMarkedOutputBeforePreparingDelivery(t *testing.T) {
	capFake(t)
	request, output := sessionFixture(t)
	output["session_state"].(map[string]any)["goal"] = "visible <private>synthetic-output</private>"
	llm = func(string, string, string, time.Duration, string, string) (any, error) { return output, nil }
	prepared, err := sessionPrepare(map[string]any{}, "s", request["records"].([]any), 0, 1, 0, nil, nil, "", func(string) error { return nil })
	if prepared != nil || errorCode(err) != "model_private_output" {
		t.Fatal(prepared, err)
	}
}

func TestPrivatePreparedRetriesParkWithoutRewritingOrNetwork(t *testing.T) {
	for _, key := range []string{"prepared_capture", "session_prepared"} {
		for _, marked := range []string{"users", "prepared"} {
			t.Run(key+"/"+marked, func(t *testing.T) {
				capFake(t)
				t.Setenv("ARKISM_SESSION_PROCESSOR", map[bool]string{true: "1", false: ""}[key == "session_prepared"])
				request, _ := sessionFixture(t)
				linked := sessionLinked
				t.Cleanup(func() { sessionLinked = linked })
				sessionLinked = func(map[string]any, string) ([]any, map[string][]any, error) {
					return request["records"].([]any), map[string][]any{"s1": {map[string]any{"archive_id": "archive"}}}, nil
				}
				job := map[string]any{"origin": "codex", "session_id": "s", "event_id": "codex:s:prompt:hash", "type": "task_finished", "session_cursor": 0,
					"users": []any{map[string]any{"text": "앞으로 한국어로 답해", "at": "2026-10-18T00:00:00Z"}},
					key: map[string]any{"text": "public", "event_type": "explicit_remember", "occurred_at": "2026-10-18T00:00:00Z",
						"operation_id": "operation", "metadata": map[string]any{"model_id": "sonnet"},
						"claims":             []any{map[string]any{"text": "public", "kind": "user", "basis": "explicit_user"}},
						"checkpoint_request": map[string]any{"scope": capScope(), "expected_revision": 0, "id": "checkpoint", "package": map[string]any{"goal": "public"}}}}
				if marked == "users" {
					job["users"].([]any)[0].(map[string]any)["text"] = "remember <private>synthetic-old-input</private>"
				} else {
					job[key].(map[string]any)["text"] = "remember <private>synthetic-old-output</private>"
				}
				before := dumps(job)
				persist := func() error { t.Fatal("private retry changed durable state"); return nil }
				if err := process(job, persist); errorCode(err) != "capture_private_prepared" {
					t.Fatal(err)
				}
				if key == "session_prepared" {
					if err := sessionProcess(job, persist, "codex"); errorCode(err) != "session_private_prepared" {
						t.Fatal(err)
					}
				}
				if dumps(job) != before {
					t.Fatal("private prepared retry changed its event or cursor")
				}
			})
		}
	}
}

func TestPrivateLegacyQueueSanitizedBeforeSearchAndModel(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(t.TempDir(), "capture.json"))
	job := map[string]any{"origin": "codex", "event_id": "codex:s:prompt:hash", "session_id": "s", "type": "turn_completed",
		"users":           []any{map[string]any{"at": "2026-10-18T00:00:00Z", "text": "앞으로 <private>synthetic-queued <private>nested</private>hidden-tail</private> 한국어로 답해"}},
		"archive_locator": map[string]any{"origin": "codex", "device": client(), "session_id": "s", "prompt_sha256": strings.Repeat("a", 64)}}
	evLocatePrompt = func(map[string]any) (any, any, string, error) { return nil, nil, "", nil }
	evPrepareUsers = func(_ any, users []any, _ string) ([]any, error) { return users, nil }
	var query, sent string
	search = func(q, _ string, _ int, _ float64) ([]any, bool, error) {
		query = q
		return []any{map[string]any{"id": "m", "text": "visible <private>synthetic-old-memory</private>"}}, false, nil
	}
	stop := errors.New("synthetic model boundary")
	llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) { sent = user; return nil, stop }
	persisted := false
	err := process(job, func() error { persisted = true; return nil })
	if !errors.Is(err, stop) || !persisted || query == "" || sent == "" {
		t.Fatal(err, persisted, query, sent)
	}
	for _, value := range []string{query, sent, dumps(job)} {
		for _, secret := range []string{"synthetic-queued", "hidden-tail", "synthetic-old-memory"} {
			if strings.Contains(value, secret) {
				t.Fatal("private text reached query/model/spool")
			}
		}
	}
	if truthy(job["archive_locator"]) {
		t.Fatal("raw private prompt hash remained reusable")
	}
}

func TestPrivateAutosavePriorStateSanitizedBeforeExtractAndSave(t *testing.T) {
	f := newAutosaveFixture(t)
	base := f.pkg
	f.pkg = func() map[string]any {
		pkg := base()
		pkg["goal"] = "public <private>synthetic-prior</private>"
		return pkg
	}
	extract := func(pkg any, messages []any) (any, error) {
		if text := dumps([]any{pkg, messages}); strings.Contains(text, "synthetic-") || privatePayload([]any{pkg, messages}) {
			t.Fatal("private dialogue/prior state reached extractor")
		}
		return f.claims, nil
	}
	_, err := autosaveProcess(&f.job, f.request, f.scope, extract, func() error { return nil })
	if err != nil || len(f.saves()) != 1 || strings.Contains(f.saves()[0], "synthetic-") {
		t.Fatal(err, f.saves())
	}
}

func TestPrivateAutosaveLegacyArchiveNeverBecomesNewEvidence(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			f := newAutosaveFixture(t)
			for _, message := range f.messages {
				if message["role"] == role {
					message["text"] = str(message["text"]) + " <private>synthetic-archive</private>"
				}
			}
			_, err := f.process(nil, nil)
			if errorCode(err) != "work_private_source" || f.extracts != 0 || len(f.saves()) != 0 {
				t.Fatal(err, f.extracts, f.saves())
			}
		})
	}
}

func TestPrivateAutosavePreparedRetryNeverReplays(t *testing.T) {
	f := newAutosaveFixture(t)
	f.job.Set("prepared", host.Obj{{Key: "package", Value: host.Obj{{Key: "goal", Value: "<private>synthetic-retry</private>"}}}})
	before := dumps(f.job)
	_, err := f.process(nil, func() error { t.Fatal("unsafe retry persisted"); return nil })
	if errorCode(err) != "work_private_prepared" || len(f.requests) != 0 || dumps(f.job) != before {
		t.Fatal(err, f.requests)
	}
}

func TestPrivateModelAutosaveAndBenchmarkUseSanitizedRequests(t *testing.T) {
	capFake(t)
	var sent []string
	llm = func(system, user, _ string, _ time.Duration, _, _ string) (any, error) {
		sent = append(sent, system+user)
		return []any{}, nil
	}
	if _, err := autosaveExtract(map[string]any{"goal": "visible <private>synthetic-prior</private>"}, []any{
		map[string]any{"role": "user", "text": "visible <private>synthetic-user</private>"},
		map[string]any{"role": "assistant", "text": "visible <private>synthetic-assistant</private>"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractClaims("visible <private>synthetic-bench</private>", "2026-10-18", []map[string]any{{"id": "memory", "text": "visible <private>synthetic-related</private>"}}, "visible <private>synthetic-guidance</private>", "test"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || strings.Contains(strings.Join(sent, ""), "synthetic-") {
		t.Fatal("private model request", len(sent))
	}
}

func TestPrivateZcodeHintsAndCaptureRefuseBeforeRequests(t *testing.T) {
	capFake(t)
	path := filepath.Join(t.TempDir(), "snapshot.jsonl")
	if err := os.WriteFile(path, []byte(`{"table":"part","record":{"data":"\u003cprivate\u003esynthetic-source"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	locator := host.Obj{{Key: "origin", Value: "zcode"}, {Key: "device", Value: "server"}, {Key: "session_id", Value: "s"}, {Key: "prompt_sha256", Value: strings.Repeat("a", 64)}}
	if hints, err := evidenceZcodeHints(path, locator); hints != nil || errorCode(err) != "session_archive_private_skipped" {
		t.Fatal(hints, err)
	}
	if _, err := sendCapture("turn_completed", "event", "session", "now", []any{map[string]any{"text": "<private>synthetic-claim</private>"}}, "codex", 0, nil); errorCode(err) != "capture_private_content" {
		t.Fatal(err)
	}
}

func TestPrivatePendingQueueRechecksCapturePolicyWithoutChangingState(t *testing.T) {
	for _, policy := range []string{"paused", "invalid", "excluded", "ignore"} {
		t.Run(policy, func(t *testing.T) {
			capFake(t)
			cwd := t.TempDir()
			settings := captureSettingsPath()
			switch policy {
			case "paused":
				if err := os.WriteFile(settings, []byte(`{"paused":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
				cwd = "" // Paused/invalid settings apply even to old jobs with no cwd.
			case "invalid":
				if err := os.WriteFile(settings, []byte(`{"paused":`), 0o600); err != nil {
					t.Fatal(err)
				}
				cwd = ""
			case "excluded":
				data, err := json.Marshal(map[string]any{"exclude": []string{cwd}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(settings, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "ignore":
				if err := os.WriteFile(filepath.Join(cwd, captureIgnoreFile), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			job := map[string]any{"cwd": cwd, "session_cursor": 2, "prepared_capture": map[string]any{"claims": []any{}}}
			before := dumps(job)
			persist := func() error { t.Fatal("paused queue changed durable state"); return nil }
			if err := process(job, persist); errorCode(err) != "capture_policy_wait" {
				t.Fatal(err)
			}
			if err := sessionProcess(job, persist, "codex"); errorCode(err) != "session_capture_policy_wait" {
				t.Fatal(err)
			}
			work := host.Obj{{Key: "cwd", Value: cwd}, {Key: "prepared", Value: host.Obj{{Key: "package", Value: host.Obj{{Key: "goal", Value: "public"}}}}}}
			workBefore := dumps(work)
			request := func(string, any) (map[string]any, error) { t.Fatal("paused autosave made a request"); return nil, nil }
			if _, err := autosaveProcess(&work, request, nil, nil, persist); errorCode(err) != "work_capture_policy_wait" {
				t.Fatal(err)
			}
			if dumps(job) != before || dumps(work) != workBefore {
				t.Fatal("paused queue changed event/cursor")
			}
		})
	}
}

func TestPrivatePreparedRepositoryRechecksIgnoreWithoutJobCWD(t *testing.T) {
	capFake(t)
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, captureIgnoreFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := map[string]any{"repository": repository, "goal": "public"}
	job := map[string]any{"session_cursor": 2, "session_prepared": map[string]any{"checkpoint_request": map[string]any{"package": pkg}}}
	before := dumps(job)
	persist := func() error { t.Fatal("ignored repository advanced durable state"); return nil }
	if err := sessionProcess(job, persist, "codex"); errorCode(err) != "session_capture_policy_wait" || dumps(job) != before {
		t.Fatal(err)
	}
	work := host.Obj{{Key: "prepared", Value: host.Obj{{Key: "package", Value: pkg}}}}
	workBefore := dumps(work)
	request := func(string, any) (map[string]any, error) {
		t.Fatal("ignored repository made a request")
		return nil, nil
	}
	if _, err := autosaveProcess(&work, request, nil, nil, persist); errorCode(err) != "work_capture_policy_wait" || dumps(work) != workBefore {
		t.Fatal(err)
	}
}
