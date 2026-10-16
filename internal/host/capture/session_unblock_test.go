package capture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// serverEmail is the e-mail branch of internal/memory's sensitivePattern, which the client must never trip.
var serverEmail = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`)

func TestMaskEmailsClearsServerFilterAndKeepsOtherText(t *testing.T) {
	in := []any{
		map[string]any{"source_id": "archive-1", "revision": 1, "role": "user", "text": "커밋 작성자는 Felix.Baek+ark@atelier-felix.co.kr 로 해줘"},
		host.Obj{{Key: "role", Value: "tool"}, {Key: "text", Value: "Author: Seungyong Baek <me@atelierfelix.dev>\nDate: today"}},
		map[string]any{"role": "assistant", "text": "줄바꿈 뒤\n@atelierfelix.dev 와 탭\t@x.io"},
		map[string]any{"role": "assistant", "text": "@app.route('/v1') 와 user@localhost, npm @types/node 는 그대로"},
	}
	out := maskEmails(in).([]any)
	content := dumps(map[string]any{"authority": "data_only_untrusted", "data": map[string]any{"records": out}})
	if m := serverEmail.FindString(strings.ReplaceAll(content, `\"`, `"`)); m != "" {
		t.Fatalf("server filter still matches %q in %s", m, content)
	}
	if got := autosaveGet(out[0], "text"); got != "커밋 작성자는 [email] 로 해줘" {
		t.Fatal(got)
	}
	if got := autosaveGet(out[1], "text"); got != "Author: Seungyong Baek <[email]>\nDate: today" {
		t.Fatal(got)
	}
	if got := autosaveGet(out[3], "text"); got != "@app.route('/v1') 와 user@localhost, npm @types/node 는 그대로" {
		t.Fatal("text without an address changed:", got)
	}
	if !strings.Contains(autosaveGet(in[0], "text").(string), "@atelier-felix.co.kr") {
		t.Fatal("input records were mutated")
	}
}

func TestSessionPrepareSendsMaskedContentWithinServerLimit(t *testing.T) {
	request, output := sessionFixture(t)
	capFake(t)
	records := request["records"].([]any)
	records[0].(map[string]any)["text"] = "한국어로 답해, 연락처 me@atelierfelix.dev"
	output["session_state"].(map[string]any)["source_refs"] = []any{map[string]any{"source_id": "s1", "revision": 1, "quote": "한국어로 답해"}}
	var authorized, sent string
	llm = func(_, content, _ string, _ time.Duration, _, _ string) (any, error) {
		sent = content
		return output, nil
	}
	previous := map[string]any{"goal": "이전 요약 me@atelierfelix.dev"}
	if _, err := sessionPrepare(map[string]any{}, "s", records, 0, 1, 0, previous, nil, "", func(c string) error { authorized = c; return nil }); err != nil {
		t.Fatal(err)
	}
	if authorized == "" || authorized != sent || strings.Contains(sent, "@atelierfelix.dev") || !strings.Contains(sent, "연락처 [email]") {
		t.Fatal("authorized and model content must match and be masked:", sent)
	}
	if !strings.Contains(records[0].(map[string]any)["text"].(string), "me@atelierfelix.dev") || previous["goal"] != "이전 요약 me@atelierfelix.dev" {
		t.Fatal("caller data mutated")
	}

	// 64,001 bytes of content passes the old 100,000 client check but the server's safeTransfer refuses it.
	big := map[string]any{"source_id": "s1", "revision": 1, "role": "tool", "text": strings.Repeat("가", 21400)}
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("oversized content reached the model")
		return nil, nil
	}
	_, err := sessionPrepare(map[string]any{}, "s", []any{big}, 0, 1, 0, nil, nil, "", func(c string) error {
		t.Fatal("oversized content reached authorize-transfer:", len(c))
		return nil
	})
	if !pending(err) || errorCode(err) != "input_chunk_required" {
		t.Fatal(err)
	}
	// Just under the limit still goes out.
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return nil, sessionWait("structured_output_invalid")
	}
	big["text"] = strings.Repeat("가", 20000)
	if _, err := sessionPrepare(map[string]any{}, "s", []any{big}, 0, 1, 0, nil, nil, "", func(c string) error {
		if len(c) > sessionTransferMaxBytes {
			t.Fatal(len(c))
		}
		return nil
	}); errorCode(err) != "structured_output_invalid" {
		t.Fatal(err)
	}
}

func sessionUnblockSpool(t *testing.T, code string) (parent, child, other string) {
	t.Helper()
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	tok := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(tok, []byte("t"), 0o600)
	t.Setenv("ARKISM_HARNESS_TOKEN_FILE", tok)
	linked := sessionLinked
	t.Cleanup(func() { sessionLinked = linked })
	sessionLinked = func(job map[string]any, _ string) ([]any, map[string][]any, error) {
		if job["session_id"] == "s2" {
			return nil, nil, sessionWait("session_batch_wait")
		}
		return nil, nil, sessionWait(code)
	}
	os.MkdirAll(spoolDir(), 0o700)
	now := time.Now().UTC().Format(time.RFC3339)
	parent, child, other = filepath.Join(spoolDir(), "1-p.json"), filepath.Join(spoolDir(), "2-c.json"), filepath.Join(spoolDir(), "3-o.json")
	os.WriteFile(parent, []byte(`{"session_id": "s1", "event_id": "e1", "type": "task_finished", "origin_product": "`+capOrigin+`",
		"queued_at": "`+now+`", "users": [{"at": "`+now+`", "text": "응"}], "session_batch_children": ["2-c.json"]}`), 0o600)
	os.WriteFile(child, []byte(`{"session_id": "s1", "event_id": "e2", "type": "turn_completed", "origin_product": "`+capOrigin+`",
		"queued_at": "`+now+`", "users": [], "session_batch_parent": "e1"}`), 0o600)
	os.WriteFile(other, []byte(`{"session_id": "s2", "event_id": "e3", "type": "turn_completed", "origin_product": "`+capOrigin+`",
		"queued_at": "`+now+`", "users": [{"at": "`+now+`", "text": "그냥 진행해"}]}`), 0o600)
	return parent, child, other
}

func TestSessionPrivateTranscriptDropsBatch(t *testing.T) {
	parent, child, other := sessionUnblockSpool(t, "session_archive_private_skipped")
	var events []string
	metric = func(fields ...host.KV) {
		if v, ok := host.Obj(fields).Get("event"); ok {
			events = append(events, str(v))
		}
	}
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	if exists(parent) || exists(child) || exists(filepath.Join(failedDir(), "1-p.json")) || exists(filepath.Join(failedDir(), "2-c.json")) {
		t.Fatal("private batch must be deleted, not retried or parked")
	}
	if !strings.Contains(strings.Join(events, ","), "session_processor_dropped") {
		t.Fatal(events)
	}
	if job, err := readJob(other); err != nil || job["last_error"] != "session_batch_wait" {
		t.Fatal("unrelated session must keep waiting normally", job, err)
	}
}

func TestSessionRepeatInputDefectsParkBatch(t *testing.T) {
	for _, code := range []string{"session_archive_window_incomplete", "oversized_record_requires_artifact_reference", "oversized_tool_group_requires_reference"} {
		t.Run(code, func(t *testing.T) {
			parent, child, other := sessionUnblockSpool(t, code)
			if err := drain(false); err != nil {
				t.Fatal(err)
			}
			if exists(parent) || exists(child) || !exists(filepath.Join(failedDir(), "2-c.json")) {
				t.Fatal("batch not parked with its child")
			}
			if job, err := readJob(filepath.Join(failedDir(), "1-p.json")); err != nil || job["last_error"] != code {
				t.Fatal(job, err)
			}
			if job, err := readJob(other); err != nil || job["last_error"] != "session_batch_wait" {
				t.Fatal(job, err)
			}
		})
	}
	// A real wait keeps its free retry: the window still exists and may be committed later.
	parent, _, _ := sessionUnblockSpool(t, "session_archive_window_unavailable")
	if err := drain(false); err != nil {
		t.Fatal(err)
	}
	if job, err := readJob(parent); err != nil || job["last_error"] != "session_archive_window_unavailable" {
		t.Fatal(job, err)
	}
}

func TestSessionWindowOnPrivateTranscriptIsTerminal(t *testing.T) {
	capFake(t)
	root := t.TempDir()
	keep := capProjects
	t.Cleanup(func() { capProjects = keep })
	capProjects = root
	sid := "0f6c2a64-6f3b-4c55-9d7e-3f4a2b1c9e10"
	path := filepath.Join(root, "-home-felix-110-arkism", sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	write := func(text string) {
		row := dumps(map[string]any{"type": "user", "sessionId": sid, "message": map[string]any{"role": "user", "content": text}})
		os.WriteFile(path, []byte(row+"\n"), 0o600)
	}
	write("<private>개인 메모</private> 이번 배포 순서 정리해줘")
	ctx := evidenceFileContext(path, root, sid, "server", true, "claude_code")
	if ctx == nil {
		t.Fatal("fixture context")
	}
	capPost = func(p string, _ any, _ time.Duration) (map[string]any, error) {
		t.Fatal("private transcript reached the archive API:", p)
		return nil, nil
	}
	job := map[string]any{"session_id": sid, "users": []any{map[string]any{"text": "이번 배포 순서 정리해줘"}},
		"archive_context": ctx, "source_start": 0, "source_end": 120}
	_, _, err := sessionLinkedUsers(job, "claude_code")
	if !pending(err) || errorCode(err) != "session_archive_private_skipped" {
		t.Fatal(err)
	}
}
