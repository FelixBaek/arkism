package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

// exercises the real arkism capture hook, drain, exporter and collector
// against HTTP and PostgreSQL. Only extraction (the Claude CLI) is a stand-in.
func TestPostgresCaptureArchiveRoundtrip(t *testing.T) {
	testCaptureArchive(t, "claude_code", "server")
}
func TestPostgresPromptArchiveRoundtrip(t *testing.T) {
	for _, pair := range [][2]string{{"codex", "server"}, {"codex", "pc"}, {"claude_code", "pc"}, {"zcode", "server"}, {"zcode", "pc"}} {
		t.Run(pair[0]+"_"+pair[1], func(t *testing.T) { testCaptureArchive(t, pair[0], pair[1]) })
	}
}

// zcodeDatabase is the Zcode CLI store with sessions "s" and "other", each with a
// real user message and an agent runtime message split into two parts.
func zcodeDatabase(t *testing.T, path string) {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sql := []string{`CREATE TABLE session(id TEXT PRIMARY KEY, time_updated INTEGER);
		CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, data TEXT);
		CREATE TABLE part(id TEXT PRIMARY KEY, message_id TEXT, sequence INTEGER, data TEXT)`}
	quote := func(v any) string { raw, _ := json.Marshal(v); return "'" + string(raw) + "'" }
	for _, sid := range []string{"s", "other"} {
		sql = append(sql, "INSERT INTO session VALUES ('"+sid+"', "+strconv.FormatInt(time.Now().UnixMilli(), 10)+")")
		for _, origin := range []string{"real_user", "agent_runtime"} {
			mid := sid + origin
			sql = append(sql, "INSERT INTO message VALUES ('"+mid+"', '"+sid+"', "+quote(map[string]any{"role": "user", "semantics": map[string]string{"origin": origin}})+")")
			for _, part := range []struct {
				seq  int
				text string
			}{{1, "답은 짧게"}, {0, "앞으로"}} { // stored out of order on purpose
				sql = append(sql, "INSERT INTO part VALUES ('"+mid+strconv.Itoa(part.seq)+"', '"+mid+"', "+strconv.Itoa(part.seq)+", "+quote(map[string]string{"type": "text", "text": part.text})+")")
			}
		}
	}
	if err := db.Exec(strings.Join(sql, ";\n")); err != nil {
		t.Fatal(err)
	}
}

func testCaptureArchive(t *testing.T, origin, device string) {
	f, _ := archiveSearchFixture(t, isolatedPostgres(t))
	// The capture request is kept so the stored source can be compared with the
	// archive references the client sent (the Python test read them from the job).
	var sent struct {
		Claims []struct {
			ArchiveMessages []ArchiveMessageRef `json:"archive_messages"`
		} `json:"claims"`
	}
	handler := Handler(f.svc)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capture" {
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Error("capture body", err)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte(f.human), 0600); err != nil {
		t.Fatal(err)
	}
	replies := filepath.Join(root, "claude")
	if err := os.MkdirAll(replies, 0700); err != nil {
		t.Fatal(err)
	}
	pc := filepath.Join(root, "pc-sessions")
	zcode := filepath.Join(root, "zcode.sqlite")
	hookState, drainState := filepath.Join(root, "hook-capture"), filepath.Join(root, "capture")
	// Hooks queue into hookState, whose drain.lock is blocked, so the drain they spawn
	// exits at once; the test moves the queue to drainState and drains it itself.
	blockSpawnedDrain(t, root)
	if err := os.Rename(drainState, hookState); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(drainState, 0700); err != nil {
		t.Fatal(err)
	}
	env := arkismEnv(root, server.URL, token, "ARKISM_ORIGIN="+origin, "ARKISM_CLIENT="+device, "ARKISM_PC_SESSIONS="+pc, "ARKISM_ZCODE_DB="+zcode,
		"ARKISM_CAPTURE_CLAUDE="+filepath.Join(fakeTool(t, "claude"), "claude"), "ARKISM_TEST_CLAUDE_DIR="+replies,
		"PATH="+fakeTool(t, "ssh")+string(os.PathListSeparator)+os.Getenv("PATH"), "ARKISM_SERVER_BIN="+arkismBinary(t))
	hookEnv := append(env[:len(env):len(env)], "ARKISM_CAPTURE_STATE="+hookState)
	text := "앞으로 답은 짧게 해주세요"
	line := func(v any) string { raw, _ := json.Marshal(v); return string(raw) + "\n" }
	write := func(path, content string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	claude := line(map[string]any{"type": "user", "sessionId": "s", "timestamp": host.NowISO(), "message": map[string]any{"role": "user", "content": text}})
	codex := line(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "s"}}) +
		line(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}}})
	var source string // the original the capture must find
	switch origin + "/" + device {
	case "claude_code/server":
		source = write(filepath.Join(root, ".claude", "projects", "s.jsonl"), claude)
	case "claude_code/pc":
		source = write(filepath.Join(pc, "claude-projects", "s.jsonl"), claude)
	case "codex/server":
		source = write(filepath.Join(root, ".codex", "sessions", "s.jsonl"), codex)
	case "codex/pc":
		source = write(filepath.Join(pc, "codex-sessions", "s.jsonl"), codex)
	case "zcode/server", "zcode/pc":
		zcodeDatabase(t, zcode)
		text, source = "앞으로\n답은 짧게", zcode
		if device == "pc" {
			var counts map[string]int
			out := arkismRun(t, env, "", "export-zcode", "--database", zcode, "--output", filepath.Join(pc, "zcode-sessions"), "--execute")
			if json.Unmarshal(out, &counts) != nil || counts["exported"] != 2 {
				t.Fatalf("zcode export: %s", out)
			}
			source = filepath.Join(pc, "zcode-sessions", ContentHash("s")+".jsonl")
		}
	}
	reply, _ := json.Marshal(map[string]any{"is_error": false, "structured_output": map[string]any{"claims": []map[string]string{{"text": "사용자는 짧은 답변을 선호한다", "kind": "user", "quote": text}}}})
	write(filepath.Join(replies, "reply0.json"), string(reply))
	if device == "server" && origin == "claude_code" {
		arkismHook(t, hookEnv, map[string]any{"hook_event_name": "Stop", "session_id": "s", "transcript_path": source, "cwd": root})
	} else {
		// Prompt-time capture, as the PC relay and the Codex/Zcode hooks run it.
		arkismHook(t, append(hookEnv, "ARKISM_CAPTURE_ON_PROMPT=1"), map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": text, "cwd": root})
	}
	if err := os.Rename(filepath.Join(hookState, "spool"), filepath.Join(drainState, "spool")); err != nil {
		t.Fatal("hook queued nothing", err)
	}
	jobs, _ := filepath.Glob(filepath.Join(drainState, "spool", "*.json"))
	if len(jobs) != 1 {
		t.Fatal("queued jobs", jobs)
	}
	readJob := func() map[string]any {
		t.Helper()
		var job map[string]any
		raw, err := os.ReadFile(jobs[0])
		if err != nil || json.Unmarshal(raw, &job) != nil {
			t.Fatal("spool job", err, string(raw))
		}
		return job
	}
	drain := func() { t.Helper(); arkismRun(t, env, "", "capture", "drain") }
	if job := readJob(); job["archive_locator"] != nil {
		// A missing original is never silently accepted: the job waits, unprepared.
		delayed := source + ".pending"
		if err := os.Rename(source, delayed); err != nil {
			t.Fatal(err)
		}
		drain()
		job = readJob()
		if job["last_error"] != "archive_prompt_pending" || job["prepared_capture"] != nil {
			t.Fatalf("missing original accepted: %v", job)
		}
		if err := os.Rename(delayed, source); err != nil {
			t.Fatal(err)
		}
		delete(job, "archive_retry_after") // skip the one-minute backoff
		raw, _ := json.Marshal(job)
		write(jobs[0], string(raw))
	}
	drain()
	if _, err := os.Stat(jobs[0]); !os.IsNotExist(err) {
		t.Fatalf("capture not delivered: %v", readJob())
	}
	if _, err := os.Stat(filepath.Join(replies, "reply0.json.used")); err != nil {
		t.Fatal("extractor not called", err)
	}
	// The drain removes its job on success, so the receipt is read back from the server.
	var memoryID string
	if memories, err := f.svc.Repo.ListRecords(testCtx, scopeA, "memories", 10); err != nil {
		t.Fatal(err)
	} else if len(memories) == 1 {
		memoryID = memories[0].ID
	} else if candidates, err := f.svc.Repo.ListRecords(testCtx, scopeA, "memory_candidates", 10); err != nil || len(candidates) != 1 {
		t.Fatalf("capture produced %d memories and candidates %v %v", len(memories), candidates, err)
	} else {
		memory, err := f.svc.ReviewCandidate(testCtx, f.human, candidates[0].ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidates[0].Revision, Action: "approve"})
		if err != nil {
			t.Fatal(err)
		}
		memoryID = memory.ID
	}
	trace, err := f.svc.TraceMemorySources(testCtx, f.agent, scopeA, memoryID)
	if err != nil || len(trace.Sources) != 1 || len(trace.Sources[0].ArchiveMessages) == 0 {
		t.Fatalf("source lost the archive reference: %+v %v", trace, err)
	}
	refs := trace.Sources[0].ArchiveMessages
	if len(sent.Claims) != 1 || !reflect.DeepEqual(sent.Claims[0].ArchiveMessages, refs) {
		t.Fatalf("source lost the sent reference: %+v %+v", sent.Claims, refs)
	}
	before, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, refs[0].ArchiveID)
	if err != nil || before.ExpiresAt == nil {
		t.Fatal("missing archive expiry", err)
	}
	var texts []string
	for _, ref := range refs {
		if ref.MessageSHA256 == "" {
			t.Fatalf("reference without message hash: %+v", ref)
		}
		view, err := f.svc.ReadSessionArchiveMessage(testCtx, f.agent, ArchiveMessageInput{Scope: scopeA, Ref: ref})
		if err != nil {
			t.Fatal("original message lookup", err)
		}
		texts = append(texts, view.Text)
	}
	if strings.Join(texts, "\n") != text {
		t.Fatal("wrong source message", texts)
	}
	after, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, refs[0].ArchiveID)
	if err != nil || after.ExpiresAt == nil || !after.ExpiresAt.Equal(*before.ExpiresAt) {
		t.Fatal("source traversal extended retention", err)
	}
}
