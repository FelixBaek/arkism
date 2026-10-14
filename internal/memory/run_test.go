package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Old fixture call sites model completed work; production never guesses metadata.
func completedRunInput(in CreateInput) CreateInput {
	end := time.Now().Add(-time.Second)
	agent := in.Scope.AgentID
	if agent == "" {
		agent = "test-agent"
	}
	in.Run = &RunDetails{AgentID: agent, TaskType: "test", Outcome: "success", StartedAt: end.Add(-time.Hour), FinishedAt: &end}
	return in
}
func runningRunInput(src Record) CreateInput {
	in := completedRunInput(CreateInput{Scope: src.Scope, Text: "private task", Sources: refs(src)})
	in.Run.Outcome = "running"
	in.Run.FinishedAt = nil
	return in
}
func TestRunStartFinish(t *testing.T) {
	for _, outcome := range []string{"success", "partial", "failed"} {
		f := newFixture(t)
		src := f.source()
		r := f.create(f.agent, "agent_runs", runningRunInput(src))
		in := FinishRunInput{Scope: scopeA, ExpectedRevision: r.Revision, Outcome: outcome, FinishedAt: time.Now().Add(-time.Second)}
		body, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", "/v1/agent_runs/"+r.ID+"/finish", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+f.agent)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 200 || strings.Contains(w.Body.String(), r.Text) {
			t.Fatal(w.Code, w.Body.String())
		}
		var ref Ref
		if e := json.Unmarshal(w.Body.Bytes(), &ref); e != nil || ref.Revision != 2 || ref.ID != r.ID {
			t.Fatal(ref, e)
		}
		got := f.get(f.agent, "agent_runs", scopeA, r.ID)
		if got.Run.Outcome != outcome || got.Run.FinishedAt == nil || got.Run.FinishedAt.Nanosecond()%1000000 != 0 || got.CreatedBy != r.CreatedBy || got.Status != "active" {
			t.Fatal(got)
		}
		if _, e := f.svc.FinishRun(testCtx, f.agent, r.ID, in); !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
		in.ExpectedRevision = got.Revision
		if _, e := f.svc.FinishRun(testCtx, f.agent, r.ID, in); !errors.Is(e, ErrConflict) {
			t.Fatal("terminal rewritten", e)
		}
	}
}
func TestRunValidation(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	for _, mutate := range []func(*CreateInput){
		func(i *CreateInput) { i.Run = nil }, func(i *CreateInput) { i.Run.AgentID = "" }, func(i *CreateInput) { i.Run.TaskType = " bad" },
		func(i *CreateInput) { i.Run.Outcome = "unknown" }, func(i *CreateInput) { i.Run.FinishedAt = nil },
		func(i *CreateInput) { i.Run.Outcome = "running" }, func(i *CreateInput) { i.Run.StartedAt = time.Time{} },
		func(i *CreateInput) { i.Run.StartedAt = time.Now().Add(time.Hour) },
		func(i *CreateInput) { i.Run.AgentID = strings.Repeat("가", 86) },
	} {
		in := completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)})
		mutate(&in)
		if _, e := f.svc.Create(testCtx, f.agent, "agent_runs", in); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	in := runningRunInput(src)
	in.Run.AgentID = "password=synthetic-secret"
	in.OnSecret = "redact"
	if _, e := f.svc.Create(testCtx, f.agent, "agent_runs", in); !errors.Is(e, ErrSecretDetected) {
		t.Fatal(e)
	}
	in = runningRunInput(src)
	in.Scope.AgentID = "other"
	if _, e := f.svc.Create(testCtx, f.agent, "agent_runs", in); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e := f.svc.Create(testCtx, f.agent, "tool_calls", runningRunInput(src)); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
func TestRunFinishPolicyScopeAndRollback(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	r := f.create(f.human, "agent_runs", runningRunInput(src))
	in := FinishRunInput{Scope: scopeA, ExpectedRevision: r.Revision, Outcome: "failed", FinishedAt: time.Now().Add(-time.Second)}
	if _, e := f.svc.FinishRun(testCtx, f.agent, r.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("agent terminated another owner", e)
	}
	for _, scope := range []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		clone := in
		clone.Scope = scope
		if _, e := f.svc.FinishRun(testCtx, f.human, r.ID, clone); !errors.Is(e, ErrNotFound) {
			t.Fatal("scope leak", e)
		}
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: r.ID}
	if _, e := f.svc.FinishRun(testCtx, f.human, r.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	f.svc.Auth = f.auth
	f.repo.failAudit = true
	if ref, e := f.svc.FinishRun(testCtx, f.human, r.ID, in); !errors.Is(e, ErrUnavailable) || ref.ID != "" {
		t.Fatal(ref, e)
	}
	got, _ := f.repo.Get(testCtx, "agent_runs", scopeA, r.ID)
	if got.Run.Outcome != "running" || got.Run.FinishedAt != nil || got.Revision != 1 {
		t.Fatal("rollback mutated run", got)
	}
	f.repo.failAudit = false
	clone := in
	clone.FinishedAt = r.Run.StartedAt.Add(-time.Second)
	if _, e := f.svc.FinishRun(testCtx, f.human, r.ID, clone); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	// Legacy rows remain readable but are never silently upgraded by finish.
	legacy := r
	legacy.Run = nil
	f.repo.store["agent_runs"][recKey(scopeA, r.ID)] = legacy
	if _, e := f.svc.Get(testCtx, f.human, "agent_runs", scopeA, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.FinishRun(testCtx, f.human, r.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}

func TestRunToolAndReflectionConsistency(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.human, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)}))
	for _, outside := range []string{"before", "after"} {
		in := toolInput(run)
		if outside == "before" {
			in.ToolCall.StartedAt = run.Run.StartedAt.Add(-time.Second)
		} else {
			in.ToolCall.FinishedAt = run.Run.FinishedAt.Add(time.Millisecond)
		}
		if _, e := f.svc.Create(testCtx, f.human, "tool_calls", in); !errors.Is(e, ErrInvalid) {
			t.Fatal(outside, e)
		}
	}
	reflection := verifiedReflection(t, f)
	parent, e := f.repo.Get(testCtx, "agent_runs", scopeA, reflection.Reflection.RunID)
	if e != nil {
		t.Fatal(e)
	}
	for _, outcome := range []string{"running", "failed"} {
		details := *parent.Run
		details.Outcome = outcome
		if outcome == "running" {
			details.FinishedAt = nil
		}
		row := parent
		row.Run = &details
		f.repo.store["agent_runs"][recKey(scopeA, row.ID)] = row
		if e := checkReflection(testCtx, f.repo, reflection); !errors.Is(e, ErrDenied) {
			t.Fatal(outcome, e)
		}
	}
}

func TestRunFinishChecksRetainedCalls(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.agent, "agent_runs", runningRunInput(src))
	call := f.create(f.agent, "tool_calls", toolInput(run))
	stored, _ := f.repo.Get(testCtx, "agent_runs", scopeA, run.ID)
	if stored.Revision != run.Revision || stored.RunCallFence != 1 || strings.Contains(structuredText(stored), "run_call_fence") {
		t.Fatal("fence exposed or logical revision changed", stored)
	}
	in := FinishRunInput{Scope: scopeA, ExpectedRevision: run.Revision, Outcome: "success", FinishedAt: call.ToolCall.FinishedAt.Add(-time.Second)}
	if _, e := f.svc.FinishRun(testCtx, f.agent, run.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("ended before tool", e)
	}
	// Retired calls and calls pinned to older revisions still constrain run time.
	old := call
	old.Status = "expired"
	old.Target = &Ref{Collection: "agent_runs", ID: run.ID, Revision: 99}
	f.repo.store["tool_calls"][recKey(scopeA, call.ID)] = old
	if _, e := f.svc.FinishRun(testCtx, f.agent, run.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	old.ToolCall = nil
	f.repo.store["tool_calls"][recKey(scopeA, call.ID)] = old
	in.FinishedAt = time.Now().Add(-time.Second)
	if _, e := f.svc.FinishRun(testCtx, f.agent, run.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("unknown call time accepted", e)
	}
	f.repo.store["tool_calls"][recKey(scopeA, call.ID)] = call
	in.FinishedAt = call.ToolCall.FinishedAt
	if _, e := f.svc.FinishRun(testCtx, f.agent, run.ID, in); e != nil {
		t.Fatal("exact endpoint refused", e)
	}
}

func TestRunCallFenceRollback(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.agent, "agent_runs", runningRunInput(src))
	f.repo.failAudit = true
	if _, e := f.svc.Create(testCtx, f.agent, "tool_calls", toolInput(run)); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	got, _ := f.repo.Get(testCtx, "agent_runs", scopeA, run.ID)
	if got.RunCallFence != 0 || got.Revision != run.Revision || len(f.repo.rows("tool_calls")) != 0 {
		t.Fatal("partial fence persisted", got)
	}
}
