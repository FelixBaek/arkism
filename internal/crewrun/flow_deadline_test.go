package crewrun

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlowDeadlineInterruptsNestedActiveWorkBeforeFailure(t *testing.T) {
	child := wf("child", &Graph{Nodes: []Node{node("read", "http", "access", "read", "url", "https://local.test")}})
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), child)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	e.Now, e.flowPollInterval = func() time.Time { return time.Unix(0, clock.Load()) }, 5*time.Millisecond
	deadline := e.now().Add(time.Minute)
	root.State.Deadline = &deadline
	fs.runs[root.ID] = clone(root)
	started, exited := blockingFlow(e)
	done := driveFlow(e, context.Background(), root.ID)
	awaitFlow(t, started)
	clock.Store(deadline.Add(time.Second).UnixNano())
	if err := awaitDrive(t, done); err != nil {
		t.Fatal(err)
	}
	awaitFlow(t, exited)
	parent, ref := childRun(t, fs, root.ID)
	got, _ := fs.GetRun(context.Background(), ref.ID)
	if parent.State.Status != "failed" || !strings.HasPrefix(parent.State.Summary, "time_limit:") || !terminal(got.State.Status) {
		t.Fatal("deadline completed while child remained active", parent.State, got.State)
	}
}

func TestFlowDeadlinePollClosesAnUndecidedApproval(t *testing.T) {
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	now := time.Now()
	e.Now = func() time.Time { return now }
	deadline := now.Add(time.Minute)
	root.State.Deadline = &deadline
	fs.runs[root.ID] = clone(root)
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	for i := 0; i < 3; i++ {
		if err := e.PollWaiting(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.Wait() // a child owned by another poll completes before the parent's next observation
	}
	parent, ref := childRun(t, fs, root.ID)
	got, _ := fs.GetRun(context.Background(), ref.ID)
	if parent.State.Status != "failed" || !strings.HasPrefix(parent.State.Summary, "time_limit:") || !terminal(got.State.Status) {
		t.Fatal("approval wait ignored deadline", parent.State, got.State)
	}
}

func TestFlowReadModelRecoveryKeepsAttemptAndDoesNotRedispatch(t *testing.T) {
	e, fs, root := childFixture(t, node("call", "ai", "access", "read", "model", "test-model"), waitingChild())
	log := fakeAI(t, e)
	fs.settleErr = &APIError{503, "unavailable"}
	if err := e.Drive(context.Background(), root.ID); err == nil {
		t.Fatal("lost receipt unexpectedly completed")
	}
	fs.settleErr = nil
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.GetRun(context.Background(), root.ID)
	if len(specs(t, log)) != 1 || got.State.Nodes["call"].Runs != 1 || got.State.Status != "failed" || !strings.Contains(got.State.Summary, "attempt_replay_blocked") {
		t.Fatal("recovery spent again after an uncertain receipt", got.State, fs.requests)
	}
}
