package memory

import (
	"time"
)

// ToolCall describes a completed invocation; Record.Status is its visibility,
// not the invocation outcome. Target pins the associated agent_runs revision.
type ToolCall struct {
	Name       string    `json:"name" bson:"name"`
	Outcome    string    `json:"outcome" bson:"outcome"`
	StartedAt  time.Time `json:"started_at" bson:"started_at"`
	FinishedAt time.Time `json:"finished_at" bson:"finished_at"`
}

func validToolCall(x *ToolCall, target *Ref, now time.Time) bool {
	return x != nil && target != nil && target.Collection == "agent_runs" &&
		validRunLabel(target.ID, 256) && target.Revision > 0 &&
		validRunLabel(x.Name, 128) &&
		terminalOutcome(x.Outcome) &&
		!x.StartedAt.Truncate(time.Millisecond).IsZero() && !x.FinishedAt.Truncate(time.Millisecond).IsZero() && !x.FinishedAt.Before(x.StartedAt) && !x.FinishedAt.After(now)
}
