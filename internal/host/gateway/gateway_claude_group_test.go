package gateway

import (
	"errors"
	"fmt"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// A group continuation waiting in the
// Claude CLI bridge stays pinned to its model and owner, and expired ids never fall back.

func TestGroupContinuationPinsModelAndExpiredIDsNeverFallBack(t *testing.T) {
	checkGatewayContinuation(t, false)
}

func TestMessagesGroupContinuationIsolatedAndExpiredIDsNeverFallBack(t *testing.T) {
	checkGatewayContinuation(t, true)
}

func checkGatewayContinuation(t *testing.T, native bool) {
	h := claudeNewHarness(t)
	f := newFixture(t)
	swap(t, &claudeRuns, h.bridge)
	f.auth = authError("claude_subscription_requires_cli")
	anthropic := func(string) *where { return &where{kind: "anthropic"} }
	routed := f.route(anthropic)
	f.setDecision("anthropic/claude-test")
	post := func(body host.Obj, model, caller string) (int, host.Obj) {
		t.Helper()
		payload, path := with(body, "model", model), "/v1/messages"
		if !native {
			var err error
			if payload, err = MessagesToChat(payload); err != nil {
				t.Fatal(err)
			}
			path = "/v1/chat/completions"
		}
		reply := send(t, "POST", f.gw.URL+path, mustPy(payload), "authorization", "Bearer "+caller, "content-type", "application/json")
		out, _ := parsed(t, reply.body).(host.Obj)
		return reply.status, out
	}
	resetDecisions := func() {
		f.mu.Lock()
		f.decisions = 0
		f.mu.Unlock()
	}

	status, first := post(claudeRequest(t), "group/test", "synthetic-caller")
	if status != 200 {
		t.Fatalf("first turn %d %v", status, first)
	}
	if !native {
		var err error
		if first, err = MessagesResponse(first, "claude-test"); err != nil {
			t.Fatal(err)
		}
	}
	next := claudeFollow(claudeRequest(t), first)

	routed = f.route(anthropic)
	resetDecisions()
	for _, c := range [][2]string{{"group/other", "synthetic-caller"}, {"group/test", "other-caller"},
		{"anthropic/claude-test", "synthetic-caller"}} {
		if status, out := post(next, c[0], c[1]); status != 409 {
			t.Fatalf("%v: %d %v", c, status, out)
		}
	}
	if f.decisions != 0 {
		t.Fatal("a mismatched continuation was routed")
	}

	routed = f.route(anthropic)
	f.setDecision("custom-other/other", "anthropic/claude-test")
	f.auth = func(string) (map[string]string, string, error) {
		t.Error("a waiting CLI must not switch to API authentication")
		return nil, "", errors.New("unexpected")
	}
	status, second := post(next, "group/test", "synthetic-caller")
	f.auth = authError("claude_subscription_requires_cli")
	if status != 200 {
		t.Fatalf("continuation %d %v", status, second)
	}
	if got := fmt.Sprint(routed.list()); got != "[anthropic/claude-test]" {
		t.Fatalf("targets %s", got)
	}
	if f.reportCount() != 2 || h.profileCalls() != 1 {
		t.Fatalf("reports %d profiles %d", f.reportCount(), h.profileCalls())
	}

	h.bridge.close()
	routed = f.route(anthropic)
	resetDecisions()
	if status, out := post(next, "group/test", "synthetic-caller"); status != 409 {
		t.Fatalf("expired continuation %d %v", status, out)
	}
	if len(routed.list()) != 0 || f.decisions != 0 {
		t.Fatal("an expired continuation fell back")
	}

	h.mode = "error"
	f.setDecision("anthropic/claude-test", "custom-other/other")
	f.mu.Lock()
	f.reports = nil
	f.mu.Unlock()
	if status, out := post(claudeRequest(t), "group/test", "synthetic-caller"); status != 502 {
		t.Fatalf("started failure %d %v", status, out)
	}
	if len(routed.list()) != 1 || f.reportCount() != 1 {
		t.Fatalf("targets %v reports %d", routed.list(), f.reportCount())
	}
}
