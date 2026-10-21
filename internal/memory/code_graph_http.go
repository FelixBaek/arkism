package memory

import (
	"context"
	"net/http"
)

func codeGraphRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/code_graph/repos", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.CodeGraphRepos(r.Context(), bearer(r), scopeQuery(r))
		if e == nil {
			respond(w, map[string]any{"repos": out, "authority": AuthorityDataOnly}, nil)
			return
		}
		respond(w, nil, e)
	})
	mux.HandleFunc("GET /v1/code_graph/files", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.CodeGraphFiles(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("repo"))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/code_graph/batch", func(w http.ResponseWriter, r *http.Request) {
		var in CodeGraphBatch
		if !decodeLimit(w, r, &in, 24<<20) {
			return
		}
		out, e := s.UploadCodeGraph(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/code_graph/finish", func(w http.ResponseWriter, r *http.Request) {
		var in CodeGraphFinish
		if !decodeLimit(w, r, &in, 64<<10) {
			return
		}
		out, e := s.FinishCodeGraph(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/code_graph/repos/delete", func(w http.ResponseWriter, r *http.Request) {
		var in CodeGraphRepoInput
		if !decode(w, r, &in) {
			return
		}
		e := s.DeleteCodeGraphRepo(r.Context(), bearer(r), in)
		respond(w, map[string]bool{"deleted": e == nil}, e)
	})
	mux.HandleFunc("POST /v1/code_graph/changed", func(w http.ResponseWriter, r *http.Request) {
		var in CodeChangedQuery
		if !decodeLimit(w, r, &in, 256<<10) {
			return
		}
		out, e := s.CodeChanged(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	for path, run := range map[string]func(context.Context, string, CodeGraphQuery) (CodeGraphAnswer, error){
		"search": s.SearchCodeSymbols, "callers": s.CodeCallers, "callees": s.CodeCallees, "impact": s.CodeImpact,
	} {
		mux.HandleFunc("POST /v1/code_graph/"+path, func(w http.ResponseWriter, r *http.Request) {
			var in CodeGraphQuery
			if !decode(w, r, &in) {
				return
			}
			out, e := run(r.Context(), bearer(r), in)
			respond(w, out, e)
		})
	}
}
