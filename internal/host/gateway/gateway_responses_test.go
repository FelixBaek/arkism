package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// The Responses entrance over real local HTTP.

type M = map[string]any

func dumpJSON(v any) string { return host.MustDumps(v, host.DumpOpts{Compact: true, NoASCII: true}) }

// events decodes every SSE data frame of a gateway answer (chat_codec.sse_frames).
func events(t *testing.T, raw []byte) []any {
	t.Helper()
	var out []any
	err := SSEFrames(once(raw), func(_, data string) error {
		out = append(out, parsed(t, []byte(data)))
		return nil
	})
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	return out
}

func last(list []any) any { return list[len(list)-1] }

// Native Chat stream helpers (test_arkism_chat_native chunk/wire/tools).
func chunk(delta any, finish any, extra ...any) M {
	if delta == nil {
		delta = M{}
	}
	c := M{"id": "chatcmpl-test", "object": "chat.completion.chunk", "created": 1, "model": "upstream",
		"choices": []any{M{"index": 0, "delta": delta, "finish_reason": finish}}}
	for i := 0; i+1 < len(extra); i += 2 {
		c[extra[i].(string)] = extra[i+1]
	}
	return c
}

func usageChunk(usage M) M { return chunk(nil, nil, "choices", []any{}, "usage", usage) }

func chatWire(events []M, done bool) []byte {
	var b bytes.Buffer
	for _, e := range events {
		b.WriteString("data: " + dumpJSON(e) + "\n\n")
	}
	if done {
		b.WriteString("data: [DONE]\n\n")
	}
	return b.Bytes()
}

func nativeTools() []M {
	calls := []any{}
	for i, name := range []string{"first", "second"} {
		calls = append(calls, M{"index": i, "id": "call_" + strconv.Itoa(i), "type": "function", "function": M{"name": name, "arguments": ""}})
	}
	return []M{chunk(M{"role": "assistant", "content": "", "reasoning_content": "private"}, nil),
		chunk(M{"tool_calls": calls}, nil),
		chunk(M{"tool_calls": []any{M{"index": 1, "function": M{"arguments": `{"y":2}`}}, M{"index": 0, "function": M{"arguments": `{"x":1}`}}}}, nil),
		chunk(nil, "tool_calls")}
}

// chatReply is a Chat completion converted from the fixture message (chat_codec.response).
func respChatReply(t *testing.T, model string) upstreamReply {
	out, err := CodecResponse(parsed(t, []byte(message("안녕하세요"))), model)
	if err != nil {
		t.Fatal(err)
	}
	return upstreamReply{200, "application/json", []byte(dumpJSON(out))}
}

// native sends every model to the fixture as a Chat provider.
func (f *fixture) respNative() {
	swap(f.t, &target, func(model string) (*where, error) {
		_, name, _ := strings.Cut(model, "/")
		return &where{kind: "chat", base: f.base + "/v1", key: "host-secret", name: name}, nil
	})
}

func (f *fixture) reply(model string, r upstreamReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[model] = r
}

func (f *fixture) seenAt(i int) seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 {
		i += len(f.seen)
	}
	return f.seen[i]
}

func (f *fixture) seenModels() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []any{}
	for _, s := range f.seen {
		out = append(out, get(s.body, "model"))
	}
	return out
}

// noIter fails the test if a provider adapter is ever started (patch.object(..., 'iter_*')).
func noCodex(t *testing.T) {
	swap(t, &codexIterMessages, func(map[string]any, string, string, float64) iter.Seq2[[]byte, error] {
		t.Error("codex executed")
		return func(func([]byte, error) bool) {}
	})
}

func noGoogle(t *testing.T) {
	swap(t, &googleIterContent, func(any, string, bool, bool) iter.Seq2[any, error] {
		t.Error("google executed")
		return func(func(any, error) bool) {}
	})
}

// Codex upstream (test_arkism_codex fixtures) served by the real subscription adapter.
const (
	codexSecret  = "synthetic-oauth-never-print"
	codexAccount = "synthetic-account"
)

func textItem(text string) M {
	return M{"id": "item_text", "type": "message", "role": "assistant",
		"content": []any{M{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func toolItem(args string) M {
	return M{"id": "item_call_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": args}
}

func completed(output []any, extra ...any) M {
	response := M{"status": "completed", "output": output, "usage": M{"input_tokens": 27, "output_tokens": 9}}
	for i := 0; i+1 < len(extra); i += 2 {
		response[extra[i].(string)] = extra[i+1]
	}
	return M{"type": "response.completed", "response": response}
}

func incompleteEvent(output []any, reason string) M {
	e := completed(output, "status", "incomplete", "incomplete_details", M{"reason": reason})
	e["type"] = "response.incomplete"
	return e
}

func textDelta(delta string) M {
	return M{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": delta}
}

func codexWire(events ...M) []byte {
	var b bytes.Buffer
	for _, e := range events {
		b.WriteString("event: " + e["type"].(string) + "\r\ndata: " + dumpJSON(e) + "\r\n\r\n")
	}
	return b.Bytes()
}

type codexUpstream struct {
	mu       sync.Mutex
	data     []byte
	extra    int
	requests []string // headers and body as sent
}

func (c *codexUpstream) respond(data []byte, extra int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data, c.extra, c.requests = data, extra, nil
}

func (c *codexUpstream) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requests...)
}

// codex routes every model to Codex with a signed-in synthetic account and a local ChatGPT backend.
func (f *fixture) codex() *codexUpstream {
	t := f.t
	c := &codexUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		r.Header.Write(&b)
		b.ReadFrom(r.Body)
		c.mu.Lock()
		c.requests = append(c.requests, r.URL.Path+"\n"+b.String())
		data, extra := c.data, c.extra
		c.mu.Unlock()
		w.Header().Set("content-type", "text/event-stream; charset=utf-8")
		w.Header().Set("content-length", strconv.Itoa(len(data)+extra))
		w.WriteHeader(200)
		for offset := 0; offset < len(data); offset += 7 {
			w.Write(data[offset:min(offset+7, len(data))])
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(server.Close)
	swap(t, &subscription.CodexBase, server.URL)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "auth.json"), dumpJSON(M{"tokens": M{"access_token": codexSecret, "account_id": codexAccount}}))
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho 'codex-cli 1.2.3'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_CODEX_BIN", cli)
	swap(t, &target, func(string) (*where, error) { return &where{kind: "codex", model: "gpt-test"}, nil })
	swap(t, &codexProfile, func(string, []string) map[string]any { return map[string]any{"id": "synthetic", "config_dir": dir} })
	return c
}

func newResponses(t *testing.T) *fixture {
	f := newFixture(t)
	f.url = strings.Replace(f.url, "/chat/completions", "/responses", 1)
	return f
}

// responsesRequest is {'model': 'custom-test/first', 'input': '안녕', 'store': False} plus extra fields.
func responsesRequest(extra ...any) string {
	body := host.Obj{{Key: "model", Value: "custom-test/first"}, {Key: "input", Value: "안녕"}, {Key: "store", Value: false}}
	for i := 0; i+1 < len(extra); i += 2 {
		body.Set(extra[i].(string), extra[i+1])
	}
	return dumpJSON(body)
}

func TestResponsesCodexAdapterPreservesIncompleteJSONAndStream(t *testing.T) {
	f := newResponses(t)
	up := f.codex()
	item := textItem("부분 응답")
	item["status"] = "incomplete"
	for _, stream := range []bool{false, true} {
		for _, reason := range []string{"max_output_tokens", "content_filter"} {
			up.respond(codexWire(textDelta("부분 응답"), M{"type": "response.output_item.done", "output_index": 0, "item": item},
				incompleteEvent([]any{item}, reason)), 0)
			reply := f.post(responsesRequest("stream", stream))
			if reply.status != 200 || reply.err != nil {
				t.Fatalf("%v %s: %d %v %s", stream, reason, reply.status, reply.err, reply.body)
			}
			var result any
			if stream {
				result = at(last(events(t, reply.body)), "response")
			} else {
				result = parsed(t, reply.body)
			}
			if at(result, "status") != "incomplete" || at(result, "incomplete_details", "reason") != reason ||
				at(result, "output", 0, "content", 0, "text") != "부분 응답" || at(result, "usage", "output_tokens") != num(9) {
				t.Fatalf("%v %s: %s", stream, reason, reply.body)
			}
			sent := up.sent()
			if len(sent) != 1 || strings.Contains(sent[0], "caller-") {
				t.Fatalf("codex requests %q", sent)
			}
		}
	}
}

func TestResponsesCodexAdapterRejectsTrailingErrorsBeforeToolCompletion(t *testing.T) {
	f := newResponses(t)
	up := f.codex()
	prefix := codexWire(completed([]any{toolItem(`{"query":"한글","nested":{"a":[1,true]}}`)}))
	for _, stream := range []bool{false, true} {
		for _, failure := range []string{"error", "length", "unfinished_tool"} {
			data, extra := prefix, 0
			switch failure {
			case "error":
				data = append(append([]byte(nil), prefix...), codexWire(M{"type": "error", "message": codexSecret})...)
			case "length":
				extra = 1
			case "unfinished_tool":
				unfinished := toolItem("{")
				unfinished["status"] = "incomplete"
				data = codexWire(incompleteEvent([]any{unfinished}, "max_output_tokens"))
			}
			up.respond(data, extra)
			reply := f.post(responsesRequest("stream", stream, "model", "group/test"))
			want := 502
			if stream && failure != "unfinished_tool" {
				want = 200
			}
			raw := string(reply.body)
			if reply.status != want || !strings.Contains(raw, "error") || strings.Contains(raw, "response.completed") ||
				strings.Contains(raw, "response.output_item.done") || strings.Contains(strings.ReplaceAll(raw, " ", ""), `"type":"function_call"`) ||
				strings.Contains(raw, codexSecret) || len(up.sent()) != 1 {
				t.Fatalf("%v %s: %d %v %d requests %s", stream, failure, reply.status, reply.err, len(up.sent()), raw)
			}
		}
	}
}

func TestResponsesCodexEmptyIncompleteAndCredentialsDoNotBecomeErrorsOrLeaks(t *testing.T) {
	f := newResponses(t)
	up := f.codex()
	for _, stream := range []bool{false, true} {
		for _, content := range []string{"", codexSecret} {
			source, deltas := []any{}, []M{}
			if content != "" {
				source = []any{textItem(content)}
				for _, char := range content {
					deltas = append(deltas, textDelta(string(char)))
				}
			}
			up.respond(codexWire(append(deltas, incompleteEvent(source, "max_output_tokens"))...), 0)
			reply := f.post(responsesRequest("stream", stream))
			var result any
			if stream {
				result = at(last(events(t, reply.body)), "response")
			} else {
				result = parsed(t, reply.body)
			}
			if reply.status != 200 || at(result, "status") != "incomplete" || strings.Contains(string(reply.body), codexSecret) {
				t.Fatalf("%v %q: %d %s", stream, content, reply.status, reply.body)
			}
			if content != "" && at(result, "output", 0, "content", 0, "text") != "***" {
				t.Fatalf("%v: not masked %s", stream, reply.body)
			}
		}
	}
}

func TestResponsesMessagesProviderAndCredentialsUseExistingEngine(t *testing.T) {
	f := newResponses(t)
	reply := f.post(responsesRequest("instructions", "규칙", "metadata", M{"task": "test"}))
	if reply.status != 200 {
		t.Fatalf("status %d %s", reply.status, reply.body)
	}
	out := parsed(t, reply.body)
	if at(out, "object") != "response" || at(out, "status") != "completed" || at(out, "store") != false ||
		at(out, "model") != "custom-test/first" || at(out, "parallel_tool_calls") != true || at(out, "tool_choice") != "auto" ||
		at(out, "output", 0, "content", 0, "text") != "안녕하세요" || at(out, "usage", "input_tokens") != num(4) {
		t.Fatalf("reply %s", reply.body)
	}
	same(t, at(out, "metadata"), `{"task":"test"}`)
	same(t, at(out, "tools"), `[]`)
	seen := f.seenAt(0)
	if seen.path != "/v1/messages" || at(seen.body, "system", 0, "text") != "규칙" || at(seen.body, "metadata") != nil {
		t.Fatalf("sent %s %s", seen.path, seen.raw)
	}
	if strings.Contains(dumpJSON(seen.header), "caller-") || reply.header.Get("x-private") != "" {
		t.Fatal("credentials crossed")
	}
}

func TestResponsesParallelFunctionStreamAndFollowupRoundTrip(t *testing.T) {
	f := newResponses(t)
	f.respNative()
	usage := M{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}
	f.reply("first", upstreamReply{200, "text/event-stream", chatWire(append(nativeTools(), usageChunk(usage)), true)})
	toolList := []any{}
	for _, name := range []string{"first", "second"} {
		toolList = append(toolList, M{"type": "function", "name": name, "parameters": M{"type": "object"}, "strict": false})
	}
	reply := f.post(responsesRequest("stream", true, "tools", toolList, "parallel_tool_calls", false))
	if reply.status != 200 || reply.err != nil {
		t.Fatalf("status %d %v", reply.status, reply.err)
	}
	out := events(t, reply.body)
	for i, e := range out {
		if at(e, "sequence_number") != num(i) {
			t.Fatalf("sequence %d: %v", i, at(e, "sequence_number"))
		}
	}
	if at(last(out), "type") != "response.completed" {
		t.Fatalf("last %s", dumpJSON(last(out)))
	}
	output := at(last(out), "response", "output").([]any)
	calls := map[string]string{}
	var callIDs []any
	for _, item := range output {
		if at(item, "type") == "function_call" {
			calls[at(item, "call_id").(string)] = dumpJSON(parsed(t, []byte(at(item, "arguments").(string))))
			callIDs = append(callIDs, at(item, "call_id"))
		}
	}
	if calls["call_0"] != `{"x":1}` || calls["call_1"] != `{"y":2}` || len(calls) != 2 {
		t.Fatalf("calls %v", calls)
	}
	sent := f.seenAt(0).body
	if at(sent, "parallel_tool_calls") != false || at(sent, "stream_options", "include_usage") != true {
		t.Fatalf("sent %s", f.seenAt(0).raw)
	}
	if bytes.Contains(reply.body, []byte("private")) {
		t.Fatal("reasoning leaked")
	}
	f.reply("first", respChatReply(t, "first"))
	history := append([]any{M{"role": "user", "content": "안녕"}}, output...)
	for _, id := range callIDs {
		history = append(history, M{"type": "function_call_output", "call_id": id, "output": "done"})
	}
	if got := f.post(responsesRequest("input", history, "tools", toolList)).status; got != 200 {
		t.Fatalf("followup %d", got)
	}
	var toolIDs []any
	for _, m := range at(f.seenAt(-1).body, "messages").([]any) {
		if at(m, "role") == "tool" {
			toolIDs = append(toolIDs, at(m, "tool_call_id"))
		}
	}
	same(t, toolIDs, `["call_0","call_1"]`)
}

func TestResponsesStoreReferenceStrictAndMalformedInputFailWithoutGeneration(t *testing.T) {
	f := newResponses(t)
	requests := []string{`{"model":"custom-test/first","input":"안녕"}`,
		responsesRequest("store", true), responsesRequest("previous_response_id", "resp_old"),
		responsesRequest("background", true), responsesRequest("conversation", "conv_old"),
		responsesRequest("tools", []any{M{"type": "function", "name": "f", "parameters": M{}}}),
		responsesRequest("input", []any{M{"type": "function_call_output", "call_id": "orphan", "output": "x"}})}
	for _, request := range requests {
		reply := f.post(request)
		if reply.status != 400 || at(parsed(t, reply.body), "error") == nil {
			t.Fatalf("%s: %d %s", request, reply.status, reply.body)
		}
	}
	if f.seenCount() != 0 || f.decisions != 0 {
		t.Fatal("generation started")
	}
	for _, raw := range []string{`{`, `[]`, `{"model":"first","model":"second","store":false,"input":"hi"}`} {
		if got := f.post(raw).status; got != 400 {
			t.Fatalf("%s: %d", raw, got)
		}
	}
	swap(t, &subscription.MaxInput, 10)
	if got := f.post(responsesRequest()).status; got != 413 {
		t.Fatalf("oversized: %d", got)
	}
}

func TestResponsesUnsupportedResourcesNeverForward(t *testing.T) {
	f := newResponses(t)
	for _, c := range []struct{ method, suffix string }{{"GET", ""}, {"GET", "/resp_previous"}, {"POST", "/resp_previous/cancel"}} {
		reply := send(t, c.method, f.url+c.suffix, nil)
		if reply.status != 501 || at(parsed(t, reply.body), "error") == nil {
			t.Fatalf("%s %s: %d %s", c.method, c.suffix, reply.status, reply.body)
		}
	}
	if f.seenCount() != 0 || len(f.authCalls) != 0 {
		t.Fatal("forwarded")
	}
}

func TestResponsesGroupSwitchesOnRejectionAndServerError(t *testing.T) {
	f := newResponses(t)
	for _, failure := range []int{429, 500} {
		f.clear()
		f.reply("first", upstreamReply{failure, "application/json", []byte(`{"error":"host-secret private-details"}`)})
		reply := f.post(responsesRequest("model", "group/test"))
		if reply.status != 200 || f.seenCount() != 2 || f.reportCount() != 2 ||
			bytes.Contains(reply.body, []byte("private-details")) || bytes.Contains(reply.body, []byte("host-secret")) {
			t.Fatalf("%d: %d seen %d reports %d %s", failure, reply.status, f.seenCount(), f.reportCount(), reply.body)
		}
		if at(parsed(t, reply.body), "model") != "group/test" {
			t.Fatalf("model %s", reply.body)
		}
	}
}

func TestResponsesExplicitLimitPreservedAndUnsupportedCodexSkippedBeforeExecution(t *testing.T) {
	f := newResponses(t)
	f.respNative()
	f.reply("first", respChatReply(t, "first"))
	if got := f.post(responsesRequest("max_output_tokens", 17)).status; got != 200 {
		t.Fatalf("status %d", got)
	}
	if at(f.seenAt(0).body, "max_completion_tokens") != num(17) {
		t.Fatalf("sent %s", f.seenAt(0).raw)
	}
	f.clear()
	swap(t, &target, func(model string) (*where, error) {
		if strings.HasSuffix(model, "/first") {
			return &where{kind: "codex", model: "gpt-test"}, nil
		}
		return &where{kind: "direct", base: f.base, key: "host-secret", name: "backup"}, nil
	})
	noCodex(t)
	if got := f.post(responsesRequest("max_output_tokens", 17)).status; got != 400 {
		t.Fatalf("codex limit: %d", got)
	}
	if got := f.post(responsesRequest("model", "group/test", "max_output_tokens", 17)).status; got != 200 {
		t.Fatalf("group: %d", got)
	}
	same(t, f.seenModels(), `["backup"]`)
}

func TestResponsesStructuredOutputIsForwardedToChatTargetsOnly(t *testing.T) {
	f := newResponses(t)
	f.respNative()
	f.reply("first", respChatReply(t, "first"))
	schema := `{"type":"object","properties":{"값":{"type":"string"}},"required":["값"],"additionalProperties":false}`
	for _, c := range []struct{ format, want string }{
		{`{"type":"json_object"}`, `{"type":"json_object"}`},
		{`{"type":"json_schema","name":"answer","schema":` + schema + `}`,
			`{"type":"json_schema","json_schema":{"name":"answer","schema":` + schema + `,"strict":false}}`},
		{`{"type":"json_schema","name":"answer","schema":` + schema + `,"strict":false}`,
			`{"type":"json_schema","json_schema":{"name":"answer","schema":` + schema + `,"strict":false}}`}} {
		f.clear()
		reply := f.post(responsesRequest("text", M{"format": parsed(t, []byte(c.format))}))
		if reply.status != 200 || at(parsed(t, reply.body), "output", 0, "content", 0, "text") != "안녕하세요" {
			t.Fatalf("%s: %d %s", c.format, reply.status, reply.body)
		}
		seen := f.seenAt(0)
		if seen.path != "/v1/chat/completions" || at(seen.body, "model") != "first" {
			t.Fatalf("sent %s %s", seen.path, seen.raw)
		}
		same(t, at(seen.body, "response_format"), c.want)
	}
}

func TestResponsesAbsentStructuredOutputSendsNoResponseFormat(t *testing.T) {
	f := newResponses(t)
	f.respNative()
	f.reply("first", respChatReply(t, "first"))
	for _, request := range []string{responsesRequest(), responsesRequest("text", M{"format": M{"type": "text"}})} {
		f.clear()
		reply := f.post(request)
		if reply.status != 200 || at(parsed(t, reply.body), "output", 0, "content", 0, "text") != "안녕하세요" || f.seenCount() != 1 {
			t.Fatalf("%s: %d %s", request, reply.status, reply.body)
		}
		if _, ok := f.seenAt(0).body.(host.Obj).Get("response_format"); ok {
			t.Fatalf("sent %s", f.seenAt(0).raw)
		}
	}
}

// refusesStructured checks a request a target cannot express is refused before any egress.
func refusesStructured(t *testing.T, f *fixture, request string, kinds map[string]*where) {
	t.Helper()
	for kind, w := range kinds {
		f.clear()
		swap(t, &target, func(string) (*where, error) { return w, nil })
		reply := f.post(request)
		if reply.status != 400 || errorMessage(t, reply.body) != "unsupported_field" {
			t.Fatalf("%s: %d %s", kind, reply.status, reply.body)
		}
		if f.seenCount() != 0 || len(f.authCalls) != 0 || f.reportCount() != 0 {
			t.Fatalf("%s: egress", kind)
		}
	}
}

func TestResponsesTargetsThatCannotExpressStructuredOutputRefuseBeforeAnyEgress(t *testing.T) {
	f := newResponses(t)
	noCodex(t)
	refusesStructured(t, f, responsesRequest("text", M{"format": M{"type": "json_object"}}), map[string]*where{
		"direct":    {kind: "direct", base: f.base, key: "host-secret", name: "first"},
		"anthropic": {kind: "anthropic"}, "codex": {kind: "codex", model: "gpt-test"}})
}

func TestResponsesGroupSkipsCandidateThatCannotExpressStructuredOutput(t *testing.T) {
	f := newResponses(t)
	f.decide = func(string, string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": "anthropic/claude-test", "fallbacks": []any{"custom-test/backup"},
			"decision": "fixed", "classifier_ms": int64(2)}, nil
	}
	swap(t, &target, func(model string) (*where, error) {
		if strings.HasPrefix(model, "anthropic/") {
			return &where{kind: "anthropic"}, nil
		}
		_, name, _ := strings.Cut(model, "/")
		return &where{kind: "chat", base: f.base + "/v1", key: "host-secret", name: name}, nil
	})
	f.reply("backup", respChatReply(t, "backup"))
	reply := f.post(responsesRequest("model", "group/test", "text", M{"format": M{"type": "json_object"}}))
	if reply.status != 200 || f.seenCount() != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	seen := f.seenAt(0)
	if seen.path != "/v1/chat/completions" || at(seen.body, "model") != "backup" {
		t.Fatalf("sent %s %s", seen.path, seen.raw)
	}
	same(t, at(seen.body, "response_format"), `{"type":"json_object"}`)
	if len(f.authCalls) != 0 {
		t.Fatal("claude login read")
	}
	// A skipped candidate is not a call that happened.
	if f.reportCount() != 1 || f.reports[0]["model"] != "custom-test/backup" || f.reports[0]["ok"] != true || f.reports[0]["error"] != "" {
		t.Fatalf("reports %v", f.reports)
	}
}

func TestResponsesGroupWhereEveryCandidateCannotExpressStructuredOutput(t *testing.T) {
	f := newResponses(t)
	f.decide = func(string, string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": "anthropic/claude-test", "fallbacks": []any{"custom-test/backup"},
			"decision": "fixed", "classifier_ms": int64(2)}, nil
	}
	swap(t, &target, func(model string) (*where, error) {
		if strings.HasPrefix(model, "anthropic/") {
			return &where{kind: "anthropic"}, nil
		}
		_, name, _ := strings.Cut(model, "/")
		return &where{kind: "direct", base: f.base, key: "host-secret", name: name}, nil
	})
	reply := f.post(responsesRequest("model", "group/test", "text", M{"format": M{"type": "json_object"}}))
	if reply.status != 400 || errorMessage(t, reply.body) != "unsupported_field" {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if f.seenCount() != 0 || len(f.authCalls) != 0 || f.reportCount() != 0 {
		t.Fatal("egress")
	}
}

func TestResponsesChatEntranceAcceptsResponseFormatUnderTheSameTargetRule(t *testing.T) {
	f := newFixture(t)
	f.respNative()
	f.reply("first", respChatReply(t, "first"))
	request := `{"model":"custom-test/first","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`
	reply := f.post(request)
	if reply.status != 200 || at(parsed(t, reply.body), "choices", 0, "message", "content") != "안녕하세요" || f.seenCount() != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	seen := f.seenAt(0)
	if seen.path != "/v1/chat/completions" || at(seen.body, "model") != "first" || f.decisions != 0 {
		t.Fatalf("sent %s %s", seen.path, seen.raw)
	}
	same(t, at(seen.body, "response_format"), `{"type":"json_object"}`)
	noCodex(t)
	noGoogle(t)
	refusesStructured(t, f, request, map[string]*where{
		"direct":    {kind: "direct", base: f.base, key: "host-secret", name: "first"},
		"anthropic": {kind: "anthropic"}, "codex": {kind: "codex", model: "gpt-test"}, "google": {kind: "google", model: "gemini-test"}})
}

func TestResponsesTruncatedStreamMissingUsageAndHTTPLengthNeverCompleteOrReplay(t *testing.T) {
	f := newResponses(t)
	f.respNative()
	for _, missing := range []string{"done", "usage", "http_length"} {
		f.clear()
		source := nativeTools()
		if missing != "usage" {
			source = append(source, usageChunk(M{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}))
		}
		f.mu.Lock()
		f.lengthExtra = 0
		if missing == "http_length" {
			f.lengthExtra = 10
		}
		f.mu.Unlock()
		f.reply("first", upstreamReply{200, "text/event-stream", chatWire(source, missing != "done")})
		reply := f.post(responsesRequest("model", "group/test", "stream", true))
		raw := string(reply.body)
		if reply.status != 200 || strings.Contains(raw, "event: response.completed") || strings.Contains(raw, "event: response.output_item.done") ||
			strings.Contains(strings.ReplaceAll(raw, " ", ""), `"type":"function_call"`) || f.seenCount() != 1 {
			t.Fatalf("%s: %d %v %s", missing, reply.status, reply.err, raw)
		}
		// Go drops the connection after a started response fails; Python ended with an error event.
		if reply.err == nil && !strings.Contains(raw, "event: error") {
			t.Fatalf("%s: no error event %s", missing, raw)
		}
		f.mu.Lock()
		ok := f.reports[len(f.reports)-1]["ok"]
		f.mu.Unlock()
		if ok != false {
			t.Fatalf("%s: reported ok", missing)
		}
	}
}

func TestResponsesRefusalAndLengthJSONFromMessagesProviderAreIncomplete(t *testing.T) {
	f := newResponses(t)
	for _, c := range []struct{ reason, want string }{{"refusal", "content_filter"}, {"max_tokens", "max_output_tokens"},
		{"model_context_window_exceeded", "max_output_tokens"}} {
		reply := strings.Replace(message("중단 설명"), `"end_turn"`, `"`+c.reason+`"`, 1)
		f.reply("first", upstreamReply{200, "application/json", []byte(reply)})
		out := f.post(responsesRequest())
		result := parsed(t, out.body)
		if out.status != 200 || at(result, "status") != "incomplete" || at(result, "incomplete_details", "reason") != c.want ||
			at(result, "output", 0, "content", 0, "text") != "중단 설명" {
			t.Fatalf("%s: %d %s", c.reason, out.status, out.body)
		}
	}
}

func TestResponsesRefusalOutputCanBeUsedInFollowupHistory(t *testing.T) {
	f := newResponses(t)
	f.respNative()
	source, err := CodecResponse(parsed(t, []byte(message("안녕하세요"))), "first")
	if err != nil {
		t.Fatal(err)
	}
	choice := get(source, "choices").([]any)[0].(host.Obj)
	choice.Set("finish_reason", "content_filter")
	choice.Set("message", host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: nil}, {Key: "refusal", Value: "거절 설명"}})
	f.reply("first", upstreamReply{200, "application/json", []byte(dumpJSON(source))})
	reply := f.post(responsesRequest())
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	output := at(parsed(t, reply.body), "output").([]any)
	history := append(append([]any{M{"role": "user", "content": "안녕"}}, output...), M{"role": "user", "content": "다른 질문"})
	if got := f.post(responsesRequest("input", history)).status; got != 200 {
		t.Fatalf("followup %d", got)
	}
	same(t, at(f.seenAt(-1).body, "messages", 1, "content"), `[{"type":"text","text":"거절 설명"}]`)
}

// delayedUpstream answers one Chat stream: early text, then the rest only after release closes.
func delayedUpstream(t *testing.T) *gate {
	g := &gate{c: make(chan struct{})}
	t.Cleanup(g.open)
	release := g.c
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body.Close()
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write(append(chatWire([]M{chunk(M{"content": "early text"}, nil)}, false), ": redaction padding\n\n"...))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		w.Write(chatWire([]M{chunk(nil, "stop", "usage", M{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3})}, true))
	}))
	t.Cleanup(server.Close)
	swap(t, &target, func(string) (*where, error) {
		return &where{kind: "chat", base: server.URL + "/v1", key: "host-secret", name: "first"}, nil
	})
	return g
}

// gate is a release signal that can be opened more than once (threading.Event.set).
type gate struct {
	c    chan struct{}
	once sync.Once
}

func (g *gate) open() { g.once.Do(func() { close(g.c) }) }

// earlyText posts body and requires early text within lines lines before the provider finishes;
// the rest of the answer must then contain finished.
func earlyText(t *testing.T, url, body string, release *gate, lines int, finished string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	found := false
	for range lines {
		line, err := reader.ReadBytes('\n')
		if bytes.Contains(line, []byte("early text")) {
			found = true
			break
		}
		if err != nil {
			break
		}
	}
	select {
	case <-release.c:
		t.Fatal("released before the text arrived")
	default:
	}
	if !found {
		t.Fatal("text was delayed until provider completion")
	}
	release.open()
	var rest bytes.Buffer
	rest.ReadFrom(reader)
	if !strings.Contains(rest.String(), finished) {
		t.Fatalf("rest %s", rest.String())
	}
}

func TestResponsesTextArrivesBeforeUpstreamCompletes(t *testing.T) {
	f := newResponses(t)
	earlyText(t, f.url, responsesRequest("stream", true), delayedUpstream(t), 25, "event: response.completed")
}

// num is how Parse spells an integer.
func num(n int) json.Number { return json.Number(strconv.Itoa(n)) }
