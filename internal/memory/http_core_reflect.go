package memory

import (
	"net/http"
	"strconv"
)

func registerReflectCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/reflect", func(w http.ResponseWriter, r *http.Request) {
		var in ReflectInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Reflect(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/directives", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 0
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			limit = n
		}
		v, e := s.ListDirectives(r.Context(), bearer(r), scopeQuery(r), limit, q.Get("cursor"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/directives", func(w http.ResponseWriter, r *http.Request) {
		var in DirectiveInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.CreateDirective(r.Context(), bearer(r), in)
		if e != nil {
			respond(w, nil, e)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	})
	mux.HandleFunc("GET /v1/directives/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.GetDirective(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/directives/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in DirectiveInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.UpdateDirective(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("DELETE /v1/directives/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in DirectiveDeleteInput
		if !decode(w, r, &in) {
			return
		}
		e := s.DeleteDirective(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, map[string]bool{"deleted": e == nil}, e)
	})
}
