package memory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func recoveryHTTP(t *testing.T, base, token, method, path string, body, output any, status int) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != status {
		t.Fatalf("recovery %s %s: status %d, want %d", method, path, res.StatusCode, status)
	}
	if output != nil {
		if err = json.NewDecoder(res.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

func finishRecoveryHTTPWork(t *testing.T, base, token string, scope Scope, id string) {
	t.Helper()
	var claim PrivacyRuntimeAction
	recoveryHTTP(t, base, token, "POST", "/v1/privacy/deletions/runtime/claim", PrivacyScopeInput{Scope: scope}, &claim, 200)
	if claim.DeletionRequestID != id || len(claim.ExpectedRunIDs) != 0 || len(claim.SourceEventIDs) != 0 {
		t.Fatal("unexpected external runtime in work-only fixture")
	}
	var status PrivacyDeletionStatus
	recoveryHTTP(t, base, token, "POST", "/v1/privacy/deletions/"+id+"/runtime-receipt", PrivacyRuntimeReceiptInput{Scope: scope, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken, Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Verified: true}}, &status, 200)
	for i := 0; i < 2; i++ {
		var next PrivacyDeletionStatus
		recoveryHTTP(t, base, token, "POST", "/v1/privacy/deletions/"+id+"/advance", PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision}, &next, 200)
		status = next
	}
	query := url.Values{"workspace_id": {scope.WorkspaceID}, "project_id": {scope.ProjectID}, "user_id": {scope.UserID}, "agent_id": {scope.AgentID}}
	recoveryHTTP(t, base, token, "GET", "/v1/privacy/deletions/"+id+"?"+query.Encode(), nil, &status, 200)
	if status.PrivacyDeletion.State != privacyWaiting {
		t.Fatal("recovery did not reach backup boundary", status.PrivacyDeletion.State)
	}
}
