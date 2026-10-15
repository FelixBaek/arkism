package memory

import "net/http"

// Collaboration uses the same authentication, scope and evidence checks as
// ordinary memory reads. A role profile narrows relevance, never permissions.
func collaborationRoutes(mux *http.ServeMux, s *Service) {
	handoffTransferRoutes(mux, s)
	mux.HandleFunc("POST /v1/memory/role-search", func(w http.ResponseWriter, r *http.Request) {
		var in RoleSearchInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.SearchForRole(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/knowledge-graph/relations", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeRelationInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.PutKnowledgeRelation(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/knowledge-graph/expand", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeGraphInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.ExpandKnowledgeGraph(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/work-checkpoints/recent", func(w http.ResponseWriter, r *http.Request) {
		var in RecentWorkInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.RecentWorkCheckpoints(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}
