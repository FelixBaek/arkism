package subscription

import (
	"errors"
	"io"
	"iter"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const (
	codexSecret  = "synthetic-oauth-never-print"
	codexAccount = "synthetic-account"
)

type M = map[string]any

func reqBody(stream bool) M {
	return M{"model": "openai/synthetic-model", "messages": []any{M{"role": "user", "content": "안녕"}}, "stream": stream, "max_tokens": 100}
}

func textItem(text string) M {
	return M{"id": "item_text", "type": "message", "role": "assistant", "content": []any{M{"type": "output_text", "text": text, "annotations": []any{}}}}
}

const hello = "안녕하세요 🌱"

func toolItem(call, args string) M {
	return M{"id": "item_" + call, "type": "function_call", "call_id": call, "name": "lookup", "arguments": args}
}

const toolArgs = `{"query":"한글","nested":{"a":[1,true]}}`

func with(m M, kv ...any) M {
	out := maps2(m, "", nil)
	delete(out, "")
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func completedEv(output []any, kv ...any) M {
	if output == nil {
		output = []any{}
	}
	return M{"type": "response.completed", "response": with(M{"status": "completed", "output": output, "usage": M{"input_tokens": 27, "output_tokens": 9}}, kv...)}
}

func incompleteEv(output []any, reason string) M {
	event := completedEv(output, "status", "incomplete", "incomplete_details", M{"reason": reason})
	event["type"] = "response.incomplete"
	return event
}

func doneEv(item M, index int) M {
	return M{"type": "response.output_item.done", "output_index": index, "item": item}
}

func textEv(kind string, index, contentIndex int, kv ...any) M {
	return with(M{"type": "response." + kind, "output_index": index, "content_index": contentIndex}, kv...)
}

func delta(text string) M { return textEv("output_text.delta", 0, 0, "delta", text) }

func wire(events ...M) []byte {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("event: " + e["type"].(string) + "\r\ndata: " + host.MustDumps(e, host.DumpOpts{NoASCII: true}) + "\r\n\r\n")
	}
	return []byte(b.String())
}

func cat(parts ...[]byte) []byte { return []byte(string(joinBytes(parts))) }

func joinBytes(parts [][]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func decodeSSE(t *testing.T, chunks [][]byte) []M {
	t.Helper()
	var out []M
	for _, c := range chunks {
		_, data, ok := strings.Cut(string(c), "data: ")
		v, err := host.Loads(data)
		if !ok || err != nil {
			t.Fatalf("bad chunk %q", c)
		}
		out = append(out, v.(M))
	}
	return out
}

func types(events []M) []any {
	out := []any{}
	for _, e := range events {
		out = append(out, e["type"])
	}
	return out
}

func deltas(events []M) []any {
	out := []any{}
	for _, e := range events {
		if e["type"] == "content_block_delta" && e["delta"].(M)["type"] == "text_delta" {
			out = append(out, e["delta"].(M)["text"])
		}
	}
	return out
}

// fakeBody serves data size bytes per Read; read overrides that.
type fakeBody struct {
	mu     sync.Mutex
	data   []byte
	size   int
	reads  int
	closed bool
	read   func(b *fakeBody, p []byte) (int, error)
	req    *http.Request
}

func (b *fakeBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	b.reads++
	read := b.read
	b.mu.Unlock()
	if read != nil {
		return read(b, p)
	}
	return b.next(p)
}

func (b *fakeBody) next(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), b.size)], b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *fakeBody) Close() error { b.mu.Lock(); b.closed = true; b.mu.Unlock(); return nil }

// blocked waits for the request deadline, like a stalled TLS read.
func (b *fakeBody) blocked() (int, error) {
	<-b.req.Context().Done()
	return 0, b.req.Context().Err()
}

type codexFixture struct {
	t        *testing.T
	dir      string
	status   int
	ctype    string
	length   int64
	header   http.Header
	body     *fakeBody
	err      error
	requests []*http.Request
	payloads []string
}

func (f *codexFixture) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	f.requests = append(f.requests, req)
	f.payloads = append(f.payloads, string(raw))
	if f.err != nil {
		return nil, f.err
	}
	h := http.Header{"Content-Type": {f.ctype}}
	for k, v := range f.header {
		h[k] = v
	}
	f.body.req = req
	return &http.Response{StatusCode: f.status, Header: h, Body: f.body, ContentLength: f.length, Request: req}, nil
}

func newCodex(t *testing.T) *codexFixture {
	f := &codexFixture{t: t, dir: t.TempDir()}
	writeAuth(t, f.dir, M{"tokens": M{"access_token": codexSecret, "account_id": codexAccount}})
	oldVersion, oldClient := version, Client
	version = func(string, time.Duration) string { return "1.2.3" }
	Client = &http.Client{Transport: f, CheckRedirect: oldClient.CheckRedirect}
	t.Cleanup(func() { version, Client = oldVersion, oldClient })
	return f
}

func writeAuth(t *testing.T, dir string, v any) {
	path := filepath.Join(dir, "auth.json")
	os.Remove(path)
	if err := os.WriteFile(path, []byte(host.MustDumps(v, host.DumpOpts{})), 0o600); err != nil {
		t.Fatal(err)
	}
}

// respond sets the next response; opts are size, length and status as ints or a content type.
func (f *codexFixture) respond(data []byte, opts ...any) *fakeBody {
	f.status, f.ctype, f.length, f.header = 200, "text/event-stream; charset=utf-8", -1, nil
	f.body = &fakeBody{data: data, size: 7}
	for i := 0; i < len(opts); i += 2 {
		switch opts[i] {
		case "size":
			f.body.size = opts[i+1].(int)
		case "length":
			f.length = int64(opts[i+1].(int))
		case "status":
			f.status = opts[i+1].(int)
		case "ctype":
			f.ctype = opts[i+1].(string)
		case "location":
			f.header = http.Header{"Location": {opts[i+1].(string)}}
		}
	}
	return f.body
}

func (f *codexFixture) iter(body M) iter.Seq2[[]byte, error] {
	return IterMessages(body, f.dir, "synthetic-codex", 120)
}

func collect(seq iter.Seq2[[]byte, error]) ([][]byte, error) {
	var out [][]byte
	for chunk, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, chunk)
	}
	return out, nil
}

func (f *codexFixture) run(data []byte, body M, opts ...any) [][]byte {
	f.t.Helper()
	f.respond(data, opts...)
	if body == nil {
		body = reqBody(false)
	}
	chunks, err := collect(f.iter(body))
	if err != nil {
		f.t.Fatalf("unexpected %v", err)
	}
	return chunks
}

func (f *codexFixture) runErr(code string, data []byte, body M, opts ...any) [][]byte {
	f.t.Helper()
	f.respond(data, opts...)
	if body == nil {
		body = reqBody(false)
	}
	chunks, err := collect(f.iter(body))
	codexErr(f.t, err, code)
	if !f.body.closed && len(f.requests) > 0 {
		f.t.Error("response left open")
	}
	return chunks
}

func codexErr(t *testing.T, err error, code string) *CodexError {
	t.Helper()
	var e *CodexError
	if !errors.As(err, &e) || e.Code != code || e.Error() != code || e.Status != codexCodes[code] {
		t.Fatalf("want %s, got %v", code, err)
	}
	return e
}

func message(t *testing.T, chunks [][]byte) M {
	t.Helper()
	if len(chunks) != 1 {
		t.Fatalf("want one message, got %d", len(chunks))
	}
	v, err := host.Loads(string(chunks[0]))
	if err != nil {
		t.Fatal(err)
	}
	return v.(M)
}

func translateErr(t *testing.T, code string, body, model any) {
	t.Helper()
	_, err := TranslateRequest(body, model)
	codexErr(t, err, code)
}

func TestTranslateMultiturnPreservesToolsIDsArgsImagesOrder(t *testing.T) {
	image := M{"type": "image", "source": M{"type": "base64", "media_type": "image/png", "data": "eHl6"}}
	request := reqBody(false)
	request["system"] = []any{M{"type": "text", "text": "one"}, M{"type": "text", "text": "two"}}
	request["tools"] = []any{M{"name": "lookup", "input_schema": M{"type": "object", "properties": M{"optional": M{"type": "string", "pattern": "a+"}}}}}
	request["tool_choice"] = M{"type": "tool", "name": "lookup", "disable_parallel_tool_use": true}
	request["messages"] = append(request["messages"].([]any),
		M{"role": "assistant", "content": []any{M{"type": "text", "text": "checking"}, M{"type": "tool_use", "id": "original-call", "name": "lookup", "input": M{"a": []any{1, "한글"}}}}},
		M{"role": "user", "content": []any{M{"type": "tool_result", "tool_use_id": "original-call", "content": []any{M{"type": "text", "text": "found"}, image}}, M{"type": "text", "text": "continue"}, image}})
	original := mustDumps(request)
	out, err := TranslateRequest(request, "synthetic-model")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, mustDumps(request), original)
	eq(t, []any{out["instructions"], out["stream"], out["store"]}, []any{"one\ntwo", true, false})
	input := out["input"].([]any)
	eq(t, types(func() []M {
		var ms []M
		for _, x := range input {
			ms = append(ms, x.(M))
		}
		return ms
	}()), []any{"message", "message", "function_call", "function_call_output", "message"})
	call, result := input[2].(M), input[3].(M)
	eq(t, []any{call["call_id"], result["call_id"]}, []any{"original-call", "original-call"})
	eq(t, call["arguments"], `{"a":[1,"한글"]}`)
	eq(t, result["output"].([]any)[1].(M)["image_url"], "data:image/png;base64,eHl6")
	eq(t, input[1].(M)["content"].([]any)[0].(M)["type"], "output_text")
	tool := out["tools"].([]any)[0].(M)
	eq(t, []any{tool["strict"], tool["parameters"]}, []any{false, request["tools"].([]any)[0].(M)["input_schema"]})
	eq(t, out["tool_choice"], M{"type": "function", "name": "lookup"})
	eq(t, out["parallel_tool_calls"], false)
	// The final user message keeps its text and image together.
	eq(t, len(input[4].(M)["content"].([]any)), 2)
}

func TestTranslateEmptyInstructionsURLImageToolChoices(t *testing.T) {
	request := reqBody(false)
	request["messages"].([]any)[0].(M)["content"] = []any{M{"type": "image", "source": M{"type": "url", "url": "https://example.test/image.png"}}}
	request["tools"] = []any{M{"name": "f", "input_schema": M{"type": "object"}}}
	for _, c := range [][2]string{{"auto", "auto"}, {"any", "required"}, {"none", "none"}} {
		request["tool_choice"] = M{"type": c[0]}
		out, err := TranslateRequest(request, "model")
		if err != nil {
			t.Fatal(err)
		}
		eq(t, []any{out["instructions"], out["tool_choice"], out["input"].([]any)[0].(M)["content"].([]any)[0].(M)["type"]}, []any{"", c[1], "input_image"})
	}
}

func TestTranslateToolErrorMarkerAndStringOutput(t *testing.T) {
	request := reqBody(false)
	result := M{"type": "tool_result", "tool_use_id": "c", "content": "failure", "is_error": true}
	request["messages"] = append(request["messages"].([]any),
		M{"role": "assistant", "content": []any{M{"type": "tool_use", "id": "c", "name": "f", "input": M{}}}},
		M{"role": "user", "content": []any{result}})
	last := func() any {
		out, err := TranslateRequest(request, "m")
		if err != nil {
			t.Fatal(err)
		}
		input := out["input"].([]any)
		return input[len(input)-1].(M)["output"]
	}
	eq(t, last(), []any{M{"type": "input_text", "text": "[tool_result is_error=true]"}, M{"type": "input_text", "text": "failure"}})
	result["is_error"] = false
	eq(t, last(), "failure")
}

func TestTranslateUnsupportedSemanticsNeverSilentlyDropped(t *testing.T) {
	for _, block := range []M{{"type": "thinking", "thinking": "secret"}, {"type": "document", "source": M{}},
		{"type": "server_tool_use"}, {"type": "text", "text": "x", "citations": []any{M{}}}} {
		request := reqBody(false)
		request["messages"].([]any)[0].(M)["content"] = []any{block}
		translateErr(t, "unsupported_request", request, "m")
	}
	for _, key := range []string{"thinking", "temperature", "top_p", "stop_sequences", "context_management"} {
		translateErr(t, "unsupported_request", with(reqBody(false), key, M{}), "m")
	}
	translateErr(t, "unsupported_request", with(reqBody(false), "tools", []any{M{"type": "web_search_20250305", "name": "web_search"}}), "m")
}

func TestTranslateInvalidInputsSafe(t *testing.T) {
	for _, request := range []any{nil, []any{}, with(reqBody(false), "messages", nil), with(reqBody(false), "stream", "yes"),
		with(reqBody(false), "max_tokens", true), with(reqBody(false), "messages", []any{M{"role": "user", "content": nil}}),
		with(reqBody(false), "messages", []any{M{"role": "user", "content": []any{M{"type": "tool_result", "tool_use_id": "missing", "content": "x"}}}})} {
		translateErr(t, "invalid_request", request, "m")
	}
	translateErr(t, "invalid_request", reqBody(false), nil)
	old := MaxInput
	MaxInput = 10
	defer func() { MaxInput = old }()
	translateErr(t, "input_too_large", reqBody(false), "m")
}

// Python urlsplit/b64decode behaviour, checked against the Python module.
func TestTranslateImageSourceEdges(t *testing.T) {
	image := func(source M) M {
		request := reqBody(false)
		request["messages"].([]any)[0].(M)["content"] = []any{M{"type": "image", "source": source}}
		return request
	}
	for data, code := range map[string]string{"eHl6": "", "eHk=": "", "eHl6eA==": "", "eHl6=": "invalid_request", "eHl6==": "invalid_request",
		"eHk": "invalid_request", "eHl6eA=": "invalid_request", "eHl6\n": "invalid_request", "eH l6": "invalid_request", "=eHl6": "invalid_request", "eHk=eHk=": "invalid_request"} {
		_, err := TranslateRequest(image(M{"type": "base64", "media_type": "image/png", "data": data}), "m")
		if code == "" && err != nil {
			t.Errorf("%q: %v", data, err)
		} else if code != "" {
			codexErr(t, err, code)
		}
	}
	for url, code := range map[string]string{"https://h/x": "", "HTTPS://h": "", "https://@h": "", " \thttps://h": "", "https://[v1.x]/": "",
		"https://[::1]:80/": "", "https://h:bad/": "", "http://h": "unsupported_request", "https://u:@h": "unsupported_request",
		"https://:p@h": "unsupported_request", "https:///x": "unsupported_request", "https:h": "unsupported_request",
		"https://[::1": "invalid_request", "https://[1.2.3.4]/": "invalid_request", "https://x[::1]/": "invalid_request"} {
		_, err := TranslateRequest(image(M{"type": "url", "url": url}), "m")
		if code == "" && err != nil {
			t.Errorf("%q: %v", url, err)
		} else if code != "" {
			codexErr(t, err, code)
		}
	}
}

func TestNonstreamTextUsageAndFixedHeaders(t *testing.T) {
	f := newCodex(t)
	result := message(t, f.run(wire(doneEv(textItem(hello), 0), completedEv(nil)), nil))
	eq(t, result["content"], []any{M{"type": "text", "text": hello}})
	eq(t, result["stop_reason"], "end_turn")
	eq(t, result["usage"], M{"input_tokens": 27, "output_tokens": 9})
	req := f.requests[0]
	eq(t, []any{req.Method, req.URL.String()}, []any{"POST", "https://chatgpt.com/backend-api/codex/responses"})
	for k, v := range map[string]string{"Authorization": "Bearer " + codexSecret, "chatgpt-account-id": codexAccount,
		"OpenAI-Beta": "responses=experimental", "originator": "codex_cli_rs", "User-Agent": "codex_cli_rs/1.2.3", "version": "1.2.3",
		"Content-Type": "application/json", "Accept": "text/event-stream", "Accept-Encoding": "identity"} {
		if got := req.Header[k]; len(got) != 1 || got[0] != v {
			t.Errorf("%s: %q", k, got)
		}
	}
	sent, _ := host.Loads(f.payloads[0])
	eq(t, sent.(M)["model"], "synthetic-model")
	if !f.body.closed {
		t.Error("response open")
	}
}

func TestStreamToolMultiturnCompleteJSONAndUsage(t *testing.T) {
	f := newCodex(t)
	tool := toolItem("call_1", toolArgs)
	events := decodeSSE(t, f.run(wire(
		M{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": `{"query":`},
		M{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": `"한글"`},
		doneEv(tool, 0), doneEv(textItem(hello), 1), completedEv([]any{tool, textItem(hello)})), reqBody(true), "size", 1))
	eq(t, events[0]["type"], "message_start")
	eq(t, events[1]["content_block"].(M)["id"], "call_1")
	args, _ := host.Loads(events[2]["delta"].(M)["partial_json"].(string))
	want, _ := host.Loads(toolArgs)
	eq(t, args, want)
	last := events[len(events)-2]
	eq(t, last["usage"], M{"input_tokens": 27, "output_tokens": 9})
	eq(t, last["delta"].(M)["stop_reason"], "tool_use")
	eq(t, events[len(events)-1]["type"], "message_stop")
}

func TestReasoningUsagePreservedInMessages(t *testing.T) {
	f := newCodex(t)
	for _, c := range []struct {
		extra     M
		reasoning any
	}{{M{}, nil}, {M{"output_tokens_details": nil}, nil}, {M{"output_tokens_details": M{}}, nil},
		{M{"output_tokens_details": M{"reasoning_tokens": nil}}, nil}, {M{"output_tokens_details": M{"reasoning_tokens": 0}}, 0},
		{M{"output_tokens_details": M{"reasoning_tokens": 4}}, 4}, {M{"output_tokens_details": M{"reasoning_tokens": 9}}, 9}} {
		for _, streaming := range []bool{false, true} {
			for _, terminal := range []string{"completed", "incomplete"} {
				usage := with(M{"input_tokens": 27, "output_tokens": 9, "input_tokens_details": M{"cached_tokens": 12}})
				for k, v := range c.extra {
					usage[k] = v
				}
				event := completedEv([]any{textItem(hello)}, "usage", usage)
				if terminal == "incomplete" {
					event["type"] = "response.incomplete"
					event["response"].(M)["status"] = "incomplete"
					event["response"].(M)["incomplete_details"] = M{"reason": "max_output_tokens"}
				}
				chunks := f.run(wire(event), reqBody(streaming))
				var got any
				if streaming {
					events := decodeSSE(t, chunks)
					got = events[len(events)-2]["usage"]
				} else {
					got = message(t, chunks)["usage"]
				}
				want := M{"input_tokens": 27, "output_tokens": 9}
				if c.reasoning != nil {
					want["output_tokens_details"] = M{"reasoning_tokens": c.reasoning}
				}
				eq(t, got, want)
			}
		}
	}
}

func TestInvalidTerminalReasoningUsageNeverCompletes(t *testing.T) {
	f := newCodex(t)
	details := []any{[]any{}, "secret", true, 0}
	for _, v := range []any{-1, true, false, 1.5, "4", []any{}, M{}, 10} {
		details = append(details, M{"reasoning_tokens": v})
	}
	for _, d := range details {
		for _, streaming := range []bool{false, true} {
			event := completedEv([]any{textItem(hello)}, "usage", M{"input_tokens": 27, "output_tokens": 9, "output_tokens_details": d})
			emitted := f.runErr("upstream_protocol", wire(event), reqBody(streaming))
			if streaming {
				for _, e := range decodeSSE(t, emitted) {
					if e["type"] == "message_delta" || e["type"] == "message_stop" {
						t.Errorf("%v completed", d)
					}
				}
			} else if len(emitted) != 0 {
				t.Errorf("%v emitted", d)
			}
		}
	}
}

func TestCompletedOnlyResponseAndParallelCalls(t *testing.T) {
	f := newCodex(t)
	result := message(t, f.run(wire(completedEv([]any{toolItem("call_1", toolArgs), toolItem("call_2", toolArgs)})), nil))
	content := result["content"].([]any)
	eq(t, []any{content[0].(M)["id"], content[1].(M)["id"], result["stop_reason"]}, []any{"call_1", "call_2", "tool_use"})
}

func TestFinalArgsOverridePartialDelta(t *testing.T) {
	f := newCodex(t)
	result := message(t, f.run(wire(M{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": "{"}, doneEv(toolItem("call_1", toolArgs), 0), completedEv(nil)), nil))
	eq(t, result["content"].([]any)[0].(M)["input"].(M)["query"], "한글")
}

func TestHTTPErrorsAndRedirectNeverReadOrFollow(t *testing.T) {
	f := newCodex(t)
	for _, c := range []struct {
		status int
		code   string
	}{{302, "upstream_error"}, {401, "upstream_auth"}, {403, "upstream_forbidden"}, {429, "upstream_rate_limit"}, {500, "upstream_error"}} {
		f.requests = nil
		f.runErr(c.code, []byte(codexSecret), nil, "status", c.status, "location", "https://chatgpt.com/elsewhere")
		if f.body.reads != 0 || len(f.requests) != 1 || !f.body.closed {
			t.Errorf("%d: reads %d requests %d", c.status, f.body.reads, len(f.requests))
		}
	}
}

func TestResponseErrorsRedacted(t *testing.T) {
	f := newCodex(t)
	for _, kind := range []string{"error", "response.failed", "response.incomplete"} {
		f.runErr("upstream_error", wire(M{"type": kind, "error": M{"message": codexSecret}}), nil)
	}
	f.runErr("upstream_error", wire(completedEv(nil, "error", M{"message": codexSecret})), nil)
	f.runErr("upstream_protocol", []byte(codexSecret), nil, "ctype", "application/json")
}

func TestCredentialsMaskedInTextDeltasToolsAndJSON(t *testing.T) {
	f := newCodex(t)
	value := "앞 " + codexSecret + " 뒤 " + codexSecret[:5]
	item := textItem(value)
	tool := toolItem("call_1", host.MustDumps(M{"nested": []any{codexSecret}, codexSecret: "value"}, host.DumpOpts{}))
	var events []M
	for _, r := range value {
		events = append(events, delta(string(r)))
	}
	events = append(events, doneEv(item, 0), doneEv(tool, 1), completedEv([]any{item, tool}))
	data := wire(events...)
	result := message(t, f.run(data, nil))
	content := result["content"].([]any)
	eq(t, content[0].(M)["text"], "앞 *** 뒤 "+codexSecret[:5])
	eq(t, content[1].(M)["input"], M{"nested": []any{"***"}, "***": "value"})
	for _, d := range [][]byte{data, wire(completedEv([]any{item, tool}))} {
		chunks := f.run(d, reqBody(true), "size", 1)
		streamed := decodeSSE(t, chunks)
		var text, args strings.Builder
		for _, e := range streamed {
			if e["type"] == "content_block_delta" {
				dl := e["delta"].(M)
				if dl["type"] == "text_delta" {
					text.WriteString(dl["text"].(string))
				} else {
					args.WriteString(dl["partial_json"].(string))
				}
			}
		}
		eq(t, text.String(), content[0].(M)["text"])
		parsed, _ := host.Loads(args.String())
		eq(t, parsed, content[1].(M)["input"])
		if strings.Contains(string(joinBytes(chunks)), codexSecret) {
			t.Error("secret streamed")
		}
	}
}

func TestMalformedAndTruncatedSSEAndArgs(t *testing.T) {
	f := newCodex(t)
	full := wire(completedEv(nil))
	for _, c := range []struct {
		data []byte
		code string
	}{{[]byte("data: {bad}\n\n"), "upstream_protocol"}, {[]byte("data: \xff\n\n"), "upstream_protocol"},
		{wire(doneEv(textItem(hello), 0)), "upstream_truncated"}, {[]byte("data: [DONE]\n\n"), "upstream_truncated"},
		{full[:len(full)-2], "upstream_truncated"}, {[]byte("data: []\n\n"), "upstream_protocol"},
		{wire(doneEv(toolItem("call_1", "{"), 0), completedEv(nil)), "upstream_protocol"},
		{wire(doneEv(toolItem("call_1", "[]"), 0), completedEv(nil)), "upstream_protocol"},
		{wire(doneEv(toolItem("call_1", `{"a":1,"a":2}`), 0), completedEv(nil)), "upstream_protocol"}} {
		f.runErr(c.code, c.data, nil)
	}
}

func TestLateErrorHasNoRetryOrSuccessTerminator(t *testing.T) {
	f := newCodex(t)
	f.respond(wire(doneEv(textItem(hello), 0), M{"type": "error", "message": codexSecret}))
	next, stop := iter.Pull2(f.iter(reqBody(true)))
	defer stop()
	first, _, _ := next()
	eq(t, decodeSSE(t, [][]byte{first})[0]["type"], "message_start")
	var rest [][]byte
	for {
		chunk, err, ok := next()
		if !ok {
			t.Fatal("no error")
		}
		if err != nil {
			codexErr(t, err, "upstream_error")
			break
		}
		rest = append(rest, chunk)
	}
	for _, e := range decodeSSE(t, rest) {
		if e["type"] == "message_stop" {
			t.Error("stopped")
		}
	}
	if len(f.requests) != 1 || !f.body.closed {
		t.Error("retried or open")
	}
}

func TestCloseDisconnectReleasesConnection(t *testing.T) {
	f := newCodex(t)
	for _, data := range [][]byte{wire(doneEv(toolItem("call_1", toolArgs), 0), completedEv(nil)), wire(delta("partial"))} {
		body := f.respond(data)
		next, stop := iter.Pull2(f.iter(reqBody(true)))
		next()
		stop()
		if !body.closed {
			t.Error("left open")
		}
	}
}

func TestTerminalWaitsForEOFAndRejectsTrailingFailure(t *testing.T) {
	f := newCodex(t)
	prefix := wire(completedEv([]any{textItem(hello)}))
	for _, c := range []struct {
		suffix []byte
		code   string
	}{{wire(M{"type": "error", "message": codexSecret}), "upstream_error"}, {wire(completedEv(nil)), "upstream_protocol"},
		{wire(delta("late")), "upstream_protocol"}, {[]byte("data: \xff\n\n"), "upstream_protocol"},
		{[]byte("data: {bad}\n\n"), "upstream_protocol"}, {[]byte("data: {"), "upstream_truncated"},
		{[]byte("data: [DONE]\n\ndata: [DONE]\n\n"), "upstream_protocol"},
		{cat([]byte("data: [DONE]\n\n"), wire(M{"type": "error"})), "upstream_error"}} {
		for _, stream := range []bool{false, true} {
			seen := f.runErr(c.code, cat(prefix, c.suffix), reqBody(stream))
			if stream {
				for _, e := range decodeSSE(t, seen) {
					if e["type"] == "message_stop" || e["type"] == "message_delta" {
						t.Errorf("%q completed", c.suffix)
					}
				}
			} else if len(seen) != 0 {
				t.Errorf("%q emitted", c.suffix)
			}
		}
	}
	f.runErr("upstream_truncated", prefix, nil, "length", len(prefix)+1)
	for _, suffix := range []string{"", "data: [DONE]\n\n"} {
		data := cat(prefix, []byte(suffix))
		eq(t, message(t, f.run(data, nil, "length", len(data)))["stop_reason"], "end_turn")
		eq(t, len(f.body.data), 0)
	}
}

func TestIncompletePreservesPartialTextUsageAndReason(t *testing.T) {
	f := newCodex(t)
	item := with(textItem("부분"), "status", "incomplete")
	for _, c := range [][2]string{{"max_output_tokens", "max_tokens"}, {"content_filter", "refusal"}} {
		for _, withDone := range []bool{false, true} {
			events := []M{delta("부분")}
			if withDone {
				events = append(events, doneEv(item, 0))
			}
			data := wire(append(events, incompleteEv([]any{item}, c[0]))...)
			result := message(t, f.run(data, nil))
			eq(t, []any{result["stop_reason"], result["content"], result["usage"]},
				[]any{c[1], []any{M{"type": "text", "text": "부분"}}, M{"input_tokens": 27, "output_tokens": 9}})
			streamed := decodeSSE(t, f.run(cat(data, []byte("data: [DONE]\n\n")), reqBody(true)))
			eq(t, streamed[len(streamed)-2]["delta"].(M)["stop_reason"], c[1])
			eq(t, streamed[len(streamed)-1]["type"], "message_stop")
			eq(t, deltas(streamed), []any{"부분"})
		}
		only := decodeSSE(t, f.run(wire(incompleteEv([]any{item}, c[0])), reqBody(true)))
		eq(t, only[0]["message"].(M)["stop_reason"], nil)
		eq(t, only[len(only)-2]["delta"].(M)["stop_reason"], c[1])
	}
}

func TestIncompleteRejectsUnknownReasonOrUnfinishedTool(t *testing.T) {
	f := newCodex(t)
	for _, event := range []M{incompleteEv([]any{textItem(hello)}, "unknown"),
		incompleteEv([]any{with(toolItem("call_1", "{"), "status", "incomplete")}, "max_output_tokens"),
		incompleteEv([]any{toolItem("call_1", "{")}, "max_output_tokens"),
		completedEv([]any{with(textItem(hello), "status", "incomplete")})} {
		f.runErr("upstream_protocol", wire(event), nil)
	}
	f.runErr("upstream_protocol", wire(doneEv(with(textItem(hello), "status", "incomplete"), 0), completedEv(nil)), nil)
}

func TestPartialDeltaCannotBeSilentlyCompleted(t *testing.T) {
	f := newCodex(t)
	d := delta("unfinished")
	f.runErr("upstream_truncated", wire(d, completedEv(nil)), nil)
	f.runErr("upstream_protocol", wire(with(d, "delta", nil), completedEv(nil)), nil)
	f.runErr("upstream_protocol", wire(d, completedEv([]any{textItem("finished")})), nil)
	eq(t, message(t, f.run(wire(d, completedEv([]any{textItem("unfinished")})), nil))["content"].([]any)[0].(M)["text"], "unfinished")
}

// waitFor fails the test when ch stays open past 100ms.
func waitFor(t *testing.T, ch chan struct{}, why string) {
	select {
	case <-ch:
	case <-time.After(100 * time.Millisecond):
		t.Error(why)
	}
}

func TestFirstTextDeltaPrecedesBlockedItemDone(t *testing.T) {
	f := newCodex(t)
	released := make(chan struct{})
	chunks := [][]byte{wire(delta("첫 🌱")), wire(doneEv(textItem("첫 🌱"), 0), completedEv(nil))}
	body := f.respond(nil)
	body.read = func(b *fakeBody, p []byte) (int, error) {
		if b.reads > 1 {
			waitFor(t, released, "item.done is unavailable until the first delta is received")
		}
		if len(chunks) == 0 {
			return 0, io.EOF
		}
		n := copy(p, chunks[0])
		chunks = chunks[1:]
		return n, nil
	}
	next, stop := iter.Pull2(f.iter(reqBody(true)))
	defer stop()
	var first [][]byte
	for range 3 {
		c, _, _ := next()
		first = append(first, c)
	}
	events := decodeSSE(t, first)
	eq(t, types(events), []any{"message_start", "content_block_start", "content_block_delta"})
	eq(t, events[2]["delta"], M{"type": "text_delta", "text": "첫 🌱"})
	eq(t, body.reads, 1)
	close(released)
	var rest [][]byte
	for {
		c, err, ok := next()
		if !ok || err != nil {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		rest = append(rest, c)
	}
	eq(t, types(decodeSSE(t, rest)), []any{"content_block_stop", "message_delta", "message_stop"})
	if len(f.requests) != 1 || !body.closed {
		t.Error("retried or open")
	}
}

func TestMultiblockUTF8ReasoningAndToolsMatchNonstream(t *testing.T) {
	f := newCodex(t)
	reasoning := M{"id": "reasoning", "type": "reasoning", "summary": []any{}}
	item := textItem("한글 🌱")
	item["content"] = append(item["content"].([]any), M{"type": "output_text", "text": "두 번째", "annotations": []any{}})
	tool := toolItem("call_1", toolArgs)
	following := with(textItem("끝"), "id", "following")
	parts := item["content"].([]any)
	data := wire(
		M{"type": "response.output_item.added", "output_index": 0, "item": reasoning},
		M{"type": "response.reasoning_summary_text.delta", "output_index": 0, "delta": "private reasoning"},
		doneEv(reasoning, 0),
		M{"type": "response.output_item.added", "output_index": 1, "item": with(item, "content", []any{}, "status", "in_progress")},
		textEv("content_part.added", 1, 0, "part", M{"type": "output_text", "text": "", "annotations": []any{}}),
		textEv("output_text.delta", 1, 0, "item_id", "item_text", "delta", "한글 "), textEv("output_text.delta", 1, 0, "item_id", "item_text", "delta", "🌱"),
		textEv("output_text.done", 1, 0, "text", "한글 🌱"),
		textEv("content_part.done", 1, 0, "part", parts[0]),
		textEv("output_text.delta", 1, 1, "delta", "두 "), textEv("output_text.delta", 1, 1, "delta", "번째"),
		textEv("content_part.done", 1, 1, "part", parts[1]), doneEv(item, 1),
		M{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": "{"}, doneEv(tool, 2),
		textEv("output_text.delta", 3, 0, "delta", "끝"), doneEv(following, 3), completedEv([]any{reasoning, item, tool, following}))
	chunks := f.run(data, reqBody(true), "size", 1)
	streamed := decodeSSE(t, chunks)
	result := message(t, f.run(data, nil, "size", 1))
	var blocks []any
	var stops []any
	for _, e := range streamed {
		switch e["type"] {
		case "content_block_start":
			eq(t, e["index"], len(blocks))
			blocks = append(blocks, e["content_block"])
		case "content_block_delta":
			i, _ := host.Int(e["index"])
			block, d := blocks[i].(M), e["delta"].(M)
			if d["type"] == "text_delta" {
				block["text"] = block["text"].(string) + d["text"].(string)
			} else {
				block["input"], _ = host.Loads(d["partial_json"].(string))
			}
		case "content_block_stop":
			stops = append(stops, e["index"])
		}
	}
	eq(t, blocks, result["content"])
	eq(t, stops, []any{0, 1, 2, 3})
	if strings.Contains(string(joinBytes(chunks)), "private reasoning") {
		t.Error("reasoning exposed")
	}
	eq(t, streamed[len(streamed)-2]["usage"], result["usage"])
	eq(t, streamed[len(streamed)-2]["delta"].(M)["stop_reason"], result["stop_reason"])
}

func indexedDeltas(events []M) (out []any, stops []any) {
	for _, e := range events {
		switch e["type"] {
		case "content_block_delta":
			out = append(out, []any{e["index"], e["delta"].(M)["text"]})
		case "content_block_stop":
			stops = append(stops, e["index"])
		}
	}
	return out, stops
}

func TestInterleavedTextIndicesKeepTheirBlocks(t *testing.T) {
	f := newCodex(t)
	item := textItem("AB")
	item["content"] = append(item["content"].([]any), M{"type": "output_text", "text": "12", "annotations": []any{}})
	got, stops := indexedDeltas(decodeSSE(t, f.run(wire(delta("A"), textEv("output_text.delta", 0, 1, "delta", "1"),
		delta("B"), textEv("output_text.delta", 0, 1, "delta", "2"), doneEv(item, 0), completedEv(nil)), reqBody(true))))
	eq(t, got, []any{[]any{0, "A"}, []any{1, "1"}, []any{0, "B"}, []any{1, "2"}})
	eq(t, stops, []any{0, 1})
}

func TestInterleavedOutputItemsCanFinishOutOfOrder(t *testing.T) {
	f := newCodex(t)
	one, two := textItem("AB"), with(textItem("12"), "id", "second")
	second := func(text string) M { return textEv("output_text.delta", 1, 0, "delta", text) }
	data := wire(delta("A"), second("1"), delta("B"), second("2"), doneEv(two, 1), doneEv(one, 0), completedEv([]any{one, two}))
	got, stops := indexedDeltas(decodeSSE(t, f.run(data, reqBody(true))))
	eq(t, got, []any{[]any{0, "A"}, []any{1, "1"}, []any{0, "B"}, []any{1, "2"}})
	eq(t, stops, []any{1, 0})
	eq(t, message(t, f.run(data, nil))["content"], []any{M{"type": "text", "text": "AB"}, M{"type": "text", "text": "12"}})
	f.runErr("upstream_truncated", wire(second("12"), doneEv(two, 1), completedEv(nil)), nil)
}

func TestPartStopPrecedesBlockedItemDone(t *testing.T) {
	f := newCodex(t)
	released := make(chan struct{})
	part := textItem("seen")["content"].([]any)[0]
	body := f.respond(wire(delta("seen"), textEv("content_part.done", 0, 0, "part", part)), "size", 8192)
	refilled := false
	body.read = func(b *fakeBody, p []byte) (int, error) {
		if len(b.data) == 0 && !refilled {
			waitFor(t, released, "item.done is unavailable until the part stop is received")
			b.data, refilled = wire(doneEv(textItem("seen"), 0), completedEv(nil)), true
		}
		return b.next(p)
	}
	next, stop := iter.Pull2(f.iter(reqBody(true)))
	defer stop()
	var first [][]byte
	for range 4 {
		c, _, _ := next()
		first = append(first, c)
	}
	eq(t, decodeSSE(t, first)[3]["type"], "content_block_stop")
	eq(t, body.reads, 1)
	close(released)
	var rest [][]byte
	for c, err, ok := next(); ok && err == nil; c, err, ok = next() {
		rest = append(rest, c)
	}
	eq(t, types(decodeSSE(t, rest)), []any{"message_delta", "message_stop"})
}

func TestDoneAndCompletedOnlyTextFallbacks(t *testing.T) {
	f := newCodex(t)
	for _, data := range [][]byte{wire(doneEv(textItem(hello), 0), completedEv(nil)), wire(completedEv([]any{textItem(hello)})),
		wire(textEv("output_text.done", 0, 0, "text", hello), completedEv([]any{textItem(hello)})),
		wire(textEv("content_part.done", 0, 0, "part", textItem(hello)["content"].([]any)[0]), completedEv([]any{textItem(hello)}))} {
		events := decodeSSE(t, f.run(data, reqBody(true)))
		eq(t, deltas(events), []any{hello})
		_, stops := indexedDeltas(events)
		eq(t, len(stops), 1)
		eq(t, events[len(events)-1]["type"], "message_stop")
	}
}

func TestEmptyTextAndRefusalDeltas(t *testing.T) {
	f := newCodex(t)
	for _, part := range []M{{"type": "output_text", "text": "", "annotations": []any{}}, {"type": "refusal", "refusal": "거절 🌱"}} {
		field := "refusal"
		if part["type"] == "output_text" {
			field = "text"
		}
		value := part[field].(string)
		item := with(textItem(hello), "content", []any{part})
		kind := part["type"].(string)
		data := wire(textEv(kind+".delta", 0, 0, "delta", value), textEv(kind+".done", 0, 0, field, value),
			textEv("content_part.done", 0, 0, "part", part), doneEv(item, 0), completedEv([]any{item}))
		eq(t, deltas(decodeSSE(t, f.run(data, reqBody(true), "size", 1))), []any{value})
		eq(t, message(t, f.run(data, nil))["content"], []any{M{"type": "text", "text": value}})
	}
}

// pullErr reads n chunks, then drains to the error; it returns everything yielded.
func pullErr(t *testing.T, seq iter.Seq2[[]byte, error], n int, code string) [][]byte {
	t.Helper()
	next, stop := iter.Pull2(seq)
	defer stop()
	var seen [][]byte
	for {
		c, err, ok := next()
		if !ok {
			t.Fatalf("ended without %s", code)
		}
		if err != nil {
			if len(seen) < n {
				t.Fatalf("failed before %d chunks: %v", n, err)
			}
			codexErr(t, err, code)
			return seen
		}
		seen = append(seen, c)
	}
}

func noStop(t *testing.T, events []M) {
	t.Helper()
	starts := 0
	for _, e := range events {
		if e["type"] == "message_stop" {
			t.Error("message_stop sent")
		}
		if e["type"] == "message_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("%d message_start", starts)
	}
}

func TestTextMismatchDuplicateDoneAndLateDeltaFailAfterBytes(t *testing.T) {
	f := newCodex(t)
	d := delta("seen")
	part := textItem("seen")["content"].([]any)[0]
	for _, ending := range [][]M{
		{doneEv(textItem("changed"), 0)}, {completedEv([]any{textItem("changed")})},
		{textEv("output_text.done", 0, 0, "text", "changed")}, {textEv("content_part.done", 0, 0, "part", textItem("changed")["content"].([]any)[0])},
		{doneEv(textItem("seen"), 0), completedEv([]any{textItem("changed")})},
		{textEv("output_text.done", 0, 0, "text", "seen"), textEv("output_text.done", 0, 0, "text", "seen")},
		{textEv("content_part.done", 0, 0, "part", part), textEv("content_part.done", 0, 0, "part", part)},
		{doneEv(textItem("seen"), 0), doneEv(textItem("seen"), 0)},
		{textEv("output_text.done", 0, 0, "text", "seen"), d},
		{textEv("content_part.done", 0, 0, "part", part), d}, {doneEv(textItem("seen"), 0), d},
	} {
		f.requests = nil
		body := f.respond(wire(append([]M{d}, ending...)...))
		seen := decodeSSE(t, pullErr(t, f.iter(reqBody(true)), 3, "upstream_protocol"))
		eq(t, seen[2]["delta"].(M)["text"], "seen")
		noStop(t, seen)
		if len(f.requests) != 1 || !body.closed {
			t.Error("retried or open")
		}
	}
}

func TestDeltaThenMalformedTruncatedErrorOrInvalidToolNoRetry(t *testing.T) {
	f := newCodex(t)
	for _, c := range []struct {
		suffix []byte
		code   string
	}{{nil, "upstream_truncated"}, {[]byte("data: {bad}\n\n"), "upstream_protocol"}, {[]byte("data: \xff\n\n"), "upstream_protocol"},
		{wire(completedEv(nil)), "upstream_truncated"}, {wire(M{"type": "error", "message": codexSecret}), "upstream_error"},
		{wire(M{"type": "response.failed"}), "upstream_error"},
		{wire(doneEv(textItem("seen"), 0), doneEv(toolItem("call_1", "{"), 1)), "upstream_protocol"}} {
		f.requests = nil
		body := f.respond(cat(wire(delta("seen")), c.suffix))
		noStop(t, decodeSSE(t, pullErr(t, f.iter(reqBody(true)), 3, c.code)))
		if len(f.requests) != 1 || !body.closed {
			t.Error("retried or open")
		}
	}
}

func TestMalformedTextMappingAndReasoningNeverExposed(t *testing.T) {
	f := newCodex(t)
	d := delta("x")
	for _, event := range []M{with(d, "content_index", nil), with(d, "content_index", true), with(d, "content_index", -1),
		with(d, "output_index", true), with(d, "output_index", -1)} {
		f.runErr("upstream_protocol", wire(event, completedEv(nil)), nil)
	}
	for _, data := range [][]byte{
		wire(M{"type": "response.output_item.added", "output_index": 0, "item": M{"type": "reasoning"}}, d),
		wire(M{"type": "response.output_item.added", "output_index": 0, "item": M{"type": "message", "role": "user"}}, d),
		wire(with(d, "item_id", "one"), with(d, "item_id", "two")),
		wire(with(d, "content_index", 1), doneEv(textItem("x"), 0)),
		wire(with(d, "item_id", "one"), doneEv(textItem("x"), 0)),
		wire(d, doneEv(M{"type": "reasoning"}, 0)),
		wire(d, textEv("content_part.done", 0, 0, "part", M{"type": "output_text", "text": "x", "annotations": []any{M{}}}))} {
		f.runErr("upstream_protocol", data, nil)
	}
}

func TestStreamBoundsStillApplyAfterFirstDelta(t *testing.T) {
	f := newCodex(t)
	prefix := wire(delta("seen"))
	for _, c := range []struct {
		limit  *int
		value  int
		suffix []byte
	}{{&MaxOutput, len(prefix) + 10, wire(delta(strings.Repeat("x", 100)))}, {&MaxEvent, len(prefix), []byte("data: " + strings.Repeat("x", 1000))}} {
		body := f.respond(cat(prefix, c.suffix), "size", len(prefix))
		next, stop := iter.Pull2(f.iter(reqBody(true)))
		var first [][]byte
		for range 3 {
			chunk, _, _ := next()
			first = append(first, chunk)
		}
		old := *c.limit
		*c.limit = c.value
		eq(t, decodeSSE(t, first)[2]["delta"].(M)["text"], "seen")
		_, err, _ := next()
		codexErr(t, err, "output_too_large")
		stop()
		*c.limit = old
		if !body.closed {
			t.Error("left open")
		}
	}
}

func TestLongContextManySmallDeltasAndBoundedInput(t *testing.T) {
	f := newCodex(t)
	request := reqBody(true)
	long := strings.Repeat("긴 문맥 🌱", 10000)
	request["messages"].([]any)[0].(M)["content"] = long
	var events []M
	for range 1000 {
		events = append(events, delta("🌱"))
	}
	data := wire(append(events, doneEv(textItem(strings.Repeat("🌱", 1000)), 0), completedEv(nil))...)
	eq(t, len(deltas(decodeSSE(t, f.run(data, request, "size", 8192)))), 1000)
	sent, _ := host.Loads(f.payloads[0])
	eq(t, sent.(M)["input"].([]any)[0].(M)["content"].([]any)[0].(M)["text"], long)
	f.requests = nil
	old := MaxInput
	MaxInput = 100
	defer func() { MaxInput = old }()
	f.respond(data)
	_, err := collect(f.iter(request))
	codexErr(t, err, "input_too_large")
	eq(t, len(f.requests), 0)
}

func TestRealDeadlineClosesBlockedRead(t *testing.T) {
	f := newCodex(t)
	body := f.respond(nil)
	body.read = func(b *fakeBody, _ []byte) (int, error) { return b.blocked() }
	start := time.Now()
	_, err := collect(IterMessages(reqBody(false), f.dir, "synthetic-codex", .05))
	codexErr(t, err, "deadline_exceeded")
	if time.Since(start) > 800*time.Millisecond || !body.closed {
		t.Error("slow or open")
	}
}

func TestDeadlineAfterTextDeltaClosesWithoutSuccessOrRetry(t *testing.T) {
	f := newCodex(t)
	body := f.respond(nil)
	body.read = func(b *fakeBody, p []byte) (int, error) {
		if b.reads == 1 {
			return copy(p, wire(delta("partial"))), nil
		}
		return b.blocked()
	}
	seen := pullErr(t, IterMessages(reqBody(true), f.dir, "synthetic-codex", .1), 3, "deadline_exceeded")
	eq(t, decodeSSE(t, seen)[2]["delta"].(M)["text"], "partial")
	if len(f.requests) != 1 || !body.closed {
		t.Error("retried or open")
	}
}

func TestStructuredStreamLimitIsReportedWithoutReplay(t *testing.T) {
	f := newCodex(t)
	limit := M{"code": "usage_limit_reached", "message": codexSecret}
	f.runErr("upstream_rate_limit", wire(M{"type": "error", "error": limit}), reqBody(true))
	f.runErr("upstream_rate_limit", wire(M{"type": "response.failed", "response": M{"error": limit}}), reqBody(true))
}

func TestRateLimitRetryAfterIsBoundedAndTokenFree(t *testing.T) {
	f := newCodex(t)
	oldNow := now
	now = func() time.Time { return time.Unix(1000, 0) }
	defer func() { now = oldNow }()
	for _, c := range []struct {
		value string
		want  float64
	}{{"42", 42}, {time.Unix(1120, 0).UTC().Format(http.TimeFormat), 120}, {"Thu, 01 Jan 1970 00:18:40 +0000", 120},
		{codexSecret, -1}, {"inf", -1}, {"-1", -1}, {"604801", -1}, {"", -1}} {
		f.respond(nil, "status", 429)
		f.header = http.Header{"Retry-After": {c.value}}
		_, err := collect(f.iter(reqBody(false)))
		e := codexErr(t, err, "upstream_rate_limit")
		if e.RetryAfter != c.want {
			t.Errorf("%q: %v", c.value, e.RetryAfter)
		}
	}
}

func TestErrorConstructorCannotEchoArbitraryCode(t *testing.T) {
	e := NewCodexError(codexSecret, -1)
	eq(t, []any{e.Status, e.Error()}, []any{502, "upstream_error"})
	for _, retry := range []float64{math.NaN(), math.Inf(1), 0, -1, 604800.5} {
		if NewCodexError("upstream_rate_limit", retry).RetryAfter != -1 {
			t.Error(retry)
		}
	}
	eq(t, NewCodexError("upstream_rate_limit", 604800).RetryAfter, 604800.0)
}

func TestAuthSymlinkRejectedAndNoPublicToken(t *testing.T) {
	f := newCodex(t)
	auth := filepath.Join(f.dir, "auth.json")
	os.Rename(auth, filepath.Join(f.dir, "real.json"))
	os.Symlink(filepath.Join(f.dir, "real.json"), auth)
	_, err := collect(f.iter(reqBody(false)))
	codexErr(t, err, "auth_missing")
	eq(t, len(f.requests), 0)
}

func TestSSECommentsMultilineAndRefusal(t *testing.T) {
	f := newCodex(t)
	event := host.MustDumps(completedEv([]any{M{"type": "message", "role": "assistant", "content": []any{M{"type": "refusal", "refusal": "declined"}}}}), host.DumpOpts{})
	event = strings.Replace(event, `, "type":`, ",\ndata: \"type\":", 1)
	if !strings.Contains(event, "\ndata: ") {
		t.Fatal("event was not split")
	}
	data := []byte(": keepalive\n\nevent: response.completed\ndata: " + event + "\n\n")
	eq(t, message(t, f.run(data, nil, "size", 1))["content"].([]any)[0].(M)["text"], "declined")
}

func TestMissingAndInvalidAuthWithoutNetwork(t *testing.T) {
	f := newCodex(t)
	for _, auth := range []M{{}, {"tokens": M{"access_token": codexSecret}}, {"tokens": M{"access_token": codexSecret, "account_id": "bad\r\nheader"}},
		{"tokens": M{"access_token": codexSecret, "account_id": codexSecret}}} {
		writeAuth(t, f.dir, auth)
		_, err := collect(f.iter(reqBody(false)))
		codexErr(t, err, "auth_missing")
	}
	os.Remove(filepath.Join(f.dir, "auth.json"))
	_, err := collect(f.iter(reqBody(false)))
	codexErr(t, err, "auth_missing")
	eq(t, len(f.requests), 0)
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return codexSecret }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestExpiredAuthAndConnectionErrorsRedacted(t *testing.T) {
	f := newCodex(t)
	writeAuth(t, f.dir, M{"tokens": M{"access_token": jwt(`{"exp": 1}`), "account_id": codexAccount}})
	_, err := collect(f.iter(reqBody(false)))
	codexErr(t, err, "auth_expired")
	writeAuth(t, f.dir, M{"tokens": M{"access_token": codexSecret, "account_id": codexAccount}})
	for _, c := range []struct {
		err  error
		code string
	}{{errors.New(codexSecret), "upstream_error"}, {timeoutErr{}, "deadline_exceeded"}} {
		f.err = c.err
		_, err := collect(f.iter(reqBody(false)))
		if e := codexErr(t, err, c.code); strings.Contains(e.Error(), codexSecret) {
			t.Error("secret in error")
		}
	}
}

func TestBoundsDeadlineAndFinalValidation(t *testing.T) {
	f := newCodex(t)
	old := MaxOutput
	MaxOutput = 10
	f.runErr("output_too_large", wire(completedEv([]any{textItem(hello)})), nil)
	MaxOutput = old
	old = MaxEvent
	MaxEvent = 10
	f.runErr("output_too_large", []byte("data: "+strings.Repeat("x", 30)), nil)
	MaxEvent = old
	for _, timeout := range []float64{0, -1, math.Inf(1), math.NaN()} {
		_, err := collect(IterMessages(reqBody(false), f.dir, "synthetic-codex", timeout))
		codexErr(t, err, "invalid_request")
	}
	for _, event := range []M{completedEv(nil, "usage", M{"input_tokens": true, "output_tokens": 1}),
		doneEv(M{"type": "web_search_call"}, 0), doneEv(textItem(hello), 2)} {
		f.runErr("upstream_protocol", wire(event), nil)
	}
	f.runErr("upstream_protocol", wire(doneEv(textItem(hello), 0), completedEv([]any{textItem("changed")})), nil)
}

func TestCallerPanicPropagates(t *testing.T) {
	f := newCodex(t)
	f.respond(wire(completedEv([]any{textItem(hello)})))
	defer func() {
		if recover() != "caller" {
			t.Error("panic swallowed")
		}
		if !f.body.closed {
			t.Error("left open")
		}
	}()
	for range f.iter(reqBody(true)) {
		panic("caller")
	}
}

func fakeCLI(t *testing.T, script string) string {
	path := filepath.Join(t.TempDir(), "fake-codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func versionErr(t *testing.T, executable string) {
	t.Helper()
	defer func() {
		e, ok := recover().(*CodexError)
		if !ok || e.Code != "cli_unavailable" {
			t.Errorf("want cli_unavailable, got %v", e)
		}
	}()
	cliVersion(executable, 2*time.Second)
}

func TestVersionOnlyCommandCached(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs")
	exe := fakeCLI(t, `[ "$1" = "--version" ] && [ "$#" = 1 ] || exit 1
echo run >> `+marker+`
printf "codex-cli 1.2.3-alpha.1\n"`)
	eq(t, cliVersion(exe, 2*time.Second), "1.2.3-alpha.1")
	eq(t, cliVersion(exe, 2*time.Second), "1.2.3-alpha.1")
	runs, _ := os.ReadFile(marker)
	eq(t, string(runs), "run\n")
}

func TestVersionMissingTimeoutAndBoundedOutputSafe(t *testing.T) {
	for _, script := range []string{`echo "secret unknown"`, `while :; do printf "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"; done`, `exec sleep 5`, `echo "codex 1.2.3"; exit 1`} {
		start := time.Now()
		versionErr(t, fakeCLI(t, script))
		if time.Since(start) > 3*time.Second {
			t.Error(script, "too slow")
		}
	}
	versionErr(t, filepath.Join(t.TempDir(), "missing"))
}

// TestCodexGolden compares whole outputs with testdata/codex.json, generated from the Python module.
func TestCodexGolden(t *testing.T) {
	golden := loadGolden(t, "codex.json")
	for i, raw := range golden["translate"].([]any) {
		c := raw.(M)
		out, err := TranslateRequest(c["in"], c["model"])
		if want, ok := c["error"]; ok {
			codexErr(t, err, want.(string))
		} else if err != nil {
			t.Errorf("translate %d: %v", i, err)
		} else {
			eq(t, embedded(out), embedded(c["out"]))
		}
	}
	f := newCodex(t)
	id := regexp.MustCompile(`msg_[0-9a-f]{32}`)
	for i, raw := range golden["iter"].([]any) {
		c := raw.(M)
		f.requests, f.payloads = nil, nil
		f.respond([]byte(c["wire"].(string)), "size", 1)
		chunks, err := collect(f.iter(reqBody(c["stream"].(bool))))
		got := []any{}
		for _, chunk := range chunks {
			text := string(chunk)
			if c["stream"].(bool) {
				_, text, _ = strings.Cut(text, "data: ")
			}
			v, perr := host.Loads(id.ReplaceAllString(text, "msg_X"))
			if perr != nil {
				t.Fatal(perr)
			}
			got = append(got, v)
		}
		eq(t, embedded(got), embedded(c["chunks"]))
		if want, ok := c["error"]; ok {
			codexErr(t, err, want.(string))
		} else if err != nil {
			t.Errorf("iter %d: %v", i, err)
		}
		sent, _ := host.Loads(f.payloads[0])
		eq(t, embedded(sent), embedded(c["request"]))
	}
}

// embedded parses JSON text held in "arguments" and "partial_json", whose key order differs
// (Go writes sorted keys, Python kept insertion order).
func embedded(v any) any {
	switch x := v.(type) {
	case []any:
		out := []any{}
		for _, item := range x {
			out = append(out, embedded(item))
		}
		return out
	case M:
		out := M{}
		for k, item := range x {
			if s, ok := item.(string); ok && (k == "arguments" || k == "partial_json") {
				if parsed, err := host.Loads(s); err == nil {
					item = parsed
				}
			}
			out[k] = embedded(item)
		}
		return out
	}
	return v
}
