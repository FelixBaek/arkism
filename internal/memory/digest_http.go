package memory

import "net/http"

// digestRoutes serves /v1/digests/*. kind is "topic" or "user" (query or body).
func digestRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/digests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, e := s.ListDigests(r.Context(), bearer(r), scopeQuery(r), q.Get("kind"), q.Get("status"))
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/digests/user/context", func(w http.ResponseWriter, r *http.Request) {
		v, _, e := s.ApprovedUserDigest(r.Context(), bearer(r), scopeQuery(r))
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/digests/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.GetDigest(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("kind"), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/digests/{id}/decide", func(w http.ResponseWriter, r *http.Request) {
		var in DigestDecisionInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.DecideDigest(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/digests/regenerate", func(w http.ResponseWriter, r *http.Request) {
		var in DigestRegenerateInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RegenerateDigest(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/digests/plan", func(w http.ResponseWriter, r *http.Request) {
		var in DigestPlanInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.PlanDigests(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/digests/proposals", func(w http.ResponseWriter, r *http.Request) {
		var in DigestProposalInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ProposeDigest(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/digests/user/judge", func(w http.ResponseWriter, r *http.Request) {
		var in DigestJudgeInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.JudgeUserDigest(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/digests/recall", func(w http.ResponseWriter, r *http.Request) {
		var in DigestRecallInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.RecallDigests(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}
