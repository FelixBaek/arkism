package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

func archiveTestSettings(t *testing.T) {
	t.Helper()
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(t.TempDir(), "capture.json"))
}

type fakeArchive struct {
	chunks  map[string][]byte
	commits []map[string]any
	mutate  func()
	t       *testing.T
}

func newFakeArchive(t *testing.T) *fakeArchive {
	return &fakeArchive{chunks: map[string][]byte{}, t: t}
}

func (f *fakeArchive) post(endpoint string, payload map[string]any) (map[string]any, error) {
	if endpoint == "chunks" {
		raw, _ := base64.StdEncoding.DecodeString(payload["data"].(string))
		f.chunks[payload["sha256"].(string)] = raw
		if f.mutate != nil {
			f.mutate()
			f.mutate = nil
		}
		return map[string]any{"id": payload["sha256"]}, nil
	}
	if endpoint != "commit" {
		f.t.Fatalf("endpoint %s", endpoint)
	}
	var raw []byte
	for _, k := range payload["chunks"].([]any) {
		raw = append(raw, f.chunks[k.(string)]...)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != payload["sha256"] {
		f.t.Fatal("sha mismatch")
	}
	f.commits = append(f.commits, payload)
	return map[string]any{"state": "active"}, nil
}

func codexRow(at float64, text string) string {
	return host.MustDumps(map[string]any{"timestamp": archiveISO(at), "type": "response_item",
		"payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}, host.DumpOpts{}) + "\n"
}

func TestArchiveCodexAppendUsesProducerTime(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	old := timeNow() - 7200
	newer := old + 3600
	os.WriteFile(path, []byte(codexRow(old, "첫 결정")), 0o644)
	touch(t, path, old)
	client := newFakeArchive(t)
	c := newCollector(client, nil, true)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.file(path, "pc", "codex", "session.jsonl", "session", -1))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(codexRow(newer, "다음 결정"))
	f.Close()
	touch(t, path, old)
	must(c.file(path, "pc", "codex", "session.jsonl", "session", -1))
	if len(client.commits) != 2 || client.commits[0]["observed_at"] != archiveISO(old) || client.commits[1]["observed_at"] != archiveISO(newer) {
		t.Fatalf("commits %v", client.commits)
	}
	touch(t, path, timeNow())
	must(c.file(path, "pc", "codex", "session.jsonl", "session", -1))
	last, prev := client.commits[len(client.commits)-1], client.commits[len(client.commits)-2]
	if last["observed_at"] != archiveISO(newer) || last["sha256"] != prev["sha256"] {
		t.Fatalf("touch only: %v", last)
	}
}

func TestArchiveCodexOldTimestampNotRejuvenatedAndFutureRejected(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	for _, c := range []struct {
		at      float64
		expired bool
	}{{timeNow() - 8*86400, true}, {timeNow() + 3600, false}} {
		os.WriteFile(path, []byte(host.MustDumps(map[string]any{"type": "event_msg", "timestamp": archiveISO(c.at),
			"payload": map[string]any{"type": "task_complete"}}, host.DumpOpts{})+"\n"), 0o644)
		client := newFakeArchive(t)
		col := newCollector(client, nil, true)
		err := col.file(path, "pc", "codex", "session.jsonl", "session", -1)
		if c.expired {
			if err != nil || col.counts.n["older_than_7d"] != 1 {
				t.Fatal(err, col.counts.n)
			}
		} else if err == nil || err.Error() != "future_time" {
			t.Fatal(err)
		}
		if len(client.chunks) != 0 || len(client.commits) != 0 {
			t.Fatal("uploaded")
		}
	}
}

func TestArchiveCodexTimestampIgnoresNestedInvalidAndOversized(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	old := timeNow() - 7200
	d := func(v any) string { return host.MustDumps(v, host.DumpOpts{}) + "\n" }
	body := d(map[string]any{"type": "response_item", "payload": map[string]any{"timestamp": archiveISO(timeNow() + 86400)}}) +
		d(map[string]any{"type": "unknown", "timestamp": archiveISO(timeNow() + 86400)}) +
		d(map[string]any{"type": "event_msg", "timestamp": "2026-10-05T12:00:00"}) +
		d(map[string]any{"type": "event_msg", "timestamp": "not a timestamp"}) +
		strings.Repeat("x", 2*archiveChunk+10) + "\n" +
		d(map[string]any{"type": "event_msg", "timestamp": archiveISO(old)}) +
		`{"timestamp":"2999-01-01T00:00:00Z","type":"event_msg"}`
	os.WriteFile(path, []byte(body), 0o644)
	if got, _ := codexObservedAt(path, 0); archiveISO(got) != archiveISO(old) {
		t.Fatalf("got %s", archiveISO(got))
	}
	os.WriteFile(path, []byte(`{"payload":{"timestamp":"2999-01-01T00:00:00Z"}}`+"\n"), 0o644)
	if got, _ := codexObservedAt(path, 123); got != 123 {
		t.Fatal(got)
	}
}

func TestArchiveCodexBOMHeaderKeepsSessionID(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "rollout-00000000-0000-0000-0000-000000000000.jsonl")
	os.WriteFile(path, []byte("\xef\xbb\xbf"+`{"type":"session_meta","payload":{"id":"real"}}`+"\n"), 0o644)
	if sid, _ := archiveSessionID(path, "codex"); sid != "real" {
		t.Fatal(sid)
	}
}

func TestArchiveAgentsViewSubagentsDoNotCollide(t *testing.T) {
	archiveTestSettings(t)
	// Imported metadata participates in capture policy, so it must use test-owned paths too.
	sourcePath := filepath.Join(t.TempDir(), "source.jsonl")
	dbpath := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sqlite.Open(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE sessions(id TEXT, agent TEXT, source_session_id TEXT, file_path TEXT, ended_at TEXT, file_mtime INTEGER, message_count INTEGER, deleted_at TEXT); CREATE TABLE messages(id INTEGER, session_id TEXT, ordinal INTEGER, role TEXT, content TEXT); CREATE TABLE tool_calls(id INTEGER, session_id TEXT, call_index INTEGER);`); err != nil {
		t.Fatal(err)
	}
	for _, i := range []string{"1", "2"} {
		if err := db.Exec("INSERT INTO sessions VALUES('" + i + "','claude','shared-parent','" + strings.ReplaceAll(sourcePath, "'", "''") + "','" + archiveISO(timeNow()) + "'," +
			str(int64(timeNow()*1e9)) + ",1,NULL)"); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO messages VALUES(" + i + ",'" + i + "',1,'user','child " + i + "')"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	client := newFakeArchive(t)
	c := newCollector(client, nil, true)
	if err := c.migrate(dbpath); err != nil {
		t.Fatal(err)
	}
	if len(client.commits) != 2 || client.commits[0]["id"] == client.commits[1]["id"] {
		t.Fatalf("commits=%v counts=%v", client.commits, c.counts.n)
	}
	for _, cm := range client.commits {
		if cm["session_id"] != "shared-parent" {
			t.Fatal(cm["session_id"])
		}
	}
}

func TestArchiveBytesPreservedAndProjectionExcludesThinkingTools(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	raw := []byte(`{"message": {"role": "assistant", "content": [{"type": "thinking", "thinking": "hidden thought"}, {"type": "text", "text": "visible answer"}, {"type": "tool_use", "input": "hidden tool"}]}}` + "\n")
	os.WriteFile(path, raw, 0o644)
	client := newFakeArchive(t)
	state := host.Obj{}
	c := newCollector(client, &state, true)
	if err := c.upload(path, "server", "claude_code", "sid", "stable/path", -1, "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	var all []byte
	for _, v := range client.chunks {
		all = append(all, v...)
	}
	if !bytes.Equal(all, raw) || client.commits[0]["text"] != "visible answer" {
		t.Fatalf("%q %v", all, client.commits[0]["text"])
	}
	c.upload(path, "server", "claude_code", "sid", "stable/path", -1, "jsonl", nil)
	if len(client.commits) != 1 || strings.Contains(host.MustDumps(state, host.DumpOpts{}), "visible answer") {
		t.Fatal("re-upload or text in state")
	}
}

func TestArchiveChangingFileIsNotCommitted(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte("initial"), 0o644)
	client := newFakeArchive(t)
	client.mutate = func() { os.WriteFile(path, []byte("changed during upload"), 0o644) }
	c := newCollector(client, nil, true)
	c.upload(path, "server", "codex", "sid", "stable", -1, "jsonl", nil)
	if len(client.commits) != 0 || c.counts.n["changed_during_read"] != 1 {
		t.Fatal(c.counts.n)
	}
}

func TestArchiveOldFileIsNotRejuvenated(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "old")
	os.WriteFile(path, []byte("old"), 0o644)
	touch(t, path, timeNow()-8*86400)
	client := newFakeArchive(t)
	c := newCollector(client, nil, true)
	c.upload(path, "server", "codex", "sid", "stable", -1, "jsonl", nil)
	if len(client.chunks) != 0 || c.counts.n["older_than_7d"] != 1 {
		t.Fatal(c.counts.n)
	}
}

func TestArchiveMultichunkRepeatedBytesPreservesOrder(t *testing.T) {
	archiveTestSettings(t)
	path := filepath.Join(t.TempDir(), "large")
	os.WriteFile(path, append(bytes.Repeat([]byte("a"), archiveChunk*2), "end"...), 0o644)
	client := newFakeArchive(t)
	if err := newCollector(client, nil, true).upload(path, "pc", "codex", "sid", "file", -1, "jsonl", nil); err != nil {
		t.Fatal(err)
	}
	chunks := client.commits[0]["chunks"].([]any)
	if len(chunks) != 3 || chunks[0] != chunks[1] {
		t.Fatal(chunks)
	}
}

func TestArchivePCIdentityDoesNotDependOnPhysicalRoot(t *testing.T) {
	archiveTestSettings(t)
	first, second := archiveIdentity("pc", "codex", "sid", "codex-sessions/2026/a.jsonl"), archiveIdentity("pc", "codex", "sid", "codex-sessions/2026/a.jsonl")
	if first != second {
		t.Fatal()
	}
	// Python golden: identity('pc','codex','sid','2026/a.jsonl')
	if got := archiveIdentity("server", "zcode", "s", "db-session"); got != sha256Hex([]byte("server\x00zcode\x00s\x00db-session")) {
		t.Fatal(got)
	}
}

func TestArchiveCounterKeepsFirstIncrementOrder(t *testing.T) {
	archiveTestSettings(t)
	c := newCounter()
	c.inc("planned")
	c.inc("failed")
	c.inc("planned")
	c.add("http_0", 1)
	if got := host.MustDumps(c.obj(), host.DumpOpts{}); got != `{"planned": 2, "failed": 1, "http_0": 1}` {
		t.Fatal(got)
	}
	if archiveISO(1767225600.1239) != "2026-01-01T00:00:00.123+00:00" {
		t.Fatal(archiveISO(1767225600.1239))
	}
}

// Like Path.rglob: a symlinked root is followed, a symlinked folder below it is not.
func TestArchiveGlobAllFollowsSymlinkedRootOnly(t *testing.T) {
	archiveTestSettings(t)
	dir := t.TempDir()
	realDir, root := filepath.Join(dir, "real"), filepath.Join(dir, "root")
	os.MkdirAll(filepath.Join(realDir, "a"), 0o700)
	os.WriteFile(filepath.Join(realDir, "a", "x.jsonl"), nil, 0o600)
	other := filepath.Join(dir, "other")
	os.MkdirAll(other, 0o700)
	os.WriteFile(filepath.Join(other, "y.jsonl"), nil, 0o600)
	if os.Symlink(realDir, root) != nil || os.Symlink(other, filepath.Join(realDir, "link")) != nil {
		t.Skip("no symlinks")
	}
	got := globAll(root, "*.jsonl")
	if len(got) != 1 || got[0] != filepath.Join(root, "a", "x.jsonl") {
		t.Fatal(got)
	}
}
