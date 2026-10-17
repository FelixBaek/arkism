package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const headerContentType = "Content-Type"

func Handler(s *Service) http.Handler { return handler(s, 45*time.Second) }

func handler(s *Service, timeout time.Duration) http.Handler {
	return handlerMode(s, timeout, false)
}

func handlerMode(s *Service, timeout time.Duration, recovery bool) http.Handler {
	stats := &requestMetrics{}
	mux := http.NewServeMux()
	s.registerKnowledgeHTTP(mux)
	googleLoginRoutes(mux, s.GoogleLogin)
	agentToolsRoutes(mux, s)
	crewRoutes(mux, s)
	contextPinRoutes(mux, s)
	mergeProposalRoutes(mux, s)
	crewRunRoutes(mux, s)
	crewAssistRoutes(mux, s)
	crewMeetingRoutes(mux, s)
	crewUsageRoutes(mux, s)
	discordRoutes(mux, s)
	skillUsageRoutes(mux, s)
	codeReviewRoutes(mux, s)
	codeGraphRoutes(mux, s)
	providersRoutes(mux, s)
	routingRoutes(mux, s)
	usageRoutes(mux, s)
	sessionManageRoutes(mux, s)
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
	mux.HandleFunc("POST /v1/consultations", func(w http.ResponseWriter, r *http.Request) {
		var in CreateConsultationInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateConsultation(r.Context(), bearer(r), in)
		if e != nil {
			respond(w, nil, e)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	})
	mux.HandleFunc("GET /v1/consultations/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.GetConsultation(r.Context(), bearer(r), r.PathValue("id"), scope)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/consultations/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var in ConsultationEventInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ConsultationEvent(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})

	downloadGate := make(chan struct{}, 1)
	// The screens live in the Arkism desktop app (desktop/); the server is API only.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentType, "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Arkism API. 화면은 Arkism 데스크톱 프로그램에서 열어요.\n"))
	})
	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
		caps, e := s.Capabilities(r.Context(), bearer(r), scope)
		respond(w, caps, e)
	})
	mux.HandleFunc("GET /v1/sources/{id}/derived/count", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseDerivedCountQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.CountSourceDerived(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/sources/{id}/derived", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseDerivedListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListSourceDerived(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/sources/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseArtifactListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListSourceArtifacts(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/sources/{id}/artifacts/{artifact_id}", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		content, err := s.ReadArtifact(r.Context(), bearer(r), scope, r.PathValue("id"), r.PathValue("artifact_id"))
		if err != nil {
			respond(w, nil, err)
			return
		}
		w.Header().Set(headerContentType, "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="artifact.bin"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	})
	// shortcut: one upload per handler bounds memory; raise concurrency only
	// with a measured memory budget and unchanged full-payload policy checks.
	uploadGate := make(chan struct{}, 1)
	mux.HandleFunc("POST /v1/sources/ingest", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) == "" {
			respond(w, nil, ErrDenied)
			return
		}
		select {
		case uploadGate <- struct{}{}:
			defer func() { <-uploadGate }()
		default:
			respond(w, nil, ErrUnavailable)
			return
		}
		s.ingestFileHTTP(w, r)
	})
	mux.HandleFunc("POST /v1/deletions/export", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scope   Scope `json:"scope"`
			Version *int  `json:"version,omitempty"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Version != nil && *in.Version == 2 {
			out, e := s.ExportRecoveryDeletions(r.Context(), bearer(r), in.Scope)
			respond(w, out, e)
			return
		}
		if in.Version != nil && *in.Version != 1 {
			respond(w, nil, ErrInvalid)
			return
		}
		out, e := s.ExportDeletions(r.Context(), bearer(r), in.Scope)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/privacy/deletions", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyDeletionRequest
		if !decode(w, r, &in) {
			return
		}
		out, err := s.RequestPrivacyDeletion(r.Context(), bearer(r), in)
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeJSON(w, http.StatusAccepted, out)
	})
	mux.HandleFunc("GET /v1/privacy/deletions/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.GetPrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), scope)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/runtime/claim", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.ClaimPrivacyDeletion(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/fence", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyFenceInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.CheckPrivacyFence(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/runtime-receipt", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyRuntimeReceiptInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.RecordPrivacyRuntimeReceipt(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/advance", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyRevisionInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.AdvancePrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/hold", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyHoldInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.HoldPrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/hold/release", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyHoldInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.ReleasePrivacyDeletionHold(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
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
	mux.HandleFunc("POST /v1/evaluations/summary", func(w http.ResponseWriter, r *http.Request) {
		var in EvaluationSummaryInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SummarizeEvaluations(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/execution_traces/completed", func(w http.ResponseWriter, r *http.Request) {
		var in TraceIngestInput
		if !decodeCompletedTrace(w, r, &in) {
			return
		}
		out, err := s.IngestCompletedTrace(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/learning", func(w http.ResponseWriter, r *http.Request) {
		var in LearningInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.Learn(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/agent_runs/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var in FinishRunInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.FinishRun(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/memory_candidates/expire", func(w http.ResponseWriter, r *http.Request) {
		var in ExpireCandidatesInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ExpireCandidates(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/memories/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.RecentMemoryUsage(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/memories/{id}/lineage", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.MemoryLineage(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/memories/{id}/sources", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.TraceMemorySources(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memories/conflicts", func(w http.ResponseWriter, r *http.Request) {
		var in ConflictInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Conflict(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memories/{id}/supersede", func(w http.ResponseWriter, r *http.Request) {
		var in SupersedeInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Supersede(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memories/{id}/expiry", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryExpiryInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.SetMemoryExpiry(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memories/{id}/reach", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryReachInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.SetMemoryReach(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/memory-reach-suggestions", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseMemoryListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ReachSuggestions(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memories/{id}/reach-suggestion/dismiss", func(w http.ResponseWriter, r *http.Request) {
		var in ReachSuggestionDismissInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.DismissReachSuggestion(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/maintenance_proposals", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ListMaintenance(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("status"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/maintenance_proposals", func(w http.ResponseWriter, r *http.Request) {
		var in MaintenanceInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ProposeMaintenance(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/maintenance_proposals/{id}/decide", func(w http.ResponseWriter, r *http.Request) {
		var in MaintenanceDecision
		if !decode(w, r, &in) {
			return
		}
		v, e := s.DecideMaintenance(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memory_candidates/review_batch", func(w http.ResponseWriter, r *http.Request) {
		var in CandidateReviewBatchInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ReviewCandidates(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memory_candidates/{id}/classify", func(w http.ResponseWriter, r *http.Request) {
		var in CandidateClassifyInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ClassifyCandidate(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/memory_candidates/classification_report", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ClassificationReport(r.Context(), bearer(r), scopeQuery(r))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memory_candidates/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var in CandidateReviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ReviewCandidate(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memories/at", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryAtInput
		if !decode(w, r, &in) {
			return
		}
		page, e := s.MemoriesAt(r.Context(), bearer(r), in)
		respond(w, page, e)
	})
	mux.HandleFunc("GET /v1/memories", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseMemoryListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		page, e := s.ListMemories(r.Context(), bearer(r), in)
		respond(w, page, e)
	})
	mux.HandleFunc("GET /v1/sources", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseSourceListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		page, e := s.ListSources(r.Context(), bearer(r), in)
		respond(w, page, e)
	})
	mux.HandleFunc("GET /v1/memory_candidates", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseReviewQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		page, e := s.ListCandidates(r.Context(), bearer(r), in)
		respond(w, page, e)
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
	mux.HandleFunc("POST /v1/skills/analyze", func(w http.ResponseWriter, r *http.Request) {
		var in AnalyzeSkillInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.AnalyzeSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/comparisons/record", func(w http.ResponseWriter, r *http.Request) {
		var in RecordSkillComparisonInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RecordSkillComparison(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/propose", func(w http.ResponseWriter, r *http.Request) {
		var in ProposeSkillInput
		if !decodeLimit(w, r, &in, 8<<20) {
			return
		}
		v, e := s.ProposeSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/generate", func(w http.ResponseWriter, r *http.Request) {
		var in GenerateSkillInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.GenerateSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/import", func(w http.ResponseWriter, r *http.Request) {
		var in ImportSkillInput
		if !decodeLimit(w, r, &in, 8<<20) {
			return
		}
		v, e := s.ImportSkill(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/list", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialListInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ListSkillTrials(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trial-outcomes", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialOutcomeInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RecordSkillTrialOutcome(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memory_candidates/{id}/hold-conflict", func(w http.ResponseWriter, r *http.Request) {
		var in CandidateConflictHoldInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.HoldCandidateConflict(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/observe", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ObserveSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/claim-check", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ClaimSkillTrialCheck(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/finish-check", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialCheckFinish
		if !decode(w, r, &in) {
			return
		}
		e := s.FinishSkillTrialCheck(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, map[string]bool{"ok": e == nil}, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/monitor", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.MonitorSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/inspect", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialReadInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.InspectSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.StartSkillTrial(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skill-trials/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		var in SkillTrialStopInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.StopSkillTrial(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/resolve", func(w http.ResponseWriter, r *http.Request) {
		var in SkillResolveInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ResolveSkills(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/skills", func(w http.ResponseWriter, r *http.Request) {
		scope, limit, cursor, e := parseSkillListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListSkills(r.Context(), bearer(r), scope, limit, cursor)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/skills/trash", func(w http.ResponseWriter, r *http.Request) {
		scope, limit, cursor, e := parseSkillListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListTrashedSkills(r.Context(), bearer(r), scope, limit, cursor)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/route", func(w http.ResponseWriter, r *http.Request) {
		var in SearchInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RouteSkills(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/twin/claims", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseIntelligenceQuery(r.URL.RawQuery, "status")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListTwinClaims(r.Context(), bearer(r), scope, splitStatuses(q.Get("status")))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/tacit/edges", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseIntelligenceQuery(r.URL.RawQuery, "node", "relation")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListTacitEdges(r.Context(), bearer(r), scope, q.Get("node"), q.Get("relation"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/reflections", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseIntelligenceQuery(r.URL.RawQuery, "subject")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListReflections(r.Context(), bearer(r), scope, q.Get("subject"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/improvements", func(w http.ResponseWriter, r *http.Request) {
		var in ImprovementInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.Improve(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /v1/context/build", func(w http.ResponseWriter, r *http.Request) {
		var in ContextBuildInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.BuildContext(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/context/tokens", func(w http.ResponseWriter, r *http.Request) {
		var in TokenCountInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.CountTokens(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/search", func(w http.ResponseWriter, r *http.Request) {
		var in SearchInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Search(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/capture", func(w http.ResponseWriter, r *http.Request) {
		var in CaptureInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Capture(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/import/sessions", func(w http.ResponseWriter, r *http.Request) {
		var in SessionImportInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ImportSessions(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/{collection}", func(w http.ResponseWriter, r *http.Request) {
		var in CreateInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Create(r.Context(), bearer(r), r.PathValue("collection"), in)
		if e != nil {
			respond(w, nil, e)
			return
		}
		writeJSON(w, 201, v)
	})
	mux.HandleFunc("GET /v1/{collection}/{id}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
		v, e := s.Get(r.Context(), bearer(r), r.PathValue("collection"), scope, r.PathValue("id"))
		if e == nil && r.PathValue("collection") == colSkillVersions && v.ComparisonPlan != nil {
			respond(w, struct {
				Record
				ComparisonPlanSHA256 string `json:"comparison_plan_sha256"`
			}{v, comparisonPlanHash(v.ComparisonPlan)}, nil)
			return
		}
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/{collection}/{id}/transition", func(w http.ResponseWriter, r *http.Request) {
		var in ChangeInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Transition(r.Context(), bearer(r), r.PathValue("collection"), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/skills/{id}/availability", func(w http.ResponseWriter, r *http.Request) {
		var in SkillAvailabilityInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.SetSkillAvailability(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
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
	mux.HandleFunc("DELETE /v1/{collection}/{id}", func(w http.ResponseWriter, r *http.Request) {
		dryRun, err := parseDeleteQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		var in ChangeInput
		if !decode(w, r, &in) {
			return
		}
		if dryRun {
			out, e := s.PreviewDeletion(r.Context(), bearer(r), r.PathValue("collection"), r.PathValue("id"), in)
			respond(w, out, e)
			return
		}
		if r.PathValue("collection") == "sources" {
			if _, ok := s.Repo.(privacyStore); ok {
				out, e := s.RequestPrivacyDeletion(r.Context(), bearer(r), PrivacyDeletionRequest{Scope: in.Scope, IdempotencyKey: "legacy-source-delete-" + ContentHash(r.PathValue("id")+":"+strconv.FormatInt(in.ExpectedRevision, 10)), TargetType: "source_event", TargetIDs: []string{r.PathValue("id")}, Reason: "legacy_source_delete", RetentionPolicyRef: retentionExternal, ExpectedRevision: in.ExpectedRevision})
				if e != nil {
					respond(w, nil, e)
					return
				}
				writeJSON(w, http.StatusAccepted, out)
				return
			}
			if _, testOnly := s.Repo.(legacySourceDeleteTestStore); !testOnly {
				respond(w, nil, ErrUnavailable)
				return
			}
		}
		e := s.Delete(r.Context(), bearer(r), r.PathValue("collection"), r.PathValue("id"), in)
		if e == nil && (r.PathValue("collection") == "sources" || r.PathValue("collection") == "memories" || r.PathValue("collection") == "memory_candidates") {
			col := r.PathValue("collection")
			coverage := sourceRecordsMemoryDependents
			if col == "memories" {
				coverage = "memory_relations_evaluations"
			} else if col == "memory_candidates" {
				coverage = "candidate_relations_evaluations"
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"deleted": true, "deletion_job_id": deletionJobID(col, in.Scope, r.PathValue("id")), "coverage": coverage, "full_erasure_verified": false})
			return
		}
		respond(w, map[string]bool{"deleted": true}, e)
	})
	timed := http.TimeoutHandler(mux, timeout, `{"code":"request_timeout"}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
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
	case errors.Is(e, ErrCandidateQuota):
		code, status = "candidate_daily_limit", 429
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
