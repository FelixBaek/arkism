package memory

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Runs against a fresh, disposable loopback database; no operating data is read.
func TestPostgresContextValidationSourceCorrection(t *testing.T) {
	exerciseContextValidation(t, isolatedPostgres(t))
}

func TestPostgresContextValidationLocksCurrentSourceAfterAuthorization(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	memory := f.activeMemory("synthetic authorized body")
	in := validationReplay(t, f, validationInput(memory))
	f.svc.Repo = &validationRaceRepo{Repository: repo, before: func() {
		source, e := repo.Get(testCtx, "sources", scopeA, memory.Sources[0].SourceID)
		if e != nil {
			t.Fatal(e)
		}
		source.PrivacyQuarantined = true
		if e = repo.Replace(testCtx, "sources", source, source.Revision); e != nil {
			t.Fatal(e)
		}
	}}
	out, e := f.svc.ValidateContext(testCtx, f.human, in)
	assertValidationUnavailable(t, out, e)
}

func TestPostgresContextValidationHTTPUsesForcedRLSAndSourcePermission(t *testing.T) {
	admin := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = admin
	memory := f.activeMemory("synthetic current memory body")
	for _, grant := range []PrincipalGrant{
		{PrincipalID: "synthetic-validator", Role: roleAgent, Token: testPrincipalToken("synthetic-validator-token"), OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "read:memories", "read:sources"}},
		{PrincipalID: "synthetic-memory-only", Role: roleAgent, Token: testPrincipalToken("synthetic-memory-only-token"), OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "read:memories"}},
	} {
		if e := admin.ProvisionPrincipal(testCtx, grant); e != nil {
			t.Fatal(e)
		}
	}
	app := restrictedPostgres(t, admin)
	auth, e := NewPostgresAuthorizer(app)
	if e != nil {
		t.Fatal(e)
	}
	h := Handler(&Service{Repo: app, Auth: auth})
	in := validationInput(memory)
	var out ContextValidationOutput
	doPostgresHTTP(t, h, testPrincipalToken("synthetic-validator-token"), http.MethodPost, "/v1/context/validate", in, http.StatusOK, &out)
	if !out.Integrity || len(out.Items) != 1 || out.Items[0].Status != "valid" {
		t.Fatal("valid restricted-role HTTP snapshot", out)
	}
	if strings.Contains(structuredText(out), memory.Text) {
		t.Fatal("validation disclosed original body")
	}
	// A memory read grant must not silently grant current original-source access.
	out = ContextValidationOutput{}
	doPostgresHTTP(t, h, testPrincipalToken("synthetic-memory-only-token"), http.MethodPost, "/v1/context/validate", in, http.StatusOK, &out)
	if out.Integrity || out.Fingerprint != "" || len(out.Items) != 1 || out.Items[0].Status != "unavailable" || out.Items[0].Fingerprint != "" {
		t.Fatal("source policy bypassed", out)
	}
	in.Scope = scopeB
	// Existing policy denial auditing can itself be refused by foreign-scope
	// RLS; either denial or dependency failure must return no validation data.
	request := httptest.NewRequest(http.MethodPost, "/v1/context/validate", strings.NewReader(structuredText(in)))
	request.Header.Set("Authorization", "Bearer synthetic-validator-token")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden && response.Code != http.StatusServiceUnavailable {
		t.Fatal("foreign-scope validation was not refused", response.Code)
	}
	if strings.Contains(response.Body.String(), "fingerprint") || strings.Contains(response.Body.String(), memory.ID) || strings.Contains(response.Body.String(), "items") {
		t.Fatal("foreign scope revealed validation state")
	}
}
