package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSearchCommitGateCancelsWaitersAndRetiresScopes(t *testing.T) {
	var gate searchCommitGate
	release, err := gate.acquire(testCtx, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(testCtx, 20*time.Millisecond)
	defer cancel()
	if cleanup, err := gate.acquire(ctx, scopeA); !errors.Is(err, context.DeadlineExceeded) {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("same-scope waiter was not bounded", err)
	}
	other, err := gate.acquire(testCtx, scopeB)
	if err != nil {
		t.Fatal("unrelated scope blocked", err)
	}
	other()
	release()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.entries) != 0 {
		t.Fatal("retired scope retained by gate")
	}
}

func TestSearchCommitGatePublishesAfterOwnerReleases(t *testing.T) {
	var gate searchCommitGate
	release, err := gate.acquire(testCtx, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	entered := make(chan struct{})
	go func() {
		next, err := gate.acquire(testCtx, scopeA)
		if err != nil {
			return
		}
		defer next()
		close(entered)
	}()
	select {
	case <-entered:
		t.Fatal("overlapping canonical commits")
	case <-time.After(10 * time.Millisecond):
	}
	release()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("owner retained gate")
	}
}
