package memory

import (
	"net/http"
)

func registerReviewCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/memory_candidates/expire", func(w http.ResponseWriter, r *http.Request) {
		var in ExpireCandidatesInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ExpireCandidates(r.Context(), bearer(r), in)
		respond(w, out, e)
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
	mux.HandleFunc("POST /v1/memory_candidates/{id}/reclassify", func(w http.ResponseWriter, r *http.Request) {
		var in ReclassifyInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RequestReclassify(r.Context(), bearer(r), r.PathValue("id"), in)
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
	mux.HandleFunc("GET /v1/memory_candidates", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseReviewQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		page, e := s.ListCandidates(r.Context(), bearer(r), in)
		respond(w, page, e)
	})
	mux.HandleFunc("POST /v1/memory_candidates/{id}/hold-conflict", func(w http.ResponseWriter, r *http.Request) {
		var in CandidateConflictHoldInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.HoldCandidateConflict(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
}

func registerMemoryreachsuggestionsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/memory-reach-suggestions", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseMemoryListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ReachSuggestions(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}

func registerMaintenanceProposalsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
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
}
