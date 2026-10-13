package capture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

type pcHookFixture struct {
	root, source, path, queue string
	event                     map[string]any
}

func newPcHookFixture(t *testing.T) *pcHookFixture {
	root := t.TempDir()
	f := &pcHookFixture{root: root, source: filepath.Join(root, "source"), queue: filepath.Join(root, "queue")}
	os.Mkdir(f.source, 0o755)
	f.path = filepath.Join(f.source, "rollout.jsonl")
	meta := host.Obj{{Key: "type", Value: "session_meta"}, {Key: "payload", Value: host.Obj{{Key: "id", Value: "s"}, {Key: "cwd", Value: root}, {Key: "source", Value: "cli"}}}}
	os.WriteFile(f.path, []byte(host.MustDumps(meta, host.DumpOpts{})+"\n"), 0o644)
	f.event = map[string]any{"hook_event_name": "SessionStart", "session_id": "s", "cwd": root, "transcript_path": f.path}
	real := pcHookRoots
	t.Cleanup(func() { pcHookRoots = real })
	pcHookRoots = map[string]string{"codex": f.source}
	return f
}

func (f *pcHookFixture) with(key string, value any) map[string]any {
	e := map[string]any{}
	for k, v := range f.event {
		e[k] = v
	}
	if value == nil {
		delete(e, key)
	} else {
		e[key] = value
	}
	return e
}

func (f *pcHookFixture) record(work string, stamp []any, event map[string]any) error {
	if stamp == nil {
		stamp = []any{"explicit", work}
	}
	if event == nil {
		event = f.event
	}
	return pcHookRecord(f.queue, "codex", event, work, stamp)
}

func (f *pcHookFixture) queued() []map[string]any {
	var out []map[string]any
	for _, p := range outboxFiles(f.queue) {
		raw, _ := os.ReadFile(p)
		m, _ := loadObjBytes(raw)
		out = append(out, m)
	}
	return out
}

func (f *pcHookFixture) actions() []string {
	var out []string
	for _, e := range f.queued() {
		out = append(out, e["action"].(string))
	}
	return out
}

func (f *pcHookFixture) append() { boundaryAppend(f.path, `{"role": "user", "content": "new"}`+"\n") }

func pcHookOffset(e map[string]any) int64 {
	n, _ := host.Int(e["boundary"].(map[string]any)["offset"])
	return n
}

func TestPcHookStartStopDuplicateAndSelectionTransitions(t *testing.T) {
	f := newPcHookFixture(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.record("work", nil, nil))
	must(f.record("work", nil, nil))
	if got := f.actions(); len(got) != 1 || got[0] != "bind" {
		t.Fatal(got)
	}
	f.append()
	must(f.record("work", nil, f.with("hook_event_name", "Stop")))
	must(f.record("work", nil, f.with("hook_event_name", "SessionEnd")))
	must(f.record("", nil, nil))
	must(f.record("other", nil, nil))
	must(f.record("other", []any{"folder", int64(1), int64(2)}, nil))
	want := []string{"bind", "stop", "suspend", "bind", "bind"}
	if got := f.actions(); host.MustDumps(got, host.DumpOpts{}) != host.MustDumps(want, host.DumpOpts{}) {
		t.Fatal(got)
	}
	q := f.queued()
	if pcHookOffset(q[0]) >= pcHookOffset(q[1]) {
		t.Fatal("stop offset did not grow")
	}
}

func TestPcHookCrashBeforePublishRestoresOriginalEventAndBoundary(t *testing.T) {
	f := newPcHookFixture(t)
	real := outboxWrite
	t.Cleanup(func() { outboxWrite = real })
	outboxWrite = func(path string, data []byte) error {
		if filepath.Ext(path) == ".json" && filepath.Base(path)[0] == '0' {
			return errors.New("interrupted")
		}
		return real(path, data)
	}
	if err := f.record("work", nil, nil); err == nil {
		t.Fatal("no crash")
	}
	outboxWrite = real
	paths, _ := filepath.Glob(filepath.Join(f.queue, "session-*.json"))
	raw, _ := os.ReadFile(paths[0])
	intent, _ := loadObjBytes(raw)
	pending := intent["pending"]
	f.append()
	if err := f.record("work", nil, nil); err != nil {
		t.Fatal(err)
	}
	q := f.queued()
	if len(q) != 1 || !eq(q[0], pending) {
		t.Fatal(q, pending)
	}
	size, _ := os.Stat(f.path)
	if pcHookOffset(q[0]) >= size.Size() {
		t.Fatal("pending boundary was recaptured")
	}
	raw, _ = os.ReadFile(paths[0])
	if state, _ := loadObjBytes(raw); state["pending"] != nil {
		t.Fatal(state)
	}
}

func TestPcHookFirstStopAndExpiredLocalBindingNeverGuessOldStart(t *testing.T) {
	f := newPcHookFixture(t)
	if err := f.record("work", nil, f.with("hook_event_name", "Stop")); err != nil {
		t.Fatal(err)
	}
	if got := f.actions(); got[0] != "bind" {
		t.Fatal(got)
	}
	paths, _ := filepath.Glob(filepath.Join(f.queue, "session-*.json"))
	raw, _ := os.ReadFile(paths[0])
	state, _ := loadObjBytes(raw)
	state["updated_at"] = timeNow() - 8*86400
	os.WriteFile(paths[0], []byte(host.MustDumps(state, host.DumpOpts{})), 0o644)
	f.append()
	if err := f.record("work", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.actions(); len(got) != 2 || got[1] != "bind" {
		t.Fatal(got)
	}
	size, _ := os.Stat(f.path)
	if q := f.queued(); pcHookOffset(q[1]) != size.Size() {
		t.Fatal(q[1])
	}
}

func TestPcHookPathlessExactSessionLookupAndAmbiguity(t *testing.T) {
	f := newPcHookFixture(t)
	event := f.with("transcript_path", nil)
	os.WriteFile(filepath.Join(f.source, "bad.jsonl"), []byte("[]\n"), 0o644)
	os.Symlink(f.path, filepath.Join(f.source, "alias.jsonl"))
	if err := f.record("work", nil, event); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f.path)
	os.WriteFile(filepath.Join(f.source, "duplicate.jsonl"), raw, 0o644)
	boundaryWantErr(t, f.record("other", nil, event), "ambiguous")
	if len(f.queued()) != 1 {
		t.Fatal(f.queued())
	}
}
