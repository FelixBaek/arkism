package gateway

import (
	"os"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// The Chat, Messages and Responses entrances over local
// TCP and the real Google adapter.

var gxProtocols = []string{"chat", "messages", "responses"}

const gxToolResult = `{"result":"결과"}`

// gxRequest is a tool-offering request for protocol; it also points the fixture at its entrance.
func (g *gxFixture) gxRequest(protocol, model string, stream bool) host.Obj {
	g.url = g.root + "/v1/" + map[string]string{"chat": "chat/completions", "messages": "messages", "responses": "responses"}[protocol]
	parameters := M{"type": "object", "properties": M{"word": M{"type": "string"}}}
	body := host.Obj{{Key: "model", Value: model}, {Key: "stream", Value: stream}}
	switch protocol {
	case "chat":
		body.Set("messages", []any{M{"role": "user", "content": "도구를 써 주세요"}})
		body.Set("max_completion_tokens", 256)
		body.Set("tools", []any{M{"type": "function", "function": M{"name": "echo", "parameters": parameters}}})
		if stream {
			body.Set("stream_options", M{"include_usage": true})
		}
	case "messages":
		body.Set("messages", []any{M{"role": "user", "content": "도구를 써 주세요"}})
		body.Set("max_tokens", 256)
		body.Set("tools", []any{M{"name": "echo", "input_schema": parameters}})
	default:
		body.Set("input", "도구를 써 주세요")
		body.Set("store", false)
		body.Set("max_output_tokens", 256)
		body.Set("tools", []any{M{"type": "function", "name": "echo", "parameters": parameters, "strict": false}})
	}
	return gxClone(g.t, body).(host.Obj)
}

// gxFollow answers the first reply's tool call in protocol's history format.
func gxCrossFollow(t *testing.T, protocol string, body host.Obj, first any) host.Obj {
	follow := gxClone(t, body).(host.Obj)
	switch protocol {
	case "chat":
		message := at(first, "choices", 0, "message")
		follow.Set("messages", append(at(follow, "messages").([]any), message,
			M{"role": "tool", "tool_call_id": at(message, "tool_calls", 0, "id"), "content": gxToolResult}))
	case "messages":
		var id any
		for _, block := range at(first, "content").([]any) {
			if at(block, "type") == "tool_use" && id == nil {
				id = at(block, "id")
			}
		}
		follow.Set("messages", append(at(follow, "messages").([]any), M{"role": "assistant", "content": at(first, "content")},
			M{"role": "user", "content": []any{M{"type": "tool_result", "tool_use_id": id, "content": gxToolResult}}}))
	default:
		var id any
		for _, item := range at(first, "output").([]any) {
			if at(item, "type") == "function_call" && id == nil {
				id = at(item, "call_id")
			}
		}
		input := append([]any{M{"role": "user", "content": at(body, "input")}}, at(first, "output").([]any)...)
		follow.Set("input", append(input, M{"type": "function_call_output", "call_id": id, "output": gxToolResult}))
	}
	return follow
}

// gxContinue appends the first reply and a new user turn in protocol's history format.
func gxContinue(t *testing.T, protocol string, body host.Obj, first any) host.Obj {
	next := gxClone(t, body).(host.Obj)
	user := M{"role": "user", "content": "continue"}
	switch protocol {
	case "chat":
		next.Set("messages", append(at(next, "messages").([]any), at(first, "choices", 0, "message"), user))
	case "messages":
		next.Set("messages", append(at(next, "messages").([]any), M{"role": "assistant", "content": at(first, "content")}, user))
	default:
		input := append([]any{M{"role": "user", "content": at(body, "input")}}, at(first, "output").([]any)...)
		next.Set("input", append(input, user))
	}
	return next
}

// gxFrames decodes the SSE data frames of raw, skipping [DONE].
func gxFrames(t *testing.T, raw []byte) []any {
	t.Helper()
	var out []any
	if err := SSEFrames(once(raw), func(_, data string) error {
		if data != "[DONE]" {
			out = append(out, parsed(t, []byte(data)))
		}
		return nil
	}); err != nil {
		t.Fatalf("frames: %v", err)
	}
	return out
}

// gxSet replaces an existing key of a decoded object in place.
func gxSet(v any, key string, value any) {
	o := v.(host.Obj)
	o.Set(key, value)
}

func gxParts() any { return at(gxResult(true), "candidates", 0, "content", "parts") }

func TestGoogleCrossAllAPIsRoundTripSignedToolsAndActualUsageInOneGroupMember(t *testing.T) {
	g := gxSetup(t)
	for _, protocol := range gxProtocols {
		g.route("google/gemini-test")
		g.setMode("tool")
		body := g.gxRequest(protocol, "group/test", false)
		reply := g.post(dumpJSON(body))
		raw := string(reply.body)
		if reply.status != 200 || strings.Contains(raw, "thoughtSignature") || strings.Contains(raw, "signed-thought") || !strings.Contains(raw, "mggoogle_") {
			t.Fatalf("%s: %d %s", protocol, reply.status, raw)
		}
		first := parsed(t, reply.body)
		usage := at(first, "usage")
		if protocol == "chat" {
			if at(usage, "prompt_tokens") != num(11) || at(usage, "completion_tokens") != num(6) || at(usage, "total_tokens") != num(17) ||
				at(usage, "completion_tokens_details", "reasoning_tokens") != num(2) {
				t.Fatalf("chat usage %s", dumpJSON(usage))
			}
		} else if at(usage, "input_tokens") != num(11) || at(usage, "output_tokens") != num(6) {
			t.Fatalf("%s usage %s", protocol, dumpJSON(usage))
		}
		follow := gxCrossFollow(t, protocol, body, first)
		g.route("google/gemini-other", "google/gemini-test")
		g.setMode("json")
		if got := g.post(dumpJSON(follow)); got.status != 200 {
			t.Fatalf("%s follow: %d %s", protocol, got.status, got.body)
		}
		sent := g.providerAt(-1)
		if sent.path != "/api/models/gemini-test:generateContent" {
			t.Fatalf("%s: %s", protocol, sent.path)
		}
		bridgeSameJSON(t, at(sent.body, "contents", 1, "parts"), gxParts())
		contents := at(sent.body, "contents").([]any)
		same(t, at(last(contents), "parts", 0, "functionResponse"), `{"name":"echo","response":{"output":"{\"result\":\"결과\"}"}}`)
		if at(sent.body, "generationConfig", "maxOutputTokens") != num(256) || strings.Contains(dumpJSON(sent.header), "caller-") {
			t.Fatalf("%s sent %s %v", protocol, dumpJSON(sent.body), sent.header)
		}
	}
}

func TestGoogleCrossAllAPIsStreamLiveTextAndMeasuredCompletion(t *testing.T) {
	g := gxSetup(t)
	for _, protocol := range gxProtocols {
		body := g.gxRequest(protocol, "google/gemini-test", true)
		reply := g.post(dumpJSON(body))
		raw := string(reply.body)
		if reply.status != 200 || reply.err != nil || !strings.Contains(reply.header.Get("content-type"), "text/event-stream") ||
			!strings.Contains(raw, "先") || !strings.Contains(raw, "완료") {
			t.Fatalf("%s: %d %v %s", protocol, reply.status, reply.err, raw)
		}
		frames := gxFrames(t, reply.body)
		switch protocol {
		case "chat":
			if at(last(frames), "usage", "total_tokens") != num(17) || !strings.HasSuffix(raw, "data: [DONE]\n\n") {
				t.Fatalf("chat %s", raw)
			}
		case "messages":
			var output any
			for _, e := range frames {
				if at(e, "type") == "message_delta" && output == nil {
					output = at(e, "usage", "output_tokens")
				}
			}
			if !strings.Contains(raw, "message_stop") || output != num(6) {
				t.Fatalf("messages %s", raw)
			}
		default:
			if at(last(frames), "type") != "response.completed" || at(last(frames), "response", "usage", "total_tokens") != num(17) {
				t.Fatalf("responses %s", raw)
			}
		}
	}
}

func TestGoogleCrossSignedHistoryRejectsOtherCallerTamperingAndMissingGroupMember(t *testing.T) {
	g := gxSetup(t)
	body := g.gxRequest("chat", "group/test", false)
	g.setMode("tool")
	reply := g.post(dumpJSON(body))
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	follow := gxCrossFollow(t, "chat", body, parsed(t, reply.body))
	g.clearAll()
	if got := g.post(dumpJSON(follow), "x-api-key", "different-caller").status; got != 409 {
		t.Fatalf("other caller %d", got)
	}
	tampered := gxClone(t, follow)
	gxSet(at(tampered, "messages", 1, "tool_calls", 0, "function"), "arguments", `{"word":"changed"}`)
	if got := g.post(dumpJSON(tampered)).status; got != 409 {
		t.Fatalf("tampered %d", got)
	}
	if g.decisionCount() != 0 {
		t.Fatal("routed")
	}
	g.route("google/gemini-other")
	if got := g.post(dumpJSON(follow)).status; got != 409 {
		t.Fatalf("missing member %d", got)
	}
	if g.providerCount() != 0 {
		t.Fatal(g.providerPaths())
	}
}

func TestGoogleCrossExpiredAndImportedAssistantHistoryDoesNotRegenerate(t *testing.T) {
	g := gxSetup(t)
	body := g.gxRequest("messages", "google/gemini-test", false)
	g.setMode("tool")
	ttl := bridgeTTL
	bridgeTTL = 0
	reply := g.post(dumpJSON(body))
	bridgeTTL = ttl
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	follow := gxCrossFollow(t, "messages", body, parsed(t, reply.body))
	g.clearAll()
	if got := g.post(dumpJSON(follow)).status; got != 409 {
		t.Fatalf("expired %d", got)
	}
	imported := gxClone(t, body).(host.Obj)
	imported.Set("messages", append(at(imported, "messages").([]any), M{"role": "assistant", "content": "external history"},
		M{"role": "user", "content": "continue"}))
	if got := g.post(dumpJSON(imported)).status; got != 409 {
		t.Fatalf("imported %d", got)
	}
	if g.providerCount() != 0 {
		t.Fatal(g.providerPaths())
	}
}

func TestGoogleCrossPlainTextResponseCanContinueWithNativePartsRestored(t *testing.T) {
	g := gxSetup(t)
	body := g.gxRequest("chat", "google/gemini-test", false)
	reply := g.post(dumpJSON(body))
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	body.Set("messages", append(at(body, "messages").([]any), at(parsed(t, reply.body), "choices", 0, "message"), M{"role": "user", "content": "계속"}))
	if got := g.post(dumpJSON(body)); got.status != 200 {
		t.Fatalf("continue %d %s", got.status, got.body)
	}
	same(t, at(g.providerAt(-1).body, "contents", 1, "parts"), `[{"text":"완료"}]`)
}

func TestGoogleCrossEmptyResponsesRoundTripThroughEachPublicHistoryFormat(t *testing.T) {
	g := gxSetup(t)
	empty := func(bool) host.Obj {
		page := gxResult(false)
		gxSet(at(page, "candidates", 0, "content"), "parts", []any{})
		return page
	}
	for _, protocol := range gxProtocols {
		body := g.gxRequest(protocol, "google/gemini-test", false)
		g.mu.Lock()
		g.result = empty
		g.mu.Unlock()
		reply := g.post(dumpJSON(body))
		g.mu.Lock()
		g.result = gxResult
		g.mu.Unlock()
		if reply.status != 200 {
			t.Fatalf("%s: %d %s", protocol, reply.status, reply.body)
		}
		next := gxContinue(t, protocol, body, parsed(t, reply.body))
		if got := g.post(dumpJSON(next)); got.status != 200 {
			t.Fatalf("%s continue: %d %s", protocol, got.status, got.body)
		}
		same(t, at(g.providerAt(-1).body, "contents", 1), `{"role":"model","parts":[{"text":""}]}`)
	}
}

func TestGoogleCrossOAuthSignedHistoryUsesExistingLoginWithoutChangingIt(t *testing.T) {
	g := gxSetup(t)
	original := g.oauth()
	for _, protocol := range gxProtocols {
		body := g.gxRequest(protocol, "google/gemini-test", false)
		g.setMode("tool")
		reply := g.post(dumpJSON(body))
		if reply.status != 200 {
			t.Fatalf("%s: %d %s", protocol, reply.status, reply.body)
		}
		follow := gxCrossFollow(t, protocol, body, parsed(t, reply.body))
		g.setMode("json")
		reply = g.post(dumpJSON(follow))
		if reply.status != 200 {
			t.Fatalf("%s follow: %d %s", protocol, reply.status, reply.body)
		}
		sent := g.providerAt(-1)
		if sent.path != "/assist:generateContent" || sent.header.Get("authorization") != "Bearer fresh-host-oauth" ||
			strings.Contains(string(reply.body), "host-") {
			t.Fatalf("%s: %s %v %s", protocol, sent.path, sent.header, reply.body)
		}
		bridgeSameJSON(t, at(sent.body, "request", "contents", 1, "parts"), gxParts())
	}
	tokens := 0
	for _, path := range g.providerPaths() {
		if path == "/token" {
			tokens++
		}
	}
	if now, _ := os.ReadFile(g.login); tokens != 1 || string(now) != string(original) {
		t.Fatalf("token requests %d, login changed %v", tokens, string(now) != string(original))
	}
}

func TestGoogleCrossGroupTokenCountDoesNotReturnLegacyEstimate(t *testing.T) {
	g := gxSetup(t)
	for _, mode := range []string{"tool", "json"} {
		body := g.gxRequest("messages", "group/test", false)
		g.setMode(mode)
		reply := g.post(dumpJSON(body))
		if reply.status != 200 {
			t.Fatalf("%s: %d %s", mode, reply.status, reply.body)
		}
		first := parsed(t, reply.body)
		if mode == "tool" {
			body = gxCrossFollow(t, "messages", body, first)
		} else {
			body = gxContinue(t, "messages", body, first)
		}
		body.Del("max_tokens")
		body.Del("stream")
		g.url += "/count_tokens"
		g.clearAll()
		if got := g.post(dumpJSON(body)).status; got != 501 {
			t.Fatalf("%s: %d", mode, got)
		}
		if g.providerCount() != 0 || g.decisionCount() != 0 {
			t.Fatalf("%s: executed", mode)
		}
	}
}

func TestGoogleCrossTextOriginLimitKeepsUnrelatedGroupHistoryCompatible(t *testing.T) {
	g := gxSetup(t)
	body := g.gxRequest("chat", "group/test", false)
	reply := g.post(dumpJSON(body))
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	body.Set("messages", append(at(body, "messages").([]any), at(parsed(t, reply.body), "choices", 0, "message"), M{"role": "user", "content": "continue"}))
	g.route("custom-test/other", "google/gemini-test")
	g.clearAll()
	swap(t, &target, func(model string) (*where, error) {
		if model == "custom-test/other" {
			return &where{kind: "chat", base: g.base + "/v1", key: "synthetic", name: "other"}, nil
		}
		return resolve(model)
	})
	g.reply("other", respChatReply(t, "other"))
	for _, c := range []string{"changed", "caller", "expired"} {
		changed := gxClone(t, body)
		var headers []string
		switch c {
		case "changed":
			gxSet(at(changed, "messages", 1), "content", "changed answer")
		case "caller":
			headers = []string{"x-api-key", "other-caller"}
		default:
			bridgeLock.Lock()
			for _, turn := range bridgeTurns {
				turn.expires = 0
			}
			bridgeLock.Unlock()
		}
		if got := g.post(dumpJSON(changed), headers...); got.status != 200 || at(parsed(t, got.body), "choices", 0, "message", "content") != "안녕하세요" {
			t.Fatalf("%s: %d %s", c, got.status, got.body)
		}
	}
	if g.providerCount() != 0 || g.seenCount() != 3 {
		t.Fatal(g.providerPaths())
	}
}

func TestGoogleCrossPreflightUnsupportedAndCountNeverGenerate(t *testing.T) {
	g := gxSetup(t)
	body := g.gxRequest("chat", "google/gemini-test", false)
	for _, extra := range []M{{"parallel_tool_calls": false},
		{"messages": []any{M{"role": "user", "content": []any{M{"type": "image_url", "image_url": M{"url": "https://example.invalid/image.png"}}}}}}} {
		changed := gxClone(t, body).(host.Obj)
		for key, value := range extra {
			changed.Set(key, value)
		}
		if got := g.post(dumpJSON(changed)).status; got != 400 {
			t.Fatalf("%v: %d", extra, got)
		}
	}
	body = g.gxRequest("messages", "google/gemini-test", false)
	if got := g.post(dumpJSON(body), "anthropic-beta", "unknown-beta").status; got != 400 {
		t.Fatalf("beta %d", got)
	}
	for _, messages := range []any{nil, 5, M{}, []any{nil}} {
		changed := gxClone(t, body).(host.Obj)
		changed.Set("messages", messages)
		if got := g.post(dumpJSON(changed)).status; got != 400 {
			t.Fatalf("messages %v: %d", messages, got)
		}
	}
	g.url += "/count_tokens"
	if got := g.post(dumpJSON(body)).status; got != 501 {
		t.Fatalf("count %d", got)
	}
	if g.providerCount() != 0 {
		t.Fatal(g.providerPaths())
	}
}

func TestGoogleCrossConfirmedRejectionCanFallBackButStartedTruncationCannot(t *testing.T) {
	g := gxSetup(t)
	for _, protocol := range gxProtocols {
		body := dumpJSON(g.gxRequest(protocol, "group/test", true))
		g.setMode("json")
		g.route("google/gemini-reject", "google/gemini-test")
		g.clearAll()
		if got := g.post(body); got.status != 200 || g.providerCount() != 2 {
			t.Fatalf("%s rejection: %d %v %s", protocol, got.status, g.providerPaths(), got.body)
		}
		g.route("google/gemini-test", "google/gemini-other")
		g.setMode("truncated")
		g.clearAll()
		reply := g.post(body)
		raw := string(reply.body)
		if reply.status != 200 || g.providerCount() != 1 {
			t.Fatalf("%s: %d %v %s", protocol, reply.status, g.providerPaths(), raw)
		}
		for _, banned := range []string{"mggoogle_", "[DONE]", "message_stop", "response.completed"} {
			if strings.Contains(raw, banned) {
				t.Fatalf("%s: %s in %s", protocol, banned, raw)
			}
		}
		// Go drops the connection after a started response fails; Python ended with an error event.
		if reply.err == nil && !strings.Contains(raw, "error") {
			t.Fatalf("%s: no error %s", protocol, raw)
		}
		if g.lastReportOK() != false {
			t.Fatalf("%s: reported ok", protocol)
		}
	}
}
