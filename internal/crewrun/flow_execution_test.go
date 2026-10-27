package crewrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type flowStoreFake struct {
	*fakeStore
	leaseMu  sync.Mutex
	token    string
	actions  []string
	readErr  error
	renewErr error
	// A stop acknowledgement must only occur after the child has exited.
	childExited <-chan struct{}
}

func (f *flowStoreFake) LeaseRun(ctx context.Context, id, action, token string) (FlowLease, error) {
	f.leaseMu.Lock()
	defer f.leaseMu.Unlock()
	if err := ctx.Err(); err != nil {
		return FlowLease{}, err
	}
	f.actions = append(f.actions, action)
	switch action {
	case "acquire":
		if f.token != "" && f.token != token {
			return FlowLease{}, &APIError{409, "revision_conflict"}
		}
		f.token = token
	case "renew":
		if f.renewErr != nil {
			return FlowLease{}, f.renewErr
		}
		if f.token != token {
			return FlowLease{}, &APIError{409, "revision_conflict"}
		}
	case "release":
		if f.token != token {
			return FlowLease{}, &APIError{409, "revision_conflict"}
		}
		f.token = ""
	}
	run, err := f.fakeStore.GetRun(ctx, id)
	return FlowLease{LeaseUntil: time.Now().Add(90 * time.Second), CancelRequested: run.State.CancelRequested}, err
}

func (f *flowStoreFake) GetRun(ctx context.Context, id string) (Run, error) {
	f.leaseMu.Lock()
	err := f.readErr
	f.leaseMu.Unlock()
	if err != nil {
		return Run{}, err
	}
	return f.fakeStore.GetRun(ctx, id)
}

func (f *flowStoreFake) UpdateRun(ctx context.Context, run Run) (Run, error) {
	f.leaseMu.Lock()
	defer f.leaseMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	if run.LeaseToken == "" || run.LeaseToken != f.token {
		return Run{}, &APIError{409, "revision_conflict"}
	}
	old, err := f.fakeStore.GetRun(ctx, run.ID)
	if err != nil {
		return Run{}, err
	}
	if old.State.CancelRequested && run.State.Status != "stopped" {
		return Run{}, &APIError{409, "revision_conflict"}
	}
	if run.State.Status == "stopped" && f.childExited != nil {
		select {
		case <-f.childExited:
		default:
			return Run{}, errors.New("stop acknowledged before child termination")
		}
	}
	run.State.CancelRequested = old.State.CancelRequested
	return f.fakeStore.UpdateRun(ctx, run)
}

func (f *flowStoreFake) requestCancel(id string) {
	f.fakeStore.mu.Lock()
	defer f.fakeStore.mu.Unlock()
	run := f.runs[id]
	run.State.CancelRequested = true
	run.Revision++
	f.runs[id] = run
}

type flowTransport func(*http.Request) (*http.Response, error)

func (f flowTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newFlowFixture(t *testing.T) (*Engine, *flowStoreFake, Run) {
	t.Helper()
	graph := &Graph{Nodes: []Node{node("read", "http", "access", "read", "url", "https://local.test/read"), node("end", "end")}, Edges: []Edge{edge("read", "end")}}
	it := wf("flow", graph)
	fs := &flowStoreFake{fakeStore: newFakeStore(it)}
	e := testEngine(t, fs.fakeStore)
	e.Store, e.flowPollInterval, e.flowRenewInterval = fs, 5*time.Millisecond, 10*time.Millisecond
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartRun(context.Background(), "flow", "call", "research")
	if err != nil {
		t.Fatal(err)
	}
	run.State.InvocationMode, run.State.Definition = "flow", &it.Crew
	fs.fakeStore.mu.Lock()
	fs.runs[run.ID] = clone(run)
	fs.fakeStore.mu.Unlock()
	return e, fs, run
}

func blockingFlow(e *Engine) (<-chan struct{}, <-chan struct{}) {
	started, exited := make(chan struct{}), make(chan struct{})
	e.HTTP = &http.Client{Transport: flowTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(exited)
		return nil, r.Context().Err()
	})}
	return started, exited
}

func awaitFlow(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(2 * time.Second):
		t.Fatal("flow did not reach expected state")
	}
}

func driveFlow(e *Engine, ctx context.Context, id string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- e.Drive(ctx, id) }()
	return done
}

func awaitDrive(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("flow driver did not stop")
		return nil
	}
}

func TestFlowLeaseExcludesSecondEngineAndShutdownKeepsRunRecoverable(t *testing.T) {
	e, fs, run := newFlowFixture(t)
	started, exited := blockingFlow(e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := driveFlow(e, ctx, run.ID)
	awaitFlow(t, started)
	other := testEngine(t, fs.fakeStore)
	other.Store = fs
	if err := other.Drive(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := awaitDrive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	awaitFlow(t, exited)
	stored, _ := fs.GetRun(context.Background(), run.ID)
	if stored.State.Status != "running" || stored.State.Nodes["read"].Runs != 1 || fs.token != "" {
		t.Fatal(stored.State, fs.token)
	}
	// A later poll recovers even a touched run, from its pinned definition.
	fs.fakeStore.crew = nil
	other.HTTP = &http.Client{Transport: flowTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("final evidence"))}, nil
	})}
	if err := other.StartRequested(context.Background()); err != nil {
		t.Fatal(err)
	}
	other.Wait()
	stored, _ = fs.GetRun(context.Background(), run.ID)
	if stored.State.Status != "succeeded" || !strings.Contains(stored.State.Summary, "final evidence") {
		t.Fatal(stored.State)
	}
}

func TestFlowCancelWaitsForChildExitBeforeStopped(t *testing.T) {
	e, fs, run := newFlowFixture(t)
	started, exited := blockingFlow(e)
	fs.childExited = exited
	done := driveFlow(e, context.Background(), run.ID)
	awaitFlow(t, started)
	fs.requestCancel(run.ID)
	if err := awaitDrive(t, done); err != nil {
		t.Fatal(err)
	}
	stored, _ := fs.GetRun(context.Background(), run.ID)
	if stored.State.Status != "stopped" || stored.State.Nodes["read"].Status != "skipped" || !stored.State.CancelRequested || fs.token != "" {
		t.Fatal(stored.State, fs.token)
	}
}

func TestFlowLeaseOrAccessLossStopsChildrenWithoutClaimingUserCancellation(t *testing.T) {
	for _, cause := range []string{"renew", "access"} {
		t.Run(cause, func(t *testing.T) {
			e, fs, run := newFlowFixture(t)
			started, exited := blockingFlow(e)
			done := driveFlow(e, context.Background(), run.ID)
			awaitFlow(t, started)
			fs.leaseMu.Lock()
			if cause == "renew" {
				fs.renewErr = &APIError{409, "revision_conflict"}
			} else {
				fs.readErr = &APIError{403, "forbidden"}
			}
			fs.leaseMu.Unlock()
			if err := awaitDrive(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			awaitFlow(t, exited)
			stored, _ := fs.fakeStore.GetRun(context.Background(), run.ID)
			if stored.State.Status != "running" || stored.State.CancelRequested || fs.token != "" {
				t.Fatal(stored.State, fs.token)
			}
		})
	}
}

func TestFlowWaitingCancellationDoesNotNeedItsResumeCondition(t *testing.T) {
	e, fs, run := newFlowFixture(t)
	future := time.Now().Add(time.Hour)
	run.State.Status, run.State.CancelRequested = "waiting", true
	run.State.Waiting = &Waiting{NodeID: "read", Kind: "wait", ResumeAt: &future}
	run.State.Nodes["read"].Status = "waiting"
	fs.fakeStore.mu.Lock()
	fs.runs[run.ID] = clone(run)
	fs.fakeStore.mu.Unlock()
	if err := e.PollWaiting(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	stored, _ := fs.GetRun(context.Background(), run.ID)
	if stored.State.Status != "stopped" || stored.State.Waiting != nil {
		t.Fatal(stored.State)
	}
}

func TestFlowWithoutLeaseStoreDoesNotRun(t *testing.T) {
	e, fs, run := newFlowFixture(t)
	e.Store = fs.fakeStore
	if err := e.Drive(context.Background(), run.ID); err == nil || !strings.Contains(err.Error(), "lease-capable") {
		t.Fatal(err)
	}
	stored, _ := fs.GetRun(context.Background(), run.ID)
	if stored.State.Nodes["read"].Runs != 0 {
		t.Fatal(stored.State)
	}
}

func TestFlowClientSendsLeaseTokenOnlyWithProtectedProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("local-test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := &Client{BaseURL: "https://local.test", TokenFile: path, Scope: Scope{WorkspaceID: "w", ProjectID: "p"}, HTTP: &http.Client{Transport: flowTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["lease_token"] != "lease-test-token" || r.Header.Get("Authorization") != "Bearer local-test-token" {
			t.Fatal(body, r.Header)
		}
		out := `{"id":"r","revision":2,"crew_run":{"status":"running"}}`
		if strings.HasSuffix(r.URL.Path, "/lease") {
			if body["action"] != "acquire" {
				t.Fatal(body)
			}
			out = `{"lease_until":"2026-10-28T12:00:00Z","cancel_requested":true}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(out))}, nil
	})}}
	ctx := context.Background()
	lease, err := c.LeaseRun(ctx, "r", "acquire", "lease-test-token")
	if err != nil || !lease.CancelRequested || lease.LeaseUntil.IsZero() {
		t.Fatal(lease, err)
	}
	run := Run{ID: "r", Revision: 1, LeaseToken: "lease-test-token", State: RunState{InvocationMode: "flow", Status: "running"}}
	if _, err := c.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.LeaseToken = ""
	if _, err := c.UpdateRun(ctx, run); err == nil || calls != 2 {
		t.Fatal(err, calls)
	}
}
