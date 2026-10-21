package memory

import (
	"net/http"
)

func registerMemoryCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/memories/{id}/usage", s.coreHTTPGetV1MemoriesIdUsage)
	mux.HandleFunc("GET /v1/memories/{id}/lineage", s.coreHTTPGetV1MemoriesIdLineage)
	mux.HandleFunc("GET /v1/memories/{id}/sources", s.coreHTTPGetV1MemoriesIdSources)
	mux.HandleFunc("POST /v1/memories/conflicts", s.coreHTTPPostV1MemoriesConflicts)
	mux.HandleFunc("POST /v1/memories/{id}/supersede", s.coreHTTPPostV1MemoriesIdSupersede)
	mux.HandleFunc("POST /v1/memories/{id}/expiry", s.coreHTTPPostV1MemoriesIdExpiry)
	mux.HandleFunc("POST /v1/memories/{id}/reach", s.coreHTTPPostV1MemoriesIdReach)
	mux.HandleFunc("POST /v1/memories/{id}/reach-suggestion/dismiss", s.coreHTTPPostV1MemoriesIdReachSuggestionDismiss)
	mux.HandleFunc("POST /v1/memories/at", s.coreHTTPPostV1MemoriesAt)
	mux.HandleFunc("GET /v1/memories", s.coreHTTPGetV1Memories)
}

func registerMemoryUsageCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/memory_usage", s.coreHTTPGetV1MemoryUsage)
}

func (s *Service) coreHTTPGetV1MemoriesIdUsage(w http.ResponseWriter, r *http.Request) {
	scope, e := parseSourceTraceQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, nil, e)
		return
	}
	out, e := s.RecentMemoryUsage(r.Context(), bearer(r), scope, r.PathValue("id"))
	respond(w, out, e)
}

func (s *Service) coreHTTPGetV1MemoriesIdLineage(w http.ResponseWriter, r *http.Request) {
	scope, e := parseSourceTraceQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, nil, e)
		return
	}
	out, e := s.MemoryLineage(r.Context(), bearer(r), scope, r.PathValue("id"))
	respond(w, out, e)
}

func (s *Service) coreHTTPGetV1MemoriesIdSources(w http.ResponseWriter, r *http.Request) {
	scope, e := parseSourceTraceQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.TraceMemorySources(r.Context(), bearer(r), scope, r.PathValue("id"))
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1MemoriesConflicts(w http.ResponseWriter, r *http.Request) {
	var in ConflictInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.Conflict(r.Context(), bearer(r), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1MemoriesIdSupersede(w http.ResponseWriter, r *http.Request) {
	var in SupersedeInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.Supersede(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1MemoriesIdExpiry(w http.ResponseWriter, r *http.Request) {
	var in MemoryExpiryInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.SetMemoryExpiry(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1MemoriesIdReach(w http.ResponseWriter, r *http.Request) {
	var in MemoryReachInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.SetMemoryReach(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1MemoriesIdReachSuggestionDismiss(w http.ResponseWriter, r *http.Request) {
	var in ReachSuggestionDismissInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.DismissReachSuggestion(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1MemoriesAt(w http.ResponseWriter, r *http.Request) {
	var in MemoryAtInput
	if !decode(w, r, &in) {
		return
	}
	page, e := s.MemoriesAt(r.Context(), bearer(r), in)
	respond(w, page, e)
}

func (s *Service) coreHTTPGetV1Memories(w http.ResponseWriter, r *http.Request) {
	in, e := parseMemoryListQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, nil, e)
		return
	}
	page, e := s.ListMemories(r.Context(), bearer(r), in)
	respond(w, page, e)
}

func (s *Service) coreHTTPGetV1MemoryUsage(w http.ResponseWriter, r *http.Request) {
	scope, e := parseSourceTraceQuery(r.URL.RawQuery)
	if e != nil {
		respond(w, nil, e)
		return
	}
	out, e := s.MemoryUsageSummary(r.Context(), bearer(r), scope)
	respond(w, out, e)
}
