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

// A replaced transcript can still have the same logical context while its next
// read fails. A directory gives a real I/O failure on every supported platform,
// without relying on chmod, administrator privileges, or a mock privacy scanner.
func inspectionBatchFixture(t *testing.T) (parent, child string, restore func(), requests, models, saves *int) {
	t.Helper()
	parent, child, _ = sessionUnblockSpool(t, "unused")
	fakeUsage(t, nil, errors.New("synthetic usage unavailable"))
	root := t.TempDir()
	path := filepath.Join(root, "s1.jsonl")
	text := "한국어로 답해"
	message := map[string]any{"role": "user", "content": text}
	raw := []byte(dumps(map[string]any{"type": "user", "sessionId": "s1", "message": message}) + "\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	context := evidenceFileContext(path, root, "s1", "server", true, "claude_code")
	if context == nil {
		t.Fatal("missing fixture context")
	}
	hint := evidenceRowHint(context, raw, 0, message, "")
	job, err := readJob(parent)
	if err != nil {
		t.Fatal(err)
	}
	job["archive_context"], job["origin_product"] = context, "claude_code"
	job["users"] = []any{map[string]any{"text": text, "at": host.NowISO(), "archive_hint": hint}}
	if err := os.WriteFile(parent, []byte(dumps(job)), 0o600); err != nil {
		t.Fatal(err)
	}
	projects := capProjects
	t.Cleanup(func() { capProjects = projects })
	capProjects = root
	sessionLinked = sessionLinkedUsers
	requests, models, saves = new(int), new(int), new(int)
	capPost = func(route string, body any, _ time.Duration) (map[string]any, error) {
		*requests++
		payload := body.(map[string]any)
		switch route {
		case "/v1/session-archives/get":
			reply := map[string]any{"state": "active", "sha256": sha256Hex(raw)}
			for _, field := range context {
				reply[field.Key] = field.Value
			}
			return reply, nil
		case "/v1/session-archives/message":
			return map[string]any{"authority": "data_only_untrusted", "role": "user", "ref": payload["ref"], "text": text}, nil
		case "/v1/work-checkpoints/get":
			return nil, &host.HTTPError{Code: 404}
		case "/v1/session-archives/authorize-transfer":
			return map[string]any{"authorized": true, "content_hash": sha256Hex([]byte(str(payload["content"])))}, nil
		case "/v1/work-checkpoints/save":
			*saves++
			return map[string]any{"id": payload["id"], "revision": 1}, nil
		default:
			t.Fatal("unexpected fixture request", route)
			return nil, nil
		}
	}
	llm = func(_, content, _ string, _ time.Duration, _, _ string) (any, error) {
		*models++
		data := mustLoad(t, content)["data"].(map[string]any)
		row := data["records"].([]any)[0].(map[string]any)
		_, output := sessionFixture(t)
		output["memory_candidates"] = []any{}
		output["session_state"].(map[string]any)["source_refs"] = []any{map[string]any{
			"source_id": row["source_id"], "revision": row["revision"], "quote": row["text"],
		}}
		coverage := output["coverage"].(map[string]any)
		for _, key := range []string{"start", "end", "expected_revision", "schema_version", "prompt_version"} {
			coverage[key] = data[key]
		}
		coverage["source_ids"] = []any{row["source_id"]}
		return output, nil
	}
	held := path + ".held"
	if err := os.Rename(path, held); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	restore = func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(held, path); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestPrivateInspectionIOFailureRetainsBatchAndRecovers(t *testing.T) {
	parent, child, restore, requests, models, saves := inspectionBatchFixture(t)
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	job, err := readJob(parent)
	if err != nil || !exists(child) {
		t.Fatal("temporary inspection failure lost the batch", err)
	}
	if job["last_error"] != "archive_inspection_unavailable" || !eq(job["attempts"], 1) {
		t.Fatal("inspection failure must use bounded retry", job)
	}
	if !truthy(autosaveGet(anyList(job["users"])[0], "archive_hint")) || *requests != 0 || *models != 0 {
		t.Fatal("inspection failure changed evidence or sent data")
	}
	restore()
	job["archive_retry_after"] = 0
	if err := os.WriteFile(parent, []byte(dumps(job)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	if exists(parent) || exists(child) || *requests == 0 || *models != 1 || *saves != 1 {
		t.Fatal("restored evidence did not resume the original batch", *requests, *models, *saves)
	}
}

func TestPrivateInspectionIOFailureExhaustsRetriesWithoutLosingBatch(t *testing.T) {
	parent, child, _, requests, models, saves := inspectionBatchFixture(t)
	for range capMaxAttempts {
		job, err := readJob(parent)
		if err != nil {
			t.Fatal("batch disappeared before retry exhaustion", err)
		}
		job["archive_retry_after"] = 0
		if err := os.WriteFile(parent, []byte(dumps(job)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := drain(false); err != nil {
			t.Fatal(err)
		}
	}
	// A subsequent drain observes the parked parent and parks its child with it.
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	failed, err := readJob(filepath.Join(failedDir(), filepath.Base(parent)))
	if err != nil || failed["last_error"] != "archive_inspection_unavailable" || !eq(failed["attempts"], capMaxAttempts) {
		t.Fatal("retry exhaustion did not preserve the failed parent", failed, err)
	}
	if exists(parent) || exists(child) || !exists(filepath.Join(failedDir(), filepath.Base(child))) {
		t.Fatal("failed batch was deleted or separated")
	}
	if !truthy(autosaveGet(anyList(failed["users"])[0], "archive_hint")) || *requests != 0 || *models != 0 || *saves != 0 {
		t.Fatal("unreadable evidence changed or reached a remote boundary")
	}
}

func TestPrivateInspectionMarkerKeepsLegacyMemoryPath(t *testing.T) {
	capFake(t)
	root := t.TempDir()
	path, _, _ := evidenceTranscript(t, root)
	context := evidenceClaudeContext(path, root, "s", "server")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("<private>synthetic private marker</private>\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	users := []any{map[string]any{"text": "visible memory", "archive_hint": map[string]any{"synthetic": "hint"},
		"archive_ref": map[string]any{"archive_id": "stale"}, "archive_refs": []any{map[string]any{"archive_id": "stale"}}}}
	got, err := evidencePrepareUsers(context, users, root, capPost, capScope())
	if err != nil || len(got) != 1 || autosaveGet(got[0], "text") != "visible memory" ||
		truthy(autosaveGet(got[0], "archive_hint")) || truthy(autosaveGet(got[0], "archive_ref")) || truthy(autosaveGet(got[0], "archive_refs")) {
		t.Fatal("private transcript must keep the legacy redacted-memory path", err)
	}
}

func TestPrivateMarkedReadFailureIsNotMarker(t *testing.T) {
	if marked, err := privateMarked(t.TempDir()); marked || err == nil {
		t.Fatal("unreadable transcript was classified as a private marker", err)
	}
	if marked, err := scanPrivate(strings.NewReader("<private>synthetic marker</private>")); !marked || err != nil {
		t.Fatal("real marker lost its terminal archive opt-out", err)
	}
}

func inspectionMissingSourceJob(t *testing.T) map[string]any {
	t.Helper()
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "0")
	root := t.TempDir()
	keep := capProjects
	t.Cleanup(func() { capProjects = keep })
	capProjects = root
	path, raw, msg := evidenceTranscript(t, root)
	ctx := evidenceClaudeContext(path, root, "s", "server")
	if ctx == nil {
		t.Fatal("context fixture")
	}
	hint := evidenceRowHint(ctx, raw, 0, msg, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{"session_id": "s", "event_id": "synthetic-missing-source", "origin_product": "claude_code", "type": "task_finished", "archive_context": ctx,
		"users": []any{map[string]any{"text": "기억해: 항상 한국어로 답해", "at": host.NowISO(), "archive_hint": hint,
			"archive_ref": map[string]any{"archive_id": "stale"}, "archive_refs": []any{map[string]any{"archive_id": "stale"}}}}}
	return mustLoad(t, dumps(job))
}

func TestPrivateMissingSourceLegacyCapturesOnlySafeDialogue(t *testing.T) {
	for _, mode := range []string{"plain", "literal_private", "escaped_private"} {
		t.Run(mode, func(t *testing.T) {
			job := inspectionMissingSourceJob(t)
			users := job["users"].([]any)
			switch mode {
			case "literal_private":
				users = append(users, map[string]any{"text": "기억해: <private>synthetic-hidden</private> 공개 대화", "at": host.NowISO(), "archive_hint": map[string]any{"synthetic": "hint"}})
			case "escaped_private":
				users = append(users, map[string]any{"text": `기억해: \u003cprivate\u003esynthetic-hidden\u003c/private\u003e 공개 대화`, "at": host.NowISO(), "archive_hint": map[string]any{"synthetic": "hint"}})
			}
			job["users"] = users
			var originalTexts []string
			for _, user := range users {
				row := user.(map[string]any)
				row["archive_ref"] = map[string]any{"archive_id": "stale"}
				row["archive_refs"] = []any{map[string]any{"archive_id": "stale"}}
				originalTexts = append(originalTexts, str(row["text"]))
			}
			if mode != "plain" {
				job["archive_locator"] = map[string]any{"synthetic": "stale locator"}
			}
			requests, models, searches := 0, 0, 0
			capPost = func(string, any, time.Duration) (map[string]any, error) {
				requests++
				return nil, errors.New("unexpected request")
			}
			capGet = func(string, time.Duration) (map[string]any, error) {
				requests++
				return nil, errors.New("unexpected read")
			}
			search = func(query, _ string, _ int, _ float64) ([]any, bool, error) {
				searches++
				if privatePayload(query) || strings.Contains(query, "synthetic-hidden") {
					t.Error("private query reached search")
				}
				return nil, false, nil
			}
			llm = func(system, user, _ string, _ time.Duration, _, _ string) (any, error) {
				models++
				if privatePayload([]string{system, user}) || strings.Contains(user, "synthetic-hidden") || !strings.Contains(user, "한국어로 답해") {
					t.Error("unsafe or lost model context")
				}
				return []any{}, nil
			}
			err := process(job, func() error { return nil })
			if err != nil || models != 1 || searches != 1 || requests != 0 || job["captured"] != true {
				t.Errorf("missing source broke safe legacy capture: err=%v models=%d searches=%d requests=%d captured=%v", err, models, searches, requests, job["captured"])
			}
			if mode != "plain" && truthy(job["archive_locator"]) {
				t.Error("private dialogue retained a raw locator")
			}
			for index, user := range users {
				if autosaveGet(user, "text") != originalTexts[index] {
					t.Error("source dialogue was mutated")
				}
			}
			if mode == "literal_private" && autosaveGet(anyList(job["users"])[1], "text") != "기억해: "+privateMark+" 공개 대화" {
				t.Error("literal private span was not redacted precisely")
			}
			if mode == "escaped_private" && autosaveGet(anyList(job["users"])[1], "text") != privateMark {
				t.Error("encoded private value was not withheld")
			}
			for _, u := range anyList(job["users"]) {
				if truthy(autosaveGet(u, "archive_hint")) || truthy(autosaveGet(u, "archive_ref")) || truthy(autosaveGet(u, "archive_refs")) {
					t.Error("missing source retained archive evidence")
				}
			}
		})
	}
}

func TestPrivateMissingSourceStructuredStillRequiresEvidence(t *testing.T) {
	job := inspectionMissingSourceJob(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	capPost = func(string, any, time.Duration) (map[string]any, error) {
		t.Fatal("missing source caused archive/model authorization request")
		return nil, nil
	}
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("missing archive evidence reached structured model")
		return nil, nil
	}
	err := process(job, func() error { return nil })
	if !errors.Is(err, errSessionPending) || errorCode(err) != "session_archive_evidence_required" {
		t.Fatal("structured missing-source boundary changed", err)
	}
}

func TestPrivateMissingSourceRemainsAnInspectionError(t *testing.T) {
	marked, err := privateMarked(filepath.Join(t.TempDir(), "missing"))
	if marked || !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing file mislabeled as a user private marker", err)
	}
}
