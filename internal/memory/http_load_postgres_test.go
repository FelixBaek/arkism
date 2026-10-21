package memory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The measurement includes the real TCP/auth/SQL/response boundary. No provider
// model is called and no user data or live database is involved.
func TestPostgresConcurrentHTTPRecallMeasurement(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "human-1", Role: "human", Token: f.human, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	f.svc.Repo = p
	expected := f.activeMemory("synthetic security load rule", "load-rule")
	app := restrictedPostgres(t, p)
	authorizer, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Repo, f.svc.Auth = app, authorizer
	srv := httptest.NewServer(Handler(f.svc))
	defer srv.Close()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(directory, "token")
	bodyFile := filepath.Join(directory, "body.json")
	if err := os.WriteFile(tokenFile, []byte(f.human), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(SearchInput{Scope: scopeA, Query: "load-rule", Modes: []string{"exact", "fts"}})
	// Compare one actual response with the synthetic source before deriving timings.
	req, err := http.NewRequest("POST", srv.URL+"/v1/search", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.human)
	req.Header.Set("Content-Type", "application/json")
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var bundle ContextBundle
	decodeErr := json.NewDecoder(response.Body).Decode(&bundle)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || bundle.Authority != AuthorityDataOnly || len(bundle.Items) != 1 || bundle.Items[0].ID != expected.ID || bundle.Items[0].Scope != expected.Scope || bundle.Items[0].Revision != expected.Revision || bundle.Items[0].Text != expected.Text {
		t.Fatal("representative HTTP result did not match canonical fixture", response.StatusCode, decodeErr)
	}

	if err := os.WriteFile(bodyFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	// This is a correctness regression; machine-dependent SLO acceptance is a
	// separate operator run (default budget 800ms), not a flaky shared-CI promise.
	cmd := exec.Command("python3", "../../tools/http_load.py", "--base-url", srv.URL, "--token-file", tokenFile, "--body-file", bodyFile, "--requests", "100", "--concurrency", "4", "--p95-ms", "10000")
	cmd.Env = append(os.Environ(), "ARKISM_HTTP_LOAD_DIR="+directory)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("HTTP measurement failed: %s", output)
	}
	var report struct {
		Requests, Successful, Failed int
		P99                          float64 `json:"p99_ms"`
		Passed                       bool
	}
	if err = json.Unmarshal(output, &report); err != nil || !report.Passed || report.Requests != 100 || report.Successful != 100 || report.Failed != 0 || report.P99 <= 0 {
		t.Fatalf("invalid observed report: %s", output)
	}
	t.Logf("observed HTTP benchmark: %s", output)
}
