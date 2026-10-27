package memory

import (
	"context"
	"time"
)

// Resolve the displayed child question using server-owned lineage. The entire
// chain and decision commit together, so a stale parent cannot approve a new
// question at the child or a cancelled run.
func (s *Service) decideFlowChild(ctx context.Context, tx Tx, token string, parent Record, in CrewRunDecisionInput, actor Decision, depth int, discord bool) (Record, error) {
	if depth > 8 || !flowDecisionMatches(parent, in) {
		return Record{}, ErrConflict
	}
	w := parent.CrewRun.Waiting
	if actor.Role == roleService && (w.DecisionKind != "budget" || !discord) {
		return Record{}, ErrDenied
	}
	ref, ok := parent.CrewRun.Children[flowChildKey(w.NodeID, parent.CrewRun.Nodes[w.NodeID].Runs)]
	if !ok || ref.ID != w.ChildRunID || ref.Kind != w.ChildKind {
		return Record{}, ErrConflict
	}
	child, err := flowChildRecord(ctx, tx, in.Scope, ref)
	if err != nil {
		return Record{}, err
	}
	if !flowChildBelongs(parent, ref, child) || child.Revision != w.ChildRevision || !flowChildQuestionMatches(w, child) {
		return Record{}, ErrConflict
	}
	if _, err = s.authorize(ctx, token, "read:"+colCrew, in.Scope, ref.CrewID, ""); err != nil {
		return Record{}, err
	}
	col := colCrewRuns
	if ref.Kind == "meeting" {
		col = colCrewMeetings
	}
	d, err := s.authorize(ctx, token, "write:"+col, in.Scope, child.ID, "")
	if err != nil {
		return Record{}, err
	}
	if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
		return Record{}, ErrDenied
	}
	if ref.Kind == "meeting" {
		err = decideLinkedMeeting(ctx, tx, child, in.Approve, actor)
	} else {
		err = s.decideLinkedRun(ctx, tx, token, child, in, actor, depth, discord)
	}
	if err != nil {
		return Record{}, err
	}
	run, wait := *parent.CrewRun, *w
	wait.DecisionKind = ""
	run.Waiting = &wait
	parent.CrewRun = &run
	return storeFlowDecision(ctx, tx, colCrewRuns, parent, actor)
}

func flowChildQuestionMatches(w *CrewRunWaiting, child Record) bool {
	if child.CrewRun != nil && child.CrewRun.Waiting != nil {
		cw := child.CrewRun.Waiting
		kind := cw.Kind
		if kind == "child" {
			kind = cw.DecisionKind
		}
		return kind == w.DecisionKind && cw.Question == w.Question
	}
	if child.CrewMeeting != nil && child.CrewMeeting.Waiting != nil {
		cw := child.CrewMeeting.Waiting
		return cw.Kind == w.DecisionKind && cw.Reason == w.Question
	}
	return false
}
func flowDecisionMatches(r Record, in CrewRunDecisionInput) bool {
	if !readableCrewRun(r) || r.Revision != in.ExpectedRevision || r.CrewRun.CancelRequested {
		return false
	}
	w := r.CrewRun.Waiting
	return r.CrewRun.Status == "waiting" && w != nil && w.Kind == "child" && w.NodeID == in.NodeID && (w.DecisionKind == "approval" || w.DecisionKind == "budget")
}

func (s *Service) decideLinkedRun(ctx context.Context, tx Tx, token string, child Record, in CrewRunDecisionInput, actor Decision, depth int, discord bool) error {
	run := *child.CrewRun
	w := run.Waiting
	if run.CancelRequested || run.Status != "waiting" || w == nil {
		return ErrConflict
	}
	childIn := in
	childIn.ExpectedRevision, childIn.NodeID = child.Revision, w.NodeID
	if w.Kind == "child" {
		_, err := s.decideFlowChild(ctx, tx, token, child, childIn, actor, depth+1, discord)
		return err
	}
	if w.Kind != "approval" && w.Kind != "budget" {
		return ErrConflict
	}
	if actor.Role == roleService && (w.Kind != "budget" || !discord) {
		return ErrDenied
	}
	if _, exists := run.Decisions[w.NodeID]; exists {
		return ErrConflict
	}
	if err := s.decideFlowEffect(ctx, tx, token, child, in.Approve, actor); err != nil {
		return err
	}
	decisions := make(map[string]CrewRunDecision, len(run.Decisions)+1)
	for k, v := range run.Decisions {
		decisions[k] = v
	}
	decisions[w.NodeID] = CrewRunDecision{Approve: in.Approve, Note: in.Note, DecidedBy: actor.PrincipalID, DecidedAt: time.Now().UTC().Truncate(time.Millisecond)}
	run.Decisions = decisions
	child.CrewRun = &run
	_, err := storeFlowDecision(ctx, tx, colCrewRuns, child, actor)
	return err
}

func decideLinkedMeeting(ctx context.Context, tx Tx, child Record, approve bool, actor Decision) error {
	if child.CrewMeeting == nil {
		return ErrConflict
	}
	m := *child.CrewMeeting
	if m.Status != "waiting" || m.Waiting == nil || m.Waiting.Kind != "budget" {
		return ErrConflict
	}
	m.Waiting, m.LeaseUntil = nil, nil
	child.LeaseToken, child.LeaseUntil = "", time.Time{}
	if approve {
		m.Status, m.BudgetApproved = "running", &MeetingApproval{By: actor.PrincipalID, At: time.Now().UTC().Truncate(time.Millisecond)}
	} else {
		m.Status, m.EndedReason, m.Speaking = "stopped", "budget", ""
	}
	child.CrewMeeting = &m
	_, err := storeFlowDecision(ctx, tx, colCrewMeetings, child, actor)
	return err
}

func storeFlowDecision(ctx context.Context, tx Tx, col string, r Record, actor Decision) (Record, error) {
	prev := r.Revision
	r.Revision++
	r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	if err := tx.Replace(ctx, col, r, prev); err != nil {
		return Record{}, err
	}
	return r, audit(ctx, tx, r.Scope, actor.PrincipalID, "decide:"+col, r.ID)
}
