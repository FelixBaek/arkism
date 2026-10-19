package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// processorCodexFlow is a PC Codex transcript synced to the server inbox and its event state.
type processorCodexFlow struct {
	*processorFixture
	source  string
	posts   []string
	session string
}

const processorCwd = `C:\Users\user\project`

func newProcessorCodexFlow(t *testing.T) *processorCodexFlow {
	t.Helper()
	keep := archivePC
	t.Cleanup(func() { archivePC = keep })
	archivePC = t.TempDir()
	t.Setenv("ARKISM_PC_LOCAL", "")
	f := &processorCodexFlow{processorFixture: newProcessorFixture(t, map[string]any{}), session: "s"}
	f.source = put(t, filepath.Join(archivePC, "codex-sessions", "day", "rollout.jsonl"),
		host.MustDumps(host.Obj{{Key: "type", Value: "session_meta"}, {Key: "payload", Value: host.Obj{{Key: "id", Value: "s"},
			{Key: "cwd", Value: processorCwd}, {Key: "source", Value: "cli"}}}}, host.DumpOpts{})+"\n")
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		f.posts = append(f.posts, path)
		switch path {
		case "/v1/session-archives/chunks":
			return map[string]any{"id": field(body, "sha256")}, nil
		case "/v1/session-archives/commit":
			return map[string]any{"state": "active"}, nil
		}
		return nil, &host.HTTPError{Code: 503, Body: []byte(`{"code": "unavailable"}`)}
	}
	return f
}

func (f *processorCodexFlow) marker() map[string]any {
	f.t.Helper()
	m, err := boundaryCapture(filepath.Join(archivePC, "codex-sessions"), "day/rollout.jsonl", "codex", f.session, processorCwd)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

// queue writes one event at the head of the queue and returns the entry as the drain reads it.
func (f *processorCodexFlow) queue(event, extra map[string]any) map[string]any {
	f.t.Helper()
	event["origin"], event["created_at"], event["session_id"] = "codex", timeNow(), f.session
	entry := map[string]any{"event": event, "sha256": "e1"}
	for k, v := range extra {
		entry[k] = v
	}
	f.write(map[string]any{"scope": map[string]any{}, "events": []any{entry}, "receipts": []any{}, "binding": nil})
	return f.load()["events"].([]any)[0].(map[string]any)
}

func TestProcessorBindChecksTheServerCopyThenStoresTheBinding(t *testing.T) {
	f := newProcessorCodexFlow(t)
	start := f.marker()
	entry := f.queue(map[string]any{"event_id": "ev-1", "action": "bind", "work_id": "work-1", "boundary": start}, map[string]any{"work_revision": 3})
	if err := processorProcess(f.path, entry, nil); err != nil {
		t.Fatal(err)
	}
	state := f.load()
	b := state["binding"].(map[string]any)
	if b["id"] != "work-1" || str(b["revision"]) != "3" || len(state["events"].([]any)) != 0 || len(f.posts) != 0 {
		t.Fatal(state, f.posts)
	}

	// a server copy that no longer starts with the marked prefix is refused
	os.WriteFile(f.source, []byte(strings.Repeat("x", 400)+"\n"), 0o600)
	entry = f.queue(map[string]any{"event_id": "ev-2", "action": "bind", "work_id": "work-1", "boundary": start}, nil)
	if err := processorProcess(f.path, entry, nil); err == nil {
		t.Fatal("rewritten copy bound")
	}
	if len(f.load()["events"].([]any)) != 1 { // the event stays queued
		t.Fatal(f.load())
	}
}

func TestProcessorStopUploadsTheNewDialogueAndKeepsTheJobWhenTheServerIsDown(t *testing.T) {
	f := newProcessorCodexFlow(t)
	start := f.marker()
	boundaryAppend(f.source, `{"type": "response_item", "payload": {"type": "message", "role": "user", "content": "이어서 해"}}`+"\n")
	end := f.marker()
	binding := map[string]any{"id": "work-1", "revision": int64(3), "boundary": start}
	entry := f.queue(map[string]any{"event_id": "ev-2", "action": "stop", "work_id": "work-1", "boundary": end}, nil)
	err := processorProcess(f.path, entry, binding)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal(err)
	}
	if f.posts[0] != "/v1/session-archives/chunks" || f.posts[1] != "/v1/session-archives/commit" {
		t.Fatal(f.posts)
	}
	job := f.load()["events"].([]any)[0].(map[string]any)["job"].(map[string]any)
	info, _ := os.Stat(f.source)
	if job["id"] != "work-1" || job["event_id"] != "ev-2" || str(job["expected_revision"]) != "3" ||
		job["archive_id"] != archiveIdentity("pc", "codex", "s", "day/rollout.jsonl") || str(job["start_byte"]) != str(start["offset"]) ||
		str(job["stop_byte"]) != str(info.Size()) {
		t.Fatal(job)
	}

	// once the server has answered, the receipt finishes the event without uploading again
	f.posts = nil
	job["receipt"] = map[string]any{"revision": 4}
	state := f.load()
	state["events"].([]any)[0].(map[string]any)["job"] = job
	f.write(state)
	entry = f.load()["events"].([]any)[0].(map[string]any)
	if err := processorProcess(f.path, entry, binding); err != nil {
		t.Fatal(err)
	}
	state = f.load()
	if b := state["binding"].(map[string]any); str(b["revision"]) != "4" || len(state["events"].([]any)) != 0 || len(f.posts) != 0 {
		t.Fatal(state, f.posts)
	}
}

func TestProcessorRefusesEventsItCannotTrust(t *testing.T) {
	f := newProcessorCodexFlow(t)
	start := f.marker()
	other := map[string]any{}
	for k, v := range start {
		other[k] = v
	}
	other["session_id"] = "other"
	moved := map[string]any{}
	for k, v := range start {
		moved[k] = v
	}
	moved["cwd"] = `C:\Users\user\elsewhere`
	earlier := map[string]any{}
	for k, v := range start {
		earlier[k] = v
	}
	earlier["offset"] = int64(1)
	binding := map[string]any{"id": "work-1", "revision": 3, "boundary": start}
	for _, c := range []struct {
		event   map[string]any
		extra   map[string]any
		binding any
		want    string
	}{
		{map[string]any{"event_id": "a", "action": "stop", "work_id": "work-1", "boundary": start}, map[string]any{"job": map[string]any{"owner_recovery": true}}, binding, "work_recovery_pending"},
		{map[string]any{"event_id": "b", "action": "stop", "work_id": "work-1", "boundary": start}, nil, nil, "work_pc_binding_missing"},
		{map[string]any{"event_id": "c", "action": "stop", "work_id": "work-2", "boundary": start}, nil, binding, "work_pc_binding_missing"},
		{map[string]any{"event_id": "d", "action": "stop", "work_id": "work-1", "boundary": other}, nil, binding, "work_pc_boundary_changed"},
		{map[string]any{"event_id": "e", "action": "stop", "work_id": "work-1", "boundary": moved}, nil, binding, "work_pc_boundary_changed"},
		{map[string]any{"event_id": "f", "action": "stop", "work_id": "work-1", "boundary": earlier}, nil, binding, "work_pc_boundary_changed"},
	} {
		entry := f.queue(c.event, c.extra)
		boundaryWantErr(t, processorProcess(f.path, entry, c.binding), c.want)
	}
	capAgentOff = func(origin string) bool { return origin == "codex" }
	entry := f.queue(map[string]any{"event_id": "g", "action": "suspend"}, nil)
	boundaryWantErr(t, processorProcess(f.path, entry, nil), "work_pc_agent_disabled")
	if len(f.posts) != 0 {
		t.Fatal(f.posts)
	}
}

func TestProcessorSourceRootNeedsExactlyOnePCFolder(t *testing.T) {
	t.Setenv("ARKISM_PC_LOCAL", "")
	if root, err := processorSourceRoot(map[string]any{"origin": "claude_code"}); err != nil || filepath.Base(root) != "claude-projects" {
		t.Fatal(root, err)
	}
	if _, err := processorSourceRoot(map[string]any{"origin": "grok"}); err == nil || err.Error() != "work_pc_root_invalid" {
		t.Fatal(err)
	}
}
