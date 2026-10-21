package memory

import (
	"net/http"
)

func registerPrivacyCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/privacy/deletions", func(w http.ResponseWriter, r *http.Request) {
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
	})
	mux.HandleFunc("GET /v1/privacy/deletions/{id}", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.GetPrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), scope)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/runtime/claim", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyScopeInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.ClaimPrivacyDeletion(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/fence", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyFenceInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.CheckPrivacyFence(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/runtime-receipt", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyRuntimeReceiptInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.RecordPrivacyRuntimeReceipt(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/advance", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyRevisionInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.AdvancePrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/hold", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyHoldInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.HoldPrivacyDeletion(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/privacy/deletions/{id}/hold/release", func(w http.ResponseWriter, r *http.Request) {
		var in PrivacyHoldInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.ReleasePrivacyDeletionHold(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
}

func registerDeletionsCoreHTTP(mux routeRegistrar, s *Service, stats *requestMetrics) {
	mux.HandleFunc("POST /v1/deletions/export", func(w http.ResponseWriter, r *http.Request) {
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
	})
}
