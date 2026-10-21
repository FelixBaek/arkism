package memory

import (
	"net/http"
)

func registerLearningCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/learning", func(w http.ResponseWriter, r *http.Request) {
		var in LearningInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.Learn(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

func registerEvaluationsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/evaluations/summary", func(w http.ResponseWriter, r *http.Request) {
		var in EvaluationSummaryInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SummarizeEvaluations(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

func registerReflectionsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/reflections", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseIntelligenceQuery(r.URL.RawQuery, "subject")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListReflections(r.Context(), bearer(r), scope, q.Get("subject"))
		respond(w, out, e)
	})
}

func registerTacitCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/tacit/edges", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseIntelligenceQuery(r.URL.RawQuery, "node", "relation")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListTacitEdges(r.Context(), bearer(r), scope, q.Get("node"), q.Get("relation"))
		respond(w, out, e)
	})
}

func registerTwinCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/twin/claims", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseIntelligenceQuery(r.URL.RawQuery, "status")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ListTwinClaims(r.Context(), bearer(r), scope, splitStatuses(q.Get("status")))
		respond(w, out, e)
	})
}

func registerImprovementsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/improvements", func(w http.ResponseWriter, r *http.Request) {
		var in ImprovementInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.Improve(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
