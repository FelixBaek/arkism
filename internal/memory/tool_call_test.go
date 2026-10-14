package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func toolInput(run Record) CreateInput {
	start := time.Now().Add(-time.Minute)
	return CreateInput{Scope: run.Scope, Text: "arguments and result summary", Sources: run.Sources, Target: &Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, ToolCall: &ToolCall{Name: "read_file", Outcome: "success", StartedAt: start, FinishedAt: start.Add(time.Second)}}
}

func TestToolCallRunLink(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)}))
	in := toolInput(run)
	r, e := f.svc.Create(testCtx, f.agent, "tool_calls", in)
	if e != nil || r.ToolCall == nil || r.Target.ID != run.ID || r.Target.Revision != run.Revision || r.ToolCall.StartedAt.Nanosecond()%1000000 != 0 || r.CreatedBy != "agent-1" {
		t.Fatal(r, e)
	}
	got := f.get(f.agent, "tool_calls", scopeA, r.ID)
	if got.ToolCall.Name != "read_file" || got.ToolCall.Outcome != "success" {
		t.Fatal(got)
	}
	for _, outcome := range []string{"partial", "failed"} {
		in.ToolCall.Outcome = outcome
		if _, e = f.svc.Create(testCtx, f.agent, "tool_calls", in); e != nil {
			t.Fatal(e)
		}
	}
	// Plaintext summaries may be explicitly redacted; identities are never rewritten.
	in.Text = "password=synthetic-secret"
	in.OnSecret = "redact"
	r, e = f.svc.Create(testCtx, f.agent, "tool_calls", in)
	if e != nil || strings.Contains(r.Text, "synthetic-secret") || !r.Inspection.Redacted {
		t.Fatal(r, e)
	}
	in.ToolCall.Name = "password=synthetic-secret"
	if _, e = f.svc.Create(testCtx, f.agent, "tool_calls", in); !errors.Is(e, ErrSecretDetected) {
		t.Fatal(e)
	}
}

func TestToolCallValidationAndIsolation(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.human, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)}))
	for _, mutate := range []func(*CreateInput){
		func(i *CreateInput) { i.Target = nil }, func(i *CreateInput) { i.ToolCall = nil },
		func(i *CreateInput) { i.Target.Collection = "memories" }, func(i *CreateInput) { i.Target.Revision = 0 },
		func(i *CreateInput) { i.ToolCall.Name = " bad" }, func(i *CreateInput) { i.ToolCall.Name = strings.Repeat("가", 43) },
		func(i *CreateInput) { i.ToolCall.Outcome = "active" }, func(i *CreateInput) { i.ToolCall.StartedAt = time.Time{} },
		func(i *CreateInput) { i.ToolCall.FinishedAt = i.ToolCall.StartedAt.Add(-time.Second) },
		func(i *CreateInput) { i.ToolCall.FinishedAt = time.Now().Add(time.Hour) },
	} {
		in := toolInput(run)
		mutate(&in)
		if r, e := f.svc.Create(testCtx, f.human, "tool_calls", in); !errors.Is(e, ErrInvalid) || r.ID != "" {
			t.Fatal(r, e)
		}
	}
	if _, e := f.svc.Create(testCtx, f.human, "agent_runs", toolInput(run)); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	for _, scope := range []Scope{scopeB, {WorkspaceID: "different", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "different"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "different"}} {
		srcOther := src
		srcOther.ID = ID()
		srcOther.Scope = scope
		if e := f.repo.Insert(testCtx, "sources", srcOther); e != nil {
			t.Fatal(e)
		}
		in := toolInput(run)
		in.Scope = scope
		in.Sources = refs(srcOther)
		if _, e := f.svc.Create(testCtx, f.human, "tool_calls", in); !errors.Is(e, ErrNotFound) {
			t.Fatal("scope leak", e)
		}
	}
	for _, kind := range []string{"changed", "inactive", "missing", "source_missing"} {
		clone := run
		switch kind {
		case "changed":
			clone.Revision++
		case "inactive":
			clone.Status = "expired"
		case "missing":
			delete(f.repo.store["agent_runs"], recKey(scopeA, run.ID))
		case "source_missing":
			clone.Sources = []SourceRef{{SourceID: ID(), Locator: "missing"}}
		}
		if kind != "missing" {
			f.repo.store["agent_runs"][recKey(scopeA, run.ID)] = clone
		}
		if r, e := f.svc.Create(testCtx, f.human, "tool_calls", toolInput(run)); e == nil || r.ID != "" {
			t.Fatal(kind, r, e)
		}
	}
	if len(f.repo.rows("tool_calls")) != 0 {
		t.Fatal("failed calls persisted")
	}
}

func TestToolCallPolicyAndAtomicity(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	run := f.create(f.human, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "task", Sources: refs(src)}))
	in := toolInput(run)
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: run.ID}
	if _, e := f.svc.Create(testCtx, f.human, "tool_calls", in); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "read:agent_runs"}
	if _, e := f.svc.Create(testCtx, f.human, "tool_calls", in); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	f.svc.Auth = f.auth
	f.repo.failAudit = true
	if r, e := f.svc.Create(testCtx, f.human, "tool_calls", in); !errors.Is(e, ErrUnavailable) || r.ID != "" || len(f.repo.rows("tool_calls")) != 0 {
		t.Fatal(r, e)
	}
}
