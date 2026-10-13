package memory

import (
	"net/http/httptest"
	"testing"
)

func TestRecoveryRoutes(t *testing.T) {
	h := RecoveryHandler(&Service{Repo: readinessRepo{}})
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/healthz", 200}, {"GET", "/readyz", 503},
		{"GET", "/recovery-identity", 503},
		{"POST", "/v1/search", 404}, {"POST", "/v1/context", 404},
		{"POST", "/v1/sources", 404}, {"POST", "/v1/deletions/export", 404},
		{"GET", "/v1/skills/example", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestRecoveryIdentityNotExposedByNormalAPI(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, httptest.NewRequest("GET", "/recovery-identity", nil))
	if w.Code != 404 {
		t.Fatalf("normal API exposed recovery identity: %d", w.Code)
	}
}

func TestRecoveryPrivacyPermissionsAndIsolation(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(RecoveryHandler(f.svc))
	defer server.Close()
	request := PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "denied", TargetType: "work_checkpoint", TargetIDs: []string{"work"}, Reason: "owner_request"}
	for _, token := range []string{"", f.agent, f.work} {
		recoveryHTTP(t, server.URL, token, "POST", "/v1/privacy/deletions", request, nil, 403)
	}
	recoveryHTTP(t, server.URL, f.human, "POST", "/v1/privacy/deletions/runtime/claim", PrivacyScopeInput{Scope: scopeA}, nil, 403)
	recoveryHTTP(t, server.URL, f.agent, "POST", "/v1/session-archives/expire", SessionArchiveExpireInput{Scope: scopeA}, nil, 403)
	for _, path := range []string{
		"/v1/work-checkpoints/get", "/v1/work-checkpoints/save", "/v1/session-archives/get",
		"/v1/session-archives/list", "/v1/session-archives/read", "/v1/session-archives/search",
		"/v1/session-archives/used", "/v1/session-archives/commit", "/v1/session-archives/chunks",
		"/v1/privacy/deletions/work/hold", "/v1/privacy/deletions/work/hold/release",
		"/v1/deletions/export",
	} {
		recoveryHTTP(t, server.URL, f.human, "POST", path, map[string]any{"scope": scopeA}, nil, 404)
	}
	recoveryHTTP(t, server.URL, f.human, "POST", "/v1/privacy/deletions/fence", nil, nil, 405)
	recoveryHTTP(t, server.URL, f.human, "GET", "/readyz", nil, nil, 503)
}
