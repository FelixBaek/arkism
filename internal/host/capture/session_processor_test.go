package capture

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func sessionFixture(t *testing.T) (request, output map[string]any) {
	t.Helper()
	request = mustLoad(t, `{"start": 0, "end": 1, "expected_revision": 0, "schema_version": "`+sessionSchemaVersion+`", "prompt_version": "`+sessionPromptVersion+`",
		"records": [{"source_id": "s1", "revision": 1, "role": "user", "text": "한국어로 답해"}]}`)
	ref := `{"source_id": "s1", "revision": 1, "quote": "한국어로 답해"}`
	output = mustLoad(t, `{"session_state": {"goal": "답변", "constraints": [], "decisions": [], "completed": [], "next_steps": [],
		"uncertainties": [], "artifacts": [], "checks": [], "source_refs": [`+ref+`]},
		"memory_candidates": [{"statement": "한국어 선호", "kind": "preference", "domains": ["personal"], "applicability": "", "observed_at": "",
			"valid_from": "", "evidence_kind": "user_statement", "source_refs": [`+ref+`], "uncertainty": ""}],
		"coverage": {"start": 0, "end": 1, "expected_revision": 0, "schema_version": "`+sessionSchemaVersion+`", "prompt_version": "`+sessionPromptVersion+`", "source_ids": ["s1"]},
		"unresolved_refs": []}`)
	return request, output
}

func pending(err error) bool { return errors.Is(err, errSessionPending) }

func TestSessionUnknownArtifactsRemainConstraintsAndHashNeedsEvidence(t *testing.T) {
	_, output := sessionFixture(t)
	state := output["session_state"].(map[string]any)
	state["artifacts"] = []any{map[string]any{"path": "unknown.txt", "sha256": ""}, map[string]any{"path": "madeup.txt", "sha256": strings.Repeat("a", 64)},
		map[string]any{"path": "observed.txt", "sha256": strings.Repeat("b", 64)}}
	ref := state["source_refs"].([]any)[0].(map[string]any)
	ref["quote"] = str(ref["quote"]) + " observed.txt " + strings.Repeat("b", 64)
	pkg := sessionCheckpointPackage(map[string]any{"output": output}, "s", "codex", "")
	if files := pkg["files"].([]any); len(files) != 1 || !eq(files[0], state["artifacts"].([]any)[2]) || len(pkg["constraints"].([]any)) != 2 {
		t.Fatal(pkg)
	}
	if err := validateCheckpointPackage(pkg); err != nil {
		t.Fatal(err)
	}
	pkg["goal"] = strings.Repeat("한", 3000)
	if err := validateCheckpointPackage(pkg); !pending(err) {
		t.Fatal(err)
	}
}

func TestSessionFabricatedOrStaleEvidenceAndFalseTestSuccessRejected(t *testing.T) {
	request, output := sessionFixture(t)
	if err := validateSessionOutput(output, request); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(out, src map[string]any){
		"id":       func(out, _ map[string]any) { candidateRef(out)["source_id"] = "fabricated" },
		"revision": func(out, _ map[string]any) { candidateRef(out)["revision"] = 2 },
		"quote":    func(out, _ map[string]any) { candidateRef(out)["quote"] = "영어로 답해" },
		"role":     func(_, src map[string]any) { src["records"].([]any)[0].(map[string]any)["role"] = "assistant" },
		"coverage": func(out, _ map[string]any) { out["coverage"].(map[string]any)["end"] = 2 },
		"test": func(out, _ map[string]any) {
			state := out["session_state"].(map[string]any)
			state["checks"] = []any{map[string]any{"command": "go test", "outcome": "passed", "summary": "", "source_refs": state["source_refs"]}}
		},
	}
	for name, mutate := range mutations {
		src, out := sessionFixture(t)
		mutate(out, src)
		if err := validateSessionOutput(out, src); !pending(err) {
			t.Fatal(name, err)
		}
	}
}

func candidateRef(out map[string]any) map[string]any {
	return out["memory_candidates"].([]any)[0].(map[string]any)["source_refs"].([]any)[0].(map[string]any)
}

func TestSessionNoCallWithoutNewInputAndBoundaryPreservation(t *testing.T) {
	if sessionDue(nil, "explicit_correction", 0, 0) || !sessionDue([]any{map[string]any{"text": "정정"}}, "explicit_correction", 0, 0) {
		t.Fatal("due")
	}
	rows := []any{map[string]any{"text": strings.Repeat("a", 40), "group_id": "tool1"}, map[string]any{"text": strings.Repeat("b", 40), "group_id": "tool1"},
		map[string]any{"text": strings.Repeat("c", 40)}}
	if chunks, err := chunkRecords(rows, 170); err != nil || len(chunks) != 2 || len(chunks[0]) != 2 || len(chunks[1]) != 1 {
		t.Fatal(chunks, err)
	}
	if _, err := chunkRecords(rows, 80); !pending(err) {
		t.Fatal(err)
	}
	if chunks, _ := chunkRecords([]any{map[string]any{"text": strings.Repeat("a", 40)}, map[string]any{"text": strings.Repeat("b", 40)}}, 70); len(chunks) != 2 {
		t.Fatal(chunks)
	}
}

func TestSessionQualityGateExactVersionsAndNoAutomaticOpus(t *testing.T) {
	request, output := sessionFixture(t)
	capFake(t)
	fakeUsage(t, nil, errors.New("no reading"))
	gateFile := filepath.Join(t.TempDir(), "gate.json")
	for gate, want := range map[string]string{"": sessionModel,
		`{"schema_version": "` + sessionSchemaVersion + `", "prompt_version": "` + sessionPromptVersion + `", "passed_categories": ["korean_correction"]}`: sessionFastModel} {
		os.WriteFile(gateFile, []byte(gate), 0o600)
		t.Setenv("ARKISM_HAIKU_QUALITY_GATE", gateFile)
		var models []string
		llm = func(_, _, model string, _ time.Duration, _, key string) (any, error) {
			if key != "" {
				t.Fatal(key)
			}
			models = append(models, model)
			return output, nil
		}
		prepared, err := sessionPrepare(map[string]any{}, "session", request["records"].([]any), 0, 1, 0, nil, nil, "korean_correction", func(string) error { return nil })
		if err != nil || len(models) != 1 || models[0] != want || prepared["end"] != int64(1) {
			t.Fatal(gate, models, prepared, err)
		}
		if sessionCheckpointPackage(prepared, "s", "codex", "")["status"] != "in_progress" {
			t.Fatal(prepared)
		}
	}
}

func TestSessionExternalTransferDenialPrecedesGeneration(t *testing.T) {
	request, _ := sessionFixture(t)
	capFake(t)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("model called before transfer authorization")
		return nil, nil
	}
	_, err := sessionPrepare(map[string]any{}, "s", request["records"].([]any), 0, 1, 0, nil, nil, "", func(string) error { return sessionWait("external_transfer_denied") })
	if !pending(err) || errorCode(err) != "external_transfer_denied" {
		t.Fatal(err)
	}
}

func TestSessionUnknownFieldsAndSummaryOnlyCandidatesFail(t *testing.T) {
	request, output := sessionFixture(t)
	output["permissions"] = []any{"admin"}
	if err := validateSessionOutput(output, request); !pending(err) {
		t.Fatal(err)
	}
	delete(output, "permissions")
	request["prior_evidence"] = request["records"]
	request["records"] = []any{map[string]any{"source_id": "s2", "revision": 1, "role": "user", "text": "new"}}
	output["coverage"].(map[string]any)["source_ids"] = []any{"s2"}
	if err := validateSessionOutput(output, request); !pending(err) || !strings.Contains(errorCode(err), "summary_only") {
		t.Fatal(err)
	}
}

// A pending window waits without spending an attempt, and the processor never runs as the owner.
func TestSessionPendingDrainKeepsAttemptsAndNeedsServiceCredential(t *testing.T) {
	dir := capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	var out, errOut strings.Builder
	t.Setenv("ARKISM_HARNESS_TOKEN_FILE", "")
	if code := captureMain([]string{"drain"}, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "harness_service_credential_required") {
		t.Fatal(code, errOut.String())
	}
	os.MkdirAll(spoolDir(), 0o700)
	path := filepath.Join(spoolDir(), "job.json")
	os.WriteFile(path, []byte(`{"session_id": "s1", "event_id": "e1", "type": "turn_completed", "origin_product": "`+capOrigin+`",
		"queued_at": "`+time.Now().UTC().Format(time.RFC3339)+`", "users": [{"at": "`+time.Now().UTC().Format(time.RFC3339)+`", "text": "그냥 진행해"}]}`), 0o600)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("model called before the batch is due")
		return nil, nil
	}
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	job, err := readJob(path)
	if err != nil || job["last_error"] != "session_batch_wait" || job["attempts"] != nil || job["archive_retry_after"] == nil {
		t.Fatal(job, err, dir)
	}
}

// A pending code that will not clear by waiting spends attempts and parks, so the model is not re-called forever.
func TestSessionPendingFailureSpendsAttemptsAndParks(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	saved := processJob
	t.Cleanup(func() { processJob = saved })
	calls := 0
	processJob = func(map[string]any, func() error) error { calls++; return sessionWait("coverage_mismatch") }
	os.MkdirAll(spoolDir(), 0o700)
	path := filepath.Join(spoolDir(), "job.json")
	os.WriteFile(path, []byte(`{"session_id": "s1", "event_id": "e1", "queued_at": "`+time.Now().UTC().Format(time.RFC3339)+`", "users": []}`), 0o600)
	for i := 0; i < capMaxAttempts; i++ {
		if job, err := readJob(path); err == nil {
			delete(job, "archive_retry_after") // skip the one-minute backoff
			atomicWrite(path, dumps(job))
		}
		if err := drain(false); err != nil {
			t.Fatal(err)
		}
	}
	job, err := readJob(filepath.Join(failedDir(), "job.json"))
	if err != nil || exists(path) || calls != capMaxAttempts || !eq(job["attempts"], capMaxAttempts) || job["last_error"] != "coverage_mismatch" {
		t.Fatal(job, err, calls)
	}
}

func TestSessionSaveFailureReplaysPreparedResultAndCursorWaits(t *testing.T) {
	request, output := sessionFixture(t)
	capFake(t)
	fakeUsage(t, nil, errors.New("no reading"))
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	linked := sessionLinked
	t.Cleanup(func() { sessionLinked = linked })
	archiveRef := map[string]any{"archive_id": "a"}
	sessionLinked = func(map[string]any, string) ([]any, map[string][]any, error) {
		return request["records"].([]any), map[string][]any{"s1": {archiveRef}}, nil
	}
	calls := 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) { calls++; return output, nil }
	var captures []any
	failSave := true
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasSuffix(path, "/get"):
			return nil, &host.HTTPError{Code: 404}
		case strings.HasSuffix(path, "/authorize-transfer"):
			return map[string]any{"authorized": true, "content_hash": sha256Hex([]byte(str(body.(map[string]any)["content"])))}, nil
		case path == "/v1/capture":
			captures = append(captures, body)
			return map[string]any{"items": []any{map[string]any{"status": "candidate"}}}, nil
		case failSave:
			return nil, errors.New("timeout")
		}
		return map[string]any{"id": body.(map[string]any)["id"], "revision": 1}, nil
	}
	job := mustLoad(t, `{"users": [{"text": "한국어로 답해", "at": "2026-10-14T00:00:00Z"}], "type": "task_finished", "session_id": "s"}`)
	var saved []string
	persist := func() error { saved = append(saved, dumps(job)); return nil }
	if err := sessionProcess(job, persist, "codex"); err == nil || job["session_cursor"] != nil || !truthy(job["session_prepared"].(map[string]any)["capture_ack"]) {
		t.Fatal(err, job)
	}
	failSave = false
	if err := sessionProcess(job, persist, "codex"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(captures) != 1 || job["session_cursor"] != 1 || job["session_processor_complete"] != true {
		t.Fatal(calls, len(captures), job)
	}
	body := dumps(captures[0])
	if !strings.Contains(body, `"automated": true`) || !strings.Contains(body, `"semantic_kind": "preference"`) || !strings.Contains(saved[0]+saved[1], `"archive_messages": [{"archive_id": "a"}]`) {
		t.Fatal(body, saved)
	}
}

func TestSessionBatchMergeRetryPreservesEachSourceOnce(t *testing.T) {
	capFake(t)
	os.MkdirAll(spoolDir(), 0o700)
	child := filepath.Join(spoolDir(), "child.json")
	os.WriteFile(child, []byte(`{"event_id": "second", "session_id": "s", "users": [{"text": "b"}], "archive_context": {"path": "same"}, "source_start": 10, "source_end": 20}`), 0o600)
	leader := mustLoad(t, `{"event_id": "first", "session_id": "s", "users": [{"text": "a"}], "archive_context": {"path": "same"}, "source_start": 0, "source_end": 10}`)
	for range 2 {
		if err := sessionCoalesce(leader, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if job, _ := readJob(child); len(anyList(leader["users"])) != 2 || leader["source_end"] != int64(20) || job["session_batch_parent"] != "first" {
		t.Fatal(leader, job)
	}
	if err := sessionFinishChildren(leader); err != nil || exists(child) {
		t.Fatal(err)
	}
}

func TestSessionMissingArchiveBlocksModelCall(t *testing.T) {
	capFake(t)
	evPrepareUsers = func(_ any, users []any, _ string) ([]any, error) { return users, nil }
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("model called without archive evidence")
		return nil, nil
	}
	job := mustLoad(t, `{"users": [{"text": "한국어"}], "type": "task_finished", "session_id": "s"}`)
	if err := sessionProcess(job, func() error { return nil }, "codex"); !pending(err) || errorCode(err) != "session_archive_evidence_required" {
		t.Fatal(err)
	}
}
