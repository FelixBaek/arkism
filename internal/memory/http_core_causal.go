package memory

import "net/http"

func registerCausalCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/causal-links", func(w http.ResponseWriter, r *http.Request) {
		var in CausalLinkInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.LinkCausal(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/causal-links", func(w http.ResponseWriter, r *http.Request) {
		limit, e := queryLimit(r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListCausalLinks(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), limit)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/causal-links/propose", func(w http.ResponseWriter, r *http.Request) {
		var in CausalProposeInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ProposeCausalLinks(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/causal-links/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var in CausalReviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ReviewCausalLink(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
}
