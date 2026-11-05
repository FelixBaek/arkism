package memory

import (
	"net/http"
	"strconv"
)

// Space routes keep their policies here; init merges them into the one route table.
var spaceRoutePolicies = map[string]routePolicy{
	"GET /v1/spaces": {action: "read:memories", scope: routeQuery},
	"GET /v1/spaces/{workspace}/{project}/config":             {action: "read:space_configs", scope: routePath},
	"PUT /v1/spaces/{workspace}/{project}/config":             {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"PATCH /v1/spaces/{workspace}/{project}/config":           {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"POST /v1/spaces/{workspace}/{project}/aliases":           {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"DELETE /v1/spaces/{workspace}/{project}/aliases/{alias}": {action: "write:space_configs", scope: routePath, roles: []string{roleHuman}},
	"POST /v1/spaces/{workspace}/{project}/export":            {action: "read:memories", scope: routePath},
	"POST /v1/spaces/{workspace}/{project}/import":            {action: "write:memory_candidates", scope: routePath, roles: []string{roleHuman}},
	"POST /v1/spaces/{workspace}/{project}/clone":             {action: "read:memories", scope: routePath},
	"GET /v1/spaces/exports/{job}":                            {action: "read:memories", scope: routeQuery},
	"GET /v1/spaces/exports/{job}/download":                   {action: "read:memories", scope: routeQuery},
	"GET /v1/spaces/imports/{job}":                            {action: "read:memories", scope: routeQuery},
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
	mux.HandleFunc("GET /v1/spaces/{workspace}/{project}/config", s.coreHTTPGetV1SpacesConfig)
	mux.HandleFunc("PUT /v1/spaces/{workspace}/{project}/config", s.coreHTTPPutV1SpacesConfig)
	mux.HandleFunc("PATCH /v1/spaces/{workspace}/{project}/config", s.coreHTTPPatchV1SpacesConfig)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/aliases", s.coreHTTPPostV1SpacesAliases)
	mux.HandleFunc("DELETE /v1/spaces/{workspace}/{project}/aliases/{alias}", s.coreHTTPDeleteV1SpacesAliases)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/export", s.coreHTTPPostV1SpacesExport)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/import", s.coreHTTPPostV1SpacesImport)
	mux.HandleFunc("POST /v1/spaces/{workspace}/{project}/clone", s.coreHTTPPostV1SpacesClone)
	mux.HandleFunc("GET /v1/spaces/exports/{job}", s.coreHTTPGetV1SpacesJob)
	mux.HandleFunc("GET /v1/spaces/imports/{job}", s.coreHTTPGetV1SpacesJob)
	mux.HandleFunc("GET /v1/spaces/exports/{job}/download", s.coreHTTPGetV1SpacesExportDownload)
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

func (s *Service) coreHTTPPostV1SpacesImport(w http.ResponseWriter, r *http.Request) {
	var in SpaceImportInput
	if !decodeLimit(w, r, &in, spaceBundleMaxBytes+(64<<10)) {
		return
	}
	out, err := s.ImportSpace(r.Context(), bearer(r), pathSpace(r), in)
	respondAccepted(w, out, err)
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
	bundle, err := s.DownloadSpaceExport(r.Context(), bearer(r), scopeQuery(r), r.PathValue("job"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	writeAttachment(w, "application/x-ndjson", "space-bundle.jsonl", []byte(bundle))
}
