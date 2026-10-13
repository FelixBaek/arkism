package memory

import (
	"testing"
	"time"
)

func TestGatewayViewSplitsSideCallsAndGroupsTurns(t *testing.T) {
	now := time.Now().UTC()
	cost := 0.01
	call := func(ago time.Duration, kind, side, session string, seq int64, ok bool) Record {
		return Record{CreatedAt: now.Add(-ago), RoutingCall: &RoutingCall{Model: "anthropic/claude-sonnet", OK: ok, LatencyMS: 900, InputTokens: 100, OutputTokens: 20, CostUSD: &cost,
			Origin: &RoutingOrigin{Kind: kind, Side: side, SessionID: session, TurnSeq: seq}}}
	}
	rows := []Record{ // newest first, as ListRecords returns them
		call(1*time.Minute, "side", "compaction", "s1", 2, true),
		call(2*time.Minute, "gateway", "", "s1", 2, false),
		call(3*time.Minute, "side", "query", "s1", 1, true),
		call(4*time.Minute, "gateway", "", "s1", 1, true),
		call(5*time.Minute, "gateway", "", "s1", 1, true),
		call(6*time.Minute, "gateway", "", "", 0, true), // no trace key: counted, not in a turn
		{CreatedAt: now.Add(-7 * time.Minute), RoutingCall: &RoutingCall{Model: "x", OK: true, Origin: &RoutingOrigin{Kind: "agent"}}}, // not a gateway call
		call(48*time.Hour, "gateway", "", "s0", 9, true), // before the range
	}
	v := gatewayView(rows, now.Add(-24*time.Hour))
	if v.Main.Requests != 4 || v.Main.Failures != 1 || v.Side.Requests != 2 || v.Side.BySide["compaction"] != 1 || v.Side.BySide["query"] != 1 {
		t.Fatalf("split = %+v / %+v", v.Main, v.Side)
	}
	if len(v.Turns) != 2 || v.Turns[0].TurnSeq != 2 || v.Turns[0].Main != 1 || v.Turns[0].Side != 1 {
		t.Fatalf("turns = %+v", v.Turns)
	}
	first := v.Turns[1]
	if first.TurnSeq != 1 || len(first.Calls) != 3 || first.Calls[2].Side != "query" || !first.Calls[0].At.Before(first.Calls[1].At) {
		t.Fatalf("turn 1 = %+v", first)
	}
}
