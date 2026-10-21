package memory

import (
	"net/http"
)

func registerCapabilitiesCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
		caps, e := s.Capabilities(r.Context(), bearer(r), scope)
		respond(w, caps, e)
	})
}
