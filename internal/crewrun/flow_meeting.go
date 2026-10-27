package crewrun

import (
	"context"
	"errors"
	"time"
)

var errFlowMeetingStop = errors.New("linked meeting stop requested")

// Linked meetings retain their existing state machine and lease. A separate
// observer interrupts an in-flight turn when the durable stop request arrives.
// The acknowledgement is written only after the turn has returned.
func (e *Engine) driveFlowMeeting(parent context.Context, rec MeetingRecord) error {
	scope, ok := parent.Value(flowCallScopeKey{}).(flowCallScope)
	if !ok || scope.run.ID != rec.Meeting.ParentID {
		return failf("flow_parent_required", "linked meetings must be driven by their leased parent")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(context.Canceled)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		e.watchFlowMeeting(ctx, rec.ID, stop, cancel)
	}()
	if rec.Meeting.Status == "stopping" {
		cancel(errFlowMeetingStop)
	}
	x := &meetingExec{e: e, base: rec, m: cloneMeeting(rec.Meeting), lease: e.meetingLease()}
	err := x.drive(ctx)
	close(stop)
	<-done
	if errors.Is(context.Cause(ctx), errFlowMeetingStop) {
		return x.acknowledgeStop(parent)
	}
	return err
}

func (e *Engine) watchFlowMeeting(ctx context.Context, id string, stop <-chan struct{}, cancel context.CancelCauseFunc) {
	every := e.flowPollInterval
	if every <= 0 {
		every = 3 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			bounded, finish := context.WithTimeout(ctx, flowRequestTimeout)
			rec, err := e.Meetings.GetMeeting(bounded, id)
			finish()
			if err != nil {
				cancel(err)
				return
			}
			if rec.Meeting.Status == "stopping" {
				cancel(errFlowMeetingStop)
				return
			}
			if terminalMeeting(rec.Meeting.Status) {
				cancel(errMeetingGone)
				return
			}
		}
	}
}

func (x *meetingExec) acknowledgeStop(parent context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
	fresh, err := x.e.Meetings.GetMeeting(ctx, x.base.ID)
	if err != nil || terminalMeeting(fresh.Meeting.Status) {
		return err
	}
	if fresh.Meeting.Status != "stopping" {
		return errors.New("linked meeting cancellation was not confirmed")
	}
	x.base, x.m = fresh, cloneMeeting(fresh.Meeting)
	x.m.Status, x.m.Speaking, x.m.EndedReason, x.m.Waiting = "stopped", "", "human_stop", nil
	return ignoreGone(x.save(ctx))
}
