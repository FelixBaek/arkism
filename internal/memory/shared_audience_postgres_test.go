package memory

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

// Real principal credentials and FORCE RLS, with an explicitly named shared partition.
func TestPostgresSharedAudienceOriginAndCheckpointFences(t *testing.T) {
	p := isolatedPostgres(t)
	app := restrictedPostgres(t, p)
	var role string
	if err := app.pool.QueryRow(testCtx, "SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(testCtx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: app, Auth: auth}
	scope := Scope{WorkspaceID: "shared-org", ProjectID: "shared-project", UserID: "shared-user", AgentID: "shared-team"}
	actions := []string{"db:read", "db:write", "write:execution_traces", "write:agent_runs", "write:sources", "read:sources", "read:work_checkpoints", "write:work_checkpoints"}
	grant := func(id, token string, allow []string) {
		t.Helper()
		if e := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: id, Role: "agent", Token: token, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: allow}); e != nil {
			t.Fatal(e)
		}
	}
	grant("agent-a", "shared-token-a", actions)
	grant("agent-b", "shared-token-b", actions)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	var first Ref
	for _, agent := range []string{"a", "b"} {
		token := "shared-token-" + agent
		in := TraceIngestInput{Scope: scope, TraceID: "shared-trace-" + agent, ThreadID: "shared-thread", AgentRun: CompletedAgentRunInput{ID: "shared-run-" + agent, OriginAgentID: "runtime-" + agent, TaskType: "work", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "unfinished task"}, Events: []TraceEventInput{{ID: "shared-event-" + agent, Kind: "langgraph.input", OccurredAt: start, Payload: json.RawMessage(`{"origin_agent_id":"runtime-` + agent + `","input":"continue"}`)}}}
		out, e := svc.IngestCompletedTrace(boundContext(token), token, in)
		if e != nil {
			t.Fatal("ingest", agent, e)
		}
		again, e := svc.IngestCompletedTrace(boundContext(token), token, in)
		if e != nil || again.Run != out.Run {
			t.Fatalf("replay %+v %v", again, e)
		}
		run, e := app.Get(boundContext(token), "agent_runs", scope, out.Run.ID)
		if e != nil || run.Run == nil || run.Run.OriginAgentID != "runtime-"+agent || run.Run.AgentID != scope.AgentID || run.CreatedBy != "agent-"+agent {
			t.Fatalf("origin/auth separation %+v %v", run, e)
		}
		if agent == "a" {
			first = out.SourceEvents[0]
		}
	}
	pkg := workCheckpointPackage()
	pkg.Status = "in_progress"
	pkg.SourceRefs = []Ref{first}
	in := workCheckpointPGInput(scope, "shared-work", "save-shared", 0, pkg)
	saved, e := svc.SaveWorkCheckpoint(boundContext("shared-token-a"), "shared-token-a", in)
	if e != nil {
		t.Fatal(e)
	}
	view, e := svc.GetWorkCheckpoint(boundContext("shared-token-b"), "shared-token-b", WorkCheckpointGetInput{Scope: scope, ID: in.ID})
	if e != nil || view.Revision != saved.Revision || len(view.Package.NextSteps) == 0 || view.Authority != AuthorityDataOnly {
		t.Fatalf("cross agent resume %+v %v", view, e)
	}
	// A reference is not an alias: a private partition's ID cannot be saved in shared scope.
	private := scope
	private.AgentID = "runtime-a"
	src := Record{ID: "private-event", Scope: private, Kind: "note", Status: "active", Revision: 1, Text: "private evidence", CreatedAt: start, UpdatedAt: start}
	if e = p.Insert(testCtx, "sources", src); e != nil {
		t.Fatal(e)
	}
	bad := pkg
	bad.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: 1}}
	if _, e = svc.SaveWorkCheckpoint(boundContext("shared-token-a"), "shared-token-a", workCheckpointPGInput(scope, "bad-shared", "bad-event", 0, bad)); e == nil {
		t.Fatal("private reference relabelled")
	}
	if _, e = svc.GetWorkCheckpoint(boundContext("shared-token-b"), "shared-token-b", WorkCheckpointGetInput{Scope: private, ID: in.ID}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("wrong partition read %v", e)
	}
	// Removing source permission invalidates a previously readable checkpoint.
	grant("agent-b", "shared-token-b", []string{"db:read", "db:write", "read:work_checkpoints"})
	if _, e = svc.GetWorkCheckpoint(boundContext("shared-token-b"), "shared-token-b", WorkCheckpointGetInput{Scope: scope, ID: in.ID}); !errors.Is(e, ErrDenied) {
		t.Fatalf("revocation %v", e)
	}
	grant("agent-b", "shared-token-b", actions)
	source, e := p.Get(testCtx, "sources", scope, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	source.Status = "deleted"
	source.Revision++
	if e = p.Replace(testCtx, "sources", source, first.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.GetWorkCheckpoint(boundContext("shared-token-b"), "shared-token-b", WorkCheckpointGetInput{Scope: scope, ID: in.ID}); e == nil {
		t.Fatal("deleted evidence returned checkpoint")
	}
}
