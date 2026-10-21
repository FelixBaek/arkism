package memory

import (
	"net/http"
)

func registerPrivacyCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/privacy/deletions", s.coreHTTPPostV1PrivacyDeletions)
	mux.HandleFunc("GET /v1/privacy/deletions/{id}", s.coreHTTPGetV1PrivacyDeletionsId)
	mux.HandleFunc("POST /v1/privacy/deletions/runtime/claim", s.coreHTTPPostV1PrivacyDeletionsRuntimeClaim)
	mux.HandleFunc("POST /v1/privacy/deletions/fence", s.coreHTTPPostV1PrivacyDeletionsFence)
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/runtime-receipt", s.coreHTTPPostV1PrivacyDeletionsIdRuntimeReceipt)
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/advance", s.coreHTTPPostV1PrivacyDeletionsIdAdvance)
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/hold", s.coreHTTPPostV1PrivacyDeletionsIdHold)
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/hold/release", s.coreHTTPPostV1PrivacyDeletionsIdHoldRelease)
}

func registerDeletionsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/deletions/export", s.coreHTTPPostV1DeletionsExport)
}

func (s *Service) coreHTTPPostV1PrivacyDeletions(w http.ResponseWriter, r *http.Request) {
	var in PrivacyDeletionRequest
	if !decode(w, r, &in) {
		return
	}
	out, err := s.RequestPrivacyDeletion(r.Context(), bearer(r), in)
	if err != nil {
		respond(w, nil, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Service) coreHTTPGetV1PrivacyDeletionsId(w http.ResponseWriter, r *http.Request) {
	scope, err := parseSourceTraceQuery(r.URL.RawQuery)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := s.GetPrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), scope)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1PrivacyDeletionsRuntimeClaim(w http.ResponseWriter, r *http.Request) {
	var in PrivacyScopeInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.ClaimPrivacyDeletion(r.Context(), bearer(r), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1PrivacyDeletionsFence(w http.ResponseWriter, r *http.Request) {
	var in PrivacyFenceInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.CheckPrivacyFence(r.Context(), bearer(r), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1PrivacyDeletionsIdRuntimeReceipt(w http.ResponseWriter, r *http.Request) {
	var in PrivacyRuntimeReceiptInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.RecordPrivacyRuntimeReceipt(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1PrivacyDeletionsIdAdvance(w http.ResponseWriter, r *http.Request) {
	var in PrivacyRevisionInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.AdvancePrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1PrivacyDeletionsIdHold(w http.ResponseWriter, r *http.Request) {
	var in PrivacyHoldInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.HoldPrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1PrivacyDeletionsIdHoldRelease(w http.ResponseWriter, r *http.Request) {
	var in PrivacyHoldInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.ReleasePrivacyDeletionHold(r.Context(), bearer(r), r.PathValue("id"), in)
	respond(w, out, err)
}

func (s *Service) coreHTTPPostV1DeletionsExport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope   Scope `json:"scope"`
		Version *int  `json:"version,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version != nil && *in.Version == 2 {
		out, e := s.ExportRecoveryDeletions(r.Context(), bearer(r), in.Scope)
		respond(w, out, e)
		return
	}
	if in.Version != nil && *in.Version != 1 {
		respond(w, nil, ErrInvalid)
		return
	}
	out, e := s.ExportDeletions(r.Context(), bearer(r), in.Scope)
	respond(w, out, e)
}
