package memory

import "net/http"

// Reflect and mental-model routes of the reflect-models package; init merges their policies into the one route table.
var reflectModelRoutePolicies = map[string]routePolicy{
	"POST /v1/mental-models/{id}/clear":        {action: "write:mental_models", scope: routeJSON, limit: 256 << 10, roles: []string{roleHuman}},
	"POST /v1/mental-models/cron-preview":      {action: "read:mental_models", scope: routeJSON, limit: 64 << 10},
	"GET /v1/observations/scopes":              {action: "read:observations", scope: routeQuery},
	"POST /v1/observations/strategies/preview": {action: "read:observations", scope: routeJSON, limit: 256 << 10},
	"DELETE /v1/observations":                  {action: "write:observations", scope: routeJSON, limit: 256 << 10, roles: []string{roleHuman}},
	"POST /v1/prompts/preview":                 {action: "search:memories", scope: routeJSON, limit: 64 << 10},
}

func init() {
	for pattern, policy := range reflectModelRoutePolicies {
		httpRoutePolicies[pattern] = policy
	}
}

func registerReflectModelsCoreHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/mental-models/{id}/clear", func(w http.ResponseWriter, r *http.Request) {
		var in MentalModelReviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.ClearMentalModel(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/mental-models/cron-preview", func(w http.ResponseWriter, r *http.Request) {
		var in CronPreviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.PreviewCron(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /v1/observations/scopes", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ListObservationScopes(r.Context(), bearer(r), scopeQuery(r))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/observations/strategies/preview", func(w http.ResponseWriter, r *http.Request) {
		var in StrategyPreviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.PreviewConsolidationStrategies(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("DELETE /v1/observations", func(w http.ResponseWriter, r *http.Request) {
		var in ObservationDeleteInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.DeleteObservations(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/prompts/preview", func(w http.ResponseWriter, r *http.Request) {
		var in PromptPreviewInput
		if !decode(w, r, &in) {
			return
		}
		v, e := s.PreviewPrompt(r.Context(), bearer(r), in)
		respond(w, v, e)
	})
}
