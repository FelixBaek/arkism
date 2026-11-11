package memory

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Legacy deferred rows can contain response bodies without a complete source
// lineage. They are never a safe result cache, even for their original creator.
const (
	deferredCallKind          = "deferred_call"
	deferredResultUnavailable = "result_unavailable"
)

// Stay below the desktop bridge's 30s limit and the HTTP server's 45s limit.
// The original request may impose an earlier deadline or cancellation.
var deferredWait = 25 * time.Second

// DeferredCall is retained only to decode and scrub legacy rows.
type DeferredCall struct {
	Route  string          `json:"route"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// DeferredAccepted is the legacy wire shape; sensitive routes no longer emit it.
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

// respondLater now keeps the call inside its original request. Persisting an
// arbitrary response in jobs bypassed source erasure and the original read
// authorization. Background completion stays disabled until it has a complete,
// revalidatable provenance contract. No result or new operation row is stored.
func (s *Service) respondLater(w http.ResponseWriter, r *http.Request, _ Scope, run func(context.Context, string) (any, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), deferredWait)
	defer cancel()
	done := make(chan deferredOutcome, 1)
	go func() {
		var out deferredOutcome
		defer func() {
			if recover() != nil {
				out = deferredOutcome{e: ErrUnavailable}
			}
			done <- out
		}()
		out.v, out.e = run(ctx, bearer(r))
	}()
	select {
	case out := <-done:
		if ctx.Err() == nil {
			respond(w, out.v, out.e)
			return
		}
	case <-ctx.Done():
	}
	code := "request_timeout"
	if ctx.Err() == context.Canceled {
		code = "request_cancelled"
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": code})
}

func deferredOperation(r Record) bool { return r.Kind == deferredCallKind || r.Deferred != nil }

// A legacy row's response is withheld regardless of whether scrubbing succeeds.
// Only the creator reaches this helper. Read-only database credentials may stop
// cleanup; they must never cause a fallback to exposing the old response.
func (s *Service) scrubDeferredResponse(ctx context.Context, r Record) {
	if r.Deferred == nil || len(r.Deferred.Result) == 0 && r.Deferred.Error == deferredResultUnavailable {
		return
	}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colJobs, r.Scope, r.ID)
		if err != nil {
			return err
		}
		if cur.CreatedBy != r.CreatedBy || cur.CreatedBy == "" {
			return ErrConflict
		}
		if cur.Deferred == nil {
			return nil
		}
		prev := cur.Revision
		cur.Deferred = &DeferredCall{Route: cur.Deferred.Route, Error: deferredResultUnavailable}
		cur.Revision, cur.UpdatedAt = prev+1, time.Now().UTC()
		return tx.Replace(ctx, colJobs, cur, prev)
	})
	if err != nil {
		slog.Warn("legacy deferred response cleanup failed", "operation_id", r.ID, "error", err)
	}
}
