package gateway

import (
	"iter"
	"slices"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Native Chat responses are validated without translating tool indices.

// MaxToolCalls bounds tool-call indices: clients often allocate by index, so sparse huge
// indices are refused.
var MaxToolCalls = 1024

func nativeUsage(value any) {
	o, ok := value.(host.Obj)
	if !ok {
		fail("invalid_usage")
	}
	var n [3]int64
	for i, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		n[i] = integer(get(o, key), "invalid_usage", 0)
	}
	if n[2] != n[0]+n[1] {
		fail("invalid_usage")
	}
}

func envelope(body any, streaming bool) host.Obj {
	o, ok := body.(host.Obj)
	if !ok || has(o, "error") {
		fail("invalid_response")
	}
	object := "chat.completion"
	if streaming {
		object = "chat.completion.chunk"
	}
	if get(o, "object") != object {
		fail("invalid_response")
	}
	jsonText(o, "invalid_response")
	str(get(o, "id"), "invalid_response", true)
	integer(get(o, "created"), "invalid_response", 0)
	if choices, ok := get(o, "choices").([]any); !ok || len(choices) > 1 {
		fail("invalid_response")
	}
	if usage := get(o, "usage"); usage != nil {
		nativeUsage(usage)
	}
	return o
}

func nativeReason(value any) {
	switch value {
	case "stop", "length", "tool_calls", "content_filter":
	default:
		fail("invalid_response")
	}
}

// zeroIndex is type(v) is int and v == 0.
func zeroIndex(v any) bool {
	n, ok := host.Int(v)
	return ok && n == 0
}

// NativeResponse validates a native Chat completion and returns an independent copy with
// model replaced and only the public message keys kept (response).
func NativeResponse(body any, model string) (out host.Obj, err error) {
	defer catch(&err)
	o := envelope(body, false)
	choices := get(o, "choices").([]any)
	if len(choices) != 1 {
		fail("invalid_response")
	}
	choice, ok := choices[0].(host.Obj)
	if !ok || !zeroIndex(get(choice, "index")) {
		fail("invalid_response")
	}
	nativeReason(get(choice, "finish_reason"))
	message, ok := get(choice, "message").(host.Obj)
	if !ok || get(message, "role") != "assistant" {
		fail("invalid_response")
	}
	for _, key := range []string{"content", "refusal"} {
		if v := get(message, key); v != nil {
			str(v, "invalid_response", false)
		}
	}
	calls, ok := getd(message, "tool_calls", []any{}).([]any)
	if getd(message, "tool_calls", nil) == nil {
		calls, ok = nil, true
	}
	if !ok {
		fail("invalid_response")
	}
	seen := map[string]bool{}
	for _, call := range calls {
		function, ok := get(call, "function").(host.Obj)
		if get(call, "type") != "function" || !ok {
			fail("invalid_response")
		}
		identity := str(get(call, "id"), "invalid_response", true)
		if seen[identity] {
			fail("invalid_response")
		}
		seen[identity] = true
		name(get(function, "name"), "invalid_response")
		arguments(get(function, "arguments"))
	}
	if get(choice, "finish_reason") == "tool_calls" && len(calls) == 0 {
		fail("invalid_response")
	}
	result := clone(o).(host.Obj)
	result.Set("model", model)
	kept := host.Obj{}
	for _, key := range []string{"role", "content", "tool_calls", "refusal", "annotations"} {
		if v, ok := message.Get(key); ok {
			kept.Set(key, clone(v))
		}
	}
	first := get(result, "choices").([]any)[0].(host.Obj)
	first.Set("message", kept)
	get(result, "choices").([]any)[0] = first
	return result, nil
}

type nativeCall struct {
	id, name string
	parts    []string
	size     int
}

// NativeStream validates native Chat SSE and re-emits it with model replaced; the terminal
// chunk, optional usage chunk and DONE are emitted only after a validated EOF (stream).
func NativeStream(chunks iter.Seq2[[]byte, error], model string, includeUsage bool, emit func([]byte) error) (err error) {
	defer catch(&err)
	nativeStream(chunks, model, includeUsage, emitter(emit))
	return nil
}

func nativeStream(chunks iter.Seq2[[]byte, error], model string, includeUsage bool, yield func([]byte)) {
	var identity any
	var terminal, usage host.Obj
	done := false
	calls := map[int64]*nativeCall{}
	ids := map[string]bool{}
	emit := func(body host.Obj) []byte {
		out := slices.Clone(body)
		out.Set("model", model)
		return []byte("data: " + jsonText(out, "invalid_stream") + "\n\n")
	}
	sseFrames(chunks, func(event, data string) {
		if done || event != "" && event != "message" {
			fail("invalid_stream")
		}
		if data == "[DONE]" {
			if terminal == nil {
				fail("truncated_stream")
			}
			done = true
			return
		}
		body := envelope(loads(data, "invalid_stream"), true)
		if identity == nil {
			identity = get(body, "id")
		} else if identity != get(body, "id") {
			fail("invalid_stream")
		}
		choices := get(body, "choices").([]any)
		if len(choices) == 0 {
			if terminal == nil || get(body, "usage") == nil || usage != nil {
				fail("invalid_stream")
			}
			usage = body
			return
		}
		if terminal != nil {
			fail("invalid_stream")
		}
		choice, ok := choices[0].(host.Obj)
		if !ok || !zeroIndex(get(choice, "index")) {
			fail("invalid_stream")
		}
		delta, ok := get(choice, "delta").(host.Obj)
		if role := get(delta, "role"); !ok || role != nil && role != "assistant" {
			fail("invalid_stream")
		}
		for _, key := range []string{"content", "refusal"} {
			if v := get(delta, key); v != nil {
				str(v, "invalid_stream", false)
			}
		}
		tools, ok := getd(delta, "tool_calls", []any{}).([]any)
		if getd(delta, "tool_calls", nil) == nil {
			tools, ok = nil, true
		}
		if !ok {
			fail("invalid_stream")
		}
		for _, call := range tools {
			function, ok := get(call, "function").(host.Obj)
			if !ok {
				fail("invalid_stream")
			}
			index := integer(get(call, "index"), "invalid_stream", 0)
			if index >= int64(MaxToolCalls) {
				fail("invalid_stream")
			}
			if calls[index] == nil {
				callID := str(get(call, "id"), "invalid_stream", true)
				if get(call, "type") != "function" || ids[callID] {
					fail("invalid_stream")
				}
				ids[callID] = true
				calls[index] = &nativeCall{id: callID, name: name(get(function, "name"), "invalid_stream")}
			}
			state := calls[index]
			if id := get(call, "id"); id != nil && id != state.id ||
				get(call, "type") != nil && get(call, "type") != "function" ||
				get(function, "name") != nil && get(function, "name") != state.name {
				fail("invalid_stream")
			}
			part := str(getd(function, "arguments", ""), "invalid_stream", false)
			state.size += len(part)
			if state.size > MaxToolArgumentBytes {
				fail("tool_arguments_too_large")
			}
			state.parts = append(state.parts, part)
		}
		kept := host.Obj{}
		for _, key := range []string{"role", "content", "tool_calls", "refusal"} {
			if v, ok := delta.Get(key); ok {
				kept.Set(key, v)
			}
		}
		choice.Set("delta", kept)
		choices[0] = choice
		if !includeUsage {
			body.Del("usage")
		}
		if reason := get(choice, "finish_reason"); reason != nil {
			nativeReason(reason)
			for i := range int64(len(calls)) {
				if calls[i] == nil {
					fail("invalid_stream")
				}
			}
			for i := range int64(len(calls)) {
				arguments(strings.Join(calls[i].parts, ""))
			}
			if reason == "tool_calls" && len(calls) == 0 {
				fail("invalid_stream")
			}
			terminal = body
		} else {
			yield(emit(body))
		}
	})
	if !done || terminal == nil {
		fail("truncated_stream")
	}
	if includeUsage && usage == nil && get(terminal, "usage") == nil {
		fail("invalid_usage")
	}
	yield(emit(terminal))
	if includeUsage && usage != nil {
		yield(emit(usage))
	}
	yield([]byte("data: [DONE]\n\n"))
}
