package memory

import (
	"context"
	"strings"
	"time"
	"unicode"
)

type RunDetails struct {
	AgentID    string     `json:"agent_id" bson:"agent_id"`
	TaskType   string     `json:"task_type" bson:"task_type"`
	Outcome    string     `json:"outcome" bson:"outcome"`
	StartedAt  time.Time  `json:"started_at" bson:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
}
type FinishRunInput struct {
	Scope            Scope     `json:"scope"`
	ExpectedRevision int64     `json:"expected_revision"`
	Outcome          string    `json:"outcome"`
	FinishedAt       time.Time `json:"finished_at"`
}

func terminalOutcome(outcome string) bool {
	return outcome == "success" || outcome == "partial" || outcome == "failed"
}
func validRun(x *RunDetails, scope Scope, now time.Time) bool {
	if x == nil || !validRunLabel(x.AgentID, 256) || !validRunLabel(x.TaskType, 128) || scope.AgentID != "" && scope.AgentID != x.AgentID || x.StartedAt.Truncate(time.Millisecond).IsZero() || x.StartedAt.After(now) {
		return false
	}
	if x.Outcome == "running" {
		return x.FinishedAt == nil
	}
	return terminalOutcome(x.Outcome) && x.FinishedAt != nil && !x.FinishedAt.Truncate(time.Millisecond).IsZero() && !x.FinishedAt.Before(x.StartedAt) && !x.FinishedAt.After(now)
}
func validRunLabel(value string, max int) bool {
	return len(value) > 0 && len(value) <= max && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func (s *Service) FinishRun(ctx context.Context, token, id string, in FinishRunInput) (Ref, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || !terminalOutcome(in.Outcome) || in.FinishedAt.Truncate(time.Millisecond).IsZero() || in.FinishedAt.After(time.Now()) {
		return Ref{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "finish:agent_runs", in.Scope, id, "")
	if e != nil {
		return Ref{}, e
	}
	if actor.Role != "human" && actor.Role != "agent" && actor.Role != "service" && actor.Role != "pipeline" {
		return Ref{}, ErrDenied
	}
	if s.Repo == nil {
		return Ref{}, ErrUnavailable
	}
	var out Ref
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = Ref{}
		r, e := tx.Get(ctx, "agent_runs", in.Scope, id)
		if e != nil {
			return e
		}
		if actor.Role == "agent" && r.CreatedBy != actor.PrincipalID {
			return ErrDenied
		}
		if r.Revision != in.ExpectedRevision || !live(r) || !validRun(r.Run, r.Scope, time.Now()) || r.Run.Outcome != "running" {
			return ErrConflict
		}
		if in.FinishedAt.Before(r.Run.StartedAt) {
			return ErrInvalid
		}
		if e = checkSources(ctx, tx, r); e != nil {
			return e
		}
		if e = tx.CheckRunToolTimes(ctx, in.Scope, id, in.FinishedAt.UTC().Truncate(time.Millisecond)); e != nil {
			return e
		}
		details := *r.Run
		finished := in.FinishedAt.UTC().Truncate(time.Millisecond)
		details.Outcome = in.Outcome
		details.FinishedAt = &finished
		r.Run = &details
		r.Revision++
		r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if e = tx.Replace(ctx, "agent_runs", r, in.ExpectedRevision); e != nil {
			return e
		}
		if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "finish:agent_runs", id); e != nil {
			return e
		}
		out = Ref{Collection: "agent_runs", ID: id, Revision: r.Revision}
		return nil
	})
	if e != nil {
		return Ref{}, e
	}
	return out, nil
}
