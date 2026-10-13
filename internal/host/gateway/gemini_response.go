package gateway

import (
	"iter"
	"slices"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Validated Chat output -> Gemini generateContent.

var geminiReasons = map[any]string{"stop": "STOP", "tool_calls": "STOP", "length": "MAX_TOKENS",
	"content_filter": "SAFETY"}

// geminiUsage reuses the Responses usage bounds; native already checked the core counts.
func geminiUsage(value any) host.Obj {
	checked := responsesUsage(value)
	candidates, _ := host.Int(get(checked, "output_tokens"))
	result := host.Obj{{Key: "promptTokenCount", Value: get(checked, "input_tokens")},
		{Key: "candidatesTokenCount", Value: get(checked, "output_tokens")}, {Key: "totalTokenCount", Value: get(checked, "total_tokens")}}
	if reasoning := get(get(checked, "output_tokens_details"), "reasoning_tokens"); reasoning != nil {
		n, _ := host.Int(reasoning)
		result.Set("candidatesTokenCount", candidates-n)
		result.Set("thoughtsTokenCount", reasoning)
	}
	if cached := get(get(checked, "input_tokens_details"), "cached_tokens"); cached != nil {
		result.Set("cachedContentTokenCount", cached)
	}
	return result
}

func geminiTexts(source any) []any {
	parts := []any{}
	for _, key := range []string{"content", "refusal"} {
		if v := get(source, key); v != nil {
			parts = append(parts, host.Obj{{Key: "text", Value: v}})
		}
	}
	return parts
}

func geminiFunction(identity, name any, args string) host.Obj {
	if len(args) > MaxToolArgumentBytes {
		fail("tool_arguments_too_large")
	}
	return host.Obj{{Key: "functionCall", Value: host.Obj{{Key: "id", Value: identity}, {Key: "name", Value: name},
		{Key: "args", Value: arguments(args)}}}}
}

func geminiResult(identity any, parts []any, reason any, usage host.Obj) host.Obj {
	if len(parts) == 0 {
		// Empty parts make the Google SDK discard this turn from chat history.
		parts = []any{host.Obj{{Key: "text", Value: ""}}}
	}
	candidate := host.Obj{{Key: "index", Value: 0}, {Key: "content", Value: host.Obj{{Key: "role", Value: "model"}, {Key: "parts", Value: parts}}}}
	if reason != nil {
		candidate.Set("finishReason", geminiReasons[reason])
	}
	result := host.Obj{{Key: "candidates", Value: []any{candidate}}, {Key: "responseId", Value: identity}}
	if usage != nil {
		result.Set("usageMetadata", usage)
	}
	return result
}

// GeminiResponse returns independent Gemini JSON after the shared Chat validation (response).
func GeminiResponse(body any, model string) (out host.Obj, err error) {
	defer catch(&err)
	str(model, "invalid_response", true)
	validated, err := NativeResponse(body, model)
	rethrow(err)
	usage := geminiUsage(get(validated, "usage"))
	choice := get(validated, "choices").([]any)[0]
	message := get(choice, "message")
	parts := geminiTexts(message)
	calls, _ := get(message, "tool_calls").([]any)
	if len(calls) > MaxToolCalls {
		fail("invalid_response")
	}
	for _, call := range calls {
		function := get(call, "function")
		parts = append(parts, geminiFunction(get(call, "id"), get(function, "name"), get(function, "arguments").(string)))
	}
	return geminiResult(get(validated, "id"), parts, get(choice, "finish_reason"), usage), nil
}

// GeminiStream emits text deltas and releases tools, finish and usage only after a validated EOF
// (stream).
func GeminiStream(chunks iter.Seq2[[]byte, error], model string, emit func([]byte) error) (err error) {
	defer catch(&err)
	yield := emitter(emit)
	str(model, "invalid_response", true)
	var identity, reason any
	var usage host.Obj
	calls := map[int64]*messagesCall{}
	send := func(parts []any, finish any, counts host.Obj) {
		yield([]byte("data: " + jsonText(geminiResult(identity, parts, finish, counts), "invalid_stream") + "\n\n"))
	}
	// shortcut: native holds finish-frame text until EOF; change native for earlier delivery.
	nativeStream(chunks, model, true, func(frame []byte) {
		if string(frame) == "data: [DONE]\n\n" {
			return
		}
		body, _ := Parse(string(frame[6:]))
		identity = get(body, "id")
		var checked host.Obj
		if v := get(body, "usage"); v != nil {
			checked = geminiUsage(v)
		}
		choices := get(body, "choices").([]any)
		if len(choices) == 0 {
			usage = checked
			return
		}
		choice := choices[0]
		delta := get(choice, "delta")
		if parts := geminiTexts(delta); len(parts) > 0 {
			send(parts, nil, nil)
		}
		tools, _ := get(delta, "tool_calls").([]any)
		for _, call := range tools {
			index, _ := host.Int(get(call, "index"))
			function := get(call, "function").(host.Obj)
			if calls[index] == nil {
				calls[index] = &messagesCall{id: get(call, "id"), name: get(function, "name")}
			}
			calls[index].parts = append(calls[index].parts, getd(function, "arguments", "").(string))
		}
		if v := get(choice, "finish_reason"); v != nil {
			reason, usage = v, checked
		}
	})
	if usage == nil {
		fail("invalid_usage")
	}
	indices := make([]int64, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	parts := []any{}
	for _, index := range indices {
		call := calls[index]
		parts = append(parts, geminiFunction(call.id, call.name, strings.Join(call.parts, "")))
	}
	send(parts, reason, usage)
	return nil
}
