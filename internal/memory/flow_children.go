package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

type FlowChildInput struct {
	Scope      Scope  `json:"scope"`
	LeaseToken string `json:"lease_token"`
	NodeID     string `json:"node_id"`
	Attempt    int    `json:"attempt"`
	Kind       string `json:"kind"`
	CrewID     string `json:"crew_id"`
	Input      string `json:"input"`
}
type FlowChildCreated struct {
	Parent Record       `json:"parent"`
	Child  FlowChildRef `json:"child"`
}
type FlowChildrenCancelled struct {
	Children []FlowChildRef `json:"children"`
}

type FlowChildrenCancel struct {
	Scope      Scope  `json:"scope"`
	LeaseToken string `json:"lease_token"`
}
type flowLimitError string

func (e flowLimitError) Error() string { return string(e) }

const (
	errFlowDepthLimit        flowLimitError = "flow_depth_limit"
	errFlowChildLimit        flowLimitError = "flow_child_limit"
	errFlowConcurrencyLimit  flowLimitError = "flow_concurrency_limit"
	errFlowBudgetLimit       flowLimitError = "flow_budget_limit"
	errFlowBudgetUnsupported flowLimitError = "flow_budget_unsupported"
)

func checkFlowChildrenTerminal(ctx context.Context, tx Tx, parent Record, status string) error {
	if parent.CrewRun.InvocationMode != "flow" || !terminalCrewRun(status) {
		return nil
	}
	return walkFlowChildren(ctx, tx, parent, func(ref FlowChildRef, r Record) error {
		done, success := flowChildTerminal(r)
		if !done {
			return ErrConflict
		}
		if status == "succeeded" && ref.Required && !success {
			return ErrConflict
		}
		return nil
	})
}
func flowChildKey(node string, attempt int) string { return fmt.Sprintf("%s:%d", node, attempt) }
func flowLeaseMatches(r Record, token string) bool {
	return token != "" && r.LeaseToken == token && r.LeaseUntil.After(time.Now())
}
func flowNode(c *Crew, id string) (CrewNode, bool) {
	if c == nil || c.Graph == nil {
		return CrewNode{}, false
	}
	for _, n := range c.Graph.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return CrewNode{}, false
}
func flowChildTarget(parent Record, in FlowChildInput) (FlowDefinition, error) {
	st := parent.CrewRun
	n, ok := flowNode(st.Definition, in.NodeID)
	if !ok {
		return FlowDefinition{}, ErrInvalid
	}
	d, ok := st.Definitions[in.CrewID]
	if !ok {
		return FlowDefinition{}, ErrInvalid
	}
	state, ok := st.Nodes[in.NodeID]
	if !ok || state.Runs != in.Attempt || state.Status != "running" {
		return FlowDefinition{}, ErrConflict
	}
	if in.Kind == "meeting" && n.Type == "team" && n.Config["team"] == d.ID && d.Crew.Type == "team" {
		return d, nil
	}
	if in.Kind == "workflow" && n.Type == "subflow" && crewKey(n.Config["workflow"]) == crewKey(d.Crew.Handle) && d.Crew.Type == "workflow" {
		return d, nil
	}
	return FlowDefinition{}, ErrInvalid
}
func flowChildRecord(ctx context.Context, tx Tx, scope Scope, ref FlowChildRef) (Record, error) {
	col := colCrewRuns
	if ref.Kind == "meeting" {
		col = colCrewMeetings
	}
	r, err := tx.Get(ctx, col, scope, ref.ID)
	if err != nil {
		return Record{}, err
	}
	if r.Status != statusActive || r.ContentErased || r.PrivacyQuarantined {
		return Record{}, ErrNotFound
	}
	return r, nil
}
func flowChildBelongs(parent Record, ref FlowChildRef, child Record) bool {
	if child.CrewRun != nil {
		c := child.CrewRun
		return ref.Kind == "workflow" && c.ParentID == parent.ID && c.ParentNodeID == ref.NodeID && c.RootID == parent.CrewRun.RootID && c.CrewID == ref.CrewID
	}
	if child.CrewMeeting != nil {
		c := child.CrewMeeting
		return ref.Kind == "meeting" && c.ParentID == parent.ID && c.ParentNodeID == ref.NodeID && c.RootID == parent.CrewRun.RootID && c.TeamID == ref.CrewID
	}
	return false
}
func flowChildTerminal(r Record) (done, success bool) {
	if r.CrewRun != nil {
		return terminalCrewRun(r.CrewRun.Status), r.CrewRun.Status == "succeeded"
	}
	if r.CrewMeeting != nil {
		return terminalMeeting(r.CrewMeeting.Status), r.CrewMeeting.Status == "concluded" && r.CrewMeeting.Conclusion != nil
	}
	// No run or meeting: the child is gone, so it never runs again.
	return true, false
}
func walkFlowChildren(ctx context.Context, tx Tx, root Record, visit func(FlowChildRef, Record) error) error {
	seen := map[string]bool{}
	var walk func(Record) error
	walk = func(parent Record) error {
		if parent.CrewRun == nil {
			return nil
		}
		for _, ref := range parent.CrewRun.Children {
			if seen[ref.ID] {
				return ErrInvalid
			}
			seen[ref.ID] = true
			if len(seen) > 100 {
				return ErrInvalid
			}
			child, err := flowChildRecord(ctx, tx, root.Scope, ref)
			if errors.Is(err, ErrNotFound) {
				// A child removed by privacy deletion ended without a result: visit it as finished, not succeeded.
				if err = visit(ref, Record{}); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if !flowChildBelongs(parent, ref, child) {
				return ErrConflict
			}
			if err = visit(ref, child); err != nil {
				return err
			}
			if err = walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}
func checkFlowChildLimits(ctx context.Context, tx Tx, parent Record, target string) error {
	p := parent.CrewRun.Policy
	if p == nil || parent.CrewRun.Depth >= p.MaxDepth {
		return errFlowDepthLimit
	}
	ancestors := map[string]bool{}
	current := parent
	for i := 0; i <= p.MaxDepth; i++ {
		ancestors[current.ID] = true
		if current.CrewRun.CrewID == target {
			return ErrInvalid
		}
		if current.CrewRun.ParentID == "" {
			break
		}
		next, err := tx.Get(ctx, colCrewRuns, parent.Scope, current.CrewRun.ParentID)
		if err != nil {
			return err
		}
		if !readableCrewRun(next) {
			return ErrNotFound
		}
		current = next
	}
	root, err := tx.Get(ctx, colCrewRuns, parent.Scope, parent.CrewRun.RootID)
	if err != nil {
		return err
	}
	count, active := 0, 0
	err = walkFlowChildren(ctx, tx, root, func(_ FlowChildRef, r Record) error {
		count++
		done, _ := flowChildTerminal(r)
		if !done && !ancestors[r.ID] {
			active++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count >= p.MaxChildren {
		return errFlowChildLimit
	}
	if active >= p.MaxConcurrent {
		return errFlowConcurrencyLimit
	}
	return nil
}
func (s *Service) CreateFlowChild(ctx context.Context, token, id string, in FlowChildInput) (FlowChildCreated, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || !validRunLabel(in.NodeID, 40) || in.Attempt < 1 || in.Attempt > 10000 || !slices.Contains([]string{"workflow", "meeting"}, in.Kind) || runes(in.Input) > 20000 {
		return FlowChildCreated{}, ErrInvalid
	}
	if err := noSecrets(in.Input); err != nil {
		return FlowChildCreated{}, err
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return FlowChildCreated{}, err
	}
	if !crewRunWriter(d.Role) {
		return FlowChildCreated{}, ErrDenied
	}
	if _, err = s.authorize(ctx, token, "read:"+colCrew, in.Scope, in.CrewID, ""); err != nil {
		return FlowChildCreated{}, err
	}
	if in.Kind == "meeting" {
		if _, err = s.authorize(ctx, token, "write:"+colCrewMeetings, in.Scope, "", ""); err != nil {
			return FlowChildCreated{}, err
		}
	}
	var out FlowChildCreated
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		parent, e := tx.Get(ctx, colCrewRuns, in.Scope, id)
		if e != nil {
			return e
		}
		if !readableCrewRun(parent) || parent.CrewRun.InvocationMode != "flow" {
			return ErrNotFound
		}
		if e := s.authorizeFlowSourcesFrom(ctx, token, parent, tx); e != nil {
			return e
		}
		if !flowLeaseMatches(parent, in.LeaseToken) || parent.CrewRun.CancelRequested || terminalCrewRun(parent.CrewRun.Status) {
			return ErrConflict
		}
		target, e := flowChildTarget(parent, in)
		if e != nil {
			return e
		}
		key := flowChildKey(in.NodeID, in.Attempt)
		if ref, ok := parent.CrewRun.Children[key]; ok {
			child, e := flowChildRecord(ctx, tx, in.Scope, ref)
			if e != nil {
				return e
			}
			if child.RequestHash != flowChildHash(in) {
				return ErrConflict
			}
			out = FlowChildCreated{parent, ref}
			return nil
		}
		cycleTarget := target.ID
		if in.Kind == "meeting" {
			cycleTarget = ""
		}
		if e = checkFlowChildLimits(ctx, tx, parent, cycleTarget); e != nil {
			return e
		}
		live, e := tx.Get(ctx, colCrew, in.Scope, target.ID)
		if e != nil {
			return e
		}
		if !liveFlowTarget(live) {
			return ErrNotFound
		}
		child, e := newFlowChild(parent, target, in)
		if e != nil {
			return e
		}
		col := colCrewRuns
		if in.Kind == "meeting" {
			col = colCrewMeetings
		}
		if e = tx.Insert(ctx, col, child); e != nil {
			return e
		}
		ref := FlowChildRef{in.Kind, child.ID, target.ID, in.NodeID, in.Attempt, true}
		st := *parent.CrewRun
		st.Children = cloneFlowChildren(st.Children)
		st.Children[key] = ref
		parent.CrewRun = &st
		rev := parent.Revision
		parent.Revision++
		parent.UpdatedAt = child.CreatedAt
		if e = tx.Replace(ctx, colCrewRuns, parent, rev); e != nil {
			return e
		}
		out = FlowChildCreated{parent, ref}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "flow_child:create", parent.ID)
	})
	return out, err
}
func flowChildHash(in FlowChildInput) string {
	return ContentHash(structuredText([]any{in.NodeID, in.Attempt, in.Kind, in.CrewID, in.Input}))
}
func cloneFlowChildren(in map[string]FlowChildRef) map[string]FlowChildRef {
	out := map[string]FlowChildRef{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func newFlowChild(parent Record, target FlowDefinition, in FlowChildInput) (Record, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := "child-" + ContentHash(structuredText([]any{parent.ID, in.NodeID, in.Attempt, in.Kind}))
	r := Record{ID: id, Scope: parent.Scope, Kind: colCrewRuns, Status: statusActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: parent.CreatedBy, CreatedByType: parent.CreatedByType, RequestHash: flowChildHash(in)}
	if in.Kind == "meeting" {
		r.Kind = colCrewMeetings
		m, e := newFlowMeeting(parent, target, in.Input)
		if m != nil {
			m.ParentNodeID = in.NodeID
		}
		r.CrewMeeting = m
		return r, e
	}
	run := CrewRun{CrewID: target.ID, CrewRevision: target.Revision, Trigger: "call", InvocationMode: "flow", Status: "running", Input: in.Input, RootID: parent.CrewRun.RootID, ParentID: parent.ID, ParentNodeID: in.NodeID, Depth: parent.CrewRun.Depth + 1, Deadline: parent.CrewRun.Deadline}
	if e := prepareFlowRun(&run, Record{ID: target.ID, Revision: target.Revision, Crew: &target.Crew}, "", false, parent.CrewRun.Definitions); e != nil {
		return Record{}, e
	}
	run.Policy = parent.CrewRun.Policy
	r.CrewRun = &run
	return r, nil
}
func newFlowMeeting(parent Record, target FlowDefinition, input string) (*CrewMeeting, error) {
	topic := input
	var data map[string]any
	if json.Unmarshal([]byte(input), &data) == nil {
		if v, ok := data["topic"].(string); ok {
			topic = v
		}
	}
	if strings.TrimSpace(topic) == "" {
		return nil, ErrInvalid
	}
	end, ok := meetingEnding(&target.Crew, "")
	if !ok {
		return nil, ErrInvalid
	}
	agents := map[string]Crew{}
	for _, m := range target.Crew.Members {
		d, ok := parent.CrewRun.Definitions[m.AgentID]
		if !ok {
			return nil, ErrInvalid
		}
		agents[m.AgentID] = d.Crew
	}
	records := map[string]Record{}
	for id, c := range agents {
		crew := c
		records[id] = Record{ID: id, Crew: &crew}
	}
	estimate := estimateTeam(target.Crew, records)
	m := &CrewMeeting{CostEstimateUSD: estimate.CostUSD, RootID: parent.CrewRun.RootID, ParentID: parent.ID, TeamID: target.ID, TeamRevision: target.Revision, TeamSnapshot: &MeetingSnapshot{Team: target.Crew, Agents: agents}, Status: "pending", Trigger: "workflow", Title: target.Crew.Name, Topic: topic, MaxRounds: end.MaxRounds, TokenCap: end.TokenCap, Turns: []MeetingTurn{}, NotesPending: []MeetingNote{}}
	return m, nil
}
func (s *Service) CancelFlowChildren(ctx context.Context, token, id string, in FlowChildrenCancel) ([]FlowChildRef, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) {
		return nil, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return nil, err
	}
	if !crewRunWriter(d.Role) {
		return nil, ErrDenied
	}
	out := []FlowChildRef{}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		parent, e := tx.Get(ctx, colCrewRuns, in.Scope, id)
		if e != nil {
			return e
		}
		if !readableCrewRun(parent) {
			return ErrNotFound
		}
		if !flowLeaseMatches(parent, in.LeaseToken) {
			return ErrConflict
		}
		return walkFlowChildren(ctx, tx, parent, func(ref FlowChildRef, r Record) error {
			out = append(out, ref)
			done, _ := flowChildTerminal(r)
			if done {
				return nil
			}
			rev := r.Revision
			col := colCrewRuns
			if r.CrewRun != nil {
				st := *r.CrewRun
				st.CancelRequested = true
				r.CrewRun = &st
			} else {
				col = colCrewMeetings
				m := *r.CrewMeeting
				m.Status = "stopping"
				m.Waiting = nil
				r.CrewMeeting = &m
			}
			r.Revision++
			r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
			return tx.Replace(ctx, col, r, rev)
		})
	})
	return out, err
}
func flowChildrenRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/crew/runs/{id}/children", func(w http.ResponseWriter, r *http.Request) {
		var in FlowChildInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateFlowChild(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs/{id}/children/cancel", func(w http.ResponseWriter, r *http.Request) {
		var in FlowChildrenCancel
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CancelFlowChildren(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, FlowChildrenCancelled{Children: out}, e)
	})
}
