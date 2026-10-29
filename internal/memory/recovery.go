package memory

import (
	"context"
	"net/http"
	"time"

	"github.com/FelixBaek/arkism/internal/buildinfo"
)

// SetRecoveryMode (postgres.go) restricts job claims to erasure jobs. Recovery
// never initializes the schema; a restored database must already contain it.

// RecoveryHandler exposes only the existing authorized deletion workflow.
// It must be bound to loopback. Readiness deliberately remains unavailable so
// this process cannot be mistaken for a restored production API.
func RecoveryHandler(s *Service) http.Handler {
	normal := handlerMode(s, 45*time.Second, true)
	mux := http.NewServeMux()
	for _, col := range []string{"sources", "memories", "memory_candidates"} {
		for _, pattern := range []string{"GET /v1/" + col + "/{id}", "DELETE /v1/" + col + "/{id}", "GET /v1/" + col + "/{id}/deletion", "POST /v1/" + col + "/{id}/deletion/retry"} {
			mux.Handle(pattern, normal)
		}
	}
	for _, pattern := range []string{
		"POST /v1/privacy/deletions",
		"GET /v1/privacy/deletions/{id}",
		"POST /v1/privacy/deletions/runtime/claim",
		"POST /v1/privacy/deletions/{id}/runtime-receipt",
		"POST /v1/privacy/deletions/{id}/advance",
		"POST /v1/session-archives/expire",
	} {
		mux.Handle(pattern, normal)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		h := buildinfo.Current()
		h.Status = "recovery"
		writeJSON(w, http.StatusOK, h)
	})
	mux.HandleFunc("GET /recovery-identity", func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.Repo.(interface {
			RecoveryIdentity(context.Context) (RestoredRecoveryIdentity, error)
		})
		if !ok {
			respond(w, nil, ErrUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		identity, err := store.RecoveryIdentity(ctx)
		respond(w, identity, err)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { respond(w, nil, ErrUnavailable) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", ID())
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}
