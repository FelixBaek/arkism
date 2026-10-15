package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestBenchmarkHTTPFactExport runs only on explicit opt-in. Each invocation uses
// a fresh disposable Postgres database and real loopback HTTP/token auth.
// The five synthetic facts were explicitly approved by the user in the evaluation
// chat. The harness submits that recorded human decision; it does not infer approval.
// This is host-assisted context delivery, not a model calling MCP itself.
func TestBenchmarkHTTPFactExport(t *testing.T) {
	fact, dest := os.Getenv("ARKISM_BENCH_FACT"), os.Getenv("ARKISM_BENCH_OUTPUT")
	if fact == "" || dest == "" {
		t.Skip("benchmark export not requested")
	}
	approved := map[string]bool{
		"Amounts are integer cents; partial-cent charges round down.":                                                              true,
		"Jobs are permitted Monday–Thursday; Friday–Sunday are blackout days.":                                                     true,
		"Trim surrounding whitespace but preserve letter case.":                                                                    true,
		"Zero TTL means do not cache; positive TTL stays unchanged.":                                                               true,
		"Retrying an event with an already-recorded event_id must leave the ledger unchanged; new events append in arrival order.": true,
	}
	if !approved[fact] {
		t.Fatal("fact outside explicitly approved set")
	}
	start := time.Now()
	pg := isolatedPostgres(t)
	for token, role := range map[string]string{"bench-a": "agent", "bench-b": "agent", "bench-human": "human"} {
		if err := pg.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: token, Role: role, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := NewPostgresAuthorizer(pg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(&Service{Repo: pg, Auth: auth, CandidateDailyLimit: 100}))
	defer server.Close()
	call := func(token, path string, in any, want int, out any) {
		t.Helper()
		raw, e := json.Marshal(in)
		if e != nil {
			t.Fatal(e)
		}
		req, e := http.NewRequest("POST", server.URL+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		// Distinct clients and transports prevent accidental connection/session state.
		tr := &http.Transport{}
		defer tr.CloseIdleConnections()
		res, e := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		body, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("POST %s status=%d", path, res.StatusCode)
		if res.StatusCode != want {
			t.Fatalf("status %d want %d: %s", res.StatusCode, want, body)
		}
		if out != nil {
			if e = json.Unmarshal(body, out); e != nil {
				t.Fatal(e)
			}
		}
	}
	var source, candidate, active Record
	call("bench-a", "/v1/sources", sourceInput(CreateInput{Scope: scopeA, Text: fact}), 201, &source)
	call("bench-a", "/v1/memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: fact, Keywords: []string{"benchfact"}, Sources: refs(source), Generator: &Generator{Model: "synthetic-host", PromptVersion: "1", PipelineVersion: "1"}}), 201, &candidate)
	input := SearchInput{Scope: scopeA, Query: "benchfact", Modes: []string{"exact"}, BudgetTokens: 2000, TokenEncoding: "cl100k_base"}
	var before ContextBundle
	call("bench-b", "/v1/search", input, 200, &before)
	if len(before.Items) != 0 {
		t.Fatal("unapproved fact leaked")
	}
	call("bench-human", "/v1/memory_candidates/"+candidate.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"}, 200, &active)
	setup := time.Since(start).Seconds()
	retrievalStart := time.Now()
	var got ContextBundle
	call("bench-b", "/v1/search", input, 200, &got)
	latency := float64(time.Since(retrievalStart).Microseconds()) / 1000
	if len(got.Items) != 1 || got.Items[0].ID != active.ID || got.Items[0].Text != fact || len(got.Items[0].Sources) != 1 || got.Items[0].Sources[0].SourceID != source.ID {
		t.Fatal("retrieved fact/provenance mismatch")
	}
	out := map[string]any{"text": got.Items[0].Text, "retrieval_ms": latency, "machine_setup_seconds": setup, "returned_rule_ids": []string{active.ID}, "source_ids": []string{source.ID}, "context": got, "approval": "explicit-user-approval-of-five-synthetic-facts", "storage": "fresh-postgres-database", "authorization": "distinct-A-B-human-bearer-tokens", "limitation": "admin database connection; RLS not asserted in this benchmark"}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dest, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
