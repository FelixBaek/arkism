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
	users := []any{map[string]any{"text": "visible memory", "archive_hint": map[string]any{"synthetic": "hint"}}}
	got, err := evidencePrepareUsers(context, users, root, capPost, capScope())
	if err != nil || len(got) != 1 || autosaveGet(got[0], "text") != "visible memory" || truthy(autosaveGet(got[0], "archive_hint")) {
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
