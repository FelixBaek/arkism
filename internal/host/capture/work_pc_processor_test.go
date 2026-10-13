package capture

// The PC retry tests are in work_pc_retry_test.go: they drive the Windows relay's drain-work loop and CLI,
// which belong to the Windows client port.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type processorFixture struct {
	t     *testing.T
	path  string
	posts int
}

func newProcessorFixture(t *testing.T, state map[string]any) *processorFixture {
	t.Helper()
	root := t.TempDir()
	s, o, sc, p, m, off := capState, capOrigin, capScope, capPost, metric, capAgentOff
	t.Cleanup(func() { capState, capOrigin, capScope, capPost, metric, capAgentOff = s, o, sc, p, m, off })
	f := &processorFixture{t: t, path: filepath.Join(root, "pc-work-events", "state.json")}
	capState, capOrigin = root, "codex"
	capScope = func() map[string]any { return map[string]any{} }
	capPost = func(string, any, time.Duration) (map[string]any, error) { f.posts++; return nil, nil }
	metric = func(...host.KV) {}
	capAgentOff = func(string) bool { return false }
	t.Setenv("ARKISM_CLIENT", "pc")
	os.Mkdir(filepath.Dir(f.path), 0o700)
	f.write(state)
	return f
}

func (f *processorFixture) write(state any) {
	os.WriteFile(f.path, []byte(host.MustDumps(state, host.DumpOpts{})), 0o600)
}

func (f *processorFixture) load() map[string]any {
	f.t.Helper()
	state, err := processorLoad(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return state
}

func processorJSON(v any) string { return host.MustDumps(v, host.DumpOpts{SortKeys: true}) }

func TestProcessorDialogueStopBoundaryIsExclusiveAndNotSilentlyTruncated(t *testing.T) {
	job := map[string]any{"archive_id": strings.Repeat("a", 64), "sha256": strings.Repeat("b", 64), "start_byte": 10, "stop_byte": 30}
	items := []any{}
	for _, n := range []int{0, 10, 20, 30} {
		items = append(items, map[string]any{"ref": map[string]any{"archive_id": strings.Repeat("a", 64), "sha256": strings.Repeat("b", 64),
			"chunk_index": 0, "chunk_offset": n}, "role": "user", "text": strconv.Itoa(n), "text_offset": 0, "authority": "data_only_untrusted"})
	}
	request := func(string, any) (map[string]any, error) {
		return map[string]any{"items": items, "authority": "data_only_untrusted"}, nil
	}
	messages, _, err := autosaveDialogue(job, request, map[string]any{})
	if err != nil || len(messages) != 2 || autosaveItem(messages[0], "text") != "10" || autosaveItem(messages[1], "text") != "20" {
		t.Fatal(messages, err)
	}
	job["stop_byte"] = 5
	_, _, err = autosaveDialogue(job, request, map[string]any{})
	wzcodeWantErr(t, err, "offset_invalid")
}

func TestProcessorExpiredEventClearsBindingPreservesHighWaterAndNeverSends(t *testing.T) {
	old := timeNow() - 8*86400
	f := newProcessorFixture(t, map[string]any{"scope": map[string]any{}, "events": []any{map[string]any{"event": map[string]any{"created_at": old}}},
		"receipts": []any{map[string]any{"created_at": old}}, "high_sequence": 10, "binding": map[string]any{"id": "work"}})
	if err := processorDrain(false); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"scope": map[string]any{}, "events": []any{}, "receipts": []any{}, "high_sequence": 10, "binding": nil}
	if processorJSON(f.load()) != processorJSON(want) || f.posts != 0 {
		t.Fatal(f.load(), f.posts)
	}
}

func TestProcessorSuspendClearsOnlyBindingAndKeepsLaterEvents(t *testing.T) {
	entry := map[string]any{"event": map[string]any{"event_id": "first", "action": "suspend", "origin": "codex", "created_at": timeNow()}, "sha256": "a"}
	next := map[string]any{"event": map[string]any{"event_id": "next"}}
	f := newProcessorFixture(t, map[string]any{"scope": map[string]any{}, "events": []any{entry, next}, "receipts": []any{},
		"high_sequence": 2, "binding": map[string]any{"id": "work"}})
	entry = f.load()["events"].([]any)[0].(map[string]any)
	if err := processorProcess(f.path, entry, map[string]any{"id": "work"}); err != nil {
		t.Fatal(err)
	}
	state := f.load()
	if state["binding"] != nil || processorJSON(state["events"]) != processorJSON([]any{next}) || f.posts != 0 {
		t.Fatal(state, f.posts)
	}
}

func TestProcessorNewSuspendParksFailedRequestExactlyAndDoesNotSendIt(t *testing.T) {
	now := timeNow()
	failed := map[string]any{"event": map[string]any{"event_id": "old", "action": "stop", "origin": "codex", "created_at": now}, "sha256": "old",
		"job":        map[string]any{"prepared": map[string]any{"expected_revision": 2, "package": map[string]any{"goal": "old body"}}},
		"last_error": "work_revision_changed", "retry_after": timeNow() + 60}
	suspend := map[string]any{"event": map[string]any{"event_id": "suspend", "action": "suspend", "origin": "codex", "created_at": now}, "sha256": "new"}
	f := newProcessorFixture(t, map[string]any{"scope": map[string]any{}, "events": []any{failed, suspend}, "receipts": []any{},
		"high_sequence": 2, "binding": map[string]any{"id": "old"}})
	if err := processorDrain(false); err != nil {
		t.Fatal(err)
	}
	state := f.load()
	if processorJSON(state["cancelled"]) != processorJSON([]any{failed}) || len(state["events"].([]any)) != 0 || state["binding"] != nil || f.posts != 0 {
		t.Fatal(state, f.posts)
	}
	autosaveItem(state["cancelled"].([]any)[0], "event").(map[string]any)["created_at"] = timeNow() - 8*86400
	f.write(state)
	if err := processorDrain(false); err != nil {
		t.Fatal(err)
	}
	if len(f.load()["cancelled"].([]any)) != 0 {
		t.Fatal(f.load())
	}
}

func TestProcessorNewBindingKeepsItsOwnRevisionAndHealthyQueueKeepsOrder(t *testing.T) {
	failed := map[string]any{"event": map[string]any{"action": "stop"}, "last_error": "work_revision_changed",
		"job": map[string]any{"prepared": map[string]any{"expected_revision": 2}}}
	nextBind := map[string]any{"event": map[string]any{"action": "bind", "work_id": "new"}, "work_revision": 7}
	state := map[string]any{"events": []any{failed, nextBind}, "binding": map[string]any{"id": "old"}}
	if !processorAdvanceControl(state) || processorJSON(state["events"]) != processorJSON([]any{nextBind}) || state["binding"] != nil ||
		!eq(autosavePath(state["cancelled"].([]any)[0], "job", "prepared", "expected_revision"), 2) {
		t.Fatal(state)
	}
	healthy := map[string]any{"events": []any{map[string]any{"event": map[string]any{"action": "stop"}}, nextBind}, "binding": map[string]any{"id": "old"}}
	if processorAdvanceControl(healthy) || len(healthy["events"].([]any)) != 2 {
		t.Fatal(healthy)
	}
}
