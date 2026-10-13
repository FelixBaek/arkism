package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// Deterministic native requests are
// compared with goldens recorded from the Python bridge (testdata/google_goldens/google_bridge.json).

const (
	bridgePublic = "google/synthetic-group"
	bridgeActual = "google/gemini-synthetic-member"
	bridgeOwner  = "synthetic-owner"
)

// bridgeFresh empties the continuation state and fixes the monotonic clock at *clock.
func bridgeFresh(t *testing.T) *float64 {
	t.Helper()
	clock := 100.0
	savedMono, savedTurns, savedBytes, savedTTL := googleMono, bridgeMaxTurns, bridgeMaxBytes, bridgeTTL
	bridgeLock.Lock()
	bridgeTurns, bridgeOrder = map[[32]byte]*bridgeTurn{}, nil
	bridgeLock.Unlock()
	googleMono = func() float64 { return clock }
	t.Cleanup(func() {
		googleMono, bridgeMaxTurns, bridgeMaxBytes, bridgeTTL = savedMono, savedTurns, savedBytes, savedTTL
	})
	return &clock
}

func bridgeObj(t *testing.T, text string) host.Obj {
	t.Helper()
	return mustParse(t, text).(host.Obj)
}

func bridgeRequest(t *testing.T, extra string) host.Obj {
	body := `{"model":"google/synthetic-group","max_tokens":128,"messages":[{"role":"user","content":"질문"}]`
	if extra != "" {
		body += "," + extra
	}
	return bridgeObj(t, body+"}")
}

const bridgeTools = `"tools":[{"type":"function","function":{"name":"echo","description":"합성 도구","parameters":{"type":"object","properties":{"value":{"type":"string"}}}}},` +
	`{"type":"function","function":{"name":"lookup","description":"합성 도구","parameters":{"type":"object","properties":{"value":{"type":"string"}}}}}]`

// bridgeAnswer is a public message with deliberately different public/native ids and the
// native parts, with signed boundaries between tools.
func bridgeAnswer(t *testing.T) (host.Obj, []any) {
	message := bridgeObj(t, `{"role":"assistant","content":"첫 답변","tool_calls":[`+
		`{"id":"mggoogle_synthetic_0","type":"function","function":{"name":"echo","arguments":"{\"value\": \"한글\", \"inline_data\": {\"function_call\": [1, true, null]}}"}},`+
		`{"id":"mggoogle_synthetic_1","type":"function","function":{"name":"lookup","arguments":"{\"value\": \"둘\", \"nested\": {\"thought_signature\": \"ordinary-json-key\"}}"}}]}`)
	parts := mustParse(t, `[{"text":"비공개 합성 생각","thought":true,"thoughtSignature":"synthetic-thought"},`+
		`{"text":"첫 ","thoughtSignature":"synthetic-text-a"},{"thoughtSignature":"synthetic-boundary"},`+
		`{"text":"답변","thoughtSignature":"synthetic-text-b"},`+
		`{"functionCall":{"id":"native-a","name":"echo","args":{"value":"한글","inline_data":{"function_call":[1,true,null]}}},"thoughtSignature":"synthetic-tool-a"},`+
		`{"text":"도구 사이 합성 생각","thought":true,"thoughtSignature":"synthetic-middle"},`+
		`{"functionCall":{"id":"native-b","name":"lookup","args":{"value":"둘","nested":{"thought_signature":"ordinary-json-key"}}},"thoughtSignature":"synthetic-tool-b"}]`).([]any)
	return message, parts
}

// bridgeFollow appends message, tool results in reverse order and a user turn.
func bridgeFollow(body, message host.Obj, outputs ...string) host.Obj {
	if outputs == nil {
		outputs = []string{"하나", "둘"}
	}
	result := clone(body).(host.Obj)
	messages := append(get(result, "messages").([]any), clone(message))
	calls, _ := get(message, "tool_calls").([]any)
	for i := min(len(calls), len(outputs)) - 1; i >= 0; i-- {
		messages = append(messages, host.Obj{{Key: "role", Value: "tool"}, {Key: "tool_call_id", Value: get(calls[i], "id")},
			{Key: "content", Value: outputs[i]}})
	}
	messages = append(messages, host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: "계속"}})
	result.Set("messages", messages)
	return result
}

func bridgePrepare(t *testing.T, chat host.Obj, owner string) *googlePrepared {
	t.Helper()
	p, err := newGooglePrepared(chat, owner)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return p
}

func bridgeRemember(t *testing.T, chat host.Obj, owner string, message host.Obj, parts []any, model string) {
	t.Helper()
	if err := bridgePrepare(t, chat, owner).remember(message, parts, model); err != nil {
		t.Fatalf("remember: %v", err)
	}
}

// bridgeSeed registers the answer to body and returns the follow-up history.
func bridgeSeed(t *testing.T, body host.Obj, model, owner string) (host.Obj, host.Obj, []any) {
	t.Helper()
	message, parts := bridgeAnswer(t)
	bridgeRemember(t, body, owner, message, parts, model)
	return bridgeFollow(body, message), message, parts
}

func wantGoogle(t *testing.T, err error, code string, status int) *GoogleError {
	t.Helper()
	var gerr *GoogleError
	if !errors.As(err, &gerr) || gerr.Code != code || gerr.Status != status || gerr.Error() != code {
		t.Fatalf("got %#v, want %s %d", err, code, status)
	}
	return gerr
}

func bridgeSameJSON(t *testing.T, got, want any) {
	t.Helper()
	opts := host.DumpOpts{NoASCII: true, SortKeys: true}
	if g, w := host.MustDumps(got, opts), host.MustDumps(want, opts); g != w {
		t.Fatalf("\n got %s\nwant %s", g, w)
	}
}

func modelParts(native host.Obj, i int) any {
	return get(get(native, "contents").([]any)[i], "parts")
}

func TestBridgeGoldens(t *testing.T) {
	raw, err := os.ReadFile("testdata/google_goldens/google_bridge.json")
	if err != nil {
		t.Fatal(err)
	}
	var scripts []struct {
		Name  string
		Steps []struct {
			Op, Owner, Model, Result string
			Chat, Message, Parts     string
			Native                   string
			Pinned                   *string
			ModelOut                 *string `json:"model_out"`
		}
	}
	if err := json.Unmarshal(raw, &scripts); err != nil {
		t.Fatal(err)
	}
	for _, script := range scripts {
		t.Run(script.Name, func(t *testing.T) {
			bridgeFresh(t)
			for i, step := range script.Steps {
				chat := mustParse(t, step.Chat).(host.Obj)
				var err error
				var got string
				switch step.Op {
				case "continuation":
					var model string
					model, err = bridgeContinuationModel(chat, step.Owner)
					if err == nil && model != bridgeOptional(step.ModelOut) {
						t.Fatalf("step %d: model %q", i, model)
					}
				default:
					var p *googlePrepared
					if p, err = newGooglePrepared(chat, step.Owner); err == nil {
						if step.Op == "prepare" {
							got = host.MustDumps(p.native, host.DumpOpts{NoASCII: true})
							if p.pinned != bridgeOptional(step.Pinned) {
								t.Fatalf("step %d: pinned %q", i, p.pinned)
							}
						} else {
							err = p.remember(mustParse(t, step.Message).(host.Obj), mustParse(t, step.Parts).([]any), step.Model)
						}
					}
				}
				result := "ok"
				var gerr *GoogleError
				var code Code
				switch {
				case errors.As(err, &gerr):
					result = fmt.Sprintf("%s:%d:%t", gerr.Code, gerr.Status, gerr.Started)
				case errors.As(err, &code):
					result = "code:" + string(code)
				case err != nil:
					t.Fatalf("step %d: %v", i, err)
				}
				if result != step.Result {
					t.Fatalf("step %d %s: %s, want %s", i, step.Op, result, step.Result)
				}
				if got != step.Native {
					t.Fatalf("step %d native\n got %s\nwant %s", i, got, step.Native)
				}
			}
		})
	}
}

func TestBridgeSamePublicTextCannotReplaceModelOrSignature(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, "")
	message := bridgeObj(t, `{"role":"assistant","content":"same"}`)
	parts := mustParse(t, `[{"text":"same","thoughtSignature":"first-signature"}]`).([]any)
	prepared := bridgePrepare(t, body, bridgeOwner)
	if err := prepared.remember(message, parts, bridgeActual); err != nil {
		t.Fatal(err)
	}
	continued := bridgeFollow(body, message)
	for _, c := range []struct {
		parts []any
		model string
	}{{parts, "google/other"}, {mustParse(t, `[{"text":"same","thoughtSignature":"second-signature"}]`).([]any), bridgeActual}} {
		err := prepared.remember(message, c.parts, c.model)
		if gerr := wantGoogle(t, err, "conversation_collision", 409); !gerr.Started {
			t.Fatal("collision must be marked started")
		}
		restored := bridgePrepare(t, continued, bridgeOwner)
		if restored.pinned != bridgeActual {
			t.Fatal(restored.pinned)
		}
		bridgeSameJSON(t, modelParts(restored.native, 1), parts)
	}
	if err := prepared.remember(message, parts, bridgeActual); err != nil || len(bridgeTurns) != 1 {
		t.Fatal(err, len(bridgeTurns))
	}
}

func TestBridgeNativeIsIndependentOfInput(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, `"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object","required":["a"]}}}],`+
		`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}]`)
	before := host.MustDumps(body, host.DumpOpts{})
	p := bridgePrepare(t, body, bridgeOwner)
	if p.pinned != "" {
		t.Fatal(p.pinned)
	}
	schema := get(get(get(p.native, "tools").([]any)[0], "functionDeclarations").([]any)[0], "parametersJsonSchema").(host.Obj)
	schema.Set("required", append(get(schema, "required").([]any), "x"))
	get(modelParts(p.native, 0).([]any)[0], "inlineData").(host.Obj)[1].Value = "changed"
	if host.MustDumps(body, host.DumpOpts{}) != before {
		t.Fatal("input mutated")
	}
}

func TestBridgeAllBase64MediaTypes(t *testing.T) {
	bridgeFresh(t)
	for _, media := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		body := bridgeRequest(t, `"stop":"끝","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:`+media+`;base64,YQ=="}}]}]`)
		native := bridgePrepare(t, body, bridgeOwner).native
		bridgeSameJSON(t, get(get(native, "generationConfig"), "stopSequences"), []any{"끝"})
		bridgeSameJSON(t, modelParts(native, 0), mustParse(t, `[{"inlineData":{"mimeType":"`+media+`","data":"YQ=="}}]`))
	}
}

func TestBridgeUnsupportedRequestsLeaveInputAndCache(t *testing.T) {
	bridgeFresh(t)
	extras := []string{`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]`,
		`"response_format":{"type":"json_object"}`, `"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{}}}`,
		`"top_k":3`}
	for _, choice := range []string{"", `,"tool_choice":"auto"`, `,"tool_choice":"required"`, `,"tool_choice":{"type":"function","function":{"name":"echo"}}`} {
		extras = append(extras, bridgeTools+`,"parallel_tool_calls":false`+choice)
	}
	for _, extra := range extras {
		body := bridgeRequest(t, extra)
		before := host.MustDumps(body, host.DumpOpts{})
		_, err := newGooglePrepared(body, bridgeOwner)
		var gerr *GoogleError
		if !errors.As(err, &gerr) || gerr.Status != 400 || gerr.Started {
			t.Fatalf("%s: %v", extra, err)
		}
		if strings.Contains(extra, "response_format") && gerr.Code != "unsupported_field" {
			t.Fatal(gerr.Code)
		}
		if host.MustDumps(body, host.DumpOpts{}) != before || len(bridgeTurns) != 0 {
			t.Fatal("input or cache changed")
		}
	}
	// The gateway calls continuation lookup with the original Chat body, so it tolerates the key.
	if model, err := bridgeContinuationModel(bridgeRequest(t, `"response_format":{"type":"json_object"}`), bridgeOwner); model != "" || err != nil {
		t.Fatal(model, err)
	}
}

func TestBridgePreparationDoesNotRegisterUncommittedResponse(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, bridgeTools)
	message, _ := bridgeAnswer(t)
	bridgePrepare(t, body, bridgeOwner)
	if model, err := bridgeContinuationModel(body, bridgeOwner); model != "" || err != nil || len(bridgeTurns) != 0 {
		t.Fatal(model, err)
	}
	_, err := newGooglePrepared(bridgeFollow(body, message), bridgeOwner)
	wantGoogle(t, err, "conversation_mismatch", 409)
}

func googleAnswerPage(t *testing.T, parts []any, usage string) iter2 {
	page := host.Obj{{Key: "candidates", Value: []any{host.Obj{{Key: "content", Value: host.Obj{{Key: "role", Value: "model"},
		{Key: "parts", Value: parts}}}, {Key: "finishReason", Value: "STOP"}}}}, {Key: "usageMetadata", Value: mustParse(t, usage)}}
	return iter2{page: page}
}

// iter2 yields one page, then runs eof (or fails with err) at native EOF.
type iter2 struct {
	page host.Obj
	eof  func()
	err  error
}

func (p iter2) seq(yield func(any, error) bool) {
	if !yield(p.page, nil) {
		return
	}
	if p.err != nil {
		yield(nil, p.err)
		return
	}
	if p.eof != nil {
		p.eof()
	}
}

func TestBridgeResponseCompletionRegistersOnlyAfterNativeEOF(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		bridgeFresh(t)
		body := bridgeRequest(t, bridgeTools)
		prepared := bridgePrepare(t, body, bridgeOwner)
		_, parts := bridgeAnswer(t)
		eof := false
		pages := googleAnswerPage(t, parts, `{"promptTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":1,"totalTokenCount":7}`)
		pages.eof = func() {
			if len(bridgeTurns) != 0 {
				t.Fatal("registered before EOF")
			}
			eof = true
		}
		var committed []host.Obj
		remember := func(message host.Obj, originals []any) {
			if !eof {
				t.Fatal("callback before EOF")
			}
			bridgeSameJSON(t, originals, parts)
			if err := prepared.remember(message, originals, bridgeActual); err != nil {
				t.Fatal(err)
			}
			committed = append(committed, clone(message).(host.Obj))
		}
		var err error
		if streaming {
			first := true
			err = GoogleStream(pages.seq, bridgePublic, remember, func([]byte) error {
				if first && len(bridgeTurns) != 0 {
					t.Fatal("registered before the first frame")
				}
				first = false
				return nil
			})
		} else {
			_, err = GoogleResponse(pages.seq, bridgePublic, remember)
		}
		if err != nil || len(committed) != 1 {
			t.Fatal(err, len(committed))
		}
		history := bridgeFollow(body, committed[0])
		if model, err := bridgeContinuationModel(history, bridgeOwner); model != bridgeActual || err != nil {
			t.Fatal(model, err)
		}
		bridgeSameJSON(t, modelParts(bridgePrepare(t, history, bridgeOwner).native, 1), parts)
	}
}

func TestBridgeNativeEOFFailureLeavesNoRecord(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		bridgeFresh(t)
		prepared := bridgePrepare(t, bridgeRequest(t, ""), bridgeOwner)
		pages := googleAnswerPage(t, mustParse(t, `[{"text":"답변"}]`).([]any), `{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}`)
		pages.err = errors.New("synthetic EOF failure")
		called := false
		callback := func(message host.Obj, parts []any) { called = true; prepared.remember(message, parts, bridgeActual) }
		var err error
		if streaming {
			err = GoogleStream(pages.seq, bridgePublic, callback, func([]byte) error { return nil })
		} else {
			_, err = GoogleResponse(pages.seq, bridgePublic, callback)
		}
		wantCode(t, err, "upstream_stream_error")
		if called || len(bridgeTurns) != 0 {
			t.Fatal("remembered a failed response")
		}
	}
}

func TestBridgeOwnedCalls(t *testing.T) {
	for _, block := range []string{`{"type":"tool_use","id":"mggoogle_fixture"}`, `{"type":"tool_result","tool_use_id":"mggoogle_fixture"}`} {
		if !bridgeOwnedCalls(mustParse(t, `[{"role":"user","content":[`+block+`]}]`)) {
			t.Fatal(block)
		}
	}
	for _, messages := range []string{`[]`, `[null,{},{"content":null}]`, `[{"content":"mggoogle_fixture"}]`,
		`[{"content":[{"type":"text","text":"mggoogle_fixture"}]}]`, `[{"content":[{"type":"tool_use","id":"other_fixture"}]}]`,
		`[{"tool_calls":[{"id":"mggoogle_fixture"}]}]`, `[{"content":[{"id":null,"tool_use_id":"mggoogle_fixture"}]}]`, `{}`} {
		if bridgeOwnedCalls(mustParse(t, messages)) {
			t.Fatal(messages)
		}
	}
}

func TestBridgeRestoresSignedPartsAndNativeIDs(t *testing.T) {
	bridgeFresh(t)
	history, _, original := bridgeSeed(t, bridgeRequest(t, bridgeTools), bridgeActual, bridgeOwner)
	before := host.MustDumps(history, host.DumpOpts{})
	if model, err := bridgeContinuationModel(history, bridgeOwner); model != bridgeActual || err != nil {
		t.Fatal(model, err)
	}
	p := bridgePrepare(t, history, bridgeOwner)
	bridgeSameJSON(t, get(p.native, "contents").([]any)[1], host.Obj{{Key: "role", Value: "model"}, {Key: "parts", Value: original}})
	bridgeSameJSON(t, get(p.native, "contents").([]any)[2], mustParse(t, `{"role":"user","parts":[`+
		`{"functionResponse":{"name":"lookup","id":"native-b","response":{"output":"둘"}}},`+
		`{"functionResponse":{"name":"echo","id":"native-a","response":{"output":"하나"}}},{"text":"계속"}]}`))
	if host.MustDumps(history, host.DumpOpts{}) != before {
		t.Fatal("history mutated")
	}
}

func TestBridgeToolResultOutputIsExactString(t *testing.T) {
	for _, output := range []string{" {\"output\":false,\"id\":\"x\",\"nested\":[1,null]}\n", "[1,2]", "null", "true", "17", "{broken-json", "", "한글 🌱"} {
		bridgeFresh(t)
		body := bridgeRequest(t, bridgeTools)
		message, parts := bridgeAnswer(t)
		bridgeRemember(t, body, bridgeOwner, message, parts, bridgeActual)
		contents := get(bridgePrepare(t, bridgeFollow(body, message, output, output), bridgeOwner).native, "contents").([]any)
		for _, part := range get(contents[len(contents)-1], "parts").([]any)[:2] {
			bridgeSameJSON(t, get(get(part, "functionResponse"), "response"), host.Obj{{Key: "output", Value: output}})
		}
	}
}

func TestBridgeMessagesResultTextBlocksConcatenate(t *testing.T) {
	bridgeFresh(t)
	history, _, _ := bridgeSeed(t, bridgeRequest(t, bridgeTools), bridgeActual, bridgeOwner)
	messages, err := CodecToMessages(history)
	if err != nil {
		t.Fatal(err)
	}
	list := get(messages, "messages").([]any)
	result := get(list[len(list)-1], "content").([]any)[0].(host.Obj)
	result.Set("content", mustParse(t, `[{"type":"text","text":"{\"x\":"},{"type":"text","text":"1}\n"}]`))
	get(list[len(list)-1], "content").([]any)[0] = result
	chat, err := MessagesToChat(messages)
	if err != nil {
		t.Fatal(err)
	}
	contents := get(bridgePrepare(t, chat, bridgeOwner).native, "contents").([]any)
	bridgeSameJSON(t, get(get(get(contents[len(contents)-1], "parts").([]any)[0], "functionResponse"), "response"),
		host.Obj{{Key: "output", Value: "{\"x\":1}\n"}})
}

func bridgeCompletion(message host.Obj) host.Obj {
	reason := "stop"
	if truthy(get(message, "tool_calls")) {
		reason = "tool_calls"
	}
	return host.Obj{{Key: "id", Value: "chatcmpl_synthetic"}, {Key: "object", Value: "chat.completion"}, {Key: "created", Value: 1},
		{Key: "model", Value: bridgePublic}, {Key: "choices", Value: []any{host.Obj{{Key: "index", Value: 0},
			{Key: "message", Value: clone(message)}, {Key: "finish_reason", Value: reason}}}},
		{Key: "usage", Value: host.Obj{{Key: "prompt_tokens", Value: 4}, {Key: "completion_tokens", Value: 2}, {Key: "total_tokens", Value: 6}}}}
}

func TestBridgeChatAndMessagesRoundtripShareCacheKey(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, bridgeTools+`,"tool_choice":"auto","messages":[{"role":"system","content":"규칙"},{"role":"user","content":"질문"}]`)
	message, parts := bridgeAnswer(t)
	bridgeRemember(t, body, bridgeOwner, message, parts, bridgeActual)
	public, err := MessagesResponse(bridgeCompletion(message), bridgePublic)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := CodecToMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	messages.Set("tool_choice", host.Obj{{Key: "type", Value: "auto"}, {Key: "disable_parallel_tool_use", Value: false}})
	calls := get(message, "tool_calls").([]any)
	messages.Set("messages", append(get(messages, "messages").([]any),
		host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: get(public, "content")}},
		host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: []any{
			host.Obj{{Key: "type", Value: "tool_result"}, {Key: "tool_use_id", Value: get(calls[1], "id")}, {Key: "content", Value: "둘"}},
			host.Obj{{Key: "type", Value: "tool_result"}, {Key: "tool_use_id", Value: get(calls[0], "id")}, {Key: "content", Value: "하나"}},
			host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: "계속"}}}}}))
	before := host.MustDumps(messages, host.DumpOpts{})
	converted, err := MessagesToChat(messages)
	if err != nil {
		t.Fatal(err)
	}
	bridgeSameJSON(t, bridgePrepare(t, converted, bridgeOwner).native, bridgePrepare(t, bridgeFollow(body, message), bridgeOwner).native)
	if model, err := bridgeContinuationModel(converted, bridgeOwner); model != bridgeActual || err != nil {
		t.Fatal(model, err)
	}
	if host.MustDumps(messages, host.DumpOpts{}) != before {
		t.Fatal("messages mutated")
	}
	encoded := host.MustDumps(public, host.DumpOpts{NoASCII: true})
	if strings.Contains(encoded, "synthetic-thought") || strings.Contains(encoded, "비공개 합성 생각") {
		t.Fatal("hidden thought leaked")
	}
}

func TestBridgeChangedContextRejectedWithoutInputMutation(t *testing.T) {
	bridgeFresh(t)
	history, _, _ := bridgeSeed(t, bridgeRequest(t, bridgeTools+`,"messages":[{"role":"system","content":"규칙"},{"role":"user","content":"질문"}]`), bridgeActual, bridgeOwner)
	edits := map[string]func(host.Obj){
		"system":      func(b host.Obj) { get(b, "messages").([]any)[0].(host.Obj)[1].Value = "변경" },
		"schema":      func(b host.Obj) { setPath(b, []any{"tools", 0, "function", "parameters"}, "required", []any{"value"}) },
		"description": func(b host.Obj) { setPath(b, []any{"tools", 0, "function"}, "description", "변경") },
		"tool_choice": nil,
		"user":        func(b host.Obj) { get(b, "messages").([]any)[1].(host.Obj)[1].Value = "변경" },
		"assistant":   func(b host.Obj) { get(b, "messages").([]any)[2].(host.Obj)[1].Value = "변경" },
		"arguments": func(b host.Obj) {
			setPath(b, []any{"messages", 2, "tool_calls", 0, "function"}, "arguments", `{"value":"변경"}`)
		},
		"tool_name": func(b host.Obj) { setPath(b, []any{"messages", 2, "tool_calls", 0, "function"}, "name", "lookup") },
		"tool_id": func(b host.Obj) {
			setPath(b, []any{"messages", 2, "tool_calls", 0}, "id", "mggoogle_unknown")
			setPath(b, []any{"messages", 4}, "tool_call_id", "mggoogle_unknown")
		},
		"tool_order": func(b host.Obj) {
			calls := get(get(b, "messages").([]any)[2], "tool_calls").([]any)
			calls[0], calls[1] = calls[1], calls[0]
		},
	}
	for name, edit := range edits {
		bad := clone(history).(host.Obj)
		if name == "tool_choice" {
			bad.Set("tool_choice", "required") // a new key grows the slice, so set it on bad itself
		} else {
			edit(bad)
		}
		before := host.MustDumps(bad, host.DumpOpts{})
		_, err := newGooglePrepared(bad, bridgeOwner)
		wantGoogle(t, err, "conversation_mismatch", 409)
		_, err = bridgeContinuationModel(bad, bridgeOwner)
		wantGoogle(t, err, "conversation_mismatch", 409)
		if host.MustDumps(bad, host.DumpOpts{}) != before {
			t.Fatal(name, "mutated")
		}
	}
}

// bridgeOptional is "" for a Python None.
func bridgeOptional(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// setPath sets key on the object reached by path (object keys and list indexes) in place.
func setPath(node any, path []any, key string, value any) any {
	if len(path) == 0 {
		o := node.(host.Obj)
		o.Set(key, value)
		return o
	}
	if i, ok := path[0].(int); ok {
		list := node.([]any)
		list[i] = setPath(list[i], path[1:], key, value)
		return list
	}
	o := node.(host.Obj)
	o.Set(path[0].(string), setPath(get(o, path[0].(string)), path[1:], key, value))
	return o
}

func TestBridgeOwnersKeepTheirOwnParts(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, bridgeTools)
	history, message, parts := bridgeSeed(t, body, bridgeActual, bridgeOwner)
	_, err := newGooglePrepared(history, "other-owner")
	wantGoogle(t, err, "conversation_mismatch", 409)
	_, err = bridgeContinuationModel(history, "other-owner")
	wantGoogle(t, err, "conversation_mismatch", 409)
	other := clone(parts).([]any)
	setPath(other, []any{0}, "thoughtSignature", "other-owner-signature")
	bridgeRemember(t, body, "other-owner", message, other, "google/other-member")
	bridgeSameJSON(t, modelParts(bridgePrepare(t, history, bridgeOwner).native, 1), parts)
	bridgeSameJSON(t, modelParts(bridgePrepare(t, history, "other-owner").native, 1), other)
	if m, _ := bridgeContinuationModel(history, "other-owner"); m != "google/other-member" {
		t.Fatal(m)
	}
}

func TestBridgeDistinctPublicModelsKeepSeparateState(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, bridgeTools)
	history, message, parts := bridgeSeed(t, body, bridgeActual, bridgeOwner)
	otherBody := clone(body).(host.Obj)
	otherBody.Set("model", "google/other-public-model")
	otherParts := clone(parts).([]any)
	setPath(otherParts, []any{0}, "thoughtSignature", "other-model-signature")
	bridgeRemember(t, otherBody, bridgeOwner, message, otherParts, "google/other-member")
	otherHistory := bridgeFollow(otherBody, message)
	if m, _ := bridgeContinuationModel(history, bridgeOwner); m != bridgeActual {
		t.Fatal(m)
	}
	bridgeSameJSON(t, modelParts(bridgePrepare(t, history, bridgeOwner).native, 1), parts)
	if m, _ := bridgeContinuationModel(otherHistory, bridgeOwner); m != "google/other-member" {
		t.Fatal(m)
	}
	bridgeSameJSON(t, modelParts(bridgePrepare(t, otherHistory, bridgeOwner).native, 1), otherParts)
}

func TestBridgeRestartLosesState(t *testing.T) {
	clock := bridgeFresh(t)
	_ = clock
	history, _, _ := bridgeSeed(t, bridgeRequest(t, bridgeTools), bridgeActual, bridgeOwner)
	bridgeFresh(t)
	_, err := newGooglePrepared(history, bridgeOwner)
	wantGoogle(t, err, "conversation_mismatch", 409)
	_, err = bridgeContinuationModel(history, bridgeOwner)
	wantGoogle(t, err, "conversation_mismatch", 409)
}

// bridgeActions are the two lookups that refresh a turn: preparation and continuation lookup.
var bridgeActions = map[string]func(host.Obj, string) error{
	"prepare":      func(c host.Obj, o string) error { _, err := newGooglePrepared(c, o); return err },
	"continuation": func(c host.Obj, o string) error { _, err := bridgeContinuationModel(c, o); return err },
}

func TestBridgeExpiryBoundary(t *testing.T) {
	for name, action := range bridgeActions {
		clock := bridgeFresh(t)
		history, _, _ := bridgeSeed(t, bridgeRequest(t, bridgeTools), bridgeActual, bridgeOwner)
		*clock += bridgeTTL
		wantGoogle(t, action(history, bridgeOwner), "conversation_mismatch", 409)
		if len(bridgeTurns) != 0 {
			t.Fatal(name, "expired turn kept")
		}
	}
}

func TestBridgeLookupRefreshesInactivityTTL(t *testing.T) {
	for name, action := range bridgeActions {
		clock := bridgeFresh(t)
		history, _, _ := bridgeSeed(t, bridgeRequest(t, bridgeTools), bridgeActual, bridgeOwner)
		*clock += bridgeTTL - 1
		if err := action(history, bridgeOwner); err != nil {
			t.Fatal(name, err)
		}
		*clock += bridgeTTL - 1
		if m, err := bridgeContinuationModel(history, bridgeOwner); m != bridgeActual || err != nil {
			t.Fatal(name, m, err)
		}
		*clock += bridgeTTL
		wantGoogle(t, action(history, bridgeOwner), "conversation_mismatch", 409)
	}
}

func bridgeUserBody(t *testing.T, text string) host.Obj {
	return bridgeRequest(t, bridgeTools+`,"messages":[{"role":"user","content":"`+text+`"}]`)
}

func TestBridgeCountLimitEvictsLeastRecentlyUsed(t *testing.T) {
	for name, action := range bridgeActions {
		bridgeFresh(t)
		bridgeMaxTurns = 2
		first, _, _ := bridgeSeed(t, bridgeUserBody(t, "첫째"), bridgeActual, bridgeOwner)
		second, _, _ := bridgeSeed(t, bridgeUserBody(t, "둘째"), bridgeActual, bridgeOwner)
		if err := action(first, bridgeOwner); err != nil {
			t.Fatal(name, err)
		}
		third, _, _ := bridgeSeed(t, bridgeUserBody(t, "셋째"), bridgeActual, bridgeOwner)
		if len(bridgeTurns) != 2 {
			t.Fatal(len(bridgeTurns))
		}
		for _, kept := range []host.Obj{first, third} {
			if m, _ := bridgeContinuationModel(kept, bridgeOwner); m != bridgeActual {
				t.Fatal(name, m)
			}
		}
		for _, action := range bridgeActions {
			wantGoogle(t, action(second, bridgeOwner), "conversation_mismatch", 409)
		}
	}
}

func TestBridgeRegisteringPrunesExpired(t *testing.T) {
	clock := bridgeFresh(t)
	first, _, _ := bridgeSeed(t, bridgeRequest(t, bridgeTools), bridgeActual, bridgeOwner)
	*clock += bridgeTTL
	second, _, _ := bridgeSeed(t, bridgeUserBody(t, "새 질문"), bridgeActual, bridgeOwner)
	if len(bridgeTurns) != 1 {
		t.Fatal(len(bridgeTurns))
	}
	if m, _ := bridgeContinuationModel(second, bridgeOwner); m != bridgeActual {
		t.Fatal(m)
	}
	_, err := newGooglePrepared(first, bridgeOwner)
	wantGoogle(t, err, "conversation_mismatch", 409)
}

func bridgePartsSize(t *testing.T) int {
	_, parts := bridgeAnswer(t)
	return len(host.MustDumps(parts, host.DumpOpts{Compact: true, NoASCII: true}))
}

func TestBridgeByteLimitCountsUTF8AndEvictsOlder(t *testing.T) {
	bridgeFresh(t)
	bridgeMaxBytes = 2*bridgePartsSize(t) - 1
	first, _, _ := bridgeSeed(t, bridgeUserBody(t, "첫째"), bridgeActual, bridgeOwner)
	second, _, _ := bridgeSeed(t, bridgeUserBody(t, "둘째"), bridgeActual, bridgeOwner)
	if len(bridgeTurns) != 1 {
		t.Fatal(len(bridgeTurns))
	}
	if m, _ := bridgeContinuationModel(second, bridgeOwner); m != bridgeActual {
		t.Fatal(m)
	}
	_, err := newGooglePrepared(first, bridgeOwner)
	wantGoogle(t, err, "conversation_mismatch", 409)
}

func TestBridgeExactCapacityAndOversizedTurnKeepsCache(t *testing.T) {
	bridgeFresh(t)
	bridgeMaxBytes = bridgePartsSize(t)
	message, parts := bridgeAnswer(t)
	history, _, _ := bridgeSeed(t, bridgeRequest(t, bridgeTools), bridgeActual, bridgeOwner)
	oversized := clone(parts).([]any)
	setPath(oversized, []any{0}, "text", get(oversized[0], "text").(string)+"🌱")
	err := bridgePrepare(t, bridgeUserBody(t, "커다란 응답"), bridgeOwner).remember(message, oversized, bridgeActual)
	if gerr := wantGoogle(t, err, "continuation_capacity", 503); !gerr.Started {
		t.Fatal("capacity must be marked started")
	}
	if m, _ := bridgeContinuationModel(history, bridgeOwner); m != bridgeActual || len(bridgeTurns) != 1 {
		t.Fatal(m, len(bridgeTurns))
	}
}

func TestBridgeReplacementDoesNotDoubleCountBytes(t *testing.T) {
	bridgeFresh(t)
	bridgeMaxBytes = bridgePartsSize(t)
	body := bridgeRequest(t, bridgeTools)
	message, parts := bridgeAnswer(t)
	prepared := bridgePrepare(t, body, bridgeOwner)
	for range 3 {
		if err := prepared.remember(message, parts, bridgeActual); err != nil {
			t.Fatal(err)
		}
	}
	if len(bridgeTurns) != 1 {
		t.Fatal(len(bridgeTurns))
	}
	bridgeSameJSON(t, modelParts(bridgePrepare(t, bridgeFollow(body, message), bridgeOwner).native, 1), parts)
}

func TestBridgeCacheDefaults(t *testing.T) {
	if bridgeMaxTurns != 1024 || bridgeMaxBytes != 16<<20 || bridgeTTL != 900 {
		t.Fatal(bridgeMaxTurns, bridgeMaxBytes, bridgeTTL)
	}
}

func TestBridgeRememberAndLookupCopyParts(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, bridgeTools)
	message, parts := bridgeAnswer(t)
	original := host.MustDumps(parts, host.DumpOpts{})
	bridgePrepare(t, body, bridgeOwner).remember(message, parts, bridgeActual)
	history := bridgeFollow(body, message)
	setPath(parts, []any{0}, "thoughtSignature", "mutated")
	get(parts[4], "functionCall").(host.Obj)[2].Value = host.Obj{}
	native := bridgePrepare(t, history, bridgeOwner).native
	if host.MustDumps(modelParts(native, 1), host.DumpOpts{}) != original {
		t.Fatal("stored parts follow caller mutation")
	}
	restored := modelParts(native, 1).([]any)
	restored[0], restored[1] = restored[1], restored[0]
	if host.MustDumps(modelParts(bridgePrepare(t, history, bridgeOwner).native, 1), host.DumpOpts{}) != original {
		t.Fatal("stored parts follow native mutation")
	}
}

func TestBridgePlainTextMessagesRoundtripAndExpiry(t *testing.T) {
	clock := bridgeFresh(t)
	body := bridgeRequest(t, "")
	message := bridgeObj(t, `{"role":"assistant","content":"답변"}`)
	parts := mustParse(t, `[{"text":"답","thoughtSignature":"synthetic-signed-text"},{"text":"변"}]`).([]any)
	bridgeRemember(t, body, bridgeOwner, message, parts, bridgeActual)
	public, err := MessagesResponse(bridgeCompletion(message), bridgePublic)
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := CodecToMessages(body)
	messages.Set("messages", append(get(messages, "messages").([]any),
		host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: get(public, "content")}},
		host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: "계속"}}))
	converted, err := MessagesToChat(messages)
	if err != nil {
		t.Fatal(err)
	}
	bridgeSameJSON(t, modelParts(bridgePrepare(t, converted, bridgeOwner).native, 1), parts)
	*clock += bridgeTTL
	_, err = newGooglePrepared(converted, bridgeOwner)
	wantGoogle(t, err, "conversation_mismatch", 409)
}

func TestBridgePlainTextContextAndOwnerChanges(t *testing.T) {
	bridgeFresh(t)
	body := bridgeRequest(t, `"messages":[{"role":"system","content":"규칙"},{"role":"user","content":"질문"}]`)
	message := bridgeObj(t, `{"role":"assistant","content":"답변"}`)
	bridgeRemember(t, body, bridgeOwner, message, mustParse(t, `[{"text":"답변","thoughtSignature":"synthetic"}]`).([]any), bridgeActual)
	history := bridgeFollow(body, message)
	_, err := newGooglePrepared(history, "other-owner")
	wantGoogle(t, err, "conversation_mismatch", 409)
	for index := range 3 {
		bad := clone(history).(host.Obj)
		setPath(bad, []any{"messages", index}, "content", "변경")
		_, err := newGooglePrepared(bad, bridgeOwner)
		wantGoogle(t, err, "conversation_mismatch", 409)
	}
}
