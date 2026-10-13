package gateway

import (
	"iter"
	"slices"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Validated Chat responses -> Anthropic Messages.

var messagesReasons = map[any]string{"stop": "end_turn", "length": "max_tokens",
	"tool_calls": "tool_use", "content_filter": "refusal"}

// messagesUsage adds Messages cache accounting; native already validated the core counts.
func messagesUsage(value any) host.Obj {
	if value == nil {
		fail("invalid_usage")
	}
	details := get(value, "prompt_tokens_details")
	if details == nil {
		details = host.Obj{}
	}
	if _, ok := details.(host.Obj); !ok {
		fail("invalid_usage")
	}
	cached := integer(getd(details.(host.Obj), "cached_tokens", 0), "invalid_usage", 0)
	prompt, _ := host.Int(get(value, "prompt_tokens"))
	completion, _ := host.Int(get(value, "completion_tokens"))
	if cached > prompt {
		fail("invalid_usage")
	}
	return host.Obj{{Key: "input_tokens", Value: prompt - cached}, {Key: "output_tokens", Value: completion},
		{Key: "cache_read_input_tokens", Value: cached}}
}

func messagesMessage(identity any, model string, content []any, reason any, usage host.Obj) host.Obj {
	return host.Obj{{Key: "id", Value: identity}, {Key: "type", Value: "message"}, {Key: "role", Value: "assistant"},
		{Key: "model", Value: model}, {Key: "content", Value: content}, {Key: "stop_reason", Value: reason},
		{Key: "stop_sequence", Value: nil}, {Key: "usage", Value: usage}}
}

func messagesSSE(kind string, fields ...host.KV) []byte {
	body := append(host.Obj{{Key: "type", Value: kind}}, fields...)
	return []byte("event: " + kind + "\ndata: " + jsonText(body, "invalid_stream") + "\n\n")
}

// MessagesResponse reuses native validation and returns an independent Messages response
// (response).
func MessagesResponse(body any, model string) (out host.Obj, err error) {
	defer catch(&err)
	str(model, "invalid_response", true)
	validated, err := NativeResponse(body, model)
	rethrow(err)
	usage := messagesUsage(get(validated, "usage"))
	choice := get(validated, "choices").([]any)[0]
	message := get(choice, "message").(host.Obj)
	content := []any{}
	for _, key := range []string{"content", "refusal"} {
		if v := get(message, key); v != nil {
			content = append(content, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: v}})
		}
	}
	calls, _ := get(message, "tool_calls").([]any)
	for _, call := range calls {
		function := get(call, "function")
		input, _ := Parse(get(function, "arguments").(string))
		content = append(content, host.Obj{{Key: "type", Value: "tool_use"}, {Key: "id", Value: get(call, "id")},
			{Key: "name", Value: get(function, "name")}, {Key: "input", Value: input}})
	}
	return messagesMessage(get(validated, "id"), model, content, messagesReasons[get(choice, "finish_reason")], usage), nil
}

type messagesCall struct {
	id, name any
	parts    []string
}

// MessagesStream emits text immediately but tools and completion only after a validated EOF
// (stream).
func MessagesStream(chunks iter.Seq2[[]byte, error], model string, emit func([]byte) error) (err error) {
	defer catch(&err)
	yield := emitter(emit)
	str(model, "invalid_response", true)
	started, textStarted := false, false
	calls := map[int64]*messagesCall{}
	var reason, usage any
	nativeStream(chunks, model, true, func(frame []byte) {
		if string(frame) == "data: [DONE]\n\n" {
			return
		}
		// native emits complete, validated Chat JSON frames, even for fragmented input.
		body, _ := Parse(string(frame[6:]))
		if !started {
			started = true
			yield(messagesSSE("message_start", host.KV{Key: "message", Value: messagesMessage(get(body, "id"), model, []any{}, nil,
				host.Obj{{Key: "input_tokens", Value: 0}, {Key: "output_tokens", Value: 0}, {Key: "cache_read_input_tokens", Value: 0}})}))
		}
		choices := get(body, "choices").([]any)
		if len(choices) == 0 {
			usage = get(body, "usage")
			return
		}
		choice := choices[0]
		delta := get(choice, "delta")
		tools, _ := get(delta, "tool_calls").([]any)
		finished := get(choice, "finish_reason") != nil
		for _, key := range []string{"content", "refusal"} {
			v := get(delta, key)
			if v != nil && (truthy(v) || finished && len(calls) == 0 && len(tools) == 0) {
				if !textStarted {
					textStarted = true
					yield(messagesSSE("content_block_start", host.KV{Key: "index", Value: 0},
						host.KV{Key: "content_block", Value: host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: ""}}}))
				}
				yield(messagesSSE("content_block_delta", host.KV{Key: "index", Value: 0},
					host.KV{Key: "delta", Value: host.Obj{{Key: "type", Value: "text_delta"}, {Key: "text", Value: v}}}))
			}
		}
		for _, call := range tools {
			index, _ := host.Int(get(call, "index"))
			function := get(call, "function").(host.Obj)
			if calls[index] == nil {
				calls[index] = &messagesCall{id: get(call, "id"), name: get(function, "name")}
			}
			calls[index].parts = append(calls[index].parts, getd(function, "arguments", "").(string))
		}
		if finished {
			reason = messagesReasons[get(choice, "finish_reason")]
			usage = get(body, "usage")
		}
	})
	finalUsage := messagesUsage(usage)
	if textStarted {
		yield(messagesSSE("content_block_stop", host.KV{Key: "index", Value: 0}))
	}
	indices := make([]int64, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	offset := int64(0)
	if textStarted {
		offset = 1
	}
	for _, index := range indices {
		call := calls[index]
		block := index + offset
		yield(messagesSSE("content_block_start", host.KV{Key: "index", Value: block},
			host.KV{Key: "content_block", Value: host.Obj{{Key: "type", Value: "tool_use"}, {Key: "id", Value: call.id},
				{Key: "name", Value: call.name}, {Key: "input", Value: host.Obj{}}}}))
		yield(messagesSSE("content_block_delta", host.KV{Key: "index", Value: block},
			host.KV{Key: "delta", Value: host.Obj{{Key: "type", Value: "input_json_delta"}, {Key: "partial_json", Value: strings.Join(call.parts, "")}}}))
		yield(messagesSSE("content_block_stop", host.KV{Key: "index", Value: block}))
	}
	yield(messagesSSE("message_delta", host.KV{Key: "delta", Value: host.Obj{{Key: "stop_reason", Value: reason}, {Key: "stop_sequence", Value: nil}}},
		host.KV{Key: "usage", Value: finalUsage}))
	yield(messagesSSE("message_stop"))
	return nil
}
