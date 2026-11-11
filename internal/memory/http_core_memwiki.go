package memory

import (
	"context"
	"net/http"
	"strconv"
)

// Kept beside the routes so the shared policy table needs no edit.
func init() {
	human := []string{roleHuman}
	for pattern, p := range map[string]routePolicy{
		"PATCH /v1/wiki/folders":     {action: "write:wiki_pages", scope: routeJSON, limit: 16 << 10, roles: human},
		"DELETE /v1/wiki/folders":    {action: "write:wiki_pages", scope: routeQuery, roles: human},
		"DELETE /v1/wiki/pages/{id}": {action: "write:wiki_pages", scope: routeQuery, roles: human},
	} {
		httpRoutePolicies[pattern] = p
	}
}

// Wiki routes for memory-built pages, the folder tree, page move/rename and
// the markdown export (wiki_memory.go, wiki_tree.go, wiki_export.go).
func registerMemwikiCoreHTTP(mux routeRegistrar, s *Service, _ *requestMetrics) {
	mux.HandleFunc("POST /v1/wiki/build_from_memories", func(w http.ResponseWriter, r *http.Request) {
		var in WikiMemoryBuildInput
		if !decode(w, r, &in) {
			return
		}
		s.respondLater(w, r, in.Scope, func(ctx context.Context, token string) (any, error) { return s.BuildWikiFromMemories(ctx, token, in) })
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
	registerWikiFolderHTTP(mux, s)
	mux.HandleFunc("DELETE /v1/wiki/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseWikiQuery(r.URL.RawQuery, "expected_revision")
		rev, err := strconv.ParseInt(q.Get("expected_revision"), 10, 64)
		if e != nil || err != nil {
			respond(w, nil, ErrInvalid)
			return
		}
		out, e := s.DeleteWikiPage(r.Context(), bearer(r), r.PathValue("id"), WikiPageDeleteInput{Scope: scope, ExpectedRevision: rev})
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

// registerWikiFolderHTTP adds the folder create, move and delete routes.
func registerWikiFolderHTTP(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/wiki/folders", func(w http.ResponseWriter, r *http.Request) {
		var in WikiFolderInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.CreateWikiFolder(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("PATCH /v1/wiki/folders", func(w http.ResponseWriter, r *http.Request) {
		var in WikiFolderMoveInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.MoveWikiFolder(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("DELETE /v1/wiki/folders", func(w http.ResponseWriter, r *http.Request) {
		scope, q, e := parseWikiQuery(r.URL.RawQuery, "path", "recursive")
		recursive := q.Get("recursive")
		if e != nil || recursive != "" && recursive != "true" && recursive != "false" {
			respond(w, nil, ErrInvalid)
			return
		}
		out, e := s.DeleteWikiFolder(r.Context(), bearer(r), WikiFolderDeleteInput{Scope: scope, Path: q.Get("path"), Recursive: recursive == "true"})
		respond(w, out, e)
	})
}
