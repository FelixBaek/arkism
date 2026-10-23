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

type handoffFixture struct {
	t       *testing.T
	root    string
	source  string
	hook    map[string]any
	key     string
	state   string
	binding string
	pending string
	posts   []string
	metrics []string
}

func newHandoffFixture(t *testing.T) *handoffFixture {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	f := &handoffFixture{t: t, root: root, source: filepath.Join(root, "projects", "session.jsonl")}
	os.Mkdir(filepath.Dir(f.source), 0o700)
	os.WriteFile(f.source, []byte(handoffRow("이전 작업")), 0o600)
	f.hook = map[string]any{"session_id": "session", "cwd": root, "transcript_path": f.source}
	s, p, c, o, sc, m, off, post, z := capState, capProjects, capCodexSessions, capOrigin, capScope, metric, capAgentOff, capPost, capZcodeDB
	t.Cleanup(func() {
		capState, capProjects, capCodexSessions, capOrigin, capScope, metric, capAgentOff, capPost, capZcodeDB = s, p, c, o, sc, m, off, post, z
	})
	capState = filepath.Join(root, "state")
	capProjects = filepath.Dir(f.source)
	capCodexSessions = filepath.Join(root, "no-codex")
	capOrigin = "claude_code"
	capScope = func() map[string]any { return map[string]any{"workspace_id": "w", "project_id": "p"} }
	metric = func(fields ...host.KV) {
		f.metrics = append(f.metrics, host.MustDumps(host.Obj(fields), host.DumpOpts{}))
	}
	capAgentOff = func(string) bool { return false }
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		f.posts = append(f.posts, path)
		return nil, errors.New("offline")
	}
	t.Setenv("ARKISM_CLIENT", "server")
	t.Setenv("ARKISM_WORK_ID", "work")
	context, err := handoffContext(f.hook, "")
	if err != nil || context == nil {
		t.Fatal(context, err)
	}
	f.key = handoffKeyFor(context)
	f.state = filepath.Join(capState, "work-handoff")
	f.binding = filepath.Join(f.state, f.key+".binding.json")
	f.pending = filepath.Join(f.state, f.key+".pending.json")
	return f
}

func handoffRow(text string) string {
	return host.MustDumps(host.Obj{{Key: "sessionId", Value: "session"}, {Key: "type", Value: "user"},
		{Key: "message", Value: host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: text}}}}, host.DumpOpts{}) + "\n"
}

func handoffRendered(t *testing.T, workID string, revision int) string {
	t.Helper()
	s, err := host.RenderHookContext(map[string]any{"id": workID, "revision": revision, "package": map[string]any{"goal": "전체 목표"}}, 8000)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *handoffFixture) bind(workID string, revision int) {
	f.t.Helper()
	if err := handoffBind(f.hook, handoffRendered(f.t, workID, revision)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *handoffFixture) queue() {
	f.t.Helper()
	if err := handoffQueue(f.hook); err != nil {
		f.t.Fatal(err)
	}
}

func (f *handoffFixture) append(text string) {
	g, _ := os.OpenFile(f.source, os.O_APPEND|os.O_WRONLY, 0)
	g.WriteString(handoffRow(text))
	g.Close()
}

func (f *handoffFixture) load(path string) host.Obj {
	f.t.Helper()
	o, err := handoffLoad(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func (f *handoffFixture) write(path string, o host.Obj) {
	os.WriteFile(path, []byte(host.MustDumps(o, host.DumpOpts{})), 0o600)
}

func (f *handoffFixture) size() int64 {
	fi, _ := os.Stat(f.source)
	return fi.Size()
}

func (f *handoffFixture) cancelled() int {
	paths, _ := filepath.Glob(filepath.Join(f.state, "*.cancelled.json"))
	return len(paths)
}

func TestHandoffBindingBaselineAndSinglePendingDoNotRebase(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	bound := f.load(f.binding)
	raw, _ := os.ReadFile(f.source)
	if !eq(autosaveItem(bound, "offset"), f.size()) || autosaveItem(bound, "prefix_sha256") != sha256Hex(raw) {
		t.Fatal(bound)
	}
	f.append("새 대화")
	f.bind("work", 2)
	if !eq(autosaveItem(f.load(f.binding), "expected_revision"), 1) {
		t.Fatal("rebased onto a concurrent writer")
	}
	f.queue()
	first, _ := os.ReadFile(f.pending)
	f.append("후속 대화")
	f.queue()
	again, _ := os.ReadFile(f.pending)
	if string(again) != string(first) || strings.Contains(string(again), "새 대화") {
		t.Fatal(string(again))
	}
}

func TestHandoffSelectionChangeParksOldRequestAndBindsNewBaseline(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	old := f.load(f.pending)
	t.Setenv("ARKISM_WORK_ID", "other")
	f.bind("other", 7)
	if exists(f.pending) || f.cancelled() != 1 {
		t.Fatal("old request not parked")
	}
	f.append("새 작업 진행")
	f.queue()
	next, _ := os.ReadFile(f.pending)
	wzcodeWantErr(t, handoffProcess(f.state, f.key, &old), "selection_changed")
	if now, _ := os.ReadFile(f.pending); string(now) != string(next) {
		t.Fatal("new request changed")
	}
}

func TestHandoffChangedPrefixAndIncompleteLineNeverGenerate(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	job := f.load(f.pending)
	raw, _ := os.ReadFile(f.source)
	raw = append(raw, "partial"...)
	// Same metadata and session, but a rewritten prefix cannot be mistaken for new dialogue.
	os.WriteFile(f.source, []byte(strings.Replace(string(raw), `"type": "user"`, `"type": "used"`, 1)), 0o600)
	wzcodeWantErr(t, handoffProcess(f.state, f.key, &job), "transcript_changed")
	if len(f.posts) != 0 {
		t.Fatal(f.posts)
	}
	_, err := handoffPrefix(f.source, f.size())
	wzcodeWantErr(t, err, "pending")
	_, err = handoffPrefix(f.source, f.size()+1)
	wzcodeWantErr(t, err, "transcript_changed")
}

func TestHandoffCrashAfterBindingAdvanceRecoversWithoutDuplicateWrite(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	job := f.load(f.pending)
	raw, _ := os.ReadFile(f.source)
	job.Set("end_byte", f.size())
	job.Set("sha256", sha256Hex(raw))
	job.Set("receipt", host.Obj{{Key: "id", Value: "work"}, {Key: "revision", Value: 2}, {Key: "status", Value: "saved"}})
	f.write(f.pending, job)
	binding := f.load(f.binding)
	binding.Set("offset", f.size())
	binding.Set("expected_revision", 2)
	binding.Set("prefix_sha256", sha256Hex(raw))
	f.write(f.binding, binding)
	if err := handoffProcess(f.state, f.key, &job); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 0 || exists(f.pending) {
		t.Fatal(f.posts)
	}
}

func TestHandoffExpiredPendingIsRemovedWithoutRestartingRetention(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	job := f.load(f.pending)
	job.Set("created_at", timeNow()-8*86400)
	f.write(f.pending, job)
	handoffDrain(false)
	if len(f.posts) != 0 || exists(f.pending) {
		t.Fatal(f.posts)
	}
	if len(f.metrics) != 1 || f.metrics[0] != `{"event": "work_handoff_expired"}` {
		t.Fatal(f.metrics)
	}
}

func TestHandoffOtherRuntimeAndPcDoNotBindServerFile(t *testing.T) {
	f := newHandoffFixture(t)
	capAgentOff = func(string) bool { return true }
	f.bind("work", 1)
	capAgentOff = func(string) bool { return false }
	capOrigin = "codex"
	f.bind("work", 1)
	capOrigin = "claude_code"
	t.Setenv("ARKISM_CLIENT", "pc")
	f.bind("work", 1)
	if exists(f.binding) {
		t.Fatal("bound")
	}
}

func TestHandoffCodexExactSessionResolutionAndRuntimeBoundaries(t *testing.T) {
	f := newHandoffFixture(t)
	codex := filepath.Join(f.root, "codex")
	os.Mkdir(codex, 0o700)
	source := filepath.Join(codex, "rollout.jsonl")
	meta := func(k string, v any) host.Obj {
		m := host.Obj{{Key: "id", Value: "codex-session"}, {Key: "cwd", Value: f.root}, {Key: "source", Value: "cli"}}
		if k != "" {
			m.Set(k, v)
		}
		return m
	}
	write := func(m host.Obj) {
		os.WriteFile(source, []byte(host.MustDumps(host.Obj{{Key: "type", Value: "session_meta"}, {Key: "payload", Value: m}}, host.DumpOpts{})+"\n"), 0o600)
	}
	write(meta("", nil))
	event := map[string]any{"session_id": "codex-session", "cwd": f.root}
	capOrigin, capCodexSessions = "codex", codex
	value, err := handoffContext(event, "")
	if err != nil || autosaveItem(value, "path") != source {
		t.Fatal(value, err)
	}
	if err := handoffBind(event, handoffRendered(t, "work", 1)); err != nil {
		t.Fatal(err)
	}
	g, _ := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	g.WriteString(`{"type": "response_item", "payload": {"role": "user", "content": [{"type": "input_text", "text": "이어서 진행"}]}}` + "\n")
	g.Close()
	if err := handoffQueue(event); err != nil {
		t.Fatal(err)
	}
	pending, _ := filepath.Glob(filepath.Join(f.state, "*.pending.json"))
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	capOrigin = "claude_code"
	handoffDrain(false)
	if len(f.posts) != 0 || len(f.metrics) != 0 {
		t.Fatal("drained another origin", f.posts, f.metrics)
	}
	capOrigin = "codex"
	duplicate := filepath.Join(codex, "duplicate.jsonl")
	raw, _ := os.ReadFile(source)
	os.WriteFile(duplicate, raw, 0o600)
	if v, err := handoffContext(event, ""); v != nil || err != nil {
		t.Fatal("ambiguous session resolved", v, err)
	}
	os.Remove(duplicate)
	for _, c := range [][2]any{{"originator", "codex_exec"}, {"source", map[string]any{"subagent": "worker"}}, {"cwd", codex}} {
		write(meta(c[0].(string), c[1]))
		if v, err := handoffContext(event, ""); v != nil || err != nil {
			t.Fatal(c, v, err)
		}
	}
	write(meta("", nil))
	if v, err := handoffContext(map[string]any{"session_id": "wrong", "cwd": f.root}, ""); v != nil || err != nil {
		t.Fatal(v, err)
	}
}

func TestHandoffPeriodicDrainVisitsOtherOriginWithoutChangingDefault(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	capOrigin = "codex"
	handoffDrain(false)
	if len(f.posts) != 0 {
		t.Fatal("processed another origin by default")
	}
	handoffDrain(true)
	if len(f.posts) == 0 || !strings.HasPrefix(f.posts[0], "/v1/session-archives/") {
		t.Fatal("did not process the claude_code checkpoint", f.posts)
	}
	if job := f.load(f.pending); !eq(autosavePath(job, "context", "origin"), "claude_code") || !truthy(autosaveGet(job, "last_error")) {
		t.Fatal(job)
	}
}

func TestHandoffPeriodicDrainRespectsDisabledAgentButStillExpiresBody(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	capOrigin = "codex"
	capAgentOff = func(origin string) bool { return origin == "claude_code" }
	handoffDrain(true)
	job := f.load(f.pending)
	if len(f.posts) != 0 || autosaveGet(job, "last_error") != "work_selection_changed" {
		t.Fatal(f.posts, job)
	}
	job.Set("created_at", timeNow()-8*86400)
	f.write(f.pending, job)
	handoffDrain(true)
	if exists(f.pending) || len(f.posts) != 0 {
		t.Fatal("expired body kept")
	}
}

func TestHandoffClearThenReselectExcludesTheUnselectedInterval(t *testing.T) {
	f := newHandoffFixture(t)
	f.bind("work", 1)
	f.append("새 대화")
	f.queue()
	t.Setenv("ARKISM_WORK_ID", "")
	if err := handoffSuspend(f.hook); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_WORK_ID", "work")
	f.append("선택하지 않은 동안의 다른 작업")
	f.bind("work", 1)
	bound := f.load(f.binding)
	if truthy(autosaveGet(bound, "suspended")) || !eq(autosaveItem(bound, "offset"), f.size()) || exists(f.pending) {
		t.Fatal(bound)
	}
}

func TestHandoffFolderReselectionWithoutInterveningHookCreatesNewBaseline(t *testing.T) {
	f := newHandoffFixture(t)
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_WORK_SELECTION_STATE", filepath.Join(f.root, "selection"))
	if _, err := selectionSelect(f.root, "work"); err != nil {
		t.Fatal(err)
	}
	f.bind("work", 1)
	old := f.load(f.binding)
	if autosaveItem(old, "selection_mode") != "folder" || !handoffActive(old) {
		t.Fatal(old)
	}
	selectionClear(f.root)
	f.append("선택 해제 중 기록")
	time.Sleep(10 * time.Millisecond) // a new selection file must get a new stamp
	selectionSelect(f.root, "work")
	if handoffActive(old) {
		t.Fatal("old folder binding still active")
	}
	f.bind("work", 1)
	current := f.load(f.binding)
	if autosaveItem(current, "epoch") == autosaveItem(old, "epoch") || !eq(autosaveItem(current, "offset"), f.size()) {
		t.Fatal(current)
	}
}

// test_arkism_work_zcode: the first Zcode binding waits until the answer is complete.
func TestHandoffZcodeInitialBindingWaitsForCompletionWithoutConsumingPartialAnswer(t *testing.T) {
	f := newHandoffFixture(t)
	z := newWzcodeFixture(t, "")
	z.add("old", wzcodeUser("real_user"))
	z.add("partial", wzcodeAssistant(false))
	capOrigin, capZcodeDB = "zcode", z.path
	capScope = func() map[string]any { return map[string]any{} }
	event := map[string]any{"session_id": "s", "cwd": z.root}
	rendered := handoffRendered(t, "work", 1)
	wzcodeWantErr(t, handoffBind(event, rendered), "work_zcode_pending")
	if paths, _ := filepath.Glob(filepath.Join(f.state, "*.binding.json")); len(paths) != 0 {
		t.Fatal(paths)
	}
	z.setMessage("partial", wzcodeAssistant(true), host.DumpOpts{})
	if err := handoffBind(event, rendered); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(f.state, "*.binding.json"))
	if len(paths) != 1 || len(autosaveItem(f.load(paths[0]), "records").(host.Obj)) != 4 {
		t.Fatal(paths)
	}
	if err := handoffQueue(event); err != nil {
		t.Fatal(err)
	}
	if paths, _ := filepath.Glob(filepath.Join(f.state, "*.pending.json")); len(paths) != 0 {
		t.Fatal(paths)
	}
}

// The binding key over a non-ASCII context comes from Python.
func TestHandoffKeyMatchesPythonGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/handoff_key.json")
	if err != nil {
		t.Fatal(err)
	}
	want := loadsOrderedMust(raw)
	get := func(k string) any { v, _ := objGet(want, k); return v }
	keep := capScope
	t.Cleanup(func() { capScope = keep })
	scope := map[string]any{}
	for _, kv := range get("scope").(host.Obj) {
		scope[kv.Key] = kv.Value
	}
	capScope = func() map[string]any { return scope }
	if handoffKeyFor(get("context")) != get("key") {
		t.Fatal(handoffKeyFor(get("context")))
	}
}

func TestHandoffFileArchiveStopsWhenTheBoundPrefixNoLongerMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding := host.Obj{}
	binding.Set("offset", 3)
	binding.Set("prefix_sha256", "stale")
	at := func(string) any { t.Fatal("collector reached"); return nil }
	job := host.Obj{}
	if err := handoffFileArchive(&job, binding, evidenceClient{}, at, path, "sid"); err == nil || !strings.Contains(err.Error(), "work_transcript_changed") {
		t.Fatal(err)
	}
	if err := handoffFileArchive(&job, binding, evidenceClient{}, at, path+".gone", "sid"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(job) != 0 {
		t.Fatal(job)
	}
}
