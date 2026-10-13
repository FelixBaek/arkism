package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"
)

// Hooks start a detached `capture drain`; these tests keep drain.lock a folder so
// that drain exits at once, wait for it in /proc (hence Linux only), and run the
// drain themselves where the Python test called cap.drain().

func settleDrains(t *testing.T, state string) {
	t.Helper()
	settleProcesses(t, "ARKISM_CAPTURE_CHILD=1", "ARKISM_CAPTURE_STATE="+state)
}

// settleProcesses waits until no process has every one of the environment entries.
func settleProcesses(t *testing.T, entries ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		busy := false
		paths, _ := filepath.Glob("/proc/[0-9]*/environ")
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			found := 0
			for _, kv := range bytes.Split(raw, []byte{0}) {
				if slices.Contains(entries, string(kv)) {
					found++
				}
			}
			busy = busy || found == len(entries)
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("spawned process still running", entries)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func blockDrains(t *testing.T, state string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(state, "drain.lock"), 0700); err != nil {
		t.Fatal(err)
	}
}

// runDrain runs `arkism capture drain` once no hook-spawned drain is alive.
func runDrain(t *testing.T, env []string, state string, args ...string) {
	t.Helper()
	settleDrains(t, state)
	lock := filepath.Join(state, "drain.lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	arkismRun(t, env, "", append([]string{"capture", "drain"}, args...)...)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	blockDrains(t, state)
}

type handoffFile struct {
	path string
	data map[string]any
}

func handoffFiles(t *testing.T, state, suffix string) []handoffFile {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(state, "work-handoff", "*"+suffix))
	var out []handoffFile
	for _, path := range paths {
		var data map[string]any
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &data) != nil {
			t.Fatal("handoff file", path, err)
		}
		out = append(out, handoffFile{path, data})
	}
	return out
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
}

func claim(field string, index int, quote string) map[string]any {
	return map[string]any{"field": field, "message_index": index, "quote": quote}
}

// lossyServer serves the API; while lose is set it applies work saves and then drops
// the connection, so the client never sees the response.
func lossyServer(t *testing.T, f *fixture, before func(*http.Request)) (*httptest.Server, *atomic.Bool) {
	var lose atomic.Bool
	handler := Handler(f.svc)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/work-checkpoints/save" {
			if before != nil {
				before(r)
			}
			if lose.Load() {
				handler.ServeHTTP(httptest.NewRecorder(), r)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &lose
}

func TestPostgresReturningWorkWriterPreservesLatest(t *testing.T) {
	f, _ := archiveSearchFixture(t, isolatedPostgres(t))
	if _, err := workCheckpointSave(f, f.agent, "return-work", "initial", 0, workCheckpointPackage()); err != nil {
		t.Fatal(err)
	}
	server, lose := lossyServer(t, f, nil)
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte(f.human), 0600); err != nil {
		t.Fatal(err)
	}
	state, replies := filepath.Join(root, "capture"), filepath.Join(root, "claude")
	blockDrains(t, state)
	if err := os.MkdirAll(replies, 0700); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(root, ".claude", "projects")
	transcript := filepath.Join(projects, "return-session.jsonl")
	if err := os.MkdirAll(projects, 0700); err != nil {
		t.Fatal(err)
	}
	row := func(text string) string {
		raw, _ := json.Marshal(map[string]any{"sessionId": "return-session", "type": "user", "message": map[string]any{"role": "user", "content": text}})
		return string(raw) + "\n"
	}
	appendFile(t, transcript, row("선택 전 대화"))
	env := arkismEnv(root, server.URL, token, "ARKISM_ORIGIN=claude_code", "ARKISM_CLIENT=server", "ARKISM_WORK_ID=return-work",
		"ARKISM_CLAUDE_PROJECTS="+projects, "ARKISM_CAPTURE_CLAUDE="+filepath.Join(fakeTool(t, "claude"), "claude"), "ARKISM_TEST_CLAUDE_DIR="+replies)
	invoke := func(event string) {
		t.Helper()
		arkismHook(t, env, map[string]any{"hook_event_name": event, "session_id": "return-session", "cwd": root, "transcript_path": transcript})
	}
	read := func() WorkCheckpointView {
		t.Helper()
		view, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "return-work"})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	invoke("SessionStart")
	bindings := handoffFiles(t, state, ".binding.json")
	if len(bindings) != 1 || bindings[0].data["expected_revision"] != 1.0 {
		t.Fatalf("binding: %+v", bindings)
	}
	pkg := read().Package
	pkg.Decisions = append(pkg.Decisions, "다른 프로그램의 결정")
	if _, err := workCheckpointSave(f, f.human, "return-work", "other", 1, pkg); err != nil {
		t.Fatal(err)
	}
	appendFile(t, transcript, row("돌아와서 다음 검증을 진행한다."))
	invoke("UserPromptSubmit")
	if b := handoffFiles(t, state, ".binding.json"); b[0].data["expected_revision"] != 1.0 {
		t.Fatal("prompt moved the binding", b[0].data)
	}
	invoke("Stop")
	queueClaude(t, replies, []map[string]any{claim("next_steps", 0, "돌아와서 다음 검증을 진행한다.")})
	lose.Store(true)
	runDrain(t, env, state)
	lose.Store(false)
	pending := handoffFiles(t, state, ".pending.json")
	if len(pending) != 1 || pending[0].data["expected_revision"] != 1.0 {
		t.Fatalf("pending after lost response: %+v", pending)
	}
	if prepared, _ := pending[0].data["prepared"].(map[string]any); prepared == nil || prepared["expected_revision"] != 2.0 {
		t.Fatalf("prepared write not kept: %+v", pending[0].data)
	}
	latest := read()
	if latest.Revision != 3 || !sameJSON(latest.Package.Decisions, pkg.Decisions) || latest.Package.Goal != pkg.Goal || !sameJSON(latest.Package.Checks, pkg.Checks) ||
		latest.Package.NextSteps[len(latest.Package.NextSteps)-1] != "사용자 발언: 돌아와서 다음 검증을 진행한다." {
		t.Fatalf("returning write lost the newer work: %+v", latest)
	}
	pending[0].data["retry_after"] = 0
	writeJSONFile(t, pending[0].path, pending[0].data)
	runDrain(t, env, state)
	if left := handoffFiles(t, state, ".pending.json"); len(left) != 0 {
		t.Fatalf("receipt not recovered: %+v", left)
	}
	info, err := os.Stat(transcript)
	if err != nil {
		t.Fatal(err)
	}
	binding := handoffFiles(t, state, ".binding.json")[0].data
	if binding["expected_revision"] != 3.0 || binding["offset"] != float64(info.Size()) {
		t.Fatalf("binding after recovery: %+v", binding)
	}
	prompts := claudePrompts(t, replies)
	if len(prompts) != 1 || !strings.Contains(prompts[0], "다른 프로그램의 결정") || !strings.Contains(prompts[0], "돌아와서 다음 검증을 진행한다.") || strings.Contains(prompts[0], "선택 전 대화") {
		t.Fatalf("extraction calls: %q", prompts)
	}
	invoke("Stop")
	runDrain(t, env, state)
	if read().Revision != 3 || len(claudePrompts(t, replies)) != 1 {
		t.Fatal("idle stop wrote again")
	}
}

// zcodeWorkDB is a Zcode CLI store for work handoff: one session in dir, messages
// added one part each.
type zcodeWorkDB struct {
	t   *testing.T
	db  *sqlite.Conn
	sid string
}

func newZcodeWorkDB(t *testing.T, path, sid, dir string) *zcodeWorkDB {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	z := &zcodeWorkDB{t, db, sid}
	z.exec(`CREATE TABLE session(id TEXT, directory TEXT, parent_id TEXT, time_updated INTEGER);
		CREATE TABLE message(id TEXT, session_id TEXT, data TEXT);
		CREATE TABLE part(id TEXT, message_id TEXT, data TEXT)`)
	z.exec("INSERT INTO session VALUES (" + sqlQuote(sid) + ", " + sqlQuote(dir) + ", NULL, " + strconv.FormatInt(time.Now().UnixMilli(), 10) + ")")
	return z
}

func sqlQuote(v any) string {
	s, ok := v.(string)
	if !ok {
		raw, _ := json.Marshal(v)
		s = string(raw)
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (z *zcodeWorkDB) exec(sql string) {
	z.t.Helper()
	if err := z.db.Exec(sql); err != nil {
		z.t.Fatal(sql, err)
	}
}

// add appends a message; completed nil leaves the answer unfinished.
func (z *zcodeWorkDB) add(id, role, text, origin string, completed any) {
	z.t.Helper()
	z.exec("INSERT INTO message VALUES (" + sqlQuote(id) + ", " + sqlQuote(z.sid) + ", " + sqlQuote(map[string]any{"role": role, "semantics": map[string]string{"origin": origin}, "time": map[string]any{"completed": completed}}) + ")")
	z.exec("INSERT INTO part VALUES (" + sqlQuote(id+"-part") + ", " + sqlQuote(id) + ", " + sqlQuote(map[string]string{"type": "text", "text": text}) + ")")
	z.exec("UPDATE session SET time_updated=MAX(time_updated+1, " + strconv.FormatInt(time.Now().UnixMilli(), 10) + ") WHERE id=" + sqlQuote(z.sid))
}

func TestPostgresAutomaticWorkWriterExactRetryAndConflict(t *testing.T) {
	f, now := archiveSearchFixture(t, isolatedPostgres(t))
	pkg := workCheckpointPackage()
	if _, err := workCheckpointSave(f, f.agent, "auto-work", "initial", 0, pkg); err != nil {
		t.Fatal(err)
	}
	id := ContentHash("automatic-work-dialogue")
	raw := jsonLine(map[string]string{"role": "user", "content": "전체 목표를 유지하고 외부 게시 금지를 지켜줘."})
	raw = append(raw, jsonLine(map[string]string{"role": "assistant", "content": strings.Repeat("진행 기록 ", 16000) + "구현을 마쳤다. 남은 일은 실제 프로그램 검증이다."})...)
	header := archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	root := t.TempDir()
	token := filepath.Join(root, "token")
	setToken := func(value string) {
		t.Helper()
		if err := os.WriteFile(token, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	setToken(f.agent)
	state, replies := filepath.Join(root, "capture"), filepath.Join(root, "claude")
	blockDrains(t, state)
	if err := os.MkdirAll(replies, 0700); err != nil {
		t.Fatal(err)
	}
	read := func() WorkCheckpointView {
		t.Helper()
		view, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "auto-work"})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	last := func(list []string) string {
		if len(list) == 0 {
			return ""
		}
		return list[len(list)-1]
	}
	outbox := func(event string) map[string]any {
		t.Helper()
		paths, _ := filepath.Glob(filepath.Join(state, "work-autosave", "*.json"))
		for _, path := range paths {
			var job map[string]any
			if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &job) == nil && job["event_id"] == event {
				return job
			}
		}
		t.Fatal("no outbox job", event)
		return nil
	}
	// A save request can be checked against the outbox, preceded by another program's
	// save (a write during extraction), or lose its response.
	var checkOutbox, compete atomic.Bool
	server, lose := lossyServer(t, f, func(r *http.Request) {
		if checkOutbox.Load() {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if prepared := outbox("auto-turn")["prepared"]; !sameJSON(json.RawMessage(body), prepared) {
				t.Errorf("save is not the persisted prepared request: %s", body)
			}
		}
		if compete.CompareAndSwap(true, false) {
			concurrent := read().Package
			concurrent.Goal = "concurrent goal"
			if _, err := workCheckpointSave(f, f.agent, "auto-work", "concurrent", 3, concurrent); err != nil {
				t.Error(err)
			}
		}
	})
	env := arkismEnv(root, server.URL, token, "ARKISM_CLIENT=server", "ARKISM_CAPTURE_CLAUDE="+filepath.Join(fakeTool(t, "claude"), "claude"), "ARKISM_TEST_CLAUDE_DIR="+replies)
	autosave := func(event string, revision int) ([]byte, error) {
		t.Helper()
		return arkismExec(t, env, "", "work-autosave", "--id", "auto-work", "--event-id", event, "--expected-revision", strconv.Itoa(revision), "--archive-id", id, "--sha256", ContentHash(string(raw)))
	}

	// work-autosave: a lost save response is recovered from the outbox without a new extraction.
	queueClaude(t, replies, []map[string]any{claim("constraints", 0, "외부 게시 금지"), claim("completed", 1, "구현을 마쳤다."), claim("next_steps", 1, "남은 일은 실제 프로그램 검증이다.")})
	checkOutbox.Store(true)
	lose.Store(true)
	if out, err := autosave("auto-turn", 1); err == nil {
		t.Fatalf("lost response not reported: %s", out)
	}
	checkOutbox.Store(false)
	lose.Store(false)
	saved := read()
	if saved.Revision != 2 || len(saved.ArchiveMessages) != 2 || !sameJSON(saved.Package.Constraints, []string{"사용자 발언: 외부 게시 금지"}) || last(saved.Package.Completed) != "에이전트 보고: 구현을 마쳤다." {
		t.Fatalf("automatic write: %+v", saved)
	}
	newer := saved.Package
	newer.Goal = "newer program goal"
	if _, err := workCheckpointSave(f, f.agent, "auto-work", "manual", 2, newer); err != nil {
		t.Fatal(err)
	}
	out, err := autosave("auto-turn", 1)
	var receipt struct {
		Revision int64 `json:"revision"`
	}
	if err != nil || json.Unmarshal(out, &receipt) != nil || receipt.Revision != 2 || outbox("auto-turn")["prepared"] != nil {
		t.Fatalf("receipt recovery: %v %s", err, out)
	}
	if prompts := claudePrompts(t, replies); len(prompts) != 1 || !strings.Contains(prompts[0], "구현을 마쳤다. 남은 일은 실제 프로그램 검증이다.") {
		t.Fatalf("extraction calls: %d", len(prompts))
	}
	if latest := read(); latest.Revision != 3 || latest.Package.Goal != "newer program goal" {
		t.Fatalf("recovery overwrote the newer work: %+v", latest)
	}
	// A write during extraction is a CAS conflict, never a silent rebase.
	queueClaude(t, replies, []map[string]any{claim("decisions", 0, "전체 목표를 유지")})
	compete.Store(true)
	if out, err := autosave("conflict-turn", 3); err == nil {
		t.Fatalf("conflicting write succeeded: %s", out)
	}
	if prepared, _ := outbox("conflict-turn")["prepared"].(map[string]any); prepared == nil || prepared["expected_revision"] != 3.0 {
		t.Fatalf("conflict lost the prepared request: %v", outbox("conflict-turn"))
	}
	if latest := read(); latest.Revision != 4 || latest.Package.Goal != "concurrent goal" {
		t.Fatalf("conflict: %+v", latest)
	}

	// Actual Claude hook -> binding -> Stop -> existing drain -> archive -> work write.
	// Raw collection uses the configured server owner/service credential, never an agent upload grant.
	setToken(f.human)
	projects := filepath.Join(root, ".claude", "projects")
	codexRoot := filepath.Join(root, ".codex", "sessions")
	zdb := filepath.Join(root, "zcode.sqlite")
	transcript := filepath.Join(projects, "hook-session.jsonl")
	row := func(role, text string) string {
		var content any = text
		if role != "user" {
			content = []map[string]string{{"type": "text", "text": text}}
		}
		raw, _ := json.Marshal(map[string]any{"sessionId": "hook-session", "type": role, "message": map[string]any{"role": role, "content": content}})
		return string(raw) + "\n"
	}
	if err := os.MkdirAll(projects, 0700); err != nil {
		t.Fatal(err)
	}
	appendFile(t, transcript, row("user", "이전의 다른 작업 기록"))
	hookEnv := func(origin string) []string {
		return append(env[:len(env):len(env)], "ARKISM_ORIGIN="+origin, "ARKISM_WORK_ID=auto-work",
			"ARKISM_CLAUDE_PROJECTS="+projects, "ARKISM_CODEX_SESSIONS="+codexRoot, "ARKISM_ZCODE_DB="+zdb)
	}
	invoke := func(origin, event string) string {
		t.Helper()
		return arkismHook(t, hookEnv(origin), map[string]any{"hook_event_name": event, "session_id": "hook-session", "cwd": root, "transcript_path": transcript})
	}
	drain := func(origin string, args ...string) { t.Helper(); runDrain(t, hookEnv(origin), state, args...) }
	pendingErrors := func() []any {
		var errs []any
		for _, p := range handoffFiles(t, state, ".pending.json") {
			errs = append(errs, p.data["last_error"])
		}
		return errs
	}
	invoke("claude_code", "SessionStart")
	appendFile(t, transcript, row("user", "프로그램을 바꿔도 전체 목표를 유지해."), row("assistant", "다음은 실제 전환 검증이다."))
	invoke("claude_code", "Stop")
	queueClaude(t, replies, []map[string]any{claim("constraints", 0, "프로그램을 바꿔도 전체 목표를 유지"), claim("next_steps", 1, "다음은 실제 전환 검증이다.")})
	drain("zcode", "--all-work-origins")
	latest := read()
	if latest.Revision != 5 || last(latest.Package.Constraints) != "사용자 발언: 프로그램을 바꿔도 전체 목표를 유지" || len(latest.ArchiveMessages) != 4 {
		t.Fatalf("claude handoff: %+v %v", latest, pendingErrors())
	}
	if prompts := claudePrompts(t, replies); len(prompts) != 3 || strings.Contains(prompts[2], "이전의 다른 작업") || !strings.Contains(prompts[2], "concurrent goal") {
		t.Fatalf("claude handoff extraction: %q", prompts[len(prompts)-1])
	}
	if left := handoffFiles(t, state, ".pending.json"); len(left) != 0 {
		t.Fatalf("pending left: %v", pendingErrors())
	}
	invoke("claude_code", "Stop")
	drain("claude_code")
	if read().Revision != 5 {
		t.Fatal("idle stop wrote again")
	}
	if text := invoke("codex", "SessionStart"); !strings.Contains(text, "다음은 실제 전환 검증이다.") {
		t.Fatal("codex did not receive the handoff", text)
	}
	other := latest.Package
	other.Goal = "other program preserved"
	if _, err := workCheckpointSave(f, f.human, "auto-work", "other-program", 5, other); err != nil {
		t.Fatal(err)
	}
	appendFile(t, transcript, row("user", "추가 진행 기록"))
	invoke("claude_code", "Stop")
	queueClaude(t, replies, []map[string]any{claim("goal", 0, "추가 진행 기록")})
	drain("claude_code")
	if latest = read(); latest.Revision != 6 || latest.Package.Goal != "other program preserved" {
		t.Fatalf("stale handoff overwrote the work: %+v", latest)
	}
	if pending := handoffFiles(t, state, ".pending.json"); len(pending) != 1 || pending[0].data["expected_revision"] != 5.0 || pending[0].data["last_error"] != "work_revision_changed" {
		t.Fatalf("stale handoff: %v", pendingErrors())
	}

	// Codex takes over the same canonical work, then writes its own exact transcript.
	transcript = filepath.Join(codexRoot, "rollout.jsonl")
	if err := os.MkdirAll(codexRoot, 0700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "hook-session", "cwd": root, "source": "cli"}})
	appendFile(t, transcript, string(meta)+"\n")
	codexRow := func(role, text string) string {
		kind := "output_text"
		if role == "user" {
			kind = "input_text"
		}
		raw, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": role, "content": []map[string]string{{"type": kind, "text": text}}}})
		return string(raw) + "\n"
	}
	if text := invoke("codex", "SessionStart"); !strings.Contains(text, "프로그램을 바꿔도 전체 목표를 유지") {
		t.Fatal("codex session start", text)
	}
	appendFile(t, transcript, codexRow("user", "같은 작업을 계속해."), codexRow("assistant", "다음은 PC 연결을 검증한다."))
	invoke("codex", "Stop")
	queueClaude(t, replies, []map[string]any{claim("next_steps", 1, "다음은 PC 연결을 검증한다.")})
	drain("codex")
	if latest = read(); latest.Revision != 7 || last(latest.Package.NextSteps) != "에이전트 보고: 다음은 PC 연결을 검증한다." || len(latest.ArchiveMessages) != 5 {
		t.Fatalf("codex handoff: %+v %v", latest, pendingErrors())
	}
	prompts := claudePrompts(t, replies)
	if codex := prompts[len(prompts)-1]; !strings.Contains(codex, "other program preserved") || !strings.Contains(codex, "프로그램을 바꿔도 전체 목표를 유지") || !strings.Contains(codex, "같은 작업을 계속해.") {
		t.Fatalf("codex extraction: %q", codex)
	}
	remaining := handoffFiles(t, state, ".pending.json")
	if len(remaining) != 1 || remaining[0].data["context"].(map[string]any)["origin"] != "claude_code" {
		t.Fatalf("codex pending: %v", pendingErrors())
	}
	invoke("codex", "Stop")
	drain("codex")
	if read().Revision != 7 {
		t.Fatal("idle codex stop wrote again")
	}

	// Zcode continues the same work from its mutable SQLite source.
	z := newZcodeWorkDB(t, zdb, "hook-session", root)
	z.add("old", "user", "이전의 다른 작업 기록", "real_user", 1)
	if text := invoke("zcode", "SessionStart"); !strings.Contains(text, "다음은 PC 연결을 검증한다.") {
		t.Fatal("zcode session start", text)
	}
	z.add("runtime", "user", "자동 생성 목표 알림", "agent_runtime", 1)
	z.add("new", "user", "같은 전체 목표를 이어가.", "real_user", 1)
	z.add("answer", "assistant", "아직 작성 중", "real_user", nil)
	invoke("zcode", "Stop")
	queueClaude(t, replies, []map[string]any{claim("next_steps", 1, "이어서 삭제 전파를 검증한다.")})
	used := len(claudePrompts(t, replies))
	drain("zcode")
	var pending handoffFile
	for _, p := range handoffFiles(t, state, ".pending.json") {
		if p.data["context"].(map[string]any)["origin"] == "zcode" {
			pending = p
		}
	}
	if pending.data["last_error"] != "work_zcode_pending" || pending.data["prepared"] != nil || read().Revision != 7 {
		t.Fatalf("unfinished zcode answer was used: %v", pending.data)
	}
	eventID := pending.data["event_id"]
	var binding map[string]any
	if raw, err := os.ReadFile(strings.TrimSuffix(pending.path, ".pending.json") + ".binding.json"); err != nil || json.Unmarshal(raw, &binding) != nil {
		t.Fatal("zcode binding", err)
	}
	if records, _ := binding["records"].(map[string]any); binding["expected_revision"] != 7.0 || len(records) != 2 {
		t.Fatalf("zcode binding: %v", binding)
	}
	z.exec("UPDATE message SET data=" + sqlQuote(map[string]any{"role": "assistant", "time": map[string]any{"completed": 1}}) + " WHERE id='answer'")
	z.exec("UPDATE part SET data=" + sqlQuote(map[string]string{"type": "text", "text": "이어서 삭제 전파를 검증한다."}) + " WHERE id='answer-part'")
	pending.data["retry_after"] = 0
	writeJSONFile(t, pending.path, pending.data)
	invoke("zcode", "Stop")
	var again map[string]any
	if raw, err := os.ReadFile(pending.path); err != nil || json.Unmarshal(raw, &again) != nil || again["event_id"] != eventID {
		t.Fatal("stop replaced the waiting zcode event", err)
	}
	drain("zcode")
	if latest = read(); latest.Revision != 8 || latest.Package.Origin != "zcode" || last(latest.Package.NextSteps) != "에이전트 보고: 이어서 삭제 전파를 검증한다." || len(latest.ArchiveMessages) != 6 {
		t.Fatalf("zcode handoff: %+v %v", latest, pendingErrors())
	}
	prompts = claudePrompts(t, replies)
	if len(prompts) != used+1 || strings.Contains(prompts[used], "이전의 다른 작업") || strings.Contains(prompts[used], "자동 생성") || !strings.Contains(prompts[used], "같은 전체 목표를 이어가.") {
		t.Fatalf("zcode extraction: %d %q", used, prompts[len(prompts)-1])
	}
	invoke("zcode", "Stop")
	drain("zcode")
	if read().Revision != 8 {
		t.Fatal("idle zcode stop wrote again")
	}
	if remaining = handoffFiles(t, state, ".pending.json"); len(remaining) != 1 || remaining[0].data["context"].(map[string]any)["origin"] != "claude_code" {
		t.Fatalf("zcode pending: %v", pendingErrors())
	}

	// PC receipt -> delayed sync -> exact start/stop window -> real work write. The PC runs
	// `arkism windows hook`; its ssh reaches this server's hook and work-pc-receiver.
	pcHome, pcSync, serverRoot := filepath.Join(root, "pc"), filepath.Join(root, "pc-sessions"), filepath.Join(root, "server-state")
	serverEnv := append(env[:len(env):len(env)], "ARKISM_PC_SESSIONS="+pcSync)
	sshEnv := filepath.Join(root, "ssh-env.json")
	writeJSONFile(t, sshEnv, serverEnv)
	pcEnv := func(work string) []string {
		return arkismEnv(pcHome, "http://127.0.0.1:9", filepath.Join(pcHome, "no-token"), "ARKISM_WORK_ID="+work, "ARKISM_SSH_HOST=arkism-test",
			"PATH="+fakeTool(t, "ssh")+string(os.PathListSeparator)+os.Getenv("PATH"), "ARKISM_SERVER_BIN="+arkismBinary(t),
			"ARKISM_SERVER_STATE="+serverRoot, "ARKISM_TEST_SSH_ENV="+sshEnv)
	}
	pcState := filepath.Join(serverRoot, "arkism-capture-pc-codex")
	blockDrains(t, pcState)
	pcDrain := func() {
		t.Helper()
		runDrain(t, append(serverEnv[:len(serverEnv):len(serverEnv)], "ARKISM_CAPTURE_STATE="+pcState, "ARKISM_ORIGIN=codex", "ARKISM_CLIENT=pc"), pcState)
	}
	pcSource := filepath.Join(pcHome, ".codex", "sessions")
	pcFile := filepath.Join(pcSource, "rollout.jsonl")
	if err := os.MkdirAll(pcSource, 0700); err != nil {
		t.Fatal(err)
	}
	pcMeta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "pc-session", "cwd": "C:/repo", "source": "cli"}})
	appendFile(t, pcFile, string(pcMeta)+"\n", codexRow("user", "이전 PC 작업"))
	syncPC := func() {
		t.Helper()
		raw, err := os.ReadFile(pcFile)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Join(pcSync, "codex-sessions"), 0700); err == nil {
			err = os.WriteFile(filepath.Join(pcSync, "codex-sessions", "rollout.jsonl"), raw, 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	pcHook := func(event, work string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": "pc-session", "cwd": "C:/repo", "transcript_path": pcFile})
		arkismRun(t, pcEnv(work), string(payload), "windows", "hook", "codex")
		settleDrains(t, pcState)
	}
	inbox := func() (string, map[string]any) {
		t.Helper()
		paths, _ := filepath.Glob(filepath.Join(pcState, "pc-work-events", "*.json"))
		if len(paths) != 1 {
			t.Fatal("pc inbox files", paths)
		}
		var data map[string]any
		if raw, err := os.ReadFile(paths[0]); err != nil || json.Unmarshal(raw, &data) != nil {
			t.Fatal("pc inbox", err)
		}
		return paths[0], data
	}
	events := func(data map[string]any) []any { list, _ := data["events"].([]any); return list }
	pcHook("SessionStart", "auto-work")
	pcHook("SessionStart", "auto-work")
	inboxPath, data := inbox()
	if list := events(data); len(list) != 1 || list[0].(map[string]any)["work_revision"] != 8.0 {
		t.Fatalf("pc start events: %v", data)
	}
	pcDrain()
	if _, data = inbox(); events(data)[0].(map[string]any)["last_error"] != "work_pc_source_unavailable" {
		t.Fatalf("unsynced pc source: %v", data)
	}
	syncPC()
	events(data)[0].(map[string]any)["retry_after"] = 0
	writeJSONFile(t, inboxPath, data)
	pcDrain()
	if _, data = inbox(); data["binding"].(map[string]any)["revision"] != 8.0 {
		t.Fatalf("pc binding: %v", data)
	}
	appendFile(t, pcFile, codexRow("user", "같은 작업을 PC에서 계속해."), codexRow("assistant", "PC에서도 전체 목표를 이어간다."))
	pcHook("Stop", "auto-work")
	appendFile(t, pcFile, codexRow("user", "종료 뒤 다른 대화"))
	syncPC()
	var unsent []string
	filepath.WalkDir(pcHome, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), "-pc-work-") && strings.HasSuffix(d.Name(), ".json") {
			unsent = append(unsent, path)
		}
		return nil
	})
	if len(unsent) != 0 {
		t.Fatal("pc work event not delivered", unsent)
	}
	queueClaude(t, replies, []map[string]any{claim("next_steps", 1, "PC에서도 전체 목표를 이어간다.")})
	used = len(claudePrompts(t, replies))
	pcDrain()
	if latest = read(); latest.Revision != 9 || last(latest.Package.NextSteps) != "에이전트 보고: PC에서도 전체 목표를 이어간다." || len(latest.ArchiveMessages) != 7 {
		_, data = inbox()
		t.Fatalf("pc handoff: %+v %v", latest, data)
	}
	prompts = claudePrompts(t, replies)
	if len(prompts) != used+1 || strings.Contains(prompts[used], "이전 PC") || strings.Contains(prompts[used], "종료 뒤") || !strings.Contains(prompts[used], "이어서 삭제 전파를 검증한다.") {
		t.Fatalf("pc extraction: %q", prompts[len(prompts)-1])
	}
	if _, data = inbox(); len(events(data)) != 0 || data["binding"].(map[string]any)["boundary"] == nil {
		t.Fatalf("pc inbox after write: %v", data)
	}
	pcDrain()
	if read().Revision != 9 {
		t.Fatal("idle pc drain wrote again")
	}
	appendFile(t, pcFile, codexRow("user", "추가 PC 진행"))
	syncPC()
	pcHook("Stop", "auto-work")
	protected := latest.Package
	protected.Goal = "newer canonical goal protected"
	if _, err := workCheckpointSave(f, f.human, "auto-work", "pc-competing", 9, protected); err != nil {
		t.Fatal(err)
	}
	queueClaude(t, replies, []map[string]any{claim("goal", 1, "추가 PC 진행")})
	pcDrain()
	_, data = inbox()
	failed := events(data)[0]
	if failed.(map[string]any)["last_error"] != "work_revision_changed" {
		t.Fatalf("stale pc event: %v", data)
	}
	pcHook("SessionStart", "")
	pcDrain()
	if _, data = inbox(); len(events(data)) != 0 || data["binding"] != nil || !sameJSON(data["cancelled"], []any{failed}) {
		t.Fatalf("pc work release: %v", data)
	}
	if latest = read(); latest.Revision != 10 || latest.Package.Goal != "newer canonical goal protected" {
		t.Fatalf("pc release changed the work: %+v", latest)
	}

	// PC Zcode DB boundaries survive delayed snapshots and later dialogue. The hook's
	// own detached export writes to a private outbox; the test syncs explicitly.
	pcOutbox := filepath.Join(root, "pc-outbox")
	pcZDB := filepath.Join(pcHome, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(pcZDB), 0700); err != nil {
		t.Fatal(err)
	}
	pz := newZcodeWorkDB(t, pcZDB, "pc-zcode-session", "C:/repo")
	pz.add("old", "user", "이전 PC Zcode 대화", "real_user", 1)
	pcZState := filepath.Join(serverRoot, "arkism-capture-pc-zcode")
	blockDrains(t, pcZState)
	syncZcode := func() {
		t.Helper()
		arkismRun(t, pcEnv("auto-work"), "", "export-zcode", "--database", pcZDB, "--output", filepath.Join(pcSync, "zcode-sessions"), "--execute")
	}
	pcZHook := func(event string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": "pc-zcode-session", "cwd": "C:/repo"})
		arkismRun(t, append(pcEnv("auto-work"), "ARKISM_PC_SESSIONS_OUTBOX="+pcOutbox), string(payload), "windows", "hook", "zcode")
		settleProcesses(t, "ARKISM_PC_SESSIONS_OUTBOX="+pcOutbox)
		settleDrains(t, pcZState)
	}
	pcZDrain := func() {
		t.Helper()
		runDrain(t, append(serverEnv[:len(serverEnv):len(serverEnv)], "ARKISM_CAPTURE_STATE="+pcZState, "ARKISM_ORIGIN=zcode", "ARKISM_CLIENT=pc"), pcZState)
	}
	zinbox := func() map[string]any {
		t.Helper()
		paths, _ := filepath.Glob(filepath.Join(pcZState, "pc-work-events", "*.json"))
		var data map[string]any
		if len(paths) != 1 {
			t.Fatal("pc zcode inbox files", paths)
		} else if raw, err := os.ReadFile(paths[0]); err != nil || json.Unmarshal(raw, &data) != nil {
			t.Fatal("pc zcode inbox", err)
		}
		return data
	}
	pcZHook("SessionStart")
	syncZcode()
	pcZDrain()
	if data = zinbox(); data["binding"] == nil || data["binding"].(map[string]any)["revision"] != 10.0 {
		t.Fatalf("pc zcode binding: %v", data)
	}
	pz.add("new", "user", "Zcode에서도 같은 목표를 이어가.", "real_user", 1)
	pz.add("answer", "assistant", "다음은 실제 앱 연결 검증이다.", "real_user", 1)
	pz.add("runtime", "user", "내부 알림은 제외한다.", "agent_runtime", 1)
	pcZHook("Stop")
	pz.add("later", "user", "종료 뒤 다른 대화", "real_user", 1)
	syncZcode()
	queueClaude(t, replies, []map[string]any{claim("next_steps", 1, "다음은 실제 앱 연결 검증이다.")})
	used = len(claudePrompts(t, replies))
	pcZDrain()
	if latest = read(); latest.Revision != 11 || latest.Package.Goal != "newer canonical goal protected" ||
		last(latest.Package.NextSteps) != "에이전트 보고: 다음은 실제 앱 연결 검증이다." || len(latest.ArchiveMessages) != 8 {
		t.Fatalf("pc zcode handoff: %+v %v", latest, zinbox())
	}
	prompts = claudePrompts(t, replies)
	if len(prompts) != used+1 || strings.Contains(prompts[used], "이전 PC Zcode") || strings.Contains(prompts[used], "내부 알림") || strings.Contains(prompts[used], "종료 뒤") ||
		!strings.Contains(prompts[used], "Zcode에서도 같은 목표를 이어가.") || !strings.Contains(prompts[used], "newer canonical goal protected") {
		t.Fatalf("pc zcode extraction: %q", prompts[len(prompts)-1])
	}
	if len(events(zinbox())) != 0 {
		t.Fatalf("pc zcode inbox after write: %v", zinbox())
	}
	pcZDrain()
	if read().Revision != 11 {
		t.Fatal("idle pc zcode drain wrote again")
	}

	// Privacy deletion quarantines the work: the outbox job cannot write it again.
	if _, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "erase-auto", TargetType: "work_checkpoint", TargetIDs: []string{"auto-work"}, Reason: "owner_request"}); err != nil {
		t.Fatal(err)
	}
	setToken(f.agent)
	queueClaude(t, replies, []map[string]any{claim("decisions", 0, "전체 목표를 유지")})
	if out, err := autosave("conflict-turn", 3); err == nil {
		t.Fatalf("privacy quarantine bypassed: %s", out)
	}
	stored, err := f.svc.Repo.Get(testCtx, colSessionArchives, scopeA, id)
	if err != nil || !stored.ExpiresAt.Equal(*header.ExpiresAt) {
		t.Fatal("automatic work write renewed archive retention", err)
	}
}
