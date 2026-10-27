package crewrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// FlowLeaseStore is optional for legacy stores. Flow runs require it: a local
// process claim alone cannot prevent a second daemon from repeating work.
type FlowLeaseStore interface {
	LeaseRun(context.Context, string, string, string) (FlowLease, error)
}

type FlowLease struct {
	LeaseUntil      time.Time `json:"lease_until"`
	CancelRequested bool      `json:"cancel_requested"`
}

var errFlowCancelled = errors.New("flow cancellation requested")

const flowRequestTimeout = 5 * time.Second

type flowExecution struct {
	e      *Engine
	store  FlowLeaseStore
	id     string
	token  string
	parent context.Context
	ctx    context.Context
	cancel context.CancelCauseFunc
	stop   chan struct{}
	done   chan struct{}
}

func (e *Engine) beginFlow(ctx context.Context, run Run) (Run, *flowExecution, error) {
	store, ok := e.Store.(FlowLeaseStore)
	if !ok {
		return Run{}, nil, errors.New("flow execution requires a lease-capable store")
	}
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Run{}, nil, err
	}
	f := &flowExecution{e: e, store: store, id: run.ID, token: hex.EncodeToString(token[:]), parent: ctx, stop: make(chan struct{}), done: make(chan struct{})}
	lease, err := f.lease(ctx, "acquire")
	if err != nil {
		return Run{}, nil, err
	}
	// Acquiring ownership and loading the execution are separate operations.
	// Re-read after the claim so no progress uses a pre-acquisition revision.
	freshCtx, done := context.WithTimeout(ctx, flowRequestTimeout)
	fresh, err := e.Store.GetRun(freshCtx, run.ID)
	done()
	if err != nil {
		_, _ = f.lease(context.WithoutCancel(ctx), "release")
		return Run{}, nil, err
	}
	fresh.LeaseToken = f.token
	f.ctx, f.cancel = context.WithCancelCause(ctx)
	if lease.CancelRequested || fresh.State.CancelRequested {
		f.cancel(errFlowCancelled)
	}
	go f.monitor()
	return fresh, f, nil
}

func (f *flowExecution) lease(ctx context.Context, action string) (FlowLease, error) {
	bounded, cancel := context.WithTimeout(ctx, flowRequestTimeout)
	defer cancel()
	return f.store.LeaseRun(bounded, f.id, action, f.token)
}

func (f *flowExecution) monitor() {
	defer close(f.done)
	pollEvery, renewEvery := f.e.flowPollInterval, f.e.flowRenewInterval
	if pollEvery <= 0 {
		pollEvery = 3 * time.Second
	}
	if renewEvery <= 0 {
		renewEvery = 20 * time.Second
	}
	poll, renew := time.NewTicker(pollEvery), time.NewTicker(renewEvery)
	defer poll.Stop()
	defer renew.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-f.parent.Done():
			return // parent cancellation leaves execution available for recovery
		case <-poll.C:
			bounded, cancel := context.WithTimeout(f.parent, flowRequestTimeout)
			run, err := f.e.Store.GetRun(bounded, f.id)
			cancel()
			if err != nil {
				f.cancel(fmt.Errorf("flow access or state unavailable: %w", err))
				return
			}
			if run.State.CancelRequested {
				f.cancel(errFlowCancelled)
			} else if f.e.flowExpired(run.State) {
				f.cancel(errFlowDeadline)
			} else if terminal(run.State.Status) {
				f.cancel(errStopped)
				return
			}
		case <-renew.C:
			lease, err := f.lease(f.parent, "renew")
			if err != nil {
				f.cancel(fmt.Errorf("flow execution lease lost: %w", err))
				return
			}
			if lease.CancelRequested {
				f.cancel(errFlowCancelled)
			}
		}
	}
}

// finish runs only after Drive has joined every node goroutine. An accepted
// user stop can then be acknowledged; process shutdown or a lost lease cannot
// claim that the requested work completed or was cancelled by the user.
func (f *flowExecution) finish(x *runExec, driveErr error) error {
	if !errors.Is(context.Cause(f.ctx), errFlowCancelled) && !errors.Is(context.Cause(f.ctx), errFlowDeadline) {
		if errors.Is(context.Cause(f.ctx), errStopped) {
			return nil
		}
		if cause := context.Cause(f.ctx); cause != nil && f.parent.Err() == nil && errors.Is(driveErr, context.Canceled) {
			return errors.Join(driveErr, cause)
		}
		return driveErr
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.parent), 10*time.Second)
	defer cancel()
	fresh, err := f.e.Store.GetRun(ctx, f.id)
	if err != nil {
		return err
	}
	if terminal(fresh.State.Status) {
		return nil
	}
	status, summary := f.stopOutcome(fresh)
	if status == "" {
		return driveErr
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	fresh.LeaseToken = f.token
	x.run = fresh
	childrenDone, err := x.cancelChildren(ctx)
	if err != nil || !childrenDone {
		return err
	}
	for _, st := range x.run.State.Nodes {
		if st.Status == "running" {
			st.Status, st.Error = "skipped", summary
			x.stamp(st)
		}
	}
	x.closeRun(status, summary)
	return x.saveLocked(ctx, "")
}

func (f *flowExecution) close() {
	close(f.stop)
	<-f.done
	f.cancel(context.Canceled)
	_, _ = f.lease(context.WithoutCancel(f.parent), "release")
}
