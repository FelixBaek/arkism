package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Real loopback HTTP, Postgres repository, token authorization and forced RLS.
// The human-role credential below is a synthetic fixture, NOT evidence that a
// person reviewed or approved anything. No model/agent inference runs here.
func TestCrossAgentHTTPPostgresReuse(t *testing.T) {
	admin := isolatedPostgres(t)
	const a = "synthetic-client-a"
	const b = "synthetic-client-b"
	const owner = "synthetic-human-fixture"
	for token, role := range map[string]string{a: "agent", b: "agent", owner: "human"} {
		for _, scope := range []Scope{scopeA, scopeB} {
			if err := admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: token, Role: role, Token: token, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	app := restrictedPostgres(t, admin)
	var appRole string
	if err := app.pool.QueryRow(testCtx, "SELECT current_user").Scan(&appRole); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.q().Exec(testCtx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(&Service{Repo: app, Auth: auth, CandidateDailyLimit: 100}))
	defer server.Close()
	// Independent transports prevent accidental client connection/session reuse.
	clients := map[string]*http.Client{}
	for _, token := range []string{a, b, owner, "invalid"} {
		tr := &http.Transport{}
		defer tr.CloseIdleConnections()
		clients[token] = &http.Client{Transport: tr, Timeout: 30 * time.Second}
	}
	call := func(t *testing.T, token, method, path string, payload any, want int, out any) {
		t.Helper()
		raw, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		req, e := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		res, e := clients[token].Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		body, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("request method=%s path=%s status=%d elapsed_ms=%.3f", method, strings.Split(path, "?")[0], res.StatusCode, float64(time.Since(start).Microseconds())/1000)
		if res.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, res.StatusCode, want, body)
		}
		if out != nil {
			if e = json.Unmarshal(body, out); e != nil {
				t.Fatal(e)
			}
		}
	}
	create := func(t *testing.T, token, col string, in CreateInput) Record {
		var out Record
		call(t, token, "POST", "/v1/"+col, in, 201, &out)
		return out
	}
	search := func(t *testing.T, token string, scope Scope, query string, want ...string) ContextBundle {
		t.Helper()
		var out ContextBundle
		call(t, token, "POST", "/v1/search", SearchInput{Scope: scope, Query: query, Modes: []string{"exact", "fts"}, BudgetTokens: 2000, TokenEncoding: "cl100k_base"}, 200, &out)
		if len(out.Items) != len(want) {
			t.Fatalf("query %s returned %d want %d: %+v", query, len(out.Items), len(want), out.Items)
		}
		for _, id := range want {
			found := false
			for _, h := range out.Items {
				if h.ID == id {
					found = true
				}
			}
			if !found {
				t.Fatal("missing", id)
			}
		}
		return out
	}
	candidate := func(t *testing.T, query string) (Record, Record) {
		src := create(t, a, "sources", sourceInput(CreateInput{Scope: scopeA, Text: query + " source: use focused tests"}))
		c := create(t, a, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: query + " use focused tests", Keywords: []string{query}, Sources: refs(src), Generator: &Generator{Model: "synthetic", PromptVersion: "1", PipelineVersion: "1"}}))
		return src, c
	}
	approve := func(t *testing.T, c Record) Record {
		var out Record
		call(t, owner, "POST", "/v1/memory_candidates/"+c.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}, 200, &out)
		return out
	}
	t.Run("approval_provenance_scope_and_revocation", func(t *testing.T) {
		src, c := candidate(t, "reuseapproval")
		search(t, b, scopeA, "reuseapproval")
		for _, token := range []string{a, b} {
			call(t, token, "POST", "/v1/memory_candidates/"+c.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}, 403, nil)
		}
		m := approve(t, c)
		out := search(t, b, scopeA, "reuseapproval", m.ID)
		if out.Items[0].TrustLevel != "user_verified" || len(out.Items[0].Sources) != 1 || out.Items[0].Sources[0].SourceID != src.ID || !strings.Contains(out.ContextData, m.Text) {
			t.Fatal("missing context or provenance")
		}
		var fetched Record
		call(t, b, "GET", "/v1/sources/"+src.ID+"?workspace_id="+url.QueryEscape(scopeA.WorkspaceID)+"&project_id="+url.QueryEscape(scopeA.ProjectID), nil, 200, &fetched)
		if fetched.Text != src.Text {
			t.Fatal("source changed")
		}
		search(t, b, scopeB, "reuseapproval")
		call(t, b, "POST", "/v1/search", SearchInput{Scope: scopeFar, Query: "reuseapproval"}, 503, nil)
		call(t, "invalid", "POST", "/v1/search", SearchInput{Scope: scopeA, Query: "reuseapproval"}, 403, nil)
		call(t, owner, "DELETE", "/v1/memories/"+m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}, 202, nil)
		search(t, b, scopeA, "reuseapproval")
	})
	t.Run("rejected_candidate", func(t *testing.T) {
		_, c := candidate(t, "reuserejected")
		call(t, owner, "POST", "/v1/memory_candidates/"+c.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "reject", Reason: "synthetic rejection"}, 200, nil)
		search(t, b, scopeA, "reuserejected")
	})
	t.Run("superseded", func(t *testing.T) {
		src, c := candidate(t, "reusechanged")
		m := approve(t, c)
		var out SupersedeOutput
		call(t, owner, "POST", "/v1/memories/"+m.ID+"/supersede", SupersedeInput{Scope: scopeA, ExpectedRevision: m.Revision, Reason: "synthetic correction", NewContent: "reusechanged run corrected tests", Sources: refs(src)}, 200, &out)
		got := search(t, b, scopeA, "reusechanged", out.New.ID)
		var historical Record
		call(t, b, "GET", "/v1/memories/"+m.ID+"?workspace_id="+url.QueryEscape(scopeA.WorkspaceID)+"&project_id="+url.QueryEscape(scopeA.ProjectID), nil, 200, &historical)
		if historical.Status != "superseded" || historical.Text != m.Text {
			t.Fatal("management history unexpectedly changed")
		}
		t.Log("observation: known-ID management GET returns superseded text; adapters must filter before injecting")
		if strings.Contains(got.ContextData, m.Text) {
			t.Fatal("stale content reused")
		}
	})
	t.Run("edited_approval", func(t *testing.T) {
		_, c := candidate(t, "reuseedited")
		var m Record
		call(t, owner, "POST", "/v1/memory_candidates/"+c.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "edit_approve", Content: "reuseedited use revised tests"}, 200, &m)
		got := search(t, b, scopeA, "reuseedited", m.ID)
		if strings.Contains(got.ContextData, c.Text) {
			t.Fatal("unedited candidate reused")
		}
	})
	t.Run("removed_source", func(t *testing.T) {
		src, c := candidate(t, "reusesourcegone")
		m := approve(t, c)
		search(t, b, scopeA, "reusesourcegone", m.ID)
		call(t, owner, "DELETE", "/v1/sources/"+src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}, 202, nil)
		search(t, b, scopeA, "reusesourcegone")
	})

	t.Run("expired", func(t *testing.T) {
		_, c := candidate(t, "reuseexpired")
		m := approve(t, c)
		soon := time.Now().Add(300 * time.Millisecond)
		call(t, owner, "POST", "/v1/memories/"+m.ID+"/expiry", MemoryExpiryInput{Scope: scopeA, ExpectedRevision: m.Revision, ExpiresAt: &soon}, 200, nil)
		time.Sleep(time.Until(soon) + 50*time.Millisecond)
		search(t, b, scopeA, "reuseexpired")
		var historical Record
		call(t, b, "GET", "/v1/memories/"+m.ID+"?workspace_id="+url.QueryEscape(scopeA.WorkspaceID)+"&project_id="+url.QueryEscape(scopeA.ProjectID), nil, 200, &historical)
		if historical.Text != m.Text || historical.ExpiresAt == nil {
			t.Fatal("expiry history unexpectedly changed")
		}
		t.Log("observation: known-ID management GET returns expired text; adapters must filter before injecting")
	})
	t.Run("conflict", func(t *testing.T) {
		_, c1 := candidate(t, "reuseconflict")
		_, c2 := candidate(t, "reuseconflict")
		m1, m2 := approve(t, c1), approve(t, c2)
		var marked ConflictOutput
		call(t, owner, "POST", "/v1/memories/conflicts", ConflictInput{Scope: scopeA, Memories: []Ref{{Collection: "memories", ID: m1.ID, Revision: m1.Revision}, {Collection: "memories", ID: m2.ID, Revision: m2.Revision}}, Action: "mark", Hold: "both", Reason: "synthetic conflict"}, 200, &marked)
		search(t, b, scopeA, "reuseconflict")
		call(t, owner, "POST", "/v1/memories/conflicts", ConflictInput{Scope: scopeA, Memories: marked.Relation.Relations, Action: "resolve", Reason: "synthetic resolution", ConflictID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Resolution: "keep_second"}, 200, nil)
		search(t, b, scopeA, "reuseconflict", m2.ID)
	})
	// Explicit opt-in for fresh external clients. Never write real credentials.
	// The caller supplies private absolute paths outside the repository.
	if statePath := os.Getenv("ARKISM_EVAL_FIXTURE_STATE"); statePath != "" {
		stopPath := os.Getenv("ARKISM_EVAL_FIXTURE_STOP")
		if stopPath == "" {
			t.Fatal("ARKISM_EVAL_FIXTURE_STOP required")
		}
		raw, err := json.Marshal(map[string]any{"url": server.URL, "scope": scopeA, "agent_a_token": a, "agent_b_token": b, "synthetic_owner_token": owner})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(statePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(statePath) })
		deadline := time.Now().Add(15 * time.Minute)
		for {
			if _, err = os.Stat(stopPath); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("fixture client deadline exceeded")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if err := admin.SetPrincipalActive(testCtx, b, false); err != nil {
		t.Fatal(err)
	}
	call(t, b, "POST", "/v1/search", SearchInput{Scope: scopeA, Query: "reusechanged"}, 503, nil)
}
