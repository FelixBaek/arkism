package memory

import (
	"context"
	"net/http"
)

const captureRuntimeHeader = "X-Arkism-Runtime-Id"

func rejectCaptureRuntime(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusConflict, map[string]string{"code": "capture_runtime_mismatch"})
}

func captureRuntimeWrite(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/capture", "/v1/capture/analysis", "/v1/sources", "/v1/session-archives/chunks", "/v1/session-archives/commit", "/v1/work-checkpoints/save", "/v1/import/sessions":
		return true
	default:
		return false
	}
}

type captureReadiness struct {
	ExtractionEnabled bool   `json:"extraction_enabled"`
	WriteAuthorized   bool   `json:"write_authorized"`
	RuntimeID         string `json:"runtime_id,omitempty"`
}

func (s *Service) captureReadiness(ctx context.Context, token string, scope Scope) (captureReadiness, error) {
	actor, err := s.authorize(ctx, token, "read:memory_candidates", scope, "", "")
	if err != nil {
		return captureReadiness{}, err
	}
	if actor.Role != roleService {
		return captureReadiness{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	if err := s.authorizeCaptureWrites(ctx, token, scope); err != nil {
		return captureReadiness{}, err
	}
	if scope.AgentID != "" {
		userCtx, userScope := captureClaimScope(ctx, CaptureInput{Scope: scope}, CaptureClaim{Basis: "explicit_user"})
		if _, err := s.authorize(userCtx, token, "read:memory_candidates", userScope, "", ""); err != nil {
			return captureReadiness{}, err
		}
		if err := s.authorizeCaptureWrites(userCtx, token, userScope); err != nil {
			return captureReadiness{}, err
		}
	}
	settings, err := s.spaceSettings(ctx, scope)
	if err != nil {
		return captureReadiness{}, err
	}
	return captureReadiness{ExtractionEnabled: settings.ExtractionEnabled, WriteAuthorized: true, RuntimeID: s.CaptureRuntimeID}, nil
}

func (s *Service) authorizeCaptureWrites(ctx context.Context, token string, scope Scope) error {
	for _, action := range []string{"write:sources", "write:memory_candidates"} {
		if _, err := s.authorize(ctx, token, action, scope, "", ""); err != nil {
			return err
		}
	}
	return nil
}
