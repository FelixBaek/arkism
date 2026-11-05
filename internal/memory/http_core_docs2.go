package memory

import "net/http"

type documentRoute func(s *Service, r *http.Request, scope Scope, id string, page DocumentPage) (any, error)

// registerDocs2CoreHTTP serves the document chunk and upload history views.
func registerDocs2CoreHTTP(mux routeRegistrar, s *Service, _ *requestMetrics) {
	for route, fn := range map[string]documentRoute{
		"GET /v1/files/memories/documents/{id}/chunks": func(s *Service, r *http.Request, scope Scope, id string, page DocumentPage) (any, error) {
			return s.DocumentChunks(r.Context(), bearer(r), scope, id, page)
		},
		"GET /v1/files/memories/documents/{id}/history": func(s *Service, r *http.Request, scope Scope, id string, page DocumentPage) (any, error) {
			return s.DocumentHistory(r.Context(), bearer(r), scope, id, page)
		},
		"GET /v1/knowledge/sources/{id}/chunks": func(s *Service, r *http.Request, scope Scope, id string, page DocumentPage) (any, error) {
			return s.KnowledgeChunks(r.Context(), bearer(r), scope, id, page)
		},
		"GET /v1/knowledge/sources/{id}/history": func(s *Service, r *http.Request, scope Scope, id string, page DocumentPage) (any, error) {
			return s.KnowledgeHistory(r.Context(), bearer(r), scope, id, page)
		},
	} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			scope, page, e := parseDocumentQuery(r.URL.RawQuery)
			if e != nil {
				respond(w, nil, e)
				return
			}
			out, e := fn(s, r, scope, r.PathValue("id"), page)
			respond(w, out, e)
		})
	}
}
