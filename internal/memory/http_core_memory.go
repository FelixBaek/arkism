package memory

import (
	"net/http"
)

func registerMemoryCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
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
	mux.HandleFunc("POST /v1/memories/{id}/reach-suggestion/dismiss", func(w http.ResponseWriter, r *http.Request) {
		var in ReachSuggestionDismissInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.DismissReachSuggestion(r.Context(), bearer(r), r.PathValue("id"), in)
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
}

func registerMemoryUsageCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/memory_usage", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.MemoryUsageSummary(r.Context(), bearer(r), scope)
		respond(w, out, e)
	})
}
