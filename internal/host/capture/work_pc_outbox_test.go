package capture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func outboxMarker() map[string]any {
	return map[string]any{"version": int64(1), "origin": "codex", "session_id": "s", "cwd": `C:\repo`, "logical": "day/session.jsonl",
		"offset": int64(200), "prefix_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
}

func outboxFiles(root string) []string {
	paths, _ := filepath.Glob(filepath.Join(root, "*-pc-work-*.json"))
	return paths
}

func outboxAck(raw []byte) (any, error) {
	job, err := loadObjBytes(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accepted": true, "event_id": job["event_id"], "sequence": job["sequence"], "sha256": sha256Hex(raw)}, nil
}

func outboxEnq(t *testing.T, root, action string) {
	t.Helper()
	if _, err := outboxEnqueue(root, "codex", "s", `C:\repo`, "work", action, outboxMarker()); err != nil {
		t.Fatal(err)
	}
}

func outboxReadAll(paths []string) []string {
	var out []string
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		out = append(out, string(b))
	}
	return out
}

func outboxSeq(t *testing.T, path string) int64 {
	raw, _ := os.ReadFile(path)
	m, _ := loadObjBytes(raw)
	n, _ := host.Int(m["sequence"])
	return n
}

func TestOutboxLostResponseRetriesIdenticalBytesWithoutOvertaking(t *testing.T) {
	root := t.TempDir()
	outboxEnq(t, root, "bind")
	outboxEnq(t, root, "stop")
	original := outboxReadAll(outboxFiles(root))
	var calls []string
	_, err := outboxDrain(root, func(raw []byte) (any, error) {
		calls = append(calls, string(raw))
		return nil, errors.New("lost response")
	}, 10)
	if err == nil || len(calls) != 1 || calls[0] != original[0] {
		t.Fatal(err, calls)
	}
	if got := outboxReadAll(outboxFiles(root)); len(got) != 2 || got[0] != original[0] || got[1] != original[1] {
		t.Fatal(got)
	}
	out, err := outboxDrain(root, func(raw []byte) (any, error) { calls = append(calls, string(raw)); return outboxAck(raw) }, 10)
	if v, _ := out.Get("acknowledged"); err != nil || v != 2 {
		t.Fatal(out, err)
	}
	if len(calls) != 3 || calls[1] != original[0] || calls[2] != original[1] || len(outboxFiles(root)) != 0 {
		t.Fatal(calls)
	}
	outboxEnq(t, root, "bind")
	if seq := outboxSeq(t, outboxFiles(root)[0]); seq != 3 {
		t.Fatal(seq)
	}
}

func TestOutboxWrongReceiptsKeepHeadAndNeverSendTail(t *testing.T) {
	root := t.TempDir()
	outboxEnq(t, root, "bind")
	outboxEnq(t, root, "stop")
	for _, c := range []host.KV{{Key: "accepted", Value: false}, {Key: "event_id", Value: "wrong"}, {Key: "sequence", Value: true}, {Key: "sequence", Value: int64(999)}, {Key: "sha256", Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}} {
		calls := 0
		_, err := outboxDrain(root, func(raw []byte) (any, error) {
			calls++
			ack, _ := outboxAck(raw)
			ack.(map[string]any)[c.Key] = c.Value
			return ack, nil
		}, 10)
		boundaryWantErr(t, err, "unacknowledged")
		if calls != 1 || len(outboxFiles(root)) != 2 {
			t.Fatal(c.Key, calls)
		}
	}
}

func TestOutboxCreatedTimeExpiryIsNotExtendedByRetryOrTouch(t *testing.T) {
	root := t.TempDir()
	orig := timeNow
	t.Cleanup(func() { timeNow = orig })
	timeNow = func() float64 { return orig() - 8*86400 }
	outboxEnq(t, root, "bind")
	timeNow = orig
	os.Chtimes(outboxFiles(root)[0], time.Now(), time.Now())
	outboxEnq(t, root, "stop")
	var seqs []int64
	out, err := outboxDrain(root, func(raw []byte) (any, error) {
		m, _ := loadObjBytes(raw)
		n, _ := host.Int(m["sequence"])
		seqs = append(seqs, n)
		return outboxAck(raw)
	}, 1)
	if err != nil || host.MustDumps(out, host.DumpOpts{}) != `{"acknowledged": 1, "expired": 1}` || len(seqs) != 1 || seqs[0] != 2 {
		t.Fatal(out, err, seqs)
	}
}

func TestOutboxReservationCrashLeavesGapNeverReusesSequence(t *testing.T) {
	root := t.TempDir()
	orig := outboxWrite
	t.Cleanup(func() { outboxWrite = orig })
	outboxWrite = func(path string, data []byte) error {
		if filepath.Base(path) != "sequence.json" {
			return errors.New("power loss")
		}
		return orig(path, data)
	}
	if _, err := outboxEnqueue(root, "codex", "s", `C:\repo`, "work", "bind", outboxMarker()); err == nil {
		t.Fatal("no crash")
	}
	outboxWrite = orig
	outboxEnq(t, root, "bind")
	if seq := outboxSeq(t, outboxFiles(root)[0]); seq != 2 {
		t.Fatal(seq)
	}
}

func TestOutboxOSLockAndPayloadValidation(t *testing.T) {
	root := t.TempDir()
	outboxEnq(t, root, "bind")
	outboxLocked(root, func(string) error {
		if _, err := outboxEnqueue(root, "codex", "s", `C:\repo`, "work", "bind", outboxMarker()); !errors.Is(err, errBusy) {
			t.Fatal(err)
		}
		if _, err := outboxDrain(root, outboxAck, 1); !errors.Is(err, errBusy) {
			t.Fatal(err)
		}
		return nil
	})
	_, err := outboxEnqueue(root, "codex", "other", `C:\repo`, "work", "bind", outboxMarker())
	boundaryWantErr(t, err, "event_invalid")
	m := outboxMarker()
	m["logical"] = "../outside.jsonl"
	_, err = outboxEnqueue(root, "codex", "s", `C:\repo`, "work", "bind", m)
	boundaryWantErr(t, err, "path_invalid")
	if _, err := outboxEnqueue(root, "codex", "s", `C:\repo`, "", "suspend", nil); err != nil {
		t.Fatal(err)
	}
	files := outboxFiles(root)
	raw, _ := os.ReadFile(files[len(files)-1])
	ev, _ := loadObjBytes(raw)
	if b, has := ev["boundary"]; !has || b != nil {
		t.Fatal(ev)
	}
}
