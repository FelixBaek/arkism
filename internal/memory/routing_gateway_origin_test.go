package memory

import (
	"errors"
	"testing"
)

func TestGatewayOriginsCarrySideKindAndTurnTrace(t *testing.T) {
	good := []*RoutingOrigin{
		{Kind: "gateway"},
		{Kind: "gateway", SessionID: "0f0e-session", TurnSeq: 3},
		{Kind: "side", Side: "fork", SessionID: "0f0e-session", TurnSeq: 3},
		{Kind: "side", Side: "query"},
		{Kind: "side", Side: "compaction"},
		{Kind: "side", Side: "auxiliary"},
		{Kind: "side", Side: "side_reply"},
	}
	for _, o := range good {
		if !validRoutingOrigin(o) {
			t.Errorf("refused %+v", o)
		}
	}
	bad := []*RoutingOrigin{
		{Kind: "side"},                    // a side call says why
		{Kind: "gateway", Side: "fork"},   // only side calls have a reason
		{Kind: "side", Side: "chitchat"},  // unknown reason
		{Kind: "gateway", SessionID: "s"}, // a session without a turn
		{Kind: "gateway", TurnSeq: 2},     // a turn without a session
		{Kind: "gateway", SessionID: " s", TurnSeq: 1},
		{Kind: "gateway", SessionID: "s", TurnSeq: -1},
		{Kind: "workflow", SessionID: "s", TurnSeq: 1}, // turn traces are gateway-only
	}
	for _, o := range bad {
		if validRoutingOrigin(o) {
			t.Errorf("accepted %+v", o)
		}
	}
	for _, o := range []*RoutingOrigin{{Kind: "gateway"}, {Kind: "side", Side: "query"}} {
		if k := callKind(o); k != "" {
			t.Errorf("%s counted as crew work: %q", o.Kind, k)
		}
	}
}

func TestGatewayCallsAreRecordedWithTheirTraceAndStayOutOfCrewUsage(t *testing.T) {
	f := newMeetingFixture(t)
	runner := "tok-runner"
	f.auth.actors[runner] = actor("crew-runner", "service", true, "read", "write")
	for _, o := range []*RoutingOrigin{
		{Kind: "gateway", SessionID: "sess", TurnSeq: 1},
		{Kind: "side", Side: "query", SessionID: "sess", TurnSeq: 1},
	} {
		r, err := f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: RoutingCall{Model: "anthropic/claude-opus-5-5", Decision: "fixed", OK: true, InputTokens: 100000, Origin: o}})
		if err != nil || r.RoutingCall.Origin.SessionID != "sess" || r.RoutingCall.Origin.TurnSeq != 1 {
			t.Fatal(r, err)
		}
	}
	if _, err := f.svc.RecordRoutingCall(testCtx, runner, RoutingCallInput{Scope: scopeA, Call: RoutingCall{Model: "a/b", Decision: "fixed",
		Origin: &RoutingOrigin{Kind: "side"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("side call without a reason", err)
	}
	u, err := f.svc.CrewUsage(testCtx, f.human, scopeA, "today", "all", "", "")
	if err != nil || u.KPI.CostUSD != 0 {
		t.Fatal("gateway calls counted as crew work", u.KPI, err)
	}
}
