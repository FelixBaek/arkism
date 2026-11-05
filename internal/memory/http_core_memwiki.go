package memory

import "net/http"

// Wiki routes for memory-built pages, the folder tree, page move/rename and
// the markdown export (wiki_memory.go, wiki_tree.go, wiki_export.go).
func registerMemwikiCoreHTTP(mux routeRegistrar, s *Service, _ *requestMetrics) {
	mux.HandleFunc("POST /v1/wiki/build_from_memories", func(w http.ResponseWriter, r *http.Request) {
		var in WikiMemoryBuildInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.BuildWikiFromMemories(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/wiki/tree", func(w http.ResponseWriter, r *http.Request) {
		scope, _, e := parseWikiQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.WikiTree(r.Context(), bearer(r), scope)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/wiki/folders", func(w http.ResponseWriter, r *http.Request) {
		var in WikiFolderInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateWikiFolder(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("PATCH /v1/wiki/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in WikiPagePatchInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.PatchWikiPage(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/wiki/export", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseWikiQuery(r.URL.RawQuery, "format")
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.ExportWiki(r.Context(), bearer(r), scope, q.Get("format"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		writeAttachment(w, out.ContentType, out.Filename, out.Data)
	})
}
