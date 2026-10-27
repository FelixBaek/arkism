package gateway

import (
	"strings"
	"testing"
)

// A group sent through the Gemini entrance
// prepares a native Google body first, then runs its members.

func TestGeminiGroupRejectionOnlyRetriesBeforeOutput(t *testing.T) {
	g := newGemini(t)
	g.endpoint("group/test", false)
	for _, failure := range []int{429, 500} {
		g.clear()
		g.reply("first", upstreamReply{failure, "application/json", []byte(`{"error":"host-secret private-details"}`)})
		reply := g.post(geminiRequest)
		raw := string(reply.body)
		if reply.status != 200 || g.seenCount() != 2 || strings.Contains(raw, "private-details") || strings.Contains(raw, "host-secret") {
			t.Fatalf("%d: %d %d %s", failure, reply.status, g.seenCount(), raw)
		}
	}
}

func TestGeminiGroupTruncatedOrMissingUsageNeverReleasesToolsOrCompletion(t *testing.T) {
	g := newGemini(t)
	g.respNative()
	g.endpoint("group/test", true)
	for _, missing := range []string{"done", "usage", "http_length"} {
		g.clear()
		source := nativeTools()
		if missing != "usage" {
			source = append(source, usageChunk(M{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}))
		}
		g.mu.Lock()
		g.lengthExtra = 0
		if missing == "http_length" {
			g.lengthExtra = 1
		}
		g.mu.Unlock()
		g.reply("first", upstreamReply{200, "text/event-stream", chatWire(source, missing != "done")})
		reply := g.post(geminiRequest)
		raw := string(reply.body)
		if reply.status != 200 || strings.Contains(raw, "finishReason") || strings.Contains(raw, "functionCall") || g.seenCount() != 1 {
			t.Fatalf("%s: %d %v %s", missing, reply.status, reply.err, raw)
		}
		// Go drops the connection after a started response fails; Python ended with an error event.
		if reply.err == nil && at(last(events(t, reply.body)), "error", "code") != num(502) {
			t.Fatalf("%s: no error event %s", missing, raw)
		}
	}
}

func TestGeminiGroupExplicitLimitSkipsCodexMember(t *testing.T) {
	g := newGemini(t)
	swap(t, &target, func(model string) (*where, error) {
		if strings.HasSuffix(model, "/first") {
			return &where{kind: "codex", model: "gpt-test"}, nil
		}
		return &where{kind: "direct", base: g.base, key: "host-secret", name: "backup"}, nil
	})
	noCodex(t)
	g.endpoint("group/test", false)
	if reply := g.post(geminiWith(t, "generationConfig", M{"maxOutputTokens": 7})); reply.status != 200 {
		t.Fatalf("group limit: %d %s", reply.status, reply.body)
	}
	if g.seenCount() != 1 || at(g.seenAt(0).body, "max_tokens") != num(7) {
		t.Fatalf("sent %d %s", g.seenCount(), g.seenAt(0).raw)
	}
}
