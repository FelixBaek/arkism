package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// These synthetic principals exercise the real HTTP boundary and forced RLS;
// the human token is fixture authorization, not evidence of human approval time.
func TestCollaborationHTTPPostgres(t *testing.T) {
	admin := isolatedPostgres(t)
	writer := testPrincipalToken("collaboration-agent-a")
	reader := testPrincipalToken("collaboration-agent-b")
	owner := testPrincipalToken("collaboration-human-fixture")
	for token, role := range map[string]string{writer: "agent", reader: "agent", owner: "human"} {
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
	client := &http.Client{Timeout: 30 * time.Second}
	call := func(token, method, path string, payload any, want int, out any) {
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
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		body, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, body)
		}
		if out != nil {
			if e = json.Unmarshal(body, out); e != nil {
				t.Fatal(e)
			}
		}
	}
	var src Record
	call(writer, "POST", "/v1/sources", sourceInput(CreateInput{Scope: scopeA, Text: "collabproof use focused checks"}), 201, &src)
	approved := func(kind string) Record {
		t.Helper()
		var candidate, mem Record
		call(writer, "POST", "/v1/memory_candidates", candidateInput(CreateInput{Scope: scopeA, Kind: kind, Text: "collabproof use focused checks", Keywords: []string{"collabproof"}, Sources: refs(src), Generator: &Generator{Model: "synthetic", PromptVersion: "1", PipelineVersion: "1"}}), 201, &candidate)
		call(owner, "POST", "/v1/memory_candidates/"+candidate.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"}, 200, &mem)
		return mem
	}
	procedural := approved("procedural")
	_ = approved("episodic")
	roleIn := RoleSearchInput{Role: "implementer", SearchInput: SearchInput{Scope: scopeA, Query: "collabproof", Modes: []string{"exact", "fts"}, Limit: 10}}
	var bundle ContextBundle
	call(reader, "POST", "/v1/memory/role-search", roleIn, 200, &bundle)
	if len(bundle.Items) != 1 || bundle.Items[0].ID != procedural.ID || bundle.Items[0].Sources[0].SourceID != src.ID {
		t.Fatalf("role narrowing/provenance: %+v", bundle)
	}
	roleIn.Role = "admin"
	call(reader, "POST", "/v1/memory/role-search", roleIn, 400, nil)
	roleIn.Role = "implementer"
	roleIn.Scope = scopeB
	call(reader, "POST", "/v1/memory/role-search", roleIn, 200, &bundle)
	if len(bundle.Items) != 0 {
		t.Fatal("role crossed scope")
	}
	roleIn.Scope = scopeA
	call("invalid", "POST", "/v1/memory/role-search", roleIn, 403, nil)

	pkg := workCheckpointPackage()
	pkg.Repository = "/synthetic/repo"
	pkg.SessionID = "previous"
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	call(writer, "POST", "/v1/work-checkpoints/save", WorkCheckpointSaveInput{Scope: scopeA, ID: "prior-work", EventID: "save-prior", Package: pkg}, 200, nil)
	pkg.SessionID = "current"
	call(writer, "POST", "/v1/work-checkpoints/save", WorkCheckpointSaveInput{Scope: scopeA, ID: "current-work", EventID: "save-current", Package: pkg}, 200, nil)
	recentIn := RecentWorkInput{Scope: scopeA, Repository: pkg.Repository, SessionID: "current", Limit: 3, MaxAgeDays: 7}
	var recent RecentWorkOutput
	call(reader, "POST", "/v1/work-checkpoints/recent", recentIn, 200, &recent)
	if len(recent.Items) != 1 || recent.Items[0].ID != "prior-work" || len(recent.Items[0].Package.SourceRefs) != 1 || recent.Items[0].Package.SourceRefs[0].ID != src.ID || len(recent.Items[0].Package.Checks) != 0 {
		t.Fatalf("recent: %+v", recent)
	}
	recentIn.Scope = scopeB
	call(reader, "POST", "/v1/work-checkpoints/recent", recentIn, 200, &recent)
	if len(recent.Items) != 0 {
		t.Fatal("recent crossed scope")
	}
	recentIn.Scope = scopeA
	call("invalid", "POST", "/v1/work-checkpoints/recent", recentIn, 403, nil)

	edge := graphClaim(procedural, "first", "second")
	call(writer, "POST", "/v1/knowledge-graph/relations", edge, 403, nil)
	call(owner, "POST", "/v1/knowledge-graph/relations", edge, 200, nil)
	graphIn := KnowledgeGraphInput{Scope: scopeA, Start: edge.Source, MaxDepth: 3, Limit: 10}
	var graph KnowledgeGraphResult
	call(reader, "POST", "/v1/knowledge-graph/expand", graphIn, 200, &graph)
	if len(graph.Items) != 1 || graph.Items[0].Evidence.ID != procedural.ID || graph.Items[0].Evidence.Revision != procedural.Revision {
		t.Fatalf("graph: %+v", graph)
	}
	graphIn.Scope = scopeB
	call(reader, "POST", "/v1/knowledge-graph/expand", graphIn, 200, &graph)
	if len(graph.Items) != 0 {
		t.Fatal("graph crossed scope")
	}
	graphIn.Scope = scopeA
	call("invalid", "POST", "/v1/knowledge-graph/expand", graphIn, 403, nil)

	// Public source deletion quarantines dependent memory and checkpoints. Both
	// response visibility and stored graph residue are checked, not merely status.
	call(owner, "DELETE", "/v1/sources/"+src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}, 202, nil)
	call(reader, "POST", "/v1/memory/role-search", roleIn, 200, &bundle)
	if len(bundle.Items) != 0 {
		t.Fatal("deleted evidence remained in role retrieval")
	}
	call(reader, "POST", "/v1/knowledge-graph/expand", graphIn, 200, &graph)
	if len(graph.Items) != 0 {
		t.Fatal("deleted evidence remained in graph")
	}
	// Quarantined candidates are excluded without suppressing other eligible sessions.
	call(reader, "POST", "/v1/work-checkpoints/recent", recentIn, 200, &recent)
	if len(recent.Items) != 0 {
		t.Fatal("deleted evidence remained in recent summaries")
	}
	for _, table := range []string{"relations", "entities"} {
		column := "relation_kind"
		if table == "entities" {
			column = "kind"
		}
		var count int
		if err := admin.q().QueryRow(testCtx, "SELECT count(*) FROM "+table+" WHERE starts_with("+column+",'asserted_') AND organization_id=$1 AND project_id=$2", scopeA.WorkspaceID, scopeA.ProjectID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d asserted records after source deletion", table, count)
		}
	}
}
