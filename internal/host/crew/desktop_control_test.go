package crew

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

func TestDesktopControlWatchLoopBusyPausePreservesRound(t *testing.T) {
	gate := &desktopcontrol.Gate{}
	entered, finish, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var rounds atomic.Int32
	go func() {
		watchLoop(gate, stop, time.Hour, func() { rounds.Add(1); close(entered); <-finish })
		close(done)
	}()
	<-entered
	if idle, err := gate.Pause(1); err != nil || idle {
		t.Fatal("active watcher round was reported idle")
	}
	select {
	case <-done:
		t.Fatal("pause cancelled work")
	default:
	}
	close(finish)
	deadline := time.Now().Add(time.Second)
	for {
		idle, err := gate.Pause(2)
		if err != nil {
			t.Fatal(err)
		}
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed round retained activity")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle loop did not stop promptly")
	}
	if rounds.Load() != 1 {
		t.Fatal("new round admitted after pause")
	}
	if gate.Resume(2) != nil {
		t.Fatal("pause lease was lost")
	}
}

func TestDesktopControlReportWaitsForPoolThenFinalAccountProbesOnEveryExit(t *testing.T) {
	failure := errors.New("inert report failure")
	for _, reportError := range []error{nil, failure} {
		var events []string
		_, err := drainProviderReport(func() (int, error) { events = append(events, "report"); return 3, reportError },
			func() { events = append(events, "pool_finished_and_submitted_probe") }, func() { events = append(events, "final_probe_finished") })
		if !errors.Is(err, reportError) || len(events) != 3 || events[1] != "pool_finished_and_submitted_probe" || events[2] != "final_probe_finished" {
			t.Fatal("report exited before final probes")
		}
	}
}

func TestDesktopControlWatcherActualDriverDrainsBeforeReceipt(t *testing.T) {
	child, parent := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	_ = parent.SetDeadline(time.Now().Add(3 * time.Second))
	settings := map[string]string{"ARKISM_DESKTOP_CONTROL": "1", "ARKISM_DESKTOP_ROLE": "watcher",
		"ARKISM_DESKTOP_GENERATION": "7", "ARKISM_DESKTOP_NONCE": strings.Repeat("a", 64)}
	config, err := desktopcontrol.ParseEnvironment(func(key string) string { return settings[key] }, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	control, err := desktopcontrol.New(*config, &desktopcontrol.Gate{}, child, child)
	if err != nil {
		t.Fatal(err)
	}
	events, done := make(chan string, 3), make(chan error, 1)
	poolFinished := make(chan struct{})
	var finishOnce sync.Once
	t.Cleanup(func() { finishOnce.Do(func() { close(poolFinished) }) })
	go func() {
		done <- runDesktopWatcher(control, func(stop <-chan struct{}) { <-stop; events <- "loop_stopped" },
			func() { events <- "pool_wait"; <-poolFinished }, func() { events <- "accounts_wait" })
	}()
	decoder := json.NewDecoder(parent)
	watcherControlReceive(t, decoder, "ready")
	watcherControlWrite(t, parent, 1, "pause", 0)
	watcherControlReceive(t, decoder, "idle")
	watcherControlWrite(t, parent, 2, "stop", 1)
	if <-events != "loop_stopped" || <-events != "pool_wait" {
		t.Fatal("watch loop was not stopped before draining the pool")
	}
	select {
	case err := <-done:
		t.Fatalf("watcher returned before drain/receipt: %v", err)
	default:
	}
	finishOnce.Do(func() { close(poolFinished) })
	if <-events != "accounts_wait" {
		t.Fatal("final account wait did not follow the pool")
	}
	watcherControlReceive(t, decoder, "stopped")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func watcherControlReceive(t *testing.T, decoder *json.Decoder, state string) {
	t.Helper()
	var row struct{ State string }
	if err := decoder.Decode(&row); err != nil || row.State != state {
		t.Fatalf("control state = %q, error = %v; want %q", row.State, err, state)
	}
}

func watcherControlWrite(t *testing.T, parent net.Conn, id uint64, op string, lease uint64) {
	t.Helper()
	row := map[string]any{"v": 1, "role": "watcher", "generation": 7, "nonce": strings.Repeat("a", 64),
		"pid": os.Getpid(), "request_id": id, "op": op, "lease_id": lease}
	if err := json.NewEncoder(parent).Encode(row); err != nil {
		t.Fatal(err)
	}
}
