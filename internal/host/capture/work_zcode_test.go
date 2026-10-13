package capture

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	sqlite "github.com/ncruces/go-sqlite3"

	"github.com/FelixBaek/arkism/internal/host"
)

// wzcodeFixture is a Zcode database with session "s" in directory dir.
type wzcodeFixture struct {
	t    *testing.T
	root string
	path string
	db   *sqlite.Conn
}

func wzcodeQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func newWzcodeFixture(t *testing.T, dir string) *wzcodeFixture {
	t.Helper()
	root := t.TempDir()
	if dir == "" {
		dir = root
	}
	f := &wzcodeFixture{t: t, root: root, path: root + "/db.sqlite"}
	db, err := sqlite.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f.db = db
	f.exec(`CREATE TABLE session(id TEXT, directory TEXT, parent_id TEXT, time_updated INTEGER);
		CREATE TABLE message(id TEXT, session_id TEXT, data TEXT);
		CREATE TABLE part(id TEXT, message_id TEXT, data TEXT);
		INSERT INTO session VALUES ('s', ` + wzcodeQuote(dir) + `, NULL, 1000)`)
	return f
}

func (f *wzcodeFixture) exec(q string) {
	f.t.Helper()
	if err := f.db.Exec(q); err != nil {
		f.t.Fatal(q, err)
	}
}

// add inserts message mid with data and one text part "mid-part".
func (f *wzcodeFixture) add(mid string, data any) {
	f.exec("INSERT INTO message VALUES (" + wzcodeQuote(mid) + ", 's', " + wzcodeQuote(host.MustDumps(data, host.DumpOpts{})) + ")")
	f.exec("INSERT INTO part VALUES (" + wzcodeQuote(mid+"-part") + ", " + wzcodeQuote(mid) + ", " +
		wzcodeQuote(host.MustDumps(host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: mid}}, host.DumpOpts{})) + ")")
}

func (f *wzcodeFixture) setMessage(mid string, data any, opts host.DumpOpts) {
	f.exec("UPDATE message SET data=" + wzcodeQuote(host.MustDumps(data, opts)) + " WHERE id=" + wzcodeQuote(mid))
}

func (f *wzcodeFixture) inspect(previous any) (host.Obj, []string, error) {
	var records host.Obj
	var eligible []string
	err := zcodeSnapshot(f.path, "s", func(snapshot string, _ float64, _ any) error {
		var err error
		records, eligible, err = wzcodeWindow(snapshot, "s", f.root, previous, false, false)
		return err
	})
	return records, eligible, err
}

func wzcodeUser(origin string) host.Obj {
	return host.Obj{{Key: "role", Value: "user"}, {Key: "semantics", Value: host.Obj{{Key: "origin", Value: origin}}}}
}

func wzcodeAssistant(completed bool) host.Obj {
	t := host.Obj{}
	if completed {
		t = host.Obj{{Key: "completed", Value: 1}}
	}
	return host.Obj{{Key: "role", Value: "assistant"}, {Key: "time", Value: t}}
}

func wzcodeWantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("error %v, want %q", err, sub)
	}
}

func TestWzcodeAppendIgnoresMetadataChangeAndOnlySelectsNewRealDialogue(t *testing.T) {
	f := newWzcodeFixture(t, "")
	f.add("old", wzcodeUser("real_user"))
	previous, _, err := f.inspect(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.exec("UPDATE session SET time_updated=2000")
	if got, keys, err := f.inspect(previous); err != nil || !eq(got, previous) || len(keys) != 0 {
		t.Fatal(got, keys, err)
	}
	f.add("runtime", wzcodeUser("agent_runtime"))
	f.add("partial", wzcodeAssistant(false))
	f.add("new", wzcodeUser("real_user"))
	f.add("answer", wzcodeAssistant(true))
	_, _, err = f.inspect(previous)
	wzcodeWantErr(t, err, "work_zcode_pending")
	f.setMessage("partial", wzcodeAssistant(true), host.DumpOpts{})
	current, keys, err := f.inspect(previous)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{wzcodePartKey("partial-part"), wzcodePartKey("new-part"), wzcodePartKey("answer-part")}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatal(keys)
	}
	for _, kv := range current {
		if len(kv.Key) != 64 || len(kv.Value.(string)) != 64 {
			t.Fatal(kv)
		}
	}
}

func TestWzcodeChangedOrDeletedHistoryIsNeverReinterpretedAsNew(t *testing.T) {
	f := newWzcodeFixture(t, "")
	f.add("old", wzcodeUser("real_user"))
	previous, _, _ := f.inspect(nil)
	f.exec(`UPDATE part SET data='{"type": "text", "text": "rewritten"}'`)
	_, _, err := f.inspect(previous)
	wzcodeWantErr(t, err, "history_changed")
	f.exec("DELETE FROM part")
	_, _, err = f.inspect(previous)
	wzcodeWantErr(t, err, "history_changed")
}

func TestWzcodeReorderingIsStableButDuplicateIdsAreRejected(t *testing.T) {
	f := newWzcodeFixture(t, "")
	f.add("old", wzcodeUser("real_user"))
	f.add("second", wzcodeAssistant(true))
	previous, _, _ := f.inspect(nil)
	f.exec("UPDATE part SET rowid=-rowid")
	if got, keys, err := f.inspect(previous); err != nil || !eq(got, previous) || len(keys) != 0 {
		t.Fatal(got, keys, err)
	}
	f.exec("INSERT INTO part SELECT * FROM part LIMIT 1")
	_, _, err := f.inspect(previous)
	wzcodeWantErr(t, err, "duplicate")
}

func TestWzcodeOtherFolderAndChildSessionAreRejected(t *testing.T) {
	f := newWzcodeFixture(t, "")
	f.add("old", wzcodeUser("real_user"))
	f.exec("UPDATE session SET directory=" + wzcodeQuote(f.root+"/.."))
	_, _, err := f.inspect(nil)
	wzcodeWantErr(t, err, "session_invalid")
	f.exec("UPDATE session SET directory=" + wzcodeQuote(f.root) + ", parent_id='parent'")
	_, _, err = f.inspect(nil)
	wzcodeWantErr(t, err, "session_invalid")
}

func TestWzcodeLimitAndInvalidRecords(t *testing.T) {
	f := newWzcodeFixture(t, "")
	f.add("old", wzcodeUser("real_user"))
	keep := wzcodeLimit
	t.Cleanup(func() { wzcodeLimit = keep })
	wzcodeLimit = 1
	_, _, err := f.inspect(nil)
	wzcodeWantErr(t, err, "work_zcode_too_large")
	wzcodeLimit = keep
	f.exec("INSERT INTO part VALUES ('', 'old', '{}')")
	_, _, err = f.inspect(nil)
	wzcodeWantErr(t, err, "work_zcode_record_invalid")
	// The export never yields an orphan part, so write that snapshot directly.
	orphan := f.root + "/orphan.jsonl"
	directory, _ := json.Marshal(f.root) // JSON-quoted: a Windows folder has backslashes
	os.WriteFile(orphan, []byte(`{"session": {"id": "s", "directory": `+string(directory)+`}}`+"\n"+
		`{"table": "part", "record": {"id": "p", "message_id": "missing", "data": "{}"}}`+"\n"), 0o600)
	_, _, err = wzcodeWindow(orphan, "s", f.root, nil, false, false)
	wzcodeWantErr(t, err, "work_zcode_message_missing")
}

// The snapshot, records, eligible keys and pre-final anchor digests come from Python.
func TestWzcodeWindowMatchesPythonGolden(t *testing.T) {
	var want struct {
		Records  map[string]string `json:"records"`
		Order    []string          `json:"order"`
		Eligible []string          `json:"eligible"`
		Previous map[string]string `json:"previous"`
		Later    []string          `json:"later_eligible"`
		Digest   string            `json:"digest"`
		Cwd      string            `json:"cwd"`
		PC       []string          `json:"pc_eligible"`
	}
	boundaryGolden(t, "wzcode_window.json", &want)
	snapshot := "testdata/wzcode_snapshot.jsonl"
	records, eligible, err := wzcodeWindow(snapshot, "s", want.Cwd, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, kv := range records {
		order = append(order, kv.Key)
		if want.Records[kv.Key] != kv.Value {
			t.Errorf("record %s = %v, want %s", kv.Key, kv.Value, want.Records[kv.Key])
		}
	}
	if strings.Join(order, ",") != strings.Join(want.Order, ",") || strings.Join(eligible, ",") != strings.Join(want.Eligible, ",") {
		t.Fatal(order, eligible)
	}
	if wzcodeDigest(records) != want.Digest {
		t.Fatal("digest", wzcodeDigest(records))
	}
	// previous holds the digests from before the anchors were finalized (both separator styles).
	prev := map[string]any{}
	for k, v := range want.Previous {
		prev[k] = v
	}
	_, later, err := wzcodeWindow(snapshot, "s", want.Cwd, prev, true, true)
	if err != nil || strings.Join(later, ",") != strings.Join(want.Later, ",") {
		t.Fatal(later, err)
	}
	marker := func(r any) map[string]any {
		return map[string]any{"version": int64(2), "origin": "zcode", "session_id": "s", "cwd": want.Cwd, "logical": "db-session", "records": r}
	}
	keys, err := pcZcodeEligible(snapshot, marker(prev), marker(records))
	if err != nil || strings.Join(keys, ",") != strings.Join(want.PC, ",") {
		t.Fatal(keys, err)
	}
}
