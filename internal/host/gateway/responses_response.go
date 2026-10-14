package gateway

import (
	"iter"
	"slices"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Validated Chat -> stateless Responses.

var responsesSettings = []string{"model", "store", "metadata", "instructions", "tools", "tool_choice",
	"parallel_tool_calls", "temperature", "top_p", "max_output_tokens",
	"text", "truncation", "background", "previous_response_id", "conversation"}

var incomplete = map[any]string{"length": "max_output_tokens", "content_filter": "content_filter"}

func responsesID(prefix string) string { return prefix + newHex() }

// responsesUsage maps native-validated usage; the details need the same bounds.
func responsesUsage(value any) host.Obj {
	if value == nil {
		fail("invalid_usage")
	}
	result := host.Obj{{Key: "input_tokens", Value: get(value, "prompt_tokens")},
		{Key: "output_tokens", Value: get(value, "completion_tokens")}, {Key: "total_tokens", Value: get(value, "total_tokens")}}
	for _, m := range []struct {
		source, target, count string
		fields                []string
	}{
		{"prompt_tokens_details", "input_tokens_details", "prompt_tokens", []string{"cached_tokens", "cache_write_tokens"}},
		{"completion_tokens_details", "output_tokens_details", "completion_tokens", []string{"reasoning_tokens"}},
	} {
		if get(value, m.source) == nil {
			continue
		}
		details, ok := get(value, m.source).(host.Obj)
		if !ok {
			fail("invalid_usage")
		}
		limit, _ := host.Int(get(value, m.count))
		for _, kv := range details {
			if kv.Value != nil && integer(kv.Value, "invalid_usage", 0) > limit {
				fail("invalid_usage")
			}
		}
		mapped := host.Obj{}
		for _, key := range m.fields {
			if v := get(details, key); v != nil {
				mapped.Set(key, v)
			}
		}
		if len(mapped) > 0 {
			result.Set(m.target, mapped)
		}
	}
	return result
}

func responsesBase(request host.Obj, created any) host.Obj {
	result := host.Obj{{Key: "parallel_tool_calls", Value: true}, {Key: "tool_choice", Value: "auto"}, {Key: "tools", Value: []any{}}}
	for _, key := range responsesSettings {
		if v, ok := request.Get(key); ok {
			result.Set(key, clone(v))
		}
	}
	for _, kv := range []host.KV{{Key: "id", Value: responsesID("resp_")}, {Key: "object", Value: "response"},
		{Key: "created_at", Value: created}, {Key: "status", Value: "in_progress"}, {Key: "error", Value: nil},
		{Key: "incomplete_details", Value: nil}, {Key: "output", Value: []any{}}, {Key: "usage", Value: nil}} {
		result.Set(kv.Key, kv.Value)
	}
	return result
}

func responsesFinish(result host.Obj, reason any, usage host.Obj) {
	result.Set("usage", usage)
	status := "completed"
	if detail, ok := incomplete[reason]; ok {
		status = "incomplete"
		result.Set("incomplete_details", host.Obj{{Key: "reason", Value: detail}})
	}
	result.Set("status", status)
	for _, item := range get(result, "output").([]any) {
		o := item.(host.Obj)
		if get(o, "type") == "function_call" {
			o.Set("status", "completed")
		} else {
			o.Set("status", status)
		}
	}
}

func responsesMessage() host.Obj {
	return host.Obj{{Key: "id", Value: responsesID("msg_")}, {Key: "type", Value: "message"}, {Key: "status", Value: "in_progress"},
		{Key: "role", Value: "assistant"}, {Key: "content", Value: []any{}}}
}

func responsesPart(key string, value any) host.Obj {
	if key == "content" {
		return host.Obj{{Key: "type", Value: "output_text"}, {Key: "text", Value: value}, {Key: "annotations", Value: []any{}}, {Key: "logprobs", Value: []any{}}}
	}
	return host.Obj{{Key: "type", Value: "refusal"}, {Key: "refusal", Value: value}}
}

func responsesFunction(callID, name any, arguments string) host.Obj {
	if len(arguments) > MaxToolArgumentBytes {
		fail("tool_arguments_too_large")
	}
	return host.Obj{{Key: "id", Value: responsesID("fc_")}, {Key: "type", Value: "function_call"}, {Key: "status", Value: "in_progress"},
		{Key: "call_id", Value: callID}, {Key: "name", Value: name}, {Key: "arguments", Value: arguments}}
}

// appendOutput appends to result's output list in place (the key always exists).
func appendOutput(result, item host.Obj) {
	result.Set("output", append(get(result, "output").([]any), item))
}

// ResponsesResponse converts native-validated Chat JSON; request was validated by the caller
// (response).
func ResponsesResponse(body any, request host.Obj) (out host.Obj, err error) {
	defer catch(&err)
	model, _ := get(request, "model").(string)
	validated, err := NativeResponse(body, model)
	rethrow(err)
	usage := responsesUsage(get(validated, "usage"))
	choice := get(validated, "choices").([]any)[0]
	source := get(choice, "message").(host.Obj)
	result := responsesBase(request, get(validated, "created"))
	parts := []any{}
	for _, key := range []string{"content", "refusal"} {
		if v := get(source, key); v != nil {
			parts = append(parts, responsesPart(key, v))
		}
	}
	if len(parts) > 0 {
		message := responsesMessage()
		message.Set("content", parts)
		appendOutput(result, message)
	}
	calls, _ := get(source, "tool_calls").([]any)
	for _, call := range calls {
		function := get(call, "function")
		appendOutput(result, responsesFunction(get(call, "id"), get(function, "name"), get(function, "arguments").(string)))
	}
	responsesFinish(result, get(choice, "finish_reason"), usage)
	return result, nil
}

type responsesText struct {
	key    string
	chunks []string
}

// ResponsesStream streams validated text and releases tools and done events only after a real
// EOF with usage (stream).
func ResponsesStream(chunks iter.Seq2[[]byte, error], request host.Obj, emit func([]byte) error) (err error) {
	defer catch(&err)
	yield := emitter(emit)
	var result, message, usage host.Obj
	var parts []*responsesText
	calls := map[int64]*messagesCall{}
	var reason any
	sequence := 0
	event := func(kind string, fields ...host.KV) {
		payload := append(host.Obj{{Key: "type", Value: kind}, {Key: "sequence_number", Value: sequence}}, fields...)
		sequence++
		yield([]byte("event: " + kind + "\ndata: " + jsonText(payload, "invalid_stream") + "\n\n"))
	}
	part := func(key string) *responsesText {
		for _, p := range parts {
			if p.key == key {
				return p
			}
		}
		return nil
	}
	model, _ := get(request, "model").(string)
	// shortcut: native holds finish-frame text until EOF; adjust native for early terminal text.
	nativeStream(chunks, model, true, func(frame []byte) {
		if string(frame) == "data: [DONE]\n\n" {
			return
		}
		// Native emits whole JSON frames and owns index, identity and argument validation.
		body, _ := Parse(string(frame[6:]))
		var checked host.Obj
		if v := get(body, "usage"); v != nil {
			checked = responsesUsage(v)
		}
		if result == nil {
			result = responsesBase(request, get(body, "created"))
			event("response.created", host.KV{Key: "response", Value: result})
			event("response.in_progress", host.KV{Key: "response", Value: result})
		}
		choices := get(body, "choices").([]any)
		if len(choices) == 0 {
			usage = checked
			return
		}
		choice := choices[0]
		delta := get(choice, "delta")
		for _, key := range []string{"content", "refusal"} {
			v := get(delta, key)
			if v == nil {
				continue
			}
			// Opening blanks allocate nothing; an explicit terminal blank keeps the assistant turn.
			if !truthy(v) && part(key) == nil && get(choice, "finish_reason") == nil {
				continue
			}
			if message == nil {
				message = responsesMessage()
				appendOutput(result, message)
				event("response.output_item.added", host.KV{Key: "output_index", Value: 0}, host.KV{Key: "item", Value: message})
			}
			state := part(key)
			if state == nil {
				state = &responsesText{key: key}
				parts = append(parts, state)
				event("response.content_part.added", host.KV{Key: "item_id", Value: get(message, "id")}, host.KV{Key: "output_index", Value: 0},
					host.KV{Key: "content_index", Value: len(parts) - 1}, host.KV{Key: "part", Value: responsesPart(key, "")})
			}
			state.chunks = append(state.chunks, v.(string))
			fields := []host.KV{{Key: "item_id", Value: get(message, "id")}, {Key: "output_index", Value: 0},
				{Key: "content_index", Value: slices.Index(parts, state)}, {Key: "delta", Value: v}}
			kind := "response.refusal.delta"
			if key == "content" {
				fields = append(fields, host.KV{Key: "logprobs", Value: []any{}})
				kind = "response.output_text.delta"
			}
			event(kind, fields...)
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

	// Native has exhausted the upstream, checked DONE and validated the final usage.
	if usage == nil {
		fail("invalid_usage")
	}
	if message != nil {
		content := []any{}
		for _, p := range parts {
			content = append(content, responsesPart(p.key, strings.Join(p.chunks, "")))
		}
		message.Set("content", content)
	}
	indices := make([]int64, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		call := calls[index]
		appendOutput(result, responsesFunction(call.id, call.name, strings.Join(call.parts, "")))
	}
	responsesFinish(result, reason, usage)

	if message != nil {
		for i, p := range get(message, "content").([]any) {
			fields := []host.KV{{Key: "item_id", Value: get(message, "id")}, {Key: "output_index", Value: 0}, {Key: "content_index", Value: i}}
			if get(p, "type") == "output_text" {
				event("response.output_text.done", append(fields, host.KV{Key: "text", Value: get(p, "text")}, host.KV{Key: "logprobs", Value: []any{}})...)
			} else {
				event("response.refusal.done", append(fields, host.KV{Key: "refusal", Value: get(p, "refusal")})...)
			}
			event("response.content_part.done", append(fields, host.KV{Key: "part", Value: p})...)
		}
		event("response.output_item.done", host.KV{Key: "output_index", Value: 0}, host.KV{Key: "item", Value: message})
	}
	for i, item := range get(result, "output").([]any) {
		if get(item, "type") != "function_call" {
			continue
		}
		added := slices.Clone(item.(host.Obj))
		added.Set("status", "in_progress")
		added.Set("arguments", "")
		event("response.output_item.added", host.KV{Key: "output_index", Value: i}, host.KV{Key: "item", Value: added})
		fields := []host.KV{{Key: "item_id", Value: get(item, "id")}, {Key: "output_index", Value: i}}
		event("response.function_call_arguments.delta", append(slices.Clone(fields), host.KV{Key: "delta", Value: get(item, "arguments")})...)
		event("response.function_call_arguments.done", append(slices.Clone(fields), host.KV{Key: "arguments", Value: get(item, "arguments")})...)
		event("response.output_item.done", host.KV{Key: "output_index", Value: i}, host.KV{Key: "item", Value: item})
	}
	event("response."+get(result, "status").(string), host.KV{Key: "response", Value: result})
	return nil
}
