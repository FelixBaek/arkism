package memory

import (
	"net/http"
)

func registerRunsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/agent_runs/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var in FinishRunInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.FinishRun(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}

func registerHandoffsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
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
}

func registerExecutionTracesCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/execution_traces/completed", func(w http.ResponseWriter, r *http.Request) {
		var in TraceIngestInput
		if !decodeCompletedTrace(w, r, &in) {
			return
		}
		out, err := s.IngestCompletedTrace(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}
