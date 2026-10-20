package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

// reflectDirs points every agent log root at empty temp folders; the returned folder is the
// Claude projects root.
func reflectDirs(t *testing.T) string {
	t.Helper()
	p, c, z := capProjects, capCodexSessions, capZcodeDB
	t.Cleanup(func() { capProjects, capCodexSessions, capZcodeDB = p, c, z })
	capProjects, capCodexSessions, capZcodeDB = t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "none.sqlite")
	capAgentOff = func(string) bool { return false }
	return capProjects
}

func writeSession(t *testing.T, projects, sid string, rows ...string) string {
	t.Helper()
	dir := filepath.Join(projects, "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendRows(t *testing.T, path string, rows ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(rows, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func readReflectState(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(capState, "reflect.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}
	} else if err != nil {
		t.Fatal(err)
	}
	return mustLoad(t, string(raw))
}

const (
	userRow = `{"type": "user", "entrypoint": "cli", "message": {"content": "%s"}}`
	aiRow   = `{"type": "assistant", "message": {"content": [{"type": "text", "text": "%s"}]}}`
)

func row(format, text string) string { return strings.Replace(format, "%s", text, 1) }

// reflectServer answers the lesson listings and candidate reads reflectRun makes, and records posts.
func reflectServer(t *testing.T) *[]string {
	t.Helper()
	var posts []string
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/memories?"):
			return mustLoad(t, `{"items": [{"id": "m1", "status": "active", "text": "배포 전에 확인한다"}]}`), nil
		case strings.HasPrefix(path, "/v1/memory_candidates?") && strings.Contains(path, "kind=lesson"):
			return mustLoad(t, `{"items": [{"id": "c1", "status": "candidate"}]}`), nil
		case strings.HasPrefix(path, "/v1/memory_candidates?"):
			return map[string]any{"items": []any{}}, nil // aiReview: nothing waiting
		}
		return map[string]any{"revision": 1, "status": "candidate"}, nil
	}
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		posts = append(posts, path+" "+host.MustDumps(body, host.DumpOpts{NoASCII: true}))
		if path == "/v1/capture" {
			return mustLoad(t, `{"items": [{"status": "candidate", "reason": "review_required", "candidate_id": "n1"}]}`), nil
		}
		return map[string]any{}, nil
	}
	return &posts
}

func TestCaptureReflectRunReadsEachSessionOnceAndRemembersHowFar(t *testing.T) {
	capFake(t)
	projects := reflectDirs(t)
	posts := reflectServer(t)
	path := writeSession(t, projects, "s1", row(userRow, "테스트 안 돌렸잖아"), row(aiRow, "죄송합니다"), row(userRow, "다시 해"))
	// an old session never seen before is outside the window and is not read
	old := writeSession(t, projects, "s0", row(userRow, "옛날 일"), row(userRow, "또 옛날 일"))
	touch(t, old, timeNow()-reflectWindow-60)
	var prompts []string
	llm = func(_, user, model string, _ time.Duration, _, key string) (any, error) {
		prompts = append(prompts, user)
		if model != reflectModel || key != "claims" {
			t.Fatal(model, key)
		}
		return []any{map[string]any{"subject": "검증", "text": "완료 전에 테스트를 돌린다.", "quote": "테스트 안 돌렸잖아", "action": "new"}}, nil
	}
	if err := reflectRun(false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 {
		t.Fatal(prompts)
	}
	if !strings.Contains(prompts[0], "EXISTING LESSONS:\nA m1: 배포 전에 확인한다\nC c1: \n") ||
		!strings.HasSuffix(prompts[0], "SESSION LOG:\n사용자: 테스트 안 돌렸잖아\nAI: 죄송합니다\n사용자: 다시 해") {
		t.Fatal(prompts[0])
	}
	info, _ := os.Stat(path)
	state := readReflectState(t)
	if n, _ := num(state[path]); int64(n) != info.Size() || len(state) != 1 {
		t.Fatal(state)
	}
	// the user's objection waits for a person with its risk call; nothing is approved or put on trial
	var capture string
	for _, p := range *posts {
		if strings.Contains(p, "/review") {
			t.Fatal(p)
		}
		if strings.HasPrefix(p, "/v1/capture ") {
			capture = p
		}
	}
	if !strings.Contains(capture, `"claude-code:s1:reflect:`) || !strings.Contains(capture, `"risk": "medium"`) || len(trialState()) != 0 {
		t.Fatal(*posts, trialState())
	}

	// nothing new: no model call
	if err := reflectRun(false, &bytes.Buffer{}); err != nil || len(prompts) != 1 {
		t.Fatal(err, prompts)
	}
	// one more user line is too little: the offset stays where it was so the line is read again
	appendRows(t, path, row(userRow, "그리고"))
	if err := reflectRun(false, &bytes.Buffer{}); err != nil || len(prompts) != 1 {
		t.Fatal(err, prompts)
	}
	if n, _ := num(readReflectState(t)[path]); int64(n) != info.Size() {
		t.Fatal(readReflectState(t))
	}
	appendRows(t, path, row(userRow, "아직도 틀렸어"))
	if err := reflectRun(false, &bytes.Buffer{}); err != nil || len(prompts) != 2 {
		t.Fatal(err, prompts)
	}
	if !strings.HasSuffix(prompts[1], "SESSION LOG:\n사용자: 그리고\n사용자: 아직도 틀렸어") {
		t.Fatal(prompts[1])
	}
}

func TestCaptureReflectRunSkipsHeadlessAndKeepsGoingPastAFailure(t *testing.T) {
	capFake(t)
	projects := reflectDirs(t)
	reflectServer(t)
	headless := writeSession(t, projects, "a-headless", `{"type": "user", "entrypoint": "sdk-cli", "message": {"content": "너는 요약기다"}}`,
		`{"type": "user", "entrypoint": "sdk-cli", "message": {"content": "요약해"}}`)
	broken := writeSession(t, projects, "b-broken", row(userRow, "하나"), row(userRow, "둘"))
	young := writeSession(t, projects, "c-young", row(userRow, "처음"))
	var events []string
	metric = func(kvs ...host.KV) {
		for _, e := range kvs {
			if e.Key == "event" {
				events = append(events, str(e.Value))
			}
		}
	}
	calls := 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		calls++
		return nil, runtimeErr("extractor_exit_1")
	}
	if err := reflectRun(false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	state := readReflectState(t)
	info, _ := os.Stat(headless)
	if n, _ := num(state[headless]); int64(n) != info.Size() || calls != 1 {
		t.Fatal(state, calls)
	}
	if _, has := state[broken]; has { // the failed one is retried next run
		t.Fatal(state)
	}
	if n, has := state[young]; !has || n != json.Number("0") { // known now, read again when it grows
		t.Fatal(state)
	}
	found := false
	for _, e := range events {
		found = found || e == "reflection_failure"
	}
	if !found {
		t.Fatal(events)
	}
}

func TestCaptureReflectDryRunPrintsLessonsAndWritesNothing(t *testing.T) {
	capFake(t)
	projects := reflectDirs(t)
	posts := reflectServer(t)
	writeSession(t, projects, "s1", row(userRow, "또 그 경로 틀렸어"), row(userRow, "확인해"))
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return []any{map[string]any{"subject": "경로", "text": "경로를 먼저 확인한다.", "quote": "또 그 경로 틀렸어", "action": "replace", "target_id": "m1"},
			map[string]any{"subject": "추측", "text": "없는 인용.", "quote": "그런 말 없음", "action": "new"}}, nil
	}
	var out bytes.Buffer
	if err := reflectRun(true, &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	ls := got["lessons"].([]any)
	if got["origin"] != "claude_code" || got["session"] != "s1" || len(ls) != 1 {
		t.Fatal(got)
	}
	l := ls[0].(map[string]any)
	if l["action"] != "replace" || l["target"] != "m1" || l["from_user"] != true {
		t.Fatal(l)
	}
	if len(*posts) != 0 || len(readReflectState(t)) != 0 {
		t.Fatal(*posts, readReflectState(t))
	}
}

func TestCaptureReflectRunBusyLockAndBadStateAndServerErrors(t *testing.T) {
	capFake(t)
	reflectDirs(t)
	capGet = func(string, time.Duration) (map[string]any, error) { t.Fatal("read while locked"); return nil, nil }
	lock, err := lockFile(filepath.Join(capState, "reflect.lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := reflectRun(false, &bytes.Buffer{}); err != nil { // another run holds it: nothing to do
		t.Fatal(err)
	}
	lock.Close()

	os.WriteFile(filepath.Join(capState, "reflect.json"), []byte("{torn"), 0o600)
	if err := reflectRun(false, &bytes.Buffer{}); err == nil {
		t.Fatal("torn state accepted")
	}
	os.WriteFile(filepath.Join(capState, "reflect.json"), []byte("[]"), 0o600) // not an object: start over
	capGet = func(string, time.Duration) (map[string]any, error) { return nil, &host.HTTPError{Code: 500} }
	if err := reflectRun(false, &bytes.Buffer{}); host.HTTPCode(err) != 500 {
		t.Fatal(err)
	}
}

func TestCaptureChatsListsEveryAgentWhoseReflectionIsOn(t *testing.T) {
	capFake(t)
	projects := reflectDirs(t)
	claude := writeSession(t, projects, "cs", row(userRow, "안녕"))
	day := filepath.Join(capCodexSessions, "2026", "10", "15")
	os.MkdirAll(day, 0o700)
	codex := filepath.Join(day, "rollout-x.jsonl")
	os.WriteFile(codex, []byte(`{"type": "session_meta", "payload": {"originator": "codex-tui"}}`+"\n"), 0o600)
	capZcodeDB = filepath.Join(t.TempDir(), "z.sqlite")
	db, err := sqlite.Open(capZcodeDB)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Exec(`CREATE TABLE session (id, time_updated, task_type, parent_id); CREATE TABLE message (id, session_id, sequence, data);
		CREATE TABLE part (id, message_id, sequence, data);
		INSERT INTO session VALUES ('zs', 1760000000000, 'interactive', NULL), ('sub', 1, 'interactive', 'zs'), ('bg', 1, 'background', NULL);
		INSERT INTO message VALUES ('m0', 'zs', 0, '{"role": "user", "semantics": {"origin": "real_user"}}'), ('m1', 'zs', 4, '{"role": "user"}'),
			('s0', 'sub', 0, '{}'), ('b0', 'bg', 0, '{}');
		INSERT INTO part VALUES ('p0', 'm0', 0, '{"type": "text", "text": "이거 해줘"}');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	all, err := chats()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range all {
		keys = append(keys, c.origin+"="+c.key)
	}
	if !reflect.DeepEqual(keys, []string{"claude_code=" + claude, "codex=" + codex, "zcode=zcode:zs"}) {
		t.Fatal(keys)
	}
	z := all[2]
	if z.sid != "zs" || z.size != 5 || z.mtime != 1760000000 {
		t.Fatal(z)
	}
	if r, err := z.read(0); err != nil || !reflect.DeepEqual(r.lines, []string{"사용자: 이거 해줘"}) || r.end != 5 {
		t.Fatal(r, err)
	}
	if all[1].sid != "rollout-x" {
		t.Fatal(all[1])
	}
	capAgentOff = func(origin string) bool { return origin != "codex" }
	if all, err := chats(); err != nil || len(all) != 1 || all[0].origin != "codex" {
		t.Fatal(all, err)
	}
}

func TestCaptureTailKeepsTheLastWholeLinesThatFit(t *testing.T) {
	lines := []string{"aaaa", "bb", "c"}
	for limit, want := range map[int][]string{100: lines, 5: {"bb", "c"}, 4: {"c"}, 1: {}, 2: {"c"}} {
		if got := tail(lines, limit); !reflect.DeepEqual(append([]string{}, got...), want) {
			t.Fatal(limit, got)
		}
	}
}

func TestCaptureToolErrorJoinsTextBlocks(t *testing.T) {
	got := toolError(map[string]any{"content": []any{map[string]any{"text": " exit 1 "}, map[string]any{"type": "image"}, "skip"}})
	if got != "도구 실패: exit 1" {
		t.Fatalf("%q", got)
	}
	if got := toolError(map[string]any{"content": 3}); got != "도구 실패: " {
		t.Fatalf("%q", got)
	}
	if _, _, err := userLines(5); err == nil {
		t.Fatal("number content accepted")
	}
	if lines, n, err := userLines(nil); err != nil || lines != nil || n != 0 {
		t.Fatal(lines, n, err)
	}
}

// With the session processor on, reflect still asks the reviewer about waiting candidates (the review
// screen's AI advice came from nowhere else).
func TestCaptureReflectWithSessionProcessorClassifiesWaiting(t *testing.T) {
	capFake(t)
	reflectDirs(t)
	reflectServer(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	t.Setenv("ARKISM_HARNESS_TOKEN_FILE", filepath.Join(t.TempDir(), "harness.token"))
	t.Setenv("ARKISM_SHARED_AUDIENCE_SCOPE", "")
	keep := host.TokenFile
	t.Cleanup(func() { host.TokenFile = keep })
	get := capGet
	var listed []string
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.Contains(path, "status=candidate") || strings.Contains(path, "status=conflicted") {
			listed = append(listed, path)
		}
		return get(path, d)
	}
	var stderr bytes.Buffer
	if code := captureMain([]string{"reflect"}, nil, &bytes.Buffer{}, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if len(listed) != 2 {
		t.Fatal(listed)
	}
}
