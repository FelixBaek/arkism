package memory

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// dynamicsBlockingEmbedder never answers before its context ends, like a provider
// that hangs until the worker iteration's deadline.
type dynamicsBlockingEmbedder struct {
	Embedder
	calls *atomic.Int64
}

func (e dynamicsBlockingEmbedder) Embed(ctx context.Context, _ []string, _ string) ([]Embedding, error) {
	e.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

// dynamicsHungEmbeddingSetup queues three native updates and an overdue embedding
// backlog whose provider hangs.
func dynamicsHungEmbeddingSetup(t *testing.T) (*nativePGFixture, *Service, string, *atomic.Int64) {
	t.Helper()
	p, svc, _, token := dynamicsFairnessSetup(t)
	for i := 0; i < 2; i++ {
		p.in.EventID = fmt.Sprintf("reverse-%d", i)
		p.accept(t)
	}
	var live Record
	for _, row := range pgCollection(t, p.admin, colJobs) {
		if row.Kind == "embedding" && row.RunAt != nil && row.RunAt.After(time.Now()) {
			live = row
		}
	}
	if live.ID == "" {
		t.Fatal("no live embedding job")
	}
	// Overdue, so the claim (ordered by run time) takes them before any other due job.
	now, overdue := time.Now().UTC(), time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		row := Record{ID: fmt.Sprintf("embedding-slow-%d", i), Scope: live.Scope, Kind: "embedding", Status: JobStatusPending, Revision: 1, Target: live.Target, RunAt: &overdue, CreatedAt: now, UpdatedAt: now}
		dynamicsRebindExpect(t, p.admin.Insert(testCtx, colJobs, row), nil, "slow embedding backlog")
	}
	calls := &atomic.Int64{}
	svc.Embedder = dynamicsBlockingEmbedder{svc.Embedder, calls}
	return p, svc, token, calls
}

// Reverse starvation: an embedding backlog whose provider hangs until the
// iteration deadline must not stop native dynamics from advancing in that round.
func TestDynamicsLoadSlowEmbeddingDoesNotStarveNativePG(t *testing.T) {
	p, svc, token, calls := dynamicsHungEmbeddingSetup(t)
	for round := 1; dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count > 0; round++ {
		if round > 3 {
			t.Fatal("native backlog not drained in three rounds")
		}
		head, called := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Head, calls.Load()
		ctx, cancel := context.WithTimeout(testCtx, time.Second)
		err := svc.WorkOnce(ctx, token)
		cancel()
		after := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
		t.Logf("round %d: head %d -> %d, withheld %v, err %v", round, head, after.Head, after.Withheld, err)
		// The round must have spent its whole deadline inside the hung provider.
		if calls.Load() == called || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("round %d did not end in the hung embedding: calls %d -> %d, err %v", round, called, calls.Load(), err)
		}
		if after.Head == head || after.Withheld {
			t.Fatalf("round %d: native made no progress behind a hung embedding", round)
		}
	}
}

// A shutdown cancellation during the hung embedding still stops the native step.
func TestDynamicsLoadCancelDuringEmbeddingStopsNativePG(t *testing.T) {
	p, svc, token, calls := dynamicsHungEmbeddingSetup(t)
	head := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Head
	ctx, cancel := context.WithCancel(testCtx)
	stop := time.AfterFunc(300*time.Millisecond, cancel)
	defer stop.Stop()
	err := svc.WorkOnce(ctx, token)
	cancel()
	if calls.Load() == 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("round did not end in the hung embedding: calls %d, err %v", calls.Load(), err)
	}
	if after := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Head; after != head {
		t.Fatalf("native ran after shutdown: head %d -> %d", head, after)
	}
}
