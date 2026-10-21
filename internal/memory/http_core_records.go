package memory

import (
	"net/http"
	"strconv"
)

func registerRecordsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/{collection}", s.coreHTTPPostV1Collection)
	mux.HandleFunc("GET /v1/{collection}/{id}", s.coreHTTPGetV1CollectionId)
	mux.HandleFunc("POST /v1/{collection}/{id}/transition", s.coreHTTPPostV1CollectionIdTransition)
	mux.HandleFunc("DELETE /v1/{collection}/{id}", s.coreHTTPDeleteV1CollectionId)
}

func (s *Service) coreHTTPPostV1Collection(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.Create(r.Context(), bearer(r), r.PathValue("collection"), in)
	if e != nil {
		respond(w, nil, e)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Service) coreHTTPGetV1CollectionId(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := Scope{WorkspaceID: q.Get("workspace_id"), ProjectID: q.Get("project_id"), UserID: q.Get("user_id"), AgentID: q.Get("agent_id")}
	v, e := s.Get(r.Context(), bearer(r), r.PathValue("collection"), scope, r.PathValue("id"))
	if e == nil && r.PathValue("collection") == colSkillVersions && v.ComparisonPlan != nil {
		respond(w, struct {
			Record
			ComparisonPlanSHA256 string `json:"comparison_plan_sha256"`
		}{v, comparisonPlanHash(v.ComparisonPlan)}, nil)
		return
	}
	respond(w, v, e)
}

func (s *Service) coreHTTPPostV1CollectionIdTransition(w http.ResponseWriter, r *http.Request) {
	var in ChangeInput
	if !decode(w, r, &in) {
		return
	}
	v, e := s.Transition(r.Context(), bearer(r), r.PathValue("collection"), r.PathValue("id"), in)
	respond(w, v, e)
}

func (s *Service) coreHTTPDeleteV1CollectionId(w http.ResponseWriter, r *http.Request) {
	dryRun, err := parseDeleteQuery(r.URL.RawQuery)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var in ChangeInput
	if !decode(w, r, &in) {
		return
	}
	if dryRun {
		out, e := s.PreviewDeletion(r.Context(), bearer(r), r.PathValue("collection"), r.PathValue("id"), in)
		respond(w, out, e)
		return
	}
	if r.PathValue("collection") == "sources" && s.coreHTTPDeleteSource(w, r, in) {
		return
	}
	e := s.Delete(r.Context(), bearer(r), r.PathValue("collection"), r.PathValue("id"), in)
	if e == nil && (r.PathValue("collection") == "sources" || r.PathValue("collection") == "memories" || r.PathValue("collection") == "memory_candidates") {
		col := r.PathValue("collection")
		coverage := sourceRecordsMemoryDependents
		if col == "memories" {
			coverage = "memory_relations_evaluations"
		} else if col == "memory_candidates" {
			coverage = "candidate_relations_evaluations"
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"deleted": true, "deletion_job_id": deletionJobID(col, in.Scope, r.PathValue("id")), "coverage": coverage, "full_erasure_verified": false})
		return
	}
	respond(w, map[string]bool{"deleted": true}, e)
}

// Source erasure uses the privacy job protocol except in an explicit legacy test store.
func (s *Service) coreHTTPDeleteSource(w http.ResponseWriter, r *http.Request, in ChangeInput) bool {
	if _, ok := s.Repo.(privacyStore); ok {
		out, e := s.RequestPrivacyDeletion(r.Context(), bearer(r), PrivacyDeletionRequest{Scope: in.Scope, IdempotencyKey: "legacy-source-delete-" + ContentHash(r.PathValue("id")+":"+strconv.FormatInt(in.ExpectedRevision, 10)), TargetType: "source_event", TargetIDs: []string{r.PathValue("id")}, Reason: "legacy_source_delete", RetentionPolicyRef: retentionExternal, ExpectedRevision: in.ExpectedRevision})
		if e != nil {
			respond(w, nil, e)
			return true
		}
		writeJSON(w, http.StatusAccepted, out)
		return true
	}
	if _, testOnly := s.Repo.(legacySourceDeleteTestStore); !testOnly {
		respond(w, nil, ErrUnavailable)
		return true
	}
	return false
}
