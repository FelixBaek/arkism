package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The desktop's built-in Arkism hands its agents an `agent` principal with every action in the
// default scope. This pins what that principal can do over real loopback HTTP with Postgres
// authorization and forced RLS: the calls the agent hook and the MCP tools make pass, and the
// owner-only transitions are refused. The human credential is a synthetic fixture, not a review.
func TestEmbeddedAgentRoleHTTPPostgres(t *testing.T) {
	admin := isolatedPostgres(t)
	agent := testPrincipalToken("synthetic-embedded-agent")
	owner := testPrincipalToken("synthetic-embedded-owner")
	for token, role := range map[string]string{agent: "agent", owner: "human"} {
		if err := admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: token, Role: role, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); err != nil {
			t.Fatal(err)
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
	call := func(t *testing.T, token, method, path string, payload any, want int, out any) {
		t.Helper()
		var body io.Reader
		if payload != nil {
			raw, e := json.Marshal(payload)
			if e != nil {
				t.Fatal(e)
			}
			body = bytes.NewReader(raw)
		}
		req, e := http.NewRequest(method, server.URL+path, body)
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
		raw, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, res.StatusCode, want, raw)
		}
		if out != nil {
			if e = json.Unmarshal(raw, out); e != nil {
				t.Fatal(e)
			}
		}
	}
	query := "?workspace_id=" + url.QueryEscape(scopeA.WorkspaceID) + "&project_id=" + url.QueryEscape(scopeA.ProjectID)
	now := time.Now().UTC().Truncate(time.Millisecond)

	// What the owner's review leaves for the agent to read.
	var src, candidate, memory Record
	call(t, owner, "POST", "/v1/sources", sourceInput(CreateInput{Scope: scopeA, Text: "embeddedrole source: run focused checks"}), 201, &src)
	call(t, owner, "POST", "/v1/memory_candidates", candidateInput(CreateInput{Scope: scopeA, Kind: "procedural", Text: "embeddedrole run focused checks", Keywords: []string{"embeddedrole"}, Sources: refs(src), Generator: &Generator{Model: "synthetic", PromptVersion: "1", PipelineVersion: "1"}}), 201, &candidate)
	call(t, owner, "POST", "/v1/memory_candidates/"+candidate.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"}, 200, &memory)

	t.Run("agent_calls_pass", func(t *testing.T) {
		// remember (MCP) and ark:remember (hook): the same capture gate, never an approval.
		var captured struct{ Items []CaptureItem }
		call(t, agent, "POST", "/v1/capture", map[string]any{
			"scope":     scopeA,
			"generator": map[string]any{"model": "external-agent", "prompt_version": "memory-mcp-v1", "pipeline_version": "memory-mcp-v1"},
			"event":     map[string]any{"id": "mcp:synthetic-embedded", "type": "turn_completed", "origin_product": "codex", "occurred_at": now},
			"claims":    []any{map[string]any{"subject": "memory", "text": "embeddedrole keep the owner token off the agents", "kind": "decision", "basis": "inferred", "reason": "synthetic"}},
		}, 200, &captured)
		if len(captured.Items) != 1 || captured.Items[0].Status != "candidate" {
			t.Fatalf("capture: %+v", captured)
		}
		// recall (MCP) and the prompt hook's recall.
		var bundle ContextBundle
		call(t, agent, "POST", "/v1/search", SearchInput{Scope: scopeA, Query: "embeddedrole", Modes: []string{"exact", "fts"}, BudgetTokens: 2000, TokenEncoding: "cl100k_base"}, 200, &bundle)
		if len(bundle.Items) != 1 || bundle.Items[0].ID != memory.ID {
			t.Fatalf("recall: %+v", bundle.Items)
		}
		call(t, agent, "GET", "/v1/memories"+query, nil, 200, nil)
		call(t, agent, "GET", "/v1/memories/"+memory.ID+query, nil, 200, nil)
		call(t, agent, "POST", "/v1/memories/at", MemoryAtInput{Scope: scopeA, AsOf: now, Limit: 5}, 200, nil)
		// recall_for_role, trace_memory_sources, record_memory_usage.
		call(t, agent, "POST", "/v1/memory/role-search", RoleSearchInput{Role: "implementer", SearchInput: SearchInput{Scope: scopeA, Query: "embeddedrole", Modes: []string{"exact", "fts"}, Limit: 10}}, 200, nil)
		// trace_memory_sources (GET /v1/memories/{id}/sources) authorizes per source inside one
		// transaction, which the one-connection fixture pool cannot serve; it has no role gate.
		call(t, agent, "POST", "/v1/session-archives/list", SessionArchiveListInput{Scope: scopeA, Limit: 100}, 200, nil)
		// route_skills, load_skill and the session-start skill list.
		call(t, agent, "POST", "/v1/skills/route", SearchInput{Scope: scopeA, Query: "embeddedrole", Limit: 5}, 200, nil)
		call(t, agent, "GET", "/v1/skills"+query+"&limit=50", nil, 200, nil)
		// save_work, resume_work, list_work.
		pkg := workCheckpointPackage()
		call(t, agent, "POST", "/v1/work-checkpoints/save", WorkCheckpointSaveInput{Scope: scopeA, ID: "embedded-work", EventID: "save-1", Package: pkg}, 200, nil)
		call(t, agent, "POST", "/v1/work-checkpoints/get", WorkCheckpointGetInput{Scope: scopeA, ID: "embedded-work"}, 200, nil)
		call(t, agent, "POST", "/v1/work-checkpoints/list", WorkCheckpointListInput{Scope: scopeA, Limit: 10}, 200, nil)
		// record_skill_usage.
		call(t, agent, "POST", "/v1/execution_traces/completed", TraceIngestInput{Scope: scopeA, TraceID: "embedded-trace", ThreadID: "embedded-thread",
			AgentRun: CompletedAgentRunInput{ID: "embedded-run", AgentID: "codex", TaskType: "skill_run", Outcome: "success", StartedAt: now.Add(-time.Minute), FinishedAt: now, Text: "synthetic skill run"},
			Events:   []TraceEventInput{{ID: "embedded-event", Kind: "message", OccurredAt: now, Payload: json.RawMessage(`{"text":"synthetic"}`)}}}, 200, nil)
		// The session-start hook: pins, the user digest and context validation.
		call(t, agent, "GET", "/v1/context/pins"+query+"&agent=claude_code", nil, 404, nil) // no pins for this agent: not found, not refused
		call(t, agent, "GET", "/v1/digests/user/context"+query, nil, 200, nil)
	})

	t.Run("owner_only_calls_are_refused", func(t *testing.T) {
		var other Record
		call(t, owner, "POST", "/v1/memory_candidates", candidateInput(CreateInput{Scope: scopeA, Kind: "procedural", Text: "embeddedrole second claim", Keywords: []string{"embeddedrole"}, Sources: refs(src), Generator: &Generator{Model: "synthetic", PromptVersion: "1", PipelineVersion: "1"}}), 201, &other)
		call(t, agent, "POST", "/v1/memory_candidates/"+other.ID+"/review", CandidateReviewInput{Scope: scopeA, ExpectedRevision: other.Revision, Action: "approve"}, 403, nil)
		call(t, agent, "POST", "/v1/memories/"+memory.ID+"/supersede", SupersedeInput{Scope: scopeA, ExpectedRevision: memory.Revision, Reason: "synthetic", NewContent: "embeddedrole corrected", Sources: refs(src)}, 403, nil)
		soon := now.Add(time.Hour)
		call(t, agent, "POST", "/v1/memories/"+memory.ID+"/expiry", MemoryExpiryInput{Scope: scopeA, ExpectedRevision: memory.Revision, ExpiresAt: &soon}, 403, nil)
		call(t, agent, "DELETE", "/v1/memories/"+memory.ID, ChangeInput{Scope: scopeA, ExpectedRevision: memory.Revision}, 403, nil)
		call(t, agent, "DELETE", "/v1/sources/"+src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}, 403, nil)
		// ark:forget opens a privacy deletion: a person's call, so the agent token is refused.
		call(t, agent, "POST", "/v1/privacy/deletions", PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "embedded-forget", TargetType: "source_event", TargetIDs: []string{src.ID}, Reason: "synthetic", RetentionPolicyRef: retentionSynthetic}, 403, nil)
		// Automated capture is the harness service's; an agent cannot claim it.
		call(t, agent, "POST", "/v1/capture", map[string]any{
			"automated": true, "scope": scopeA,
			"generator": map[string]any{"model": "external-agent", "prompt_version": "memory-mcp-v1", "pipeline_version": "memory-mcp-v1"},
			"event":     map[string]any{"id": "mcp:synthetic-automated", "type": "turn_completed", "origin_product": "codex", "occurred_at": now},
			"claims":    []any{map[string]any{"subject": "memory", "text": "embeddedrole automated", "kind": "decision", "basis": "inferred", "reason": "synthetic"}},
		}, 403, nil)
		// The memory the agent could not touch is still there for it to read.
		call(t, agent, "GET", "/v1/memories/"+memory.ID+query, nil, 200, nil)
	})
}
