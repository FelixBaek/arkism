package gateway

import (
	"net/url"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// The Gemini entrance over real local HTTP. Cases that send a
// group/ or google/ model through this entrance are in gateway_gemini_group_test.go and gateway_google_test.go.

const geminiRequest = `{"contents":[{"role":"user","parts":[{"text":"안녕"}]}]}`

type gemini struct {
	*fixture
	root string
}

func newGemini(t *testing.T) *gemini {
	g := &gemini{fixture: newFixture(t)}
	g.root = g.gw.URL
	g.endpoint("custom-test/first", false)
	return g
}

func (g *gemini) endpoint(model string, stream bool) {
	g.url = g.root + "/v1beta/models/" + url.PathEscape(model)
	if stream {
		g.url += ":streamGenerateContent?alt=sse&key=caller-query-secret"
	} else {
		g.url += ":generateContent?key=caller-query-secret"
	}
}

func (g *gemini) get(path string) (int, any) {
	reply := fetch(g.t, g.root+path, "x-goog-api-key", "caller-google-secret")
	return reply.status, parsed(g.t, reply.body)
}

// geminiWith is geminiRequest plus extra top-level fields.
func geminiWith(t *testing.T, extra ...any) string {
	body := parsed(t, []byte(geminiRequest)).(host.Obj)
	for i := 0; i+1 < len(extra); i += 2 {
		body.Set(extra[i].(string), extra[i+1])
	}
	return dumpJSON(body)
}

func TestGeminiJSONMessagesEngineAliasesAndCallerCredentials(t *testing.T) {
	g := newGemini(t)
	reply := g.post(geminiWith(t, "model", "models/custom-test/first", "system_instruction", M{"parts": []any{M{"text": "규칙"}}},
		"generation_config", M{"max_output_tokens": 31}), "x-goog-api-key", "caller-google-secret")
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	result := parsed(t, reply.body)
	same(t, at(result, "candidates", 0, "content", "parts"), `[{"text":"안녕하세요"}]`)
	if at(result, "candidates", 0, "finishReason") != "STOP" || at(result, "usageMetadata", "promptTokenCount") != num(4) {
		t.Fatalf("result %s", reply.body)
	}
	seen := g.seenAt(0)
	if seen.path != "/v1/messages" || at(seen.body, "system", 0, "text") != "규칙" || at(seen.body, "max_tokens") != num(31) {
		t.Fatalf("sent %s %s", seen.path, seen.raw)
	}
	if strings.Contains(dumpJSON(seen.header), "caller-") || reply.header.Get("x-private") != "" {
		t.Fatal("credentials crossed")
	}
}

func TestGeminiModelsListingPaginationAndExactLookup(t *testing.T) {
	g := newGemini(t)
	rows := []host.Obj{{{Key: "id", Value: "group/test"}, {Key: "label", Value: "그룹"}}, {{Key: "id", Value: "custom-test/first"}, {Key: "label", Value: "모델"}}}
	swap(t, &routingModels, func() []host.Obj { return rows })
	status, page := g.get("/v1beta/models?pageSize=1&key=caller-secret")
	if status != 200 || at(page, "models", 0, "name") != "models/group/test" || at(page, "nextPageToken") != "1" {
		t.Fatalf("%d %s", status, dumpJSON(page))
	}
	_, lastPage := g.get("/v1beta/models?page_size=1&page_token=1")
	if _, ok := lastPage.(host.Obj).Get("nextPageToken"); ok {
		t.Fatalf("last page %s", dumpJSON(lastPage))
	}
	_, model := g.get("/v1beta/models/custom-test%2Ffirst")
	same(t, model, dumpJSON(at(lastPage, "models", 0)))
	same(t, at(model, "supportedGenerationMethods"), `["generateContent"]`)
	if _, ok := model.(host.Obj).Get("inputTokenLimit"); ok {
		t.Fatal("token limit invented")
	}
	if status, _ := g.get("/v1beta/models/missing"); status != 404 {
		t.Fatalf("missing: %d", status)
	}
	for _, query := range []string{"pageSize=0", "pageSize=1&page_size=1", "pageToken=bad", "key=a&key=b"} {
		if status, _ := g.get("/v1beta/models?" + query); status != 400 {
			t.Fatalf("%s: %d", query, status)
		}
	}
	if g.seenCount() != 0 || g.decisions != 0 {
		t.Fatal("listing reached a provider")
	}
}

func TestGeminiParallelToolsStreamAndGeneratedContentRoundTrip(t *testing.T) {
	g := newGemini(t)
	g.respNative()
	usage := M{"prompt_tokens": 3, "completion_tokens": 7, "total_tokens": 10, "completion_tokens_details": M{"reasoning_tokens": 2}}
	g.reply("first", upstreamReply{200, "text/event-stream", chatWire(append(nativeTools(), usageChunk(usage)), true)})
	declarations := []any{M{"functionDeclarations": []any{M{"name": "first", "parameters": M{"type": "OBJECT"}}, M{"name": "second", "parameters": M{"type": "OBJECT"}}}}}
	g.endpoint("custom-test/first", true)
	reply := g.post(geminiWith(t, "tools", declarations))
	if reply.status != 200 || reply.err != nil {
		t.Fatalf("%d %v %s", reply.status, reply.err, reply.body)
	}
	out := events(t, reply.body)
	final := last(out)
	if at(final, "candidates", 0, "finishReason") != "STOP" || at(final, "usageMetadata", "candidatesTokenCount") != num(5) ||
		at(final, "usageMetadata", "thoughtsTokenCount") != num(2) || strings.Contains(string(reply.body), "[DONE]") {
		t.Fatalf("final %s", reply.body)
	}
	var parts, calls []any
	for _, event := range out {
		candidates, _ := at(event, "candidates").([]any)
		for _, candidate := range candidates {
			list, _ := at(candidate, "content", "parts").([]any)
			parts = append(parts, list...)
		}
	}
	args := map[string]string{}
	for _, part := range parts {
		if call := at(part, "functionCall"); call != nil {
			calls = append(calls, call)
			args[at(call, "id").(string)] = dumpJSON(at(call, "args"))
		}
	}
	if len(args) != 2 || args["call_0"] != `{"x":1}` || args["call_1"] != `{"y":2}` {
		t.Fatalf("calls %v", args)
	}
	if at(g.seenAt(0).body, "stream_options", "include_usage") != true {
		t.Fatalf("sent %s", g.seenAt(0).raw)
	}
	g.endpoint("custom-test/first", false)
	g.reply("first", respChatReply(t, "first"))
	var responses []any
	for _, call := range calls {
		responses = append(responses, M{"functionResponse": M{"id": at(call, "id"), "name": at(call, "name"), "response": M{"result": "완료"}}})
	}
	history := append(at(parsed(t, []byte(geminiRequest)), "contents").([]any), M{"role": "model", "parts": parts}, M{"role": "user", "parts": responses})
	if got := g.post(dumpJSON(M{"contents": history, "tools": declarations})).status; got != 200 {
		t.Fatalf("followup %d", got)
	}
	var toolIDs []any
	for _, m := range at(g.seenAt(-1).body, "messages").([]any) {
		if at(m, "role") == "tool" {
			toolIDs = append(toolIDs, at(m, "tool_call_id"))
		}
	}
	same(t, toolIDs, `["call_0","call_1"]`)
}

func TestGeminiStopsAreNotReportedAsNormalCompletion(t *testing.T) {
	g := newGemini(t)
	for _, c := range []struct{ reason, finish string }{{"max_tokens", "MAX_TOKENS"}, {"refusal", "SAFETY"}, {"model_context_window_exceeded", "MAX_TOKENS"}} {
		g.reply("first", upstreamReply{200, "application/json", []byte(strings.Replace(message("중단"), `"end_turn"`, `"`+c.reason+`"`, 1))})
		reply := g.post(geminiRequest)
		if reply.status != 200 || at(parsed(t, reply.body), "candidates", 0, "finishReason") != c.finish {
			t.Fatalf("%s: %d %s", c.reason, reply.status, reply.body)
		}
	}
}

func TestGeminiUnsupportedResourcesInputsAliasesAndLimitsFailBeforeExecution(t *testing.T) {
	g := newGemini(t)
	for _, path := range []string{"/v1beta/files", "/v1beta/models/custom-test/first:countTokens", "/v1beta/models/x:embedContent"} {
		g.url = g.root + path
		reply := g.post(geminiRequest)
		if reply.status != 501 || at(parsed(t, reply.body), "error", "status") != "UNIMPLEMENTED" {
			t.Fatalf("%s: %d %s", path, reply.status, reply.body)
		}
	}
	if status, _ := g.get("/v1beta/files"); status != 501 {
		t.Fatalf("GET files: %d", status)
	}
	g.endpoint("custom-test/first", false)
	for _, update := range []string{
		geminiWith(t, "cachedContent", "cache"),
		geminiWith(t, "safetySettings", []any{M{"category": "HARM_CATEGORY_HATE_SPEECH"}}),
		geminiWith(t, "generationConfig", M{"candidateCount": 2}),
		geminiWith(t, "systemInstruction", M{}, "system_instruction", M{}),
		dumpJSON(M{"contents": []any{M{"parts": []any{M{"functionResponse": M{"name": "orphan", "response": M{}}}}}}}),
		`{"contents":[],"contents":[]}`} {
		if got := g.post(update).status; got != 400 {
			t.Fatalf("%s: %d", update, got)
		}
	}
	swap(t, &subscription.MaxInput, 10)
	if got := g.post(geminiRequest).status; got != 413 {
		t.Fatalf("oversized: %d", got)
	}
	if g.seenCount() != 0 || len(g.authCalls) != 0 || g.decisions != 0 {
		t.Fatal("executed")
	}
}

// The group half is TestGeminiGroupExplicitLimitSkipsCodexMember.
func TestGeminiExplicitLimitCannotBeLostOnCodexAndStreamRequiresSSE(t *testing.T) {
	g := newGemini(t)
	swap(t, &target, func(model string) (*where, error) {
		if strings.HasSuffix(model, "/first") {
			return &where{kind: "codex", model: "gpt-test"}, nil
		}
		return &where{kind: "direct", base: g.base, key: "host-secret", name: "backup"}, nil
	})
	noCodex(t)
	if got := g.post(geminiWith(t, "generationConfig", M{"maxOutputTokens": 7})).status; got != 400 {
		t.Fatalf("codex limit: %d", got)
	}
	g.endpoint("custom-test/first", true)
	g.url, _, _ = strings.Cut(g.url, "?")
	if got := g.post(geminiRequest).status; got != 400 {
		t.Fatalf("stream without alt=sse: %d", got)
	}
	if g.seenCount() != 0 {
		t.Fatal("forwarded")
	}
}

func TestGeminiCodexAdapterPreservesIncompleteAndMaskedText(t *testing.T) {
	g := newGemini(t)
	up := g.codex()
	var deltas []M
	for _, char := range codexSecret {
		deltas = append(deltas, textDelta(string(char)))
	}
	for _, stream := range []bool{false, true} {
		g.endpoint("custom-test/first", stream)
		up.respond(codexWire(append(deltas, incompleteEvent([]any{textItem(codexSecret)}, "max_output_tokens"))...), 0)
		reply := g.post(geminiRequest)
		if reply.status != 200 {
			t.Fatalf("%v: %d %s", stream, reply.status, reply.body)
		}
		var frames []any
		if stream {
			frames = events(t, reply.body)
		} else {
			frames = []any{parsed(t, reply.body)}
		}
		var text strings.Builder
		for _, frame := range frames {
			for _, c := range at(frame, "candidates").([]any) {
				for _, p := range at(c, "content", "parts").([]any) {
					text.WriteString(at(p, "text").(string))
				}
			}
		}
		if at(last(frames), "candidates", 0, "finishReason") != "MAX_TOKENS" || text.String() != "***" ||
			strings.Contains(string(reply.body), codexSecret) {
			t.Fatalf("%v: %s", stream, reply.body)
		}
	}
}

func TestGeminiCodexReasoningCountsSurviveJSONAndStream(t *testing.T) {
	g := newGemini(t)
	up := g.codex()
	for _, stream := range []bool{false, true} {
		g.endpoint("custom-test/first", stream)
		terminal := completed([]any{textItem("안녕하세요 🌱")})
		terminal["response"].(M)["usage"].(M)["output_tokens_details"] = M{"reasoning_tokens": 4}
		up.respond(codexWire(terminal), 0)
		reply := g.post(geminiRequest)
		var final any
		if stream {
			final = last(events(t, reply.body))
		} else {
			final = parsed(t, reply.body)
		}
		if reply.status != 200 || at(final, "usageMetadata", "candidatesTokenCount") != num(5) ||
			at(final, "usageMetadata", "thoughtsTokenCount") != num(4) || at(final, "candidates", 0, "finishReason") != "STOP" {
			t.Fatalf("%v: %d %s", stream, reply.status, reply.body)
		}
	}
}

func TestGeminiTextArrivesBeforeProviderCompletes(t *testing.T) {
	g := newGemini(t)
	release := delayedUpstream(t)
	g.endpoint("custom-test/first", true)
	earlyText(t, g.url, geminiRequest, release, 20, "finishReason")
}
