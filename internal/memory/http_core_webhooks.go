package memory

import (
	"net/http"
	"strconv"
)

// Kept beside the routes so the shared policy table needs no edit.
func init() {
	human := []string{roleHuman}
	for pattern, p := range map[string]routePolicy{
		"GET /v1/webhooks":                 {action: "write:webhooks", scope: routeQuery, roles: human},
		"POST /v1/webhooks":                {action: "write:webhooks", scope: routeJSON, limit: 64 << 10, roles: human},
		"GET /v1/webhooks/{id}":            {action: "write:webhooks", scope: routeQuery, roles: human},
		"PATCH /v1/webhooks/{id}":          {action: "write:webhooks", scope: routeJSON, limit: 64 << 10, roles: human},
		"DELETE /v1/webhooks/{id}":         {action: "write:webhooks", scope: routeQuery, roles: human},
		"GET /v1/webhooks/{id}/deliveries": {action: "write:webhooks", scope: routeQuery, roles: human},
		"POST /v1/webhooks/{id}/test":      {action: "write:webhooks", scope: routeJSON, limit: 16 << 10, roles: human},
		"GET /v1/llm-requests":             {action: "read:llm_requests", scope: routeQuery, roles: human},
		"GET /v1/llm-requests/stats":       {action: "read:llm_requests", scope: routeQuery, roles: human},
	} {
		httpRoutePolicies[pattern] = p
	}
}

func queryInt(r *http.Request, name string) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil
}

func registerWebhooksCoreHTTP(mux routeRegistrar, s *Service, _ *requestMetrics) {
	mux.HandleFunc("GET /v1/webhooks", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.ListWebhooks(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/webhooks", func(w http.ResponseWriter, r *http.Request) {
		var in WebhookInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateWebhook(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.GetWebhook(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("PATCH /v1/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in WebhookUpdate
		if !decode(w, r, &in) {
			return
		}
		out, e := s.UpdateWebhook(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("DELETE /v1/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := s.DeleteWebhook(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, map[string]bool{"ok": true}, e)
	})
	mux.HandleFunc("GET /v1/webhooks/{id}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		limit, ok := queryInt(r, "limit")
		if !ok {
			respond(w, nil, ErrInvalid)
			return
		}
		out, e := s.ListWebhookDeliveries(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"), r.URL.Query().Get("before"), limit)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/webhooks/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scope Scope `json:"scope"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, e := s.TestWebhook(r.Context(), bearer(r), in.Scope, r.PathValue("id"))
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/llm-requests", func(w http.ResponseWriter, r *http.Request) {
		limit, ok := queryInt(r, "limit")
		if !ok {
			respond(w, nil, ErrInvalid)
			return
		}
		q := r.URL.Query()
		out, e := s.ListLLMRequests(r.Context(), bearer(r), scopeQuery(r), LLMRequestFilter{Operation: q.Get("operation"), Status: q.Get("status"), Before: q.Get("before"), Limit: limit})
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/llm-requests/stats", func(w http.ResponseWriter, r *http.Request) {
		days, ok := queryInt(r, "days")
		if !ok {
			respond(w, nil, ErrInvalid)
			return
		}
		out, e := s.LLMRequestStatsFor(r.Context(), bearer(r), scopeQuery(r), days)
		respond(w, out, e)
	})
}
