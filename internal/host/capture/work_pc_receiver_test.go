package capture

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type receiverFixture struct {
	root, client string
	scope        map[string]any
	work         map[string]any
	postErr      error
	raw          []byte
}

func newReceiverFixture(t *testing.T) *receiverFixture {
	f := &receiverFixture{root: t.TempDir(), scope: map[string]any{"workspace_id": "w", "project_id": "p"}}
	f.client = filepath.Join(f.root, "client")
	f.work = map[string]any{"id": "work", "revision": int64(5), "authority": "data_only_untrusted", "package": map[string]any{"goal": "PRIVATE BODY"}}
	state, origin, scope, post := capState, capOrigin, capScope, capPost
	t.Cleanup(func() { capState, capOrigin, capScope, capPost = state, origin, scope, post })
	capState, capOrigin = filepath.Join(f.root, "server"), "codex"
	capScope = func() map[string]any { return f.scope }
	capPost = func(string, any, time.Duration) (map[string]any, error) { return f.work, f.postErr }
	t.Setenv("ARKISM_CLIENT", "pc")
	if _, err := outboxEnqueue(f.client, "codex", "s", `C:\repo`, "work", "bind", outboxMarker()); err != nil {
		t.Fatal(err)
	}
	f.raw, _ = os.ReadFile(outboxFiles(f.client)[0])
	return f
}

func (f *receiverFixture) stateFiles() []string {
	paths, _ := filepath.Glob(filepath.Join(capState, "pc-work-events", "*.json"))
	return paths
}

func (f *receiverFixture) state(t *testing.T) map[string]any {
	raw, err := os.ReadFile(f.stateFiles()[0])
	if err != nil {
		t.Fatal(err)
	}
	m, _ := loadObjBytes(raw)
	return m
}

func receiverSend(raw []byte) (any, error) {
	r, err := receiverReceive(raw)
	if err != nil {
		return nil, err
	}
	return loads([]byte(host.MustDumps(r, host.DumpOpts{})))
}

func receiverWith(t *testing.T, raw []byte, changes map[string]any) []byte {
	e, _ := loadObjBytes(raw)
	for k, v := range changes {
		e[k] = v
	}
	return []byte(host.MustDumps(e, host.DumpOpts{}))
}

func TestReceiverResponseLossRetriesWithoutRequeueOrCurrentWorkBody(t *testing.T) {
	f := newReceiverFixture(t)
	_, err := outboxDrain(f.client, func(raw []byte) (any, error) {
		receiverReceive(raw)
		return nil, errors.New("lost response")
	}, 1)
	if err == nil {
		t.Fatal("no error")
	}
	st := f.state(t)
	events := st["events"].([]any)
	if len(events) != 1 || !eq(events[0].(map[string]any)["work_revision"], 5) || strings.Contains(host.MustDumps(st, host.DumpOpts{}), "PRIVATE BODY") {
		t.Fatal(st)
	}
	f.work["revision"] = int64(9)
	if _, err := outboxDrain(f.client, receiverSend, 1); err != nil {
		t.Fatal(err)
	}
	events = f.state(t)["events"].([]any)
	if len(events) != 1 || !eq(events[0].(map[string]any)["work_revision"], 5) || len(outboxFiles(f.client)) != 0 {
		t.Fatal(events)
	}
}

func TestReceiverSequenceIDAndHashReuseAreRejected(t *testing.T) {
	f := newReceiverFixture(t)
	if _, err := receiverReceive(f.raw); err != nil {
		t.Fatal(err)
	}
	ev, _ := loadObjBytes(f.raw)
	created, _ := num(ev["created_at"])
	for _, c := range []map[string]any{{"event_id": "pc-work-" + strings.Repeat("b", 32)}, {"sequence": int64(2)}, {"created_at": created + 1}} {
		_, err := receiverReceive(receiverWith(t, f.raw, c))
		boundaryWantErr(t, err, "conflict")
	}
	if _, err := receiverReceive(receiverWith(t, f.raw, map[string]any{"event_id": "pc-work-" + strings.Repeat("c", 32), "sequence": int64(5)})); err != nil {
		t.Fatal(err)
	}
	_, err := receiverReceive(receiverWith(t, f.raw, map[string]any{"event_id": "pc-work-" + strings.Repeat("d", 32), "sequence": int64(3)}))
	boundaryWantErr(t, err, "stale")
	if !eq(f.state(t)["high_sequence"], 5) {
		t.Fatal(f.state(t))
	}
}

func TestReceiverPermissionFailureAndStorageFailureNeverAcknowledge(t *testing.T) {
	f := newReceiverFixture(t)
	f.postErr = fs.ErrPermission
	if _, err := receiverReceive(f.raw); !errors.Is(err, fs.ErrPermission) {
		t.Fatal(err)
	}
	if len(f.stateFiles()) != 0 {
		t.Fatal("stored")
	}
	f.postErr = nil
	orig := outboxWrite
	t.Cleanup(func() { outboxWrite = orig })
	outboxWrite = func(string, []byte) error { return errors.New("disk full") }
	if _, err := receiverReceive(f.raw); err == nil {
		t.Fatal("no error")
	}
	if len(f.stateFiles()) != 0 {
		t.Fatal("stored")
	}
}

func TestReceiverRuntimeScopeAndExpiryBoundaries(t *testing.T) {
	f := newReceiverFixture(t)
	t.Setenv("ARKISM_CLIENT", "server")
	_, err := receiverReceive(f.raw)
	boundaryWantErr(t, err, "runtime_invalid")
	t.Setenv("ARKISM_CLIENT", "pc")
	capOrigin = "claude_code"
	_, err = receiverReceive(f.raw)
	boundaryWantErr(t, err, "runtime_invalid")
	capOrigin = "codex"
	if _, err := receiverReceive(f.raw); err != nil {
		t.Fatal(err)
	}
	f.scope = map[string]any{"workspace_id": "other", "project_id": "p"}
	if _, err := receiverReceive(f.raw); err != nil {
		t.Fatal(err)
	}
	if len(f.stateFiles()) != 2 {
		t.Fatal(f.stateFiles())
	}
	ev, _ := loadObjBytes(f.raw)
	created, _ := num(ev["created_at"])
	_, err = receiverReceive(receiverWith(t, f.raw, map[string]any{"created_at": created - 8*86400}))
	boundaryWantErr(t, err, "expired")
}

func TestReceiverMainPrintsReceiptOrCode(t *testing.T) {
	f := newReceiverFixture(t)
	spawned := 0
	orig := spawnDrain
	t.Cleanup(func() { spawnDrain = orig })
	spawnDrain = func() { spawned++ }
	var out strings.Builder
	if code := receiverMain(nil, strings.NewReader(string(f.raw)), &out, &out); code != 0 || spawned != 1 ||
		!strings.HasPrefix(out.String(), `{"accepted": true, "event_id": "pc-work-`) {
		t.Fatal(code, out.String())
	}
	out.Reset()
	t.Setenv("ARKISM_CLIENT", "server")
	if code := receiverMain(nil, strings.NewReader(string(f.raw)), &out, &out); code != 1 ||
		out.String() != `{"accepted": false, "error": "work_pc_runtime_invalid"}`+"\n" {
		t.Fatal(code, out.String())
	}
}
