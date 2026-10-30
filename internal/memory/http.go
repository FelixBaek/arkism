package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
)

const headerContentType = "Content-Type"

type requestWorkKey struct{}

// WithRequestWork retains outer request work and the mux work inside its timeout
// separately. A timeout may end the response while the actual operation (including
// deferred cleanup) is still running. A nil wrapper keeps ordinary behavior.
func WithRequestWork(handler http.Handler, wrap func(http.Handler) http.Handler) http.Handler {
	if wrap == nil {
		return handler
	}
	return wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), requestWorkKey{}, wrap)
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
}

func requestWork(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wrap, ok := r.Context().Value(requestWorkKey{}).(func(http.Handler) http.Handler); ok {
			wrap(handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func Handler(s *Service) http.Handler { return handler(s, 45*time.Second) }

func handler(s *Service, timeout time.Duration) http.Handler {
	return handlerMode(s, timeout, false)
}

func handlerMode(s *Service, timeout time.Duration, recovery bool) http.Handler {
	stats := &requestMetrics{}
	mux := newAuthorizationMux(s)
	registerHandoffsCoreHTTP(mux, s, stats)
	registerHealthCoreHTTP(mux, s, stats)
	registerCapabilitiesCoreHTTP(mux, s, stats)
	registerSourcesCoreHTTP(mux, s, stats)
	registerDeletionsCoreHTTP(mux, s, stats)
	registerPrivacyCoreHTTP(mux, s, stats)
	registerEvaluationsCoreHTTP(mux, s, stats)
	registerExecutionTracesCoreHTTP(mux, s, stats)
	registerLearningCoreHTTP(mux, s, stats)
	registerRunsCoreHTTP(mux, s, stats)
	registerReviewCoreHTTP(mux, s, stats)
	registerMemoryUsageCoreHTTP(mux, s, stats)
	registerRecallsCoreHTTP(mux, s, stats)
	registerMemoryCoreHTTP(mux, s, stats)
	registerMemoryreachsuggestionsCoreHTTP(mux, s, stats)
	registerMaintenanceProposalsCoreHTTP(mux, s, stats)
	registerSkillsCoreHTTP(mux, s, stats)
	registerSkilltrialsCoreHTTP(mux, s, stats)
	registerSkilltrialoutcomesCoreHTTP(mux, s, stats)
	registerTwinCoreHTTP(mux, s, stats)
	registerTacitCoreHTTP(mux, s, stats)
	registerReflectionsCoreHTTP(mux, s, stats)
	registerImprovementsCoreHTTP(mux, s, stats)
	registerContextCoreHTTP(mux, s, stats)
	registerSearchCoreHTTP(mux, s, stats)
	registerCaptureCoreHTTP(mux, s, stats)
	registerRecordsCoreHTTP(mux, s, stats)
	catalogQueryRoutes(mux, s)
	s.registerKnowledgeHTTP(mux)
	googleLoginRoutes(mux, s.GoogleLogin)
	agentToolsRoutes(mux, s)
	crewRoutes(mux, s)
	contextPinRoutes(mux, s)
	mergeProposalRoutes(mux, s)
	decisionRoutes(mux, s)
	crewRunRoutes(mux, s)
	crewAssistRoutes(mux, s)
	crewMeetingRoutes(mux, s)
	crewUsageRoutes(mux, s)
	crewAgentCallRoutes(mux, s)
	discordRoutes(mux, s)
	messengerRoutes(mux, s)
	skillUsageRoutes(mux, s)
	skillRouteLogRoutes(mux, s)
	codeReviewRoutes(mux, s)
	codeGraphRoutes(mux, s)
	providersRoutes(mux, s)
	routingRoutes(mux, s)
	usageRoutes(mux, s)
	sessionManageRoutes(mux, s)
	usageSessionFindRoutes(mux, s)
	registerKnowledgeRetrievalHTTP(mux, s)
	sessionArchiveRoutes(mux, s)
	workCheckpointRoutes(mux, s)
	collaborationRoutes(mux, s)
	digestRoutes(mux, s)
	registerWikiHTTP(mux, s)
	modelBudgetRoutes(mux, s)
	modelTransferRoutes(mux, s)
	skillTrialStatusRoutes(mux, s)
	modelInputRoutes(mux, s)
	memoryOutcomeRoutes(mux, s)
	memoryOutcomeReadRoutes(mux, s)
	memoryOutcomeComparisonRoutes(mux, s)
	memorySignalRoutes(mux, s)
	candidateEvidenceRoutes(mux, s)
	memoryReviewDueRoutes(mux, s)
	memoryConfirmRoutes(mux, s)
	memoryManagementRoutes(mux, s)
	memoryGraphRoutes(mux, s)

	downloadGate := make(chan struct{}, 1)
	// The screens live in the Arkism desktop app (desktop/); the server is API only.
	// shortcut: one upload per handler bounds memory; raise concurrency only
	// with a measured memory budget and unchanged full-payload policy checks.
	for _, col := range []string{"sources", "memories", "memory_candidates"} {
		mux.HandleFunc("POST /v1/"+col+"/{id}/deletion/retry", func(w http.ResponseWriter, r *http.Request) {
			var in RetrySourceDeletionInput
			if !decode(w, r, &in) {
				return
			}
			out, e := s.retryDeletion(r.Context(), bearer(r), col, r.PathValue("id"), in)
			respond(w, out, e)
		})
		mux.HandleFunc("GET /v1/"+col+"/{id}/deletion", func(w http.ResponseWriter, r *http.Request) {
			scope, e := parseSourceTraceQuery(r.URL.RawQuery)
			if e != nil {
				respond(w, nil, e)
				return
			}
			out, e := s.deletionStatus(r.Context(), bearer(r), col, scope, r.PathValue("id"))
			respond(w, out, e)
		})
	}
	for _, action := range []string{"apply", "rollback"} {
		mux.HandleFunc("POST /v1/skills/{id}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var in ChangeInput
			if !decode(w, r, &in) {
				return
			}
			v, e := s.ApplySkill(r.Context(), bearer(r), r.PathValue("id"), in, action == "rollback")
			respond(w, v, e)
		})
	}
	timed := http.TimeoutHandler(requestWork(mux), timeout, `{"code":"request_timeout"}`)
	return httpguard.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "POST /v1/sources/ingest" {
			// Set before TimeoutHandler wraps the writer. Keep the deadline on
			// timeout so a blocked body read cannot outlive the upload gate forever.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(min(timeout, 15*time.Second)))
		}
		recorded := &statusWriter{ResponseWriter: w}
		w = recorded
		started := time.Now()
		requestID := ID()
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(ContextWithPostgresToken(context.WithValue(r.Context(), auditRequestKey{}, requestID), bearer(r)))
		defer func() {
			status := recorded.status
			if status == 0 {
				status = http.StatusOK
			}
			stats.observe(pattern, status, time.Since(started))
			slog.Info("memory request", "status", status, "request_id", requestID, "method", r.Method, "route", pattern, "duration_ms", time.Since(started).Milliseconds())
		}()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		if !recovery && r.URL.Path != "/healthz" {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			err := s.CheckRestorePublication(ctx)
			cancel()
			if err != nil {
				w.Header().Set("Cache-Control", "no-store")
				respond(w, nil, ErrUnavailable)
				return
			}
		}
		if pattern == "GET /v1/sources/{id}/artifacts/{artifact_id}" {
			// shortcut: one download through the actual flush bounds memory.
			// Raise concurrency only after measuring the 200 MiB buffers.
			select {
			case downloadGate <- struct{}{}:
				defer func() { <-downloadGate }()
			default:
				respond(w, nil, ErrUnavailable)
				return
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(min(timeout+5*time.Second, 50*time.Second)))
		}
		timed.ServeHTTP(w, r)
	}))
}
func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, 256<<10)
}

// decodeLimit is decode with a body cap other than the default 256 KiB.
func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		respond(w, nil, ErrInvalid)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		respond(w, nil, ErrInvalid)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set(headerContentType, "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		writeJSON(w, 200, v)
		return
	}
	code, status := errorResponse(e)
	writeJSON(w, status, map[string]string{"code": code})
}
func errorResponse(e error) (string, int) {
	var flowLimit flowLimitError
	if errors.As(e, &flowLimit) {
		return flowLimit.Error(), 409
	}
	code, status := "dependency_unavailable", 503
	switch {
	case errors.Is(e, errBlobTooLarge):
		code, status = "payload_too_large", 413
	case errors.Is(e, errBlobDigestMismatch):
		code, status = "digest_mismatch", 422
	case errors.Is(e, ErrSecretDetected):
		code, status = "secret_detected", 422
	case errors.Is(e, ErrSemanticLaneDown):
		code, status = "semantic_lane_unavailable", 503
	case errors.Is(e, ErrInvalid):
		code, status = "invalid_request", 400
	case errors.Is(e, ErrDenied):
		code, status = "access_denied", 403
	case errors.Is(e, ErrNotFound):
		code, status = "not_found", 404
	case errors.Is(e, ErrModelBudget):
		return e.Error(), http.StatusTooManyRequests
	case errors.Is(e, ErrDynamicsQueueFull):
		code, status = "dynamics_queue_full", http.StatusTooManyRequests
	case errors.Is(e, ErrCandidateQuota):
		code, status = "candidate_daily_limit", 429
	case errors.Is(e, ErrHandleTaken):
		code, status = "handle_taken", 409
	case errors.Is(e, ErrConflict):
		code, status = "revision_conflict", 409
	}
	return code, status
}

// The API is non-streaming; Unwrap keeps ResponseController support available.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
