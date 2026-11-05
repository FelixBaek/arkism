package memory

import (
	"context"
	"net/http"
)

func registerOpsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	// Unauthenticated on purpose: answers while the process runs, before any dependency.
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /v1/audit-events", func(w http.ResponseWriter, r *http.Request) {
		in, e := ParseOpsListQuery(r.URL.Query())
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListAuditEvents(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/audit-events/stats", func(w http.ResponseWriter, r *http.Request) {
		from, to, e := parseOpsRange(r.URL.Query(), true)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.AuditEventStats(r.Context(), bearer(r), scopeQuery(r), from, to)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/audit-events/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.GetAuditEvent(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/operations", func(w http.ResponseWriter, r *http.Request) {
		in, e := ParseOpsListQuery(r.URL.Query())
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListOperations(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.GetOperation(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/operations/{id}/cancel", operationChangeHandler(s.CancelOperation))
	mux.HandleFunc("POST /v1/operations/{id}/retry", operationChangeHandler(s.RetryOperation))
	mux.HandleFunc("DELETE /v1/operations/{id}", operationChangeHandler(s.DeleteOperation))
	registerStatsTagsHTTP(mux, s)
}

func operationChangeHandler(change func(context.Context, string, string, OperationChangeInput) (OperationView, error)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var in OperationChangeInput
		if !decode(w, r, &in) {
			return
		}
		v, e := change(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	}
}

func registerStatsTagsHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Stats(r.Context(), bearer(r), scopeQuery(r))
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/stats/timeseries", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, to, e := parseOpsRange(q, true)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.StatsTimeseries(r.Context(), bearer(r), StatsTimeseriesInput{Scope: scopeQuery(r), Collection: q.Get("collection"), Bucket: q.Get("bucket"), By: q.Get("by"), From: from, To: to})
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/tags", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ListTags(r.Context(), bearer(r), scopeQuery(r))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/memories/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryTagsInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.SetMemoryTags(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/tags/rename", func(w http.ResponseWriter, r *http.Request) {
		var in TagRenameInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RenameTag(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}
