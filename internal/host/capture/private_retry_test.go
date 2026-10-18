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

func privateTestResume(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(captureSettingsPath(), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrivatePolicyDrainPreservesPreparedBudgetAndResumesExactRequest(t *testing.T) {
	for _, processor := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "session"}[processor], func(t *testing.T) {
			capFake(t)
			t.Setenv("ARKISM_SESSION_PROCESSOR", map[bool]string{false: "", true: "1"}[processor])
			scope := capScope()
			job := map[string]any{"origin": "codex", "session_id": "s", "event_id": "codex:s:prompt:original", "type": "task_finished",
				"queued_at": archiveISO(timeNow()), "attempts": 2, "session_cursor": 0,
				"users": []any{map[string]any{"text": "앞으로 한국어로 답해", "at": archiveISO(timeNow())}}}
			request := map[string]any{"scope": scope, "id": "session-" + sdigest([]any{scope, "codex", "s"}),
				"event_id": "original-operation", "expected_revision": 0, "package": map[string]any{"goal": "original"}}
			if processor {
				job["session_prepared"] = map[string]any{"operation_id": "original-operation", "checkpoint_request": request, "claims": []any{}, "capture_ack": true}
				linked := sessionLinked
				t.Cleanup(func() { sessionLinked = linked })
				sessionLinked = func(map[string]any, string) ([]any, map[string][]any, error) {
					return []any{map[string]any{"source_id": "source", "revision": 1, "role": "user", "text": "한국어로 답해"}}, map[string][]any{"source": {map[string]any{"archive_id": "archive"}}}, nil
				}
			} else {
				job["prepared_capture"] = map[string]any{"event_type": "explicit_remember", "occurred_at": "original-time",
					"claims": []any{map[string]any{"text": "original claim", "kind": "user", "basis": "explicit_user"}}}
			}
			path := put(t, filepath.Join(spoolDir(), "original.json"), dumps(job))
			before, _ := os.ReadFile(path)
			posts := 0
			capPost = func(route string, payload any, _ time.Duration) (map[string]any, error) {
				posts++
				if processor {
					if route != "/v1/work-checkpoints/save" || !eq(payload, request) {
						t.Fatal("resumed a different prepared request", route)
					}
					return map[string]any{"id": request["id"], "revision": 1}, nil
				}
				if route != "/v1/capture" || field(field(payload, "event"), "id") != job["event_id"] || !eq(field(payload, "claims"), field(job["prepared_capture"], "claims")) {
					t.Fatal("resumed a different capture event", route)
				}
				return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
			}
			privateTestPause(t)
			for range capMaxAttempts + 2 {
				if err := drain(false); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(before) || posts != 0 {
					t.Fatal("paused drain consumed durable state", err, posts)
				}
			}
			privateTestResume(t)
			for range 2 {
				if err := drain(false); err != nil {
					t.Fatal(err)
				}
			}
			if exists(path) || exists(filepath.Join(failedDir(), "original.json")) || posts != 1 {
				t.Fatal("resume must deliver the original once", posts)
			}
		})
	}
}

func TestPrivatePolicyWaitClassificationDoesNotMakeDefectsFree(t *testing.T) {
	for _, code := range []string{"capture_private_prepared", "synthetic_unknown_wait"} {
		t.Run(code, func(t *testing.T) {
			capFake(t)
			t.Setenv("ARKISM_SESSION_PROCESSOR", "")
			process := processJob
			t.Cleanup(func() { processJob = process })
			processJob = func(map[string]any, func() error) error { return sessionWait(code) }
			path := put(t, filepath.Join(spoolDir(), "defect.json"), dumps(map[string]any{"queued_at": archiveISO(timeNow()), "session_id": "s", "users": []any{}}))
			for range capMaxAttempts {
				if err := drain(false); err != nil {
					t.Fatal(err)
				}
			}
			failed, err := readJob(filepath.Join(failedDir(), "defect.json"))
			if err != nil || exists(path) || !eq(failed["attempts"], capMaxAttempts) || failed["last_error"] != code {
				t.Fatal("permanent failure contract changed", err, failed)
			}
		})
	}
}

func TestPrivatePolicyHandoffOutboxRetainsReceiptAndBindingUntilResume(t *testing.T) {
	f := newHandoffFixture(t)
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(f.root, "capture.json"))
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	job := f.load(f.pending)
	raw, _ := os.ReadFile(f.source)
	job.Set("end_byte", f.size())
	job.Set("sha256", sha256Hex(raw))
	job.Set("receipt", host.Obj{{Key: "id", Value: "work"}, {Key: "revision", Value: 2}, {Key: "status", Value: "saved"}})
	f.write(f.pending, job)
	before, _ := os.ReadFile(f.pending)
	bindingBefore, _ := os.ReadFile(f.binding)
	privateTestPause(t)
	for range capMaxAttempts + 2 {
		if err := handoffDrain(false); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(f.pending)
		bindingAfter, _ := os.ReadFile(f.binding)
		if string(after) != string(before) || string(bindingAfter) != string(bindingBefore) || len(f.posts) != 0 {
			t.Fatal("paused handoff consumed its receipt or binding")
		}
	}
	privateTestResume(t)
	if err := handoffDrain(false); err != nil {
		t.Fatal(err)
	}
	if exists(f.pending) || !eq(autosaveItem(f.load(f.binding), "expected_revision"), 2) || len(f.posts) != 0 {
		t.Fatal("handoff receipt did not resume exactly once")
	}
}

func TestPrivatePolicyPCQueueRetainsEventAndBindingUntilResume(t *testing.T) {
	entry := map[string]any{"event": map[string]any{"event_id": "original", "action": "suspend", "origin": "codex", "created_at": timeNow()}, "sha256": "original"}
	f := newProcessorFixture(t, map[string]any{"scope": map[string]any{}, "events": []any{entry}, "receipts": []any{}, "high_sequence": 7, "binding": map[string]any{"id": "work"}})
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(capState, "capture.json"))
	before, _ := os.ReadFile(f.path)
	privateTestPause(t)
	for range capMaxAttempts + 2 {
		if err := processorDrain(false); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(f.path)
		if string(after) != string(before) || f.posts != 0 {
			t.Fatal("paused PC queue changed state")
		}
	}
	privateTestResume(t)
	if err := processorDrain(false); err != nil {
		t.Fatal(err)
	}
	state := f.load()
	if len(anyList(state["events"])) != 0 || state["binding"] != nil || !eq(state["high_sequence"], 7) || f.posts != 0 {
		t.Fatal("PC queue did not resume", state)
	}
}

func TestPrivateCheckpointIdentityRefusedBeforeModelOrNewSave(t *testing.T) {
	for _, identity := range []string{"repository", "file_path", "source_ref", "check_command"} {
		t.Run(identity, func(t *testing.T) {
			capFake(t)
			f := newAutosaveFixture(t)
			base := f.pkg
			marked := "/synthetic/<private>identity</private>"
			f.pkg = func() map[string]any {
				pkg := base()
				switch identity {
				case "repository":
					pkg["repository"] = marked
				case "file_path":
					pkg["files"] = []any{map[string]any{"path": marked}}
				case "source_ref":
					pkg["source_refs"] = []any{map[string]any{"collection": "sources", "id": marked, "revision": 1}}
				case "check_command":
					pkg["checks"] = []any{map[string]any{"command": marked}}
				}
				return pkg
			}
			_, err := f.process(nil, nil)
			if errorCode(err) != "work_private_identity" || f.extracts != 0 || len(f.saves()) != 0 {
				t.Fatal(err, f.extracts, f.saves())
			}
			request, _ := sessionFixture(t)
			_, err = sessionPrepare(map[string]any{}, "s", request["records"].([]any), 0, 1, 0, f.pkg(), nil, "", func(string) error {
				t.Fatal("private checkpoint identity reached authorization")
				return nil
			})
			if errorCode(err) != "session_private_identity" {
				t.Fatal(err)
			}
		})
	}
	capFake(t)
	request, _ := sessionFixture(t)
	_, err := sessionPrepare(map[string]any{}, "s", request["records"].([]any), 0, 1, 0, nil,
		[]any{map[string]any{"source_id": "id<private>identity</private>", "revision": 1, "quote": "visible"}}, "", func(string) error {
			t.Fatal("private prior evidence reached authorization")
			return nil
		})
	if !strings.Contains(errorCode(err), "private_identity") {
		t.Fatal(err)
	}
}

func TestPrivatePriorDescriptionsRedactedAndIdentityPreservedAtAuthorization(t *testing.T) {
	capFake(t)
	request, _ := sessionFixture(t)
	ref := map[string]any{"collection": "sources", "id": "source@example.invalid", "revision": 1,
		"quote": "visible <private>synthetic-quote</private>"}
	previous := map[string]any{"repository": "/synthetic/repository", "goal": "visible <private>synthetic-goal</private>", "source_refs": []any{ref},
		"checks": []any{map[string]any{"command": "go test", "summary": "visible <private>synthetic-summary</private>", "source_refs": []any{ref}}}}
	prior := []any{map[string]any{"source_id": "prior@example.invalid", "revision": 1, "quote": "visible <private>synthetic-prior</private>"}}
	stop := errors.New("synthetic authorization stop")
	_, err := sessionPrepare(map[string]any{}, "s", request["records"].([]any), 0, 1, 0, previous, prior, "", func(content string) error {
		if strings.Contains(content, "synthetic-") || privatePayload(content) {
			t.Fatal("private descriptions reached authorization")
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(content), &payload); err != nil {
			t.Fatal(err)
		}
		data := payload["data"].(map[string]any)
		state := data["previous_state"].(map[string]any)
		if field(state, "repository") != previous["repository"] || field(state["source_refs"].([]any)[0], "id") != ref["id"] ||
			field(data["prior_evidence"].([]any)[0], "source_id") != field(prior[0], "source_id") {
			t.Fatal("state/source identity was rewritten")
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestPrivatePolicyChangedDuringTransportDoesNotSpendDrainAttempt(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	process := processJob
	t.Cleanup(func() { processJob = process })
	path := put(t, filepath.Join(spoolDir(), "transport.json"), dumps(map[string]any{"queued_at": archiveISO(timeNow()), "session_id": "s", "attempts": 2}))
	before, _ := os.ReadFile(path)
	processJob = func(map[string]any, func() error) error {
		privateTestPause(t)
		return errors.New("synthetic transport failure")
	}
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("policy changed during failed transport consumed a retry", err)
	}
	privateTestResume(t)
	processJob = func(map[string]any, func() error) error { return nil }
	if err := drain(false); err != nil || exists(path) {
		t.Fatal("transport did not resume", err)
	}
}
