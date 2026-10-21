package memory

import (
	"net/http"
	"strconv"
)

func registerSourcesCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	uploadGate := make(chan struct{}, 1)
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
	mux.HandleFunc("GET /v1/sources", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseSourceListQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		page, e := s.ListSources(r.Context(), bearer(r), in)
		respond(w, page, e)
	})
}
