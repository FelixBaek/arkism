package memory

import "net/http"

// registerDocs3CoreHTTP serves a document's text (read, edit and extract again) and
// the documents zip (export a pick, import with a conflict policy).
func registerDocs3CoreHTTP(mux routeRegistrar, s *Service, _ *requestMetrics) {
	mux.HandleFunc("GET /v1/files/memories/documents/{id}/text", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.DocumentText(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, err)
	})
	mux.HandleFunc("PUT /v1/files/memories/documents/{id}/text", func(w http.ResponseWriter, r *http.Request) {
		var in DocumentTextInput
		if !decodeLimit(w, r, &in, fileExtractMaxText+64<<10) {
			return
		}
		out, err := s.EditDocumentText(r.Context(), bearer(r), r.PathValue("id"), in)
		s.startExtractJob(r, in.Scope, out, err)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/files/memories/documents/export", func(w http.ResponseWriter, r *http.Request) {
		var in DocumentsExportInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.ExportDocuments(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/files/memories/documents/import", func(w http.ResponseWriter, r *http.Request) {
		var in DocumentsImportInput
		if !decodeLimit(w, r, &in, docImportRequestBytes) {
			return
		}
		out, err := s.ImportDocuments(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}

// docImportRequestBytes is the zip as base64 plus the JSON around it.
const docImportRequestBytes = docZipMaxBytes*4/3 + 64<<10
