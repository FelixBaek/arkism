package memory

import (
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"
)

// Space routes keep their policies here; init merges them into the one route table.
var spaceRoutePolicies = map[string]routePolicy{
	"GET /v1/spaces":                                          {action: "read:memories", scope: routeQuery},
	"GET /v1/spaces/template-schema":                          {action: "read:space_configs", scope: routeQuery},
	"GET /v1/spaces/{workspace}/{project}/config":             {action: "read:space_configs", scope: routePath},
	"PUT /v1/spaces/{workspace}/{project}/config":             {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"PATCH /v1/spaces/{workspace}/{project}/config":           {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"GET /v1/spaces/{workspace}/{project}/config/effective":   {action: "search:memories", scope: routePath},
	"POST /v1/spaces/{workspace}/{project}/aliases":           {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"DELETE /v1/spaces/{workspace}/{project}/aliases/{alias}": {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"POST /v1/spaces/{workspace}/{project}/export":            {action: "read:memories", scope: routePath},
	"POST /v1/spaces/{workspace}/{project}/import":            {action: "write:memory_candidates", scope: routePath, roles: []string{roleHuman}},
	"POST /v1/spaces/{workspace}/{project}/clone":             {action: "read:memories", scope: routePath},
	"GET /v1/spaces/exports/{job}":                            {action: "read:memories", scope: routeQuery},
	"GET /v1/spaces/exports/{job}/download":                   {action: "read:memories", scope: routeQuery},
	"GET /v1/spaces/imports/{job}":                            {action: "read:memories", scope: routeQuery},
	"DELETE /v1/spaces/{workspace}/{project}":                 {action: "delete:spaces", scope: routePath, roles: []string{roleHuman}},
	"DELETE /v1/spaces/{workspace}/{project}/memories":        {action: "delete:memories", scope: routePath, roles: []string{roleHuman}},
	"POST /v1/spaces/delete":                                  {action: "delete:spaces", scope: routeQuery, roles: []string{roleHuman}},
	"POST /v1/spaces/{workspace}/{project}/rename":            {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
}

func init() {
	for pattern, policy := range spaceRoutePolicies {
		httpRoutePolicies[pattern] = policy
	}
}

func pathSpace(r *http.Request) Scope {
	return Scope{WorkspaceID: r.PathValue("workspace"), ProjectID: r.PathValue("project")}
}

func registerSpacesCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("GET /v1/spaces", s.coreHTTPGetV1Spaces)
	mux.HandleFunc("GET /v1/spaces/template-schema", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, SpaceTemplateSchema()) })
	mux.HandleFunc("GET /v1/spaces/{workspace}/{project}/config", s.coreHTTPGetV1SpacesConfig)
	mux.HandleFunc("PUT /v1/spaces/{workspace}/{project}/config", s.coreHTTPPutV1SpacesConfig)
	mux.HandleFunc("PATCH /v1/spaces/{workspace}/{project}/config", s.coreHTTPPatchV1SpacesConfig)
	mux.HandleFunc("GET /v1/spaces/{workspace}/{project}/config/effective", s.coreHTTPGetV1SpacesConfigEffective)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/aliases", s.coreHTTPPostV1SpacesAliases)
	mux.HandleFunc("DELETE /v1/spaces/{workspace}/{project}/aliases/{alias}", s.coreHTTPDeleteV1SpacesAliases)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/export", s.coreHTTPPostV1SpacesExport)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/import", s.coreHTTPPostV1SpacesImport)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/clone", s.coreHTTPPostV1SpacesClone)
	mux.HandleFunc("GET /v1/spaces/exports/{job}", s.coreHTTPGetV1SpacesJob)
	mux.HandleFunc("GET /v1/spaces/imports/{job}", s.coreHTTPGetV1SpacesJob)
	mux.HandleFunc("GET /v1/spaces/exports/{job}/download", s.coreHTTPGetV1SpacesExportDownload)
	mux.HandleFunc("DELETE /v1/spaces/{workspace}/{project}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.DeleteSpace(r.Context(), bearer(r), pathSpace(r), r.URL.Query().Get("confirm"))
		respond(w, out, err)
	})
	mux.HandleFunc("DELETE /v1/spaces/{workspace}/{project}/memories", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.ClearSpaceMemories(r.Context(), bearer(r), pathSpace(r), q.Get("confirm"), q.Get("kind"))
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/spaces/delete", s.coreHTTPPostV1SpacesDelete)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/rename", s.coreHTTPPostV1SpacesRename)
}

func (s *Service) coreHTTPPostV1SpacesDelete(w http.ResponseWriter, r *http.Request) {
	var in SpaceDeleteBatchInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.DeleteSpaces(r.Context(), bearer(r), scopeQuery(r).WorkspaceID, in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1SpacesRename(w http.ResponseWriter, r *http.Request) {
	var in SpaceRenameInput
	if !decode(w, r, &in) {
		return
	}
	extendSpaceTransfer(w) // a large space can take longer than one ordinary request
	out, err := s.RenameSpace(r.Context(), bearer(r), pathSpace(r), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPGetV1Spaces(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.ListSpaces(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("after"), limit)
	respond(w, out, err)
}

func (s *Service) coreHTTPGetV1SpacesConfig(w http.ResponseWriter, r *http.Request) {
	out, err := s.GetSpaceConfig(r.Context(), bearer(r), pathSpace(r))
	respond(w, out, err)
}

func (s *Service) coreHTTPGetV1SpacesConfigEffective(w http.ResponseWriter, r *http.Request) {
	out, err := s.EffectiveSpaceConfig(r.Context(), bearer(r), pathSpace(r))
	respond(w, out, err)
}

func (s *Service) coreHTTPPutV1SpacesConfig(w http.ResponseWriter, r *http.Request) {
	var in SpaceConfigInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.PutSpaceConfig(r.Context(), bearer(r), pathSpace(r), in, true)
	respond(w, out, err)
}

func (s *Service) coreHTTPPatchV1SpacesConfig(w http.ResponseWriter, r *http.Request) {
	var in SpaceConfigInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.PutSpaceConfig(r.Context(), bearer(r), pathSpace(r), in, false)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1SpacesAliases(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Alias string `json:"alias"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.SetSpaceAlias(r.Context(), bearer(r), pathSpace(r), in.Alias, true)
	respond(w, out, err)
}

func (s *Service) coreHTTPDeleteV1SpacesAliases(w http.ResponseWriter, r *http.Request) {
	out, err := s.SetSpaceAlias(r.Context(), bearer(r), pathSpace(r), r.PathValue("alias"), false)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1SpacesExport(w http.ResponseWriter, r *http.Request) {
	var in SpaceExportInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.ExportSpace(r.Context(), bearer(r), pathSpace(r), in)
	respondAccepted(w, out, err)
}

// coreHTTPPostV1SpacesImport takes a JSON body ({bundle, ...}, up to 8 MB) or,
// as application/x-ndjson, the bundle itself of any size up to 1 GiB with the
// options in the query (import_as, as_template, document_conflict).
func (s *Service) coreHTTPPostV1SpacesImport(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get(headerContentType)); mt == "application/x-ndjson" {
		extendSpaceTransfer(w)
		q := r.URL.Query()
		in := SpaceImportInput{ImportAs: q.Get("import_as"), AsTemplate: q.Get("as_template") == "true", DocumentConflict: q.Get("document_conflict")}
		out, err := s.ImportSpaceStream(r.Context(), bearer(r), pathSpace(r), in, http.MaxBytesReader(w, r.Body, spaceStreamMaxBytes))
		respondAccepted(w, out, err)
		return
	}
	var in SpaceImportInput
	if !decodeLimit(w, r, &in, spaceBundleMaxBytes+(64<<10)) {
		return
	}
	out, err := s.ImportSpace(r.Context(), bearer(r), pathSpace(r), in)
	respondAccepted(w, out, err)
}

// extendSpaceTransfer lifts the server-wide read and write deadlines for one bundle transfer.
func extendSpaceTransfer(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(spaceJobTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}

func (s *Service) coreHTTPPostV1SpacesClone(w http.ResponseWriter, r *http.Request) {
	var in SpaceCloneInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.CloneSpace(r.Context(), bearer(r), pathSpace(r), in)
	respondAccepted(w, out, err)
}

func respondAccepted(w http.ResponseWriter, out any, err error) {
	if err != nil {
		respond(w, nil, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Service) coreHTTPGetV1SpacesJob(w http.ResponseWriter, r *http.Request) {
	out, err := s.GetSpaceJob(r.Context(), bearer(r), scopeQuery(r), r.PathValue("job"))
	respond(w, out, err)
}

func (s *Service) coreHTTPGetV1SpacesExportDownload(w http.ResponseWriter, r *http.Request) {
	stream, err := s.DownloadSpaceExport(r.Context(), bearer(r), scopeQuery(r), r.PathValue("job"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	// Streamed without a length: a transfer cut by an error ends without the trailer line, and import refuses it.
	extendSpaceTransfer(w)
	w.Header().Set(headerContentType, "application/x-ndjson")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "space-bundle.jsonl"}))
	w.WriteHeader(http.StatusOK)
	if err = stream(w); err != nil {
		slog.Warn("space bundle stream cut", "job", r.PathValue("job"), "error", captureErrorCode(err))
	}
}
