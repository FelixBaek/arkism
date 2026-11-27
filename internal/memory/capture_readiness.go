package memory

import (
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
