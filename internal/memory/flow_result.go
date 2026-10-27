package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/FelixBaek/arkism/internal/flowresult"
)

type FlowResultView struct {
	Budget          *FlowBudget             `json:"budget,omitempty"`
	RunID           string                  `json:"run_id"`
	Revision        int64                   `json:"revision"`
	CrewID          string                  `json:"crew_id"`
	Status          string                  `json:"status"`
	InvocationMode  string                  `json:"invocation_mode"`
	SourceType      string                  `json:"source_type"`
	CancelRequested bool                    `json:"cancel_requested"`
	Result          string                  `json:"result"`
	Waiting         *CrewRunWaiting         `json:"waiting,omitempty"`
	Children        map[string]FlowChildRef `json:"children,omitempty"`
	Delivery        *FlowDeliveryView       `json:"delivery,omitempty"`
}

func (s *Service) FlowResult(ctx context.Context, token string, scope Scope, id string) (FlowResultView, error) {
	r, err := s.GetCrewRun(ctx, token, scope, id)
	if err != nil {
		return FlowResultView{}, err
	}
	run := r.CrewRun
	raw, _ := json.Marshal(run)
	out := FlowResultView{Budget: r.FlowBudget, RunID: r.ID, Revision: r.Revision, CrewID: run.CrewID, Status: run.Status, InvocationMode: run.InvocationMode, SourceType: run.SourceType, CancelRequested: run.CancelRequested, Result: flowresult.Text(raw), Waiting: run.Waiting, Children: run.Children}
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
