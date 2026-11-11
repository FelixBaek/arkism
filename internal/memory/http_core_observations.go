package memory

import (
	"context"
	"net/http"
	"strconv"
)

// queryLimit reads an optional ?limit= (0 when absent; the service applies its default and bound).
func queryLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, ErrInvalid
	}
	return n, nil
}

type mentalModelDeleteInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func registerObservationsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/observations/consolidate", func(w http.ResponseWriter, r *http.Request) {
		var in ObservationConsolidateInput
		if !decode(w, r, &in) {
			return
		}
		s.respondLater(w, r, in.Scope, func(ctx context.Context, token string) (any, error) { return s.ConsolidateObservations(ctx, token, in) })
	})
	mux.HandleFunc("GET /v1/observations", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, e := queryLimit(r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		f := ObservationFilter{Status: q.Get("status"), Durability: q.Get("durability"), SourceMemoryID: q.Get("source_memory_id"), Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: limit}
		v, e := s.ListObservations(r.Context(), bearer(r), scopeQuery(r), f)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/observations/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.GetObservation(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/observations/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ObservationHistory(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/observations/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var in ObservationReviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ReviewObservation(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/observations/review_batch", func(w http.ResponseWriter, r *http.Request) {
		var in ObservationReviewBatchInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ReviewObservations(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	registerMentalModelsHTTP(mux, s)
}

func registerMentalModelsHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/mental-models", func(w http.ResponseWriter, r *http.Request) {
		var in MentalModelInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.CreateMentalModel(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/mental-models", func(w http.ResponseWriter, r *http.Request) {
		limit, e := queryLimit(r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.ListMentalModels(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), limit)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/mental-models/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.GetMentalModel(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("PATCH /v1/mental-models/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in MentalModelInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.UpdateMentalModel(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("DELETE /v1/mental-models/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in mentalModelDeleteInput
		if !decode(w, r, &in) {
			return
		}
		e := s.DeleteMentalModel(r.Context(), bearer(r), in.Scope, r.PathValue("id"), in.ExpectedRevision)
		respond(w, map[string]bool{"deleted": e == nil}, e)
	})
	mux.HandleFunc("GET /v1/mental-models/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.MentalModelHistory(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/mental-models/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		var in MentalModelRefreshInput
		if !decode(w, r, &in) {
			return
		}
		s.respondLater(w, r, in.Scope, func(ctx context.Context, token string) (any, error) {
			return s.RefreshMentalModel(ctx, token, r.PathValue("id"), in)
		})
	})
	mux.HandleFunc("POST /v1/mental-models/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var in MentalModelReviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ReviewMentalModel(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
}
