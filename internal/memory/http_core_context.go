package memory

import (
	"net/http"
)

func registerContextCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/context/tokens", func(w http.ResponseWriter, r *http.Request) {
		var in TokenCountInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.CountTokens(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/context/validate", func(w http.ResponseWriter, r *http.Request) {
		var in ContextValidationInput
		if !decodeLimit(w, r, &in, 64<<10) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		v, e := s.ValidateContext(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}

func registerCaptureCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
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
}

func registerSearchCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/search", func(w http.ResponseWriter, r *http.Request) {
		var in SearchInput
		if !decode(w, r, &in) {
			return
		}
		in.client, in.session = recallLabel(r.Header.Get("X-Arkism-Client"), 64), recallLabel(r.Header.Get("X-Arkism-Session"), 128)
		v, e := s.Search(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}

func registerRecallsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/recalls", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.RecentRecalls(r.Context(), bearer(r), scope, 20)
		respond(w, out, e)
	})
}

// recallLabel keeps a caller label that is short, printable and not a secret; anything else is dropped, not refused.
func recallLabel(v string, maxN int) string {
	if !validRunLabel(v, maxN) || containsSecret(v) {
		return ""
	}
	return v
}
