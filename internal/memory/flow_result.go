package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/FelixBaek/arkism/internal/flowresult"
)

type FlowResultView struct {
	Budget          *FlowBudget              `json:"budget,omitempty"`
	RunID           string                   `json:"run_id"`
	Revision        int64                    `json:"revision"`
	CrewID          string                   `json:"crew_id"`
	Status          string                   `json:"status"`
	InvocationMode  string                   `json:"invocation_mode"`
	SourceType      string                   `json:"source_type"`
	CancelRequested bool                     `json:"cancel_requested"`
	Result          string                   `json:"result"`
	Waiting         *CrewRunWaiting          `json:"waiting,omitempty"`
	Children        map[string]FlowChildView `json:"children,omitempty"`
	Delivery        *FlowDeliveryView        `json:"delivery,omitempty"`
}

// FlowChildView is a child reference plus the summary a result screen lists.
// Summary fields stay empty when the reader cannot read the child itself.
type FlowChildView struct {
	FlowChildRef
	Status     string     `json:"status,omitempty"`
	Name       string     `json:"name,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Tokens     int64      `json:"tokens,omitempty"`
	Round      int        `json:"round,omitempty"`
}

func (s *Service) flowChildViews(ctx context.Context, token string, scope Scope, refs map[string]FlowChildRef) map[string]FlowChildView {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]FlowChildView, len(refs))
	for key, ref := range refs {
		v := FlowChildView{FlowChildRef: ref}
		get := s.GetCrewRun
		if ref.Kind == "meeting" {
			get = s.GetCrewMeeting
		}
		// The child read rechecks access; a denied or missing child keeps the bare reference.
		if r, err := get(ctx, token, scope, ref.ID); err == nil {
			fillFlowChildView(&v, r)
		}
		out[key] = v
	}
	return out
}

// fillFlowChildView adds the summary of the child record r the reader could read.
func fillFlowChildView(v *FlowChildView, r Record) {
	started := r.CreatedAt
	v.StartedAt = &started
	if done, _ := flowChildTerminal(r); done {
		finished := r.UpdatedAt
		v.FinishedAt = &finished
	}
	if run := r.CrewRun; run != nil {
		v.Status = run.Status
		if run.Definition != nil {
			v.Name = run.Definition.Name
		}
		for _, n := range run.Nodes {
			v.Tokens += n.Tokens
		}
	} else if m := r.CrewMeeting; m != nil {
		v.Status, v.Name, v.Tokens, v.Round = m.Status, m.Title, m.TokensUsed, m.Round
	}
}

func (s *Service) FlowResult(ctx context.Context, token string, scope Scope, id string) (FlowResultView, error) {
	r, err := s.GetCrewRun(ctx, token, scope, id)
	if err != nil {
		return FlowResultView{}, err
	}
	run := r.CrewRun
	raw, _ := json.Marshal(run)
	out := FlowResultView{Budget: r.FlowBudget, RunID: r.ID, Revision: r.Revision, CrewID: run.CrewID, Status: run.Status, InvocationMode: run.InvocationMode, SourceType: run.SourceType, CancelRequested: run.CancelRequested, Result: flowresult.Text(raw), Waiting: run.Waiting, Children: s.flowChildViews(ctx, token, scope, run.Children)}
	d, err := s.Repo.Get(ctx, colJobs, scope, flowDeliveryID(id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return FlowResultView{}, err
	}
	if readableFlowDelivery(d) {
		v := flowDeliveryView(d)
		out.Delivery = &v
	}
	return out, nil
}

// Parent outputs and pinned definitions can contain child material. Recheck
// each source on every read, including ordinary get/list used by MCP workers.
func (s *Service) authorizeFlowSources(ctx context.Context, token string, r Record) error {
	return s.authorizeFlowSourcesFrom(ctx, token, r, s.Repo)
}

type flowRecordReader interface {
	Get(context.Context, string, Scope, string) (Record, error)
}

func (s *Service) authorizeFlowSourcesFrom(ctx context.Context, token string, r Record, reader flowRecordReader) error {
	if r.CrewRun == nil || r.CrewRun.InvocationMode != "flow" {
		return nil
	}
	for source := range r.CrewRun.Definitions {
		if _, err := s.authorize(ctx, token, "read:"+colCrew, r.Scope, source, ""); err != nil {
			return err
		}
		live, err := reader.Get(ctx, colCrew, r.Scope, source)
		if err != nil {
			return err
		}
		if live.ContentErased || live.PrivacyQuarantined {
			return ErrNotFound
		}
	}
	return nil
}

func flowResultRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/crew/runs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.FlowResult(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, err)
	})
}
