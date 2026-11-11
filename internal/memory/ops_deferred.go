package memory

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// A deferred call is a model-backed route that outlived its request: the caller gets 202 with an
// operation to read, the work keeps running, and the answer lands in that operation's result.
// shortcut: finished rows are never swept and cannot be cancelled or deleted through the
// operations API; add a retention sweep when a space collects enough of them to matter.
const (
	deferredCallKind    = "deferred_call"
	deferredInterrupted = "interrupted"
	deferredCallMax     = 15 * time.Minute
)

// deferredWait is how long such a route answers inside the request. It stays under the desktop
// bridge's 30s call limit and the server's 45s request limit.
var deferredWait = 20 * time.Second

type DeferredCall struct {
	Route  string          `json:"route"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// DeferredAccepted is the 202 body: where to read the outcome.
type DeferredAccepted struct {
	OperationID string `json:"operation_id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Location    string `json:"location"`
}

type deferredOutcome struct {
	v any
	e error
}

// respondLater answers like respond when run finishes within deferredWait. Past that it stores a
// running operation, answers 202, and lets run finish on a context the request no longer cancels.
func (s *Service) respondLater(w http.ResponseWriter, r *http.Request, scope Scope, run func(ctx context.Context, token string) (any, error)) {
	token := bearer(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), deferredCallMax)
	done := make(chan deferredOutcome, 1)
	go func() {
		var out deferredOutcome
		defer func() {
			if recover() != nil {
				out = deferredOutcome{e: ErrUnavailable}
			}
			done <- out
		}()
		out.v, out.e = run(ctx, token)
	}()
	timer := time.NewTimer(deferredWait)
	defer timer.Stop()
	select {
	case out := <-done:
		cancel()
		respond(w, out.v, out.e)
		return
	case <-timer.C:
	}
	row, err := s.acceptDeferred(ctx, token, scope, r.Pattern)
	if err != nil { // no operation to point at: wait in the request as before
		out := <-done
		cancel()
		respond(w, out.v, out.e)
		return
	}
	go func() {
		defer cancel()
		s.finishDeferred(ctx, row, <-done)
	}()
	q := url.Values{"workspace_id": {scope.WorkspaceID}, "project_id": {scope.ProjectID}, "user_id": {scope.UserID}, "agent_id": {scope.AgentID}}
	location := "/v1/operations/" + row.ID + "?" + q.Encode()
	w.Header().Set("Location", location)
	writeJSON(w, http.StatusAccepted, DeferredAccepted{OperationID: row.ID, Kind: row.Kind, Status: row.Status, Location: location})
}

func (s *Service) acceptDeferred(ctx context.Context, token string, scope Scope, route string) (Record, error) {
	d, err := s.opsAuthorize(ctx, token, "read:jobs", scope, "", roleHuman, roleService, roleAgent)
	if err != nil {
		return Record{}, err
	}
	if s.Repo == nil {
		return Record{}, ErrUnavailable
	}
	now := time.Now().UTC()
	row := Record{ID: "deferred-" + ID(), Scope: scope, Kind: deferredCallKind, Status: JobStatusRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
		CreatedBy: d.PrincipalID, CreatedByType: d.Role, LeaseUntil: now.Add(deferredCallMax + time.Minute), Deferred: &DeferredCall{Route: route}}
	return row, s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colJobs, row); err != nil {
			return err
		}
		return audit(ctx, tx, scope, d.PrincipalID, "deferred_call:accepted", row.ID)
	})
}

func (s *Service) finishDeferred(ctx context.Context, row Record, out deferredOutcome) {
	call := DeferredCall{Route: row.Deferred.Route}
	status := JobStatusDone
	if out.e == nil {
		call.Result, out.e = json.Marshal(out.v)
	}
	if out.e != nil {
		status = JobStatusFailed
		call.Error, _ = errorResponse(out.e)
	}
	// The work's own deadline may be what ended it, so the outcome is written on a fresh one.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colJobs, row.Scope, row.ID) // the stored row, so its origin fields match exactly
		if err != nil {
			return err
		}
		prev := cur.Revision
		cur.Status, cur.Deferred, cur.Revision, cur.UpdatedAt, cur.LeaseUntil = status, &call, prev+1, time.Now().UTC(), time.Time{}
		return tx.Replace(ctx, colJobs, cur, prev)
	})
	if err != nil {
		slog.Warn("deferred call outcome not stored", "operation_id", row.ID, "route", call.Route, "error", err)
	}
}
