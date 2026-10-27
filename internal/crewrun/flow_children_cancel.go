package crewrun

import (
	"context"
	"errors"
)

// cancelChildren confirms termination, not merely acceptance of a stop
// request. A remote driver may still be joining its active model call.
func (x *runExec) cancelChildren(ctx context.Context) (bool, error) {
	if len(x.run.State.Children) == 0 {
		return true, nil
	}
	store, ok := x.e.Store.(FlowChildStore)
	if !ok {
		return false, errors.New("durable child cancellation unavailable")
	}
	if err := store.CancelFlowChildren(ctx, x.run); err != nil {
		return false, err
	}
	all := true
	for _, ref := range x.run.State.Children {
		done, err := x.cancelledChild(ctx, ref)
		if err != nil {
			return false, err
		}
		all = all && done
	}
	return all, nil
}

func (x *runExec) cancelledChild(ctx context.Context, ref FlowChild) (bool, error) {
	if ref.Kind == "workflow" {
		if err := x.e.Drive(ctx, ref.ID); err != nil {
			return false, err
		}
		run, err := x.e.Store.GetRun(ctx, ref.ID)
		return terminal(run.State.Status), err
	}
	if ref.Kind != "meeting" || x.e.Meetings == nil {
		return false, errors.New("meeting cancellation unavailable")
	}
	if err := x.e.DriveMeeting(x.childCallContext(ctx, ref), ref.ID); err != nil {
		return false, err
	}
	rec, err := x.e.Meetings.GetMeeting(ctx, ref.ID)
	return terminalMeeting(rec.Meeting.Status), err
}
