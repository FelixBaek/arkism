package memory

import (
	"context"
	"net/http"
	"time"
)

type FlowLeaseInput struct {
	Scope      Scope  `json:"scope"`
	Action     string `json:"action"`
	LeaseToken string `json:"lease_token"`
}
type FlowLeaseView struct {
	LeaseUntil      time.Time `json:"lease_until"`
	CancelRequested bool      `json:"cancel_requested"`
}
type FlowStopInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func (s *Service) FlowLease(ctx context.Context, token, id string, in FlowLeaseInput) (FlowLeaseView, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || !validRunLabel(in.LeaseToken, 128) || len(in.LeaseToken) < 16 || (in.Action != "acquire" && in.Action != "renew" && in.Action != "release") {
		return FlowLeaseView{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return FlowLeaseView{}, err
	}
	if !crewRunWriter(d.Role) {
		return FlowLeaseView{}, ErrDenied
	}
	var out FlowLeaseView
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colCrewRuns, in.Scope, id)
		if e != nil {
			return e
		}
		if !readableCrewRun(r) {
			return ErrNotFound
		}
		if r.CrewRun.InvocationMode != "flow" {
			return ErrInvalid
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		if in.Action == "release" {
			if r.LeaseToken != in.LeaseToken {
				return ErrConflict
			}
			r.LeaseToken = ""
			r.LeaseUntil = time.Time{}
		} else {
			if terminalCrewRun(r.CrewRun.Status) {
				return ErrConflict
			}
			if in.Action == "renew" && (r.LeaseToken != in.LeaseToken || !r.LeaseUntil.After(now)) {
				return ErrConflict
			}
			if in.Action == "acquire" && r.LeaseUntil.After(now) && r.LeaseToken != in.LeaseToken {
				return ErrConflict
			}
			r.LeaseToken = in.LeaseToken
			r.LeaseUntil = now.Add(90 * time.Second)
		}
		// Serialize metadata without changing the execution's content revision.
		if e = tx.Replace(ctx, colCrewRuns, r, r.Revision); e != nil {
			return e
		}
		out = FlowLeaseView{r.LeaseUntil, r.CrewRun.CancelRequested}
		return nil
	})
	return out, err
}

func (s *Service) StopFlow(ctx context.Context, token, id string, in FlowStopInput) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colCrewRuns, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if !crewRunWriter(d.Role) {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colCrewRuns, in.Scope, id)
		if e != nil {
			return e
		}
		if !readableCrewRun(r) {
			return ErrNotFound
		}
		if d.Role != roleHuman && r.CreatedBy != d.PrincipalID {
			return ErrDenied
		}
		if terminalCrewRun(r.CrewRun.Status) || r.CrewRun.CancelRequested {
			out = s.flowStopResponse(ctx, token, r, tx)
			return nil
		}
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		if r.CrewRun.InvocationMode != "flow" {
			return ErrInvalid
		}
		prev := r.Revision
		run := *r.CrewRun
		run.CancelRequested = true
		r.CrewRun = &run
		r.Revision++
		r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if e = tx.Replace(ctx, colCrewRuns, r, prev); e != nil {
			return e
		}
		out = s.flowStopResponse(ctx, token, r, tx)
		return audit(ctx, tx, in.Scope, d.PrincipalID, "cancel:"+colCrewRuns, id)
	})
	return out, err
}

func flowExecutionRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/crew/runs/{id}/lease", func(w http.ResponseWriter, r *http.Request) {
		var in FlowLeaseInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.FlowLease(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/crew/runs/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		var in FlowStopInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.StopFlow(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
