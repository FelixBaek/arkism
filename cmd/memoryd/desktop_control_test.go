package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
	"github.com/FelixBaek/arkism/internal/memory"
)

type memoryControlFixture struct {
	t       *testing.T
	role    string
	parent  net.Conn
	decoder *json.Decoder
	control *desktopcontrol.Controller
}

func newMemoryControlFixture(t *testing.T, role string) *memoryControlFixture {
	t.Helper()
	child, parent := net.Pipe()
	values := map[string]string{"ARKISM_DESKTOP_CONTROL": "1", "ARKISM_DESKTOP_ROLE": role,
		"ARKISM_DESKTOP_GENERATION": "8", "ARKISM_DESKTOP_NONCE": strings.Repeat("a", 64)}
	config, err := desktopcontrol.ParseEnvironment(func(key string) string { return values[key] }, role)
	if err != nil {
		t.Fatal(err)
	}
	control, err := desktopcontrol.New(*config, &desktopcontrol.Gate{}, child, child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	if err := parent.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return &memoryControlFixture{t: t, role: role, parent: parent, decoder: json.NewDecoder(parent), control: control}
}

func (f *memoryControlFixture) request(id uint64, operation string, lease uint64) {
	f.t.Helper()
	row := map[string]any{"v": 1, "role": f.role, "generation": 8, "nonce": strings.Repeat("a", 64),
		"pid": os.Getpid(), "request_id": id, "op": operation, "lease_id": lease}
	if err := json.NewEncoder(f.parent).Encode(row); err != nil {
		f.t.Fatal(err)
	}
}

func (f *memoryControlFixture) receive(state string) string {
	f.t.Helper()
	var row struct {
		State string
		Error string
	}
	if err := f.decoder.Decode(&row); err != nil || row.State != state {
		f.t.Fatalf("state %q/error %v; want %q", row.State, err, state)
	}
	return row.Error
}

func TestDesktopControlMemoryWorkerBusyPreservesPrivacyWorkAndFinish(t *testing.T) {
	f := newMemoryControlFixture(t, "worker")
	entered, privacyDone, workStarted, workDone, finishStarted, finishDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var closePrivacy, closeWork, closeFinish sync.Once
	t.Cleanup(func() {
		closePrivacy.Do(func() { close(privacyDone) })
		closeWork.Do(func() { close(workDone) })
		closeFinish.Do(func() { close(finishDone) })
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var rounds atomic.Int32
	go func() {
		done <- runMemoryWorker(ctx, f.control, time.Hour, func(ctx context.Context) {
			rounds.Add(1)
			defer func() { close(finishStarted); <-finishDone }()
			close(entered)
			<-privacyDone
			if ctx.Err() != nil {
				t.Error("busy privacy operation cancelled")
			}
			close(workStarted)
			<-workDone
			if ctx.Err() != nil {
				t.Error("busy WorkOnce operation cancelled")
			}
		})
	}()
	f.receive("ready")
	<-entered
	f.request(1, "pause", 0)
	f.receive("busy")
	closePrivacy.Do(func() { close(privacyDone) })
	<-workStarted
	f.request(2, "pause", 0)
	f.receive("busy")
	closeWork.Do(func() { close(workDone) })
	<-finishStarted
	f.request(3, "pause", 0)
	f.receive("busy")
	closeFinish.Do(func() { close(finishDone) })
	// Enter/exit once the real round's deferred finish has released admission.
	for deadline := time.Now().Add(time.Second); ; {
		idle, err := f.control.Admission().Pause(90)
		if err != nil {
			t.Fatal(err)
		}
		if idle {
			if f.control.Admission().Resume(90) != nil {
				t.Fatal("fixture resume failed")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed round stayed busy")
		}
		time.Sleep(time.Millisecond)
	}
	f.request(4, "pause", 0)
	f.receive("idle")
	if memoryWorkerRound(ctx, f.control.Admission(), func(context.Context) { t.Error("paused worker claimed another job") }) {
		t.Fatal("closed round admitted")
	}
	f.request(5, "resume", 4)
	f.receive("resumed")
	f.request(6, "pause", 0)
	f.receive("idle")
	f.request(7, "stop", 6)
	f.receive("stopped")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if rounds.Load() != 1 || ctx.Err() != nil {
		t.Fatal("stop replayed or cancelled work")
	}
}

func TestDesktopControlMemoryWorkerPauseAndClaimAreAtomic(t *testing.T) {
	for i := 0; i < 40; i++ {
		gate := &desktopcontrol.Gate{}
		start, finish := make(chan struct{}), make(chan struct{})
		var claims atomic.Int32
		roundDone := make(chan bool, 1)
		go func() {
			<-start
			roundDone <- memoryWorkerRound(context.Background(), gate, func(context.Context) { claims.Add(1); <-finish })
		}()
		paused := make(chan bool, 1)
		go func() {
			<-start
			idle, err := gate.Pause(1)
			if err != nil {
				t.Error(err)
			}
			paused <- idle
		}()
		close(start)
		idle := <-paused
		if idle && claims.Load() != 0 {
			t.Fatal("pause winner still claimed work")
		}
		close(finish)
		admitted := <-roundDone
		if idle == admitted {
			t.Fatal("pause and round both won or both lost")
		}
		if idle {
			if gate.Resume(1) != nil {
				t.Fatal("resume failed")
			}
		} else if claims.Load() != 1 {
			t.Fatal("busy round was lost")
		}
	}
}

type memoryClaimFixture struct {
	memory.Repository
	claimed, allowClaim, finishing, allowFinish chan struct{}
	claims, finishes                            atomic.Int32
}

func (r *memoryClaimFixture) ClaimJob(context.Context, time.Time, time.Duration) (memory.Record, error) {
	r.claims.Add(1)
	close(r.claimed)
	<-r.allowClaim
	return memory.Record{Kind: "inert-unsupported", Attempts: 1}, nil
}
func (r *memoryClaimFixture) FinishJob(ctx context.Context, _ memory.Record, _ bool) error {
	r.finishes.Add(1)
	close(r.finishing)
	<-r.allowFinish
	return ctx.Err()
}

func TestDesktopControlMemoryWorkerActualWorkOnceDeferredFinishStaysActive(t *testing.T) {
	repo := &memoryClaimFixture{claimed: make(chan struct{}), allowClaim: make(chan struct{}), finishing: make(chan struct{}), allowFinish: make(chan struct{})}
	var claimOnce, finishOnce sync.Once
	t.Cleanup(func() {
		claimOnce.Do(func() { close(repo.allowClaim) })
		finishOnce.Do(func() { close(repo.allowFinish) })
	})
	svc := &memory.Service{Repo: repo}
	gate := &desktopcontrol.Gate{}
	done := make(chan bool, 1)
	go func() {
		done <- memoryWorkerRound(context.Background(), gate, func(ctx context.Context) {
			if err := svc.WorkOnce(ctx, "inert-worker"); !errors.Is(err, memory.ErrInvalid) {
				t.Errorf("actual WorkOnce failed: %v", err)
			}
		})
	}()
	<-repo.claimed
	if idle, err := gate.Pause(1); err != nil || idle {
		t.Fatal("in-flight claim reported idle")
	}
	claimOnce.Do(func() { close(repo.allowClaim) })
	<-repo.finishing
	if idle, err := gate.Pause(2); err != nil || idle {
		t.Fatal("deferred FinishJob reported idle")
	}
	finishOnce.Do(func() { close(repo.allowFinish) })
	if !<-done {
		t.Fatal("admitted round was lost")
	}
	if idle, err := gate.Pause(3); err != nil || !idle {
		t.Fatal("finished round not idle")
	}
	if memoryWorkerRound(context.Background(), gate, func(ctx context.Context) { _ = svc.WorkOnce(ctx, "inert-worker") }) {
		t.Fatal("paused worker made another claim")
	}
	if repo.claims.Load() != 1 || repo.finishes.Load() != 1 {
		t.Fatal("claim or finish was duplicated")
	}
}

type memoryIdleListener struct {
	closed     chan struct{}
	accepted   chan struct{}
	acceptOnce sync.Once
	once       sync.Once
	closeErr   error
}

func (l *memoryIdleListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() { close(l.accepted) })
	<-l.closed
	return nil, net.ErrClosed
}
func (l *memoryIdleListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (l *memoryIdleListener) Close() error   { l.once.Do(func() { close(l.closed) }); return l.closeErr }

func memoryServerFixture(t *testing.T, handler http.Handler, closeErr error) (*memoryControlFixture, *http.Server, *memoryIdleListener, <-chan error) {
	t.Helper()
	f := newMemoryControlFixture(t, "server")
	listener := &memoryIdleListener{closed: make(chan struct{}), accepted: make(chan struct{}), closeErr: closeErr}
	server := &http.Server{Handler: handler}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = server.Close(); _ = listener.Close() })
	done := make(chan error, 1)
	go func() { done <- serveMemoryServer(ctx, server, listener, f.control) }()
	f.receive("ready")
	<-listener.accepted
	return f, server, listener, done
}

func TestDesktopControlMemoryServerBusyAndPauseWinnerPreserveRequests(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(finish) }) })
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-finish
		if r.Context().Err() != nil {
			t.Error("busy request cancelled")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f, server, listener, done := memoryServerFixture(t, handler, nil)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		server.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/work", nil))
	}()
	<-entered
	f.request(1, "pause", 0)
	f.receive("busy")
	select {
	case <-listener.closed:
		t.Fatal("busy server was shut down")
	default:
	}
	release.Do(func() { close(finish) })
	<-requestDone
	f.request(2, "pause", 0)
	f.receive("idle")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest("POST", "/work", nil))
	if response.Code != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatal("paused handler performed work")
	}
	f.request(3, "stop", 99)
	if f.receive("error") != "invalid_lease" {
		t.Fatal("unrelated lease accepted")
	}
	select {
	case <-listener.closed:
		t.Fatal("wrong lease stopped server")
	default:
	}
	f.request(4, "stop", 2)
	f.receive("stopped")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDesktopControlMemoryServerShutdownErrorIsNotStoppedOrSuccess(t *testing.T) {
	f, _, _, done := memoryServerFixture(t, http.NotFoundHandler(), errors.New("inert listener close failed"))
	f.request(1, "pause", 0)
	f.receive("idle")
	f.request(2, "stop", 1)
	if f.receive("error") != "shutdown_failed" {
		t.Fatal("failed shutdown claimed stopped")
	}
	if err := <-done; !errors.Is(err, desktopcontrol.ErrStop) {
		t.Fatal("shutdown error lost behind server closed")
	}
}

func TestDesktopControlMemoryServerFinalReceiptWriteFailureStaysFailure(t *testing.T) {
	f, _, _, done := memoryServerFixture(t, http.NotFoundHandler(), nil)
	f.request(1, "pause", 0)
	f.receive("idle")
	f.request(2, "stop", 1)
	// Closing the parent prevents the final write; successful local shutdown
	// is insufficient evidence of a delivered stopped receipt.
	_ = f.parent.Close()
	select {
	case err := <-done:
		if !errors.Is(err, desktopcontrol.ErrTransport) && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("missing receipt became success: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing receipt was not reported")
	}
}
