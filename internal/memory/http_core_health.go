package memory

import (
	"context"
	"net/http"
	"time"

	"github.com/FelixBaek/arkism/internal/buildinfo"
)

func registerHealthCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentType, "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Arkism API. 화면은 Arkism 데스크톱 프로그램에서 열어요.\n"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		q := r.URL.Query()
		scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
		d, e := s.authorize(ctx, bearer(r), "ops:metrics", scope, "", "")
		if e != nil {
			respond(w, nil, e)
			return
		}
		if d.Role != "human" && d.Role != "service" {
			respond(w, nil, ErrDenied)
			return
		}
		if s.Repo == nil {
			respond(w, nil, ErrUnavailable)
			return
		}
		store, e := s.Repo.OperationalMetrics(ctx, scope)
		if e != nil {
			respond(w, nil, e)
			return
		}
		ready := s.Repo.SearchReady(ctx) == nil
		deletion := DeletionWorkflowMetrics{Available: true, Coverage: "tracked_jobs_only"}
		writeJSON(w, http.StatusOK, map[string]any{"store": store, "requests": stats.snapshot(), "requests_scope": "process", "latency_bounds_seconds": latencyBounds, "indexes_ready": ready, "cost": store.Cost, "deletion_workflow": deletion})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		blobCtx := ctx
		if s.ReadinessToken != "" {
			blobCtx = ContextWithPostgresToken(ctx, s.ReadinessToken)
		}
		if s.Repo == nil || s.Repo.SearchReady(ctx) != nil || s.CheckBlobStorage(blobCtx) != nil {
			respond(w, nil, ErrUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	// Unauthenticated on purpose: the app reads the version before it trusts a feature exists.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, buildinfo.Current()) })
	mux.HandleFunc("POST /v1/context/build", func(w http.ResponseWriter, r *http.Request) {
		var in ContextBuildInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.BuildContext(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}
