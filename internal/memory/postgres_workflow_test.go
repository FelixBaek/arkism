package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestPostgresHTTPWorkflow exercises the same HTTP boundary used by memoryd,
// with the real PostgreSQL repository underneath it.
func TestPostgresHTTPWorkflow(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	for token, principal := range map[string]struct{ id, role string }{f.human: {"human-1", "human"}, f.human2: {"human-2", "human"}, f.agent: {"agent-1", "agent"}, f.work: {"worker-1", "service"}} {
		for _, scope := range []Scope{scopeA, scopeB} {
			if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: principal.id, Role: principal.role, Token: token, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	app := restrictedPostgres(t, p)
	var appRole string
	if err := app.pool.QueryRow(testCtx, "SELECT current_user").Scan(&appRole); err != nil {
		t.Fatal(err)
	}
	// Match the deployed app role's native-table DML grants; RLS remains forced.
	if _, err := p.q().Exec(testCtx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{appRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Repo, f.svc.Auth = app, auth
	h := Handler(f.svc)

	create := func(token, collection string, in CreateInput) Record {
		var out Record
		doPostgresHTTP(t, h, token, http.MethodPost, "/v1/"+collection, in, http.StatusCreated, &out)
		return out
	}
	search := func(scope Scope, query string) ContextBundle {
		var out ContextBundle
		doPostgresHTTP(t, h, f.human, http.MethodPost, "/v1/search", SearchInput{Scope: scope, Query: query, Modes: []string{"exact", "fts"}}, http.StatusOK, &out)
		return out
	}

	source := create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "postgres workflow source"}))
	candidate := create(f.human, "memory_candidates", candidateInput(CreateInput{
		Scope: scopeA, Text: "postgres workflow searchable rule", Keywords: []string{"workflow-rule"}, Sources: refs(source),
	}))

	// Agent credentials can create records but cannot approve them.
	doPostgresHTTP(t, h, f.agent, http.MethodPost, "/v1/memory_candidates/"+candidate.ID+"/review", CandidateReviewInput{
		Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve",
	}, http.StatusForbidden, nil)

	var memory Record
	doPostgresHTTP(t, h, f.human, http.MethodPost, "/v1/memory_candidates/"+candidate.ID+"/review", CandidateReviewInput{
		Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve",
	}, http.StatusOK, &memory)
	if memory.Status != "active" || memory.ApprovedBy == "" {
		t.Fatalf("approved memory = %+v", memory)
	}

	got := search(scopeA, "workflow-rule")
	if len(got.Items) != 1 || got.Items[0].ID != memory.ID {
		t.Fatalf("search before deletion = %+v, want memory %s", got.Items, memory.ID)
	}

	// A matching record in another scope must never cross the scope boundary.
	foreignSource := create(f.human, "sources", sourceInput(CreateInput{Scope: scopeB, Text: "foreign workflow source"}))
	foreignCandidate := create(f.human, "memory_candidates", candidateInput(CreateInput{
		Scope: scopeB, Text: "postgres workflow searchable rule", Keywords: []string{"workflow-rule"}, Sources: refs(foreignSource),
	}))
	var foreignMemory Record
	doPostgresHTTP(t, h, f.human, http.MethodPost, "/v1/memory_candidates/"+foreignCandidate.ID+"/review", CandidateReviewInput{
		Scope: scopeB, ExpectedRevision: foreignCandidate.Revision, Action: "approve",
	}, http.StatusOK, &foreignMemory)
	got = search(scopeA, "workflow-rule")
	if len(got.Items) != 1 || got.Items[0].ID != memory.ID {
		t.Fatalf("cross-scope search = %+v, want only %s", got.Items, memory.ID)
	}

	// Source deletion quarantines access before the runtime receipt permits purge.
	var deletion PrivacyDeletionStatus
	doPostgresHTTP(t, h, f.human, http.MethodDelete, "/v1/sources/"+source.ID, ChangeInput{
		Scope: scopeA, ExpectedRevision: source.Revision,
	}, http.StatusAccepted, &deletion)
	got = search(scopeA, "workflow-rule")
	if len(got.Items) != 0 {
		t.Fatalf("search after source deletion = %+v, want no results", got.Items)
	}
	quarantined, err := p.Get(context.Background(), "sources", scopeA, source.ID)
	if err != nil || !quarantined.PrivacyQuarantined {
		t.Fatalf("source must remain quarantined until runtime receipt: %+v %v", quarantined, err)
	}
	if _, err := f.svc.Get(ContextWithPostgresToken(testCtx, f.human), f.human, "sources", scopeA, source.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("quarantined source exposed by API: %v", err)
	}
	var claim PrivacyRuntimeAction
	doPostgresHTTP(t, h, f.work, http.MethodPost, "/v1/privacy/deletions/runtime/claim", PrivacyScopeInput{Scope: scopeA}, http.StatusOK, &claim)
	// This Go-only fixture has no runtime runs; the combined Python E2E checks
	// real checkpoint/journal purge before submitting the same receipt contract.
	doPostgresHTTP(t, h, f.work, http.MethodPost, "/v1/privacy/deletions/"+deletion.ID+"/runtime-receipt", PrivacyRuntimeReceiptInput{
		Scope: scopeA, ExpectedRevision: claim.Revision, LeaseToken: claim.LeaseToken,
		Receipt: PrivacyRuntimeReceipt{RequestHash: claim.RequestHash, Quarantined: true, Verified: true, ExpectedRunIDs: claim.ExpectedRunIDs},
	}, http.StatusOK, nil)
	workerCtx := ContextWithPostgresToken(testCtx, f.work)
	deadline := time.Now().Add(20 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err := f.svc.WorkOnce(workerCtx, f.work); err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		current, err := f.svc.GetPrivacyDeletion(ContextWithPostgresToken(testCtx, f.human), f.human, deletion.ID, scopeA)
		if err != nil {
			t.Fatal(err)
		}
		if current.PrivacyDeletion.State == privacyWaiting {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("canonical worker did not verify purge and record retention pending")
	}
	if _, err := p.Get(context.Background(), "sources", scopeA, source.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted source lookup = %v, want ErrNotFound", err)
	}
	if got := search(scopeB, "workflow-rule"); len(got.Items) != 1 || got.Items[0].ID != foreignMemory.ID {
		t.Fatalf("foreign memory changed during purge: %+v", got.Items)
	}
}

func doPostgresHTTP(t *testing.T, h http.Handler, token, method, path string, in any, wantStatus int, out any) {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != wantStatus {
		t.Fatalf("%s %s = %d, body %s; want %d", method, path, res.Code, res.Body.String(), wantStatus)
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
}

func TestPostgresLearningRequiresThreeDistinctRunsAndDoesNotApprove(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	for token, principal := range map[string]struct{ id, role string }{f.human: {"human-1", "human"}, f.human2: {"human-2", "human"}, f.agent: {"agent-1", "agent"}, f.work: {"worker-1", "service"}} {
		for _, scope := range []Scope{scopeA, scopeB} {
			if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: principal.id, Role: principal.role, Token: token, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 3; i++ {
		src := f.source()
		run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "learning run", Sources: refs(src)}))
		target := Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}
		passed := true
		ev := f.create(f.human, "evaluations", CreateInput{Scope: scopeA, Text: "independent check", Sources: refs(src), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed}})
		var out LearningResult
		doPostgresHTTP(t, Handler(f.svc), f.agent, http.MethodPost, "/v1/learning", LearningInput{Scope: scopeA, Run: target, Evaluation: Ref{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}, TaskType: "index", Lesson: "verify local index", VerificationMethod: "query check"}, http.StatusOK, &out)
		if out.CompatibleRuns != i+1 || out.ReviewSuggested != (i == 2) {
			t.Fatalf("iteration %d: %+v", i, out)
		}
	}
	for collection, want := range map[string]int{"memories": 0, "reflections": 3, "memory_candidates": 3} {
		var count int
		if err := p.q().QueryRow(testCtx, "SELECT count(*) FROM memory_records WHERE collection=$1", collection).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count %d want %d: %v", collection, count, want, err)
		}
	}
}

func TestPostgresTraceLearningCycleWithRLS(t *testing.T) {
	admin := isolatedPostgres(t)
	app := restrictedPostgres(t, admin)
	var role string
	if err := app.pool.QueryRow(testCtx, "SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.q().Exec(testCtx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	scope := Scope{WorkspaceID: "team-org", ProjectID: "team-project", UserID: "owner", AgentID: "agent-1"}
	for _, grant := range []PrincipalGrant{
		{PrincipalID: "agent-1", Role: "agent", Token: testPrincipalToken("trace-agent")},
		{PrincipalID: "independent-checker", Role: "service", Token: testPrincipalToken("trace-checker")},
		{PrincipalID: "owner", Role: "human", Token: testPrincipalToken("trace-owner")},
	} {
		grant.OrganizationID, grant.ProjectID, grant.Actions = scope.WorkspaceID, scope.ProjectID, []string{"*"}
		if err := admin.ProvisionPrincipal(testCtx, grant); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: app, Auth: auth, CandidateDailyLimit: 50}
	h := Handler(svc)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	var trace TraceIngestResult
	doPostgresHTTP(t, h, testPrincipalToken("trace-agent"), http.MethodPost, "/v1/execution_traces/completed", TraceIngestInput{
		Scope: scope, TraceID: "cycle-trace", ThreadID: "cycle-thread", LangSmithRunID: "cycle-observation",
		AgentRun: CompletedAgentRunInput{ID: "cycle-run", TaskType: "repair", Outcome: "success", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "verified query repair"},
		Events:   []TraceEventInput{{ID: "cycle-event", Kind: "tool_result", OccurredAt: start, Payload: json.RawMessage(`{"check":"query succeeded"}`)}},
	}, http.StatusOK, &trace)
	src, err := admin.Get(testCtx, "sources", scope, trace.SourceEvents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	passed := true
	var evaluation Record
	doPostgresHTTP(t, h, testPrincipalToken("trace-checker"), http.MethodPost, "/v1/evaluations", CreateInput{Scope: scope, Text: "independently replayed", Sources: refs(src), Target: &trace.Run, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}}, http.StatusCreated, &evaluation)
	var lesson LearningResult
	doPostgresHTTP(t, h, testPrincipalToken("trace-agent"), http.MethodPost, "/v1/learning", LearningInput{Scope: scope, Run: trace.Run, Evaluation: Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}, TaskType: "repair", Lesson: "verify-cycle-keyword", VerificationMethod: "independent query replay"}, http.StatusOK, &lesson)
	candidate, err := admin.Get(testCtx, "memory_candidates", scope, lesson.Lesson.ID)
	if err != nil || candidate.TraceID != trace.Trace.ID || candidate.VerificationStatus != VerificationCandidate || candidate.Status != "candidate" {
		t.Fatalf("candidate provenance: %+v %v", candidate, err)
	}
	var approved Record
	doPostgresHTTP(t, h, testPrincipalToken("trace-owner"), http.MethodPost, "/v1/memory_candidates/"+candidate.ID+"/review", CandidateReviewInput{Scope: scope, ExpectedRevision: candidate.Revision, Action: "approve"}, http.StatusOK, &approved)
	var bundle ContextBundle
	doPostgresHTTP(t, h, testPrincipalToken("trace-agent"), http.MethodPost, "/v1/search", SearchInput{Scope: scope, Query: "verify-cycle-keyword", Modes: []string{"exact", "fts"}}, http.StatusOK, &bundle)
	if len(bundle.Items) != 1 || bundle.Items[0].VerificationStatus != VerificationVerified || bundle.Items[0].TraceID != trace.Trace.ID || bundle.Items[0].Authority != "data_only_untrusted" {
		t.Fatalf("next-run context: %+v", bundle.Items)
	}
}

// An unknown token is refused as 403 even though FORCE RLS rejects its denial
// audit row; only an unreachable database is 503.
func TestPostgresUnknownTokenDeniedAndOutageUnavailable(t *testing.T) {
	admin := isolatedPostgres(t)
	app := restrictedPostgres(t, admin)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(&Service{Repo: app, Auth: auth})
	in := SearchInput{Scope: scopeA, Query: "anything", Modes: []string{"exact"}}
	doPostgresHTTP(t, h, "nope", http.MethodPost, "/v1/search", in, http.StatusForbidden, nil)
	app.Close()
	doPostgresHTTP(t, h, "nope", http.MethodPost, "/v1/search", in, http.StatusServiceUnavailable, nil)
}
