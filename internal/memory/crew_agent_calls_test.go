package memory

import (
	"errors"
	"testing"
)

func TestCrewAgentCallsListsOnlyDirectCallsOfThatAgent(t *testing.T) {
	f := newMeetingFixture(t)
	runner := "tok-runner"
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "write")
	for _, o := range []*RoutingOrigin{
		{Kind: "agent", AgentID: f.a.ID, Trigger: "manual"},
		{Kind: "agent", AgentID: f.a.ID},
		{Kind: "workflow", AgentID: f.a.ID, WorkflowID: "wf", RunID: "run-1"}, // a workflow step: listed with runs
		{Kind: "agent", AgentID: f.b.ID},
	} {
		if _, err := f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: RoutingCall{Model: "anthropic/claude-opus-5-5", Decision: "fixed", OK: true, InputTokens: 10, Origin: o}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.svc.CrewAgentCalls(testCtx, f.human, scopeA, f.a.ID, 30)
	if err != nil || len(got.Items) != 2 {
		t.Fatal(got, err)
	}
	if got, _ := f.svc.CrewAgentCalls(testCtx, f.human, scopeA, f.a.ID, 1); len(got.Items) != 1 {
		t.Fatal("limit", got)
	}
	if _, err := f.svc.CrewAgentCalls(testCtx, f.human, scopeA, f.a.ID, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("limit 0", err)
	}
}
