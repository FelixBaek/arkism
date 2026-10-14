package capture

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The PC hook queue drained by `windows drain-work`.

func pcRetryFixture(t *testing.T) *pcHookFixture {
	f := newPcHookFixture(t)
	winFixture(t)
	winState = filepath.Join(f.root, "config")
	t.Setenv("ARKISM_SSH_HOST", "test-server")
	f.queue = winWorkRoot("codex")
	return f
}

// pcRetryRun answers each sent event with err, or with a matching receipt; it records
// what was sent.
func pcRetryRun(t *testing.T, sent *[][]byte, err error) func([]string, []byte, time.Duration) (int, []byte, error) {
	return func(_ []string, raw []byte, _ time.Duration) (int, []byte, error) {
		*sent = append(*sent, raw)
		if err != nil {
			return 0, nil, err
		}
		event, lerr := loadObjBytes(raw)
		if lerr != nil {
			t.Fatal(lerr)
		}
		ack := host.Obj{{Key: "accepted", Value: true}, {Key: "event_id", Value: event["event_id"]},
			{Key: "sequence", Value: event["sequence"]}, {Key: "sha256", Value: sha256Hex(raw)}}
		return 0, []byte(host.MustDumps(ack, host.DumpOpts{})), nil
	}
}

func pcRetryDrain(t *testing.T) host.Obj {
	t.Helper()
	result, err := winDrainWork("codex")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func pcRetryGet(o host.Obj, key string) any { v, _ := o.Get(key); return v }

func TestPcRetryPeriodicRetryRecoversUnpublishedIntentWithoutNewHook(t *testing.T) {
	f := pcRetryFixture(t)
	orig := outboxWrite
	outboxWrite = func(path string, data []byte) error {
		if filepath.Ext(path) == ".json" && filepath.Base(path)[0] == '0' {
			return errors.New("interrupted")
		}
		return orig(path, data)
	}
	err := f.record("work", nil, nil)
	outboxWrite = orig
	if err == nil {
		t.Fatal("no crash")
	}
	paths, _ := filepath.Glob(filepath.Join(f.queue, "session-*.json"))
	raw, _ := os.ReadFile(paths[0])
	intent, _ := loadObjBytes(raw)
	var sent [][]byte
	winRun = pcRetryRun(t, &sent, nil)
	result := pcRetryDrain(t)
	if !eq(pcRetryGet(result, "recovered"), 1) || !eq(pcRetryGet(result, "acknowledged"), 1) || pcRetryGet(result, "failed") != false {
		t.Fatal(result)
	}
	got, _ := loadObjBytes(sent[len(sent)-1])
	if !eq(got, intent["pending"]) || len(f.queued()) != 0 {
		t.Fatal(got, intent["pending"])
	}
}

func TestPcRetryFailurePreservesOrderAndOtherHostIsNeverUsed(t *testing.T) {
	f := pcRetryFixture(t)
	if err := f.record("work", nil, nil); err != nil {
		t.Fatal(err)
	}
	f.append()
	if err := f.record("work", nil, f.with("hook_event_name", "Stop")); err != nil {
		t.Fatal(err)
	}
	original := f.queued()
	var sent [][]byte
	winRun = pcRetryRun(t, &sent, errors.New("offline"))
	result := pcRetryDrain(t)
	if pcRetryGet(result, "failed") != true || !eq(pcRetryGet(result, "remaining"), 2) || len(sent) != 1 {
		t.Fatal(result, len(sent))
	}
	t.Setenv("ARKISM_SSH_HOST", "other-host")
	winRun = func([]string, []byte, time.Duration) (int, []byte, error) {
		t.Fatal("other host used")
		return 0, nil, nil
	}
	if result := pcRetryDrain(t); !eq(pcRetryGet(result, "remaining"), 0) {
		t.Fatal(result)
	}
	t.Setenv("ARKISM_SSH_HOST", "test-server")
	if !eq(f.queued(), original) {
		t.Fatal("queue changed")
	}
	winRun = pcRetryRun(t, &sent, nil)
	if result := pcRetryDrain(t); !eq(pcRetryGet(result, "acknowledged"), 2) {
		t.Fatal(result)
	}
}

func TestPcRetryExpiryRemovesSessionAndEventsButPreservesSequence(t *testing.T) {
	f := pcRetryFixture(t)
	if err := f.record("work", nil, nil); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(f.queue, "session-*.json"))
	raw, _ := os.ReadFile(paths[0])
	state, _ := loadObjBytes(raw)
	old := timeNow() - 8*86400
	state["updated_at"] = old
	os.WriteFile(paths[0], []byte(host.MustDumps(state, host.DumpOpts{})), 0o600)
	events := winWorkFiles(f.queue)
	raw, _ = os.ReadFile(events[0])
	event, _ := loadObjBytes(raw)
	event["created_at"] = old
	os.WriteFile(events[0], []byte(host.MustDumps(event, host.DumpOpts{})), 0o600)
	winRun = func([]string, []byte, time.Duration) (int, []byte, error) {
		t.Fatal("expired event sent")
		return 0, nil, nil
	}
	result := pcRetryDrain(t)
	if !eq(pcRetryGet(result, "expired_sessions"), 1) || !eq(pcRetryGet(result, "expired"), 1) || exists(paths[0]) {
		t.Fatal(result)
	}
	if err := f.record("work", nil, nil); err != nil {
		t.Fatal(err)
	}
	if q := f.queued(); len(q) != 1 || !eq(q[0]["sequence"], 2) {
		t.Fatal(q)
	}
}

func TestPcRetryCLIReportsFailedRetryWithNonzeroExit(t *testing.T) {
	f := pcRetryFixture(t)
	if err := f.record("work", nil, nil); err != nil {
		t.Fatal(err)
	}
	var sent [][]byte
	winRun = pcRetryRun(t, &sent, errors.New("offline"))
	var out strings.Builder
	if code := winMain([]string{"drain-work", "codex"}, strings.NewReader(""), &out, io.Discard); code != 1 {
		t.Fatal(code)
	}
	got := mustLoad(t, out.String())
	if got["failed"] != true || !eq(got["remaining"], 1) {
		t.Fatal(out.String())
	}
}
