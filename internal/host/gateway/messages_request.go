package gateway

import (
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Portable Anthropic Messages request -> Chat request. Failures
// are invalid_request or unsupported_request only.

// attempt runs f and returns the code it failed with, if any.
func attempt(f func()) (code Code) {
	defer func() {
		if r := recover(); r != nil {
			c, ok := r.(Code)
			if !ok {
				panic(r)
			}
			code = c
		}
	}()
	f()
	return ""
}

// msgKeys is the module's _keys: unknown keys are unsupported_request; cache allows a valid
// cache_control hint, which is then dropped.
func msgKeys(value any, allowed map[string]bool, cache bool) host.Obj {
	o, ok := value.(host.Obj)
	if !ok {
		fail("invalid_request")
	}
	for _, kv := range o {
		if !allowed[kv.Key] && !(cache && kv.Key == "cache_control") {
			fail("unsupported_request")
		}
	}
	if hint, ok := o.Get("cache_control"); cache && ok {
		h := msgKeys(hint, set("type", "ttl"), false)
		if get(h, "type") != "ephemeral" {
			fail("invalid_request")
		}
		if ttl, ok := h.Get("ttl"); ok && ttl != "5m" && ttl != "1h" {
			fail("invalid_request")
		}
	}
	return o
}

func msgParts(value any, empty bool) []any {
	if s, ok := value.(string); ok {
		return []any{host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: s}}}
	}
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 && !empty {
		fail("invalid_request")
	}
	return parts
}

func msgText(block any) host.Obj {
	msgKeys(block, set("type", "text"), true)
	if get(block, "type") != "text" {
		str(get(block, "type"), "invalid_request", true)
		fail("unsupported_request")
	}
	return host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: str(get(block, "text"), "invalid_request", false)}}
}

func msgImage(block any) host.Obj {
	msgKeys(block, set("type", "source"), true)
	source, ok := get(block, "source").(host.Obj)
	if !ok {
		fail("invalid_request")
	}
	var url string
	switch kind := str(get(source, "type"), "invalid_request", true); kind {
	case "base64":
		msgKeys(source, set("type", "media_type", "data"), false)
		media := str(get(source, "media_type"), "invalid_request", true)
		if !set("image/png", "image/jpeg", "image/gif", "image/webp")[media] {
			fail("unsupported_request")
		}
		url = "data:" + media + ";base64," + str(get(source, "data"), "invalid_request", true)
	case "url":
		msgKeys(source, set("type", "url"), false)
		url = str(get(source, "url"), "invalid_request", true)
		if lower := strings.ToLower(url); !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			fail("invalid_request")
		}
	default:
		fail("unsupported_request")
	}
	part := host.Obj{{Key: "type", Value: "image_url"}, {Key: "image_url", Value: host.Obj{{Key: "url", Value: url}}}}
	if attempt(func() { content([]any{part}, true) }) != "" {
		fail("invalid_request")
	}
	return part
}

func msgResult(block any) any {
	b := msgKeys(block, set("type", "tool_use_id", "content", "is_error"), true)
	if v, ok := b.Get("is_error"); ok {
		if _, isBool := v.(bool); !isBool {
			fail("invalid_request")
		}
	}
	var result any = getd(b, "content", "")
	if _, ok := result.(string); !ok {
		texts := []any{}
		for _, part := range msgParts(result, true) {
			texts = append(texts, msgText(part))
		}
		result = texts
	}
	if get(b, "is_error") == true {
		// Prefix the original data without parsing or repacking JSON inside its text.
		marker := "[tool_result is_error=true]"
		if s, ok := result.(string); ok {
			result = marker + "\n" + s
		} else {
			result = append([]any{host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: marker}}}, result.([]any)...)
		}
	}
	if !truthy(result) {
		return ""
	}
	return result
}

// MessagesToChat converts a portable Anthropic request to Chat without mutating its input
// (to_chat).
func MessagesToChat(body any) (out host.Obj, err error) {
	defer catch(&err)
	if nesting(body, 0) > 2*maxNesting {
		fail("invalid_request")
	}
	jsonText(body, "invalid_request")
	b := msgKeys(body, set("model", "messages", "system", "tools", "tool_choice", "max_tokens",
		"temperature", "top_p", "stop_sequences", "stream"), false)
	out = host.Obj{{Key: "model", Value: str(get(b, "model"), "invalid_request", true)}}
	integer(get(b, "max_tokens"), "invalid_request", 1)
	out.Set("max_tokens", get(b, "max_tokens"))
	for _, key := range []string{"temperature", "top_p"} {
		if v, ok := b.Get(key); ok {
			if !isNumber(v) || !(number(v) >= 0 && number(v) <= 1) {
				fail("invalid_request")
			}
			out.Set(key, v)
		}
	}
	if v, ok := b.Get("stop_sequences"); ok {
		stops, ok := v.([]any)
		if !ok {
			fail("invalid_request")
		}
		sequences := make([]any, len(stops))
		for i, item := range stops {
			sequences[i] = str(item, "invalid_request", true)
		}
		out.Set("stop", sequences)
		if len(stops) > 4 {
			fail("unsupported_request")
		}
	}
	if v, ok := b.Get("stream"); ok {
		stream, ok := v.(bool)
		if !ok {
			fail("invalid_request")
		}
		out.Set("stream", stream)
		if stream {
			out.Set("stream_options", host.Obj{{Key: "include_usage", Value: true}})
		}
	}

	names := map[string]bool{}
	if v, ok := b.Get("tools"); ok {
		list, ok := v.([]any)
		if !ok {
			fail("invalid_request")
		}
		tools := []any{}
		for _, tool := range list {
			t := msgKeys(tool, set("type", "name", "description", "input_schema"), true)
			if getd(t, "type", "custom") != "custom" {
				str(get(t, "type"), "invalid_request", true)
				fail("unsupported_request")
			}
			toolName := name(get(t, "name"), "invalid_request")
			schema, ok := get(t, "input_schema").(host.Obj)
			if names[toolName] || !ok {
				fail("invalid_request")
			}
			names[toolName] = true
			function := host.Obj{{Key: "name", Value: toolName}, {Key: "parameters", Value: deepcopy(schema, "invalid_request")}}
			if description, ok := t.Get("description"); ok {
				function.Set("description", str(description, "invalid_request", false))
			}
			tools = append(tools, host.Obj{{Key: "type", Value: "function"}, {Key: "function", Value: function}})
		}
		out.Set("tools", tools)
	}
	if choice, ok := b.Get("tool_choice"); ok {
		c := msgKeys(choice, set("type", "name", "disable_parallel_tool_use"), false)
		switch kind := str(get(c, "type"), "invalid_request", true); kind {
		case "auto", "any", "none":
			if has(c, "name") || kind != "none" && len(names) == 0 {
				fail("invalid_request")
			}
			if kind == "any" {
				kind = "required"
			}
			out.Set("tool_choice", kind)
		case "tool":
			chosen := name(get(c, "name"), "invalid_request")
			if !names[chosen] {
				fail("invalid_request")
			}
			out.Set("tool_choice", host.Obj{{Key: "type", Value: "function"}, {Key: "function", Value: host.Obj{{Key: "name", Value: chosen}}}})
		default:
			fail("unsupported_request")
		}
		if v, ok := c.Get("disable_parallel_tool_use"); ok {
			disable, ok := v.(bool)
			if !ok {
				fail("invalid_request")
			}
			out.Set("parallel_tool_calls", !disable)
		}
	}

	converted := []any{}
	if system, ok := b.Get("system"); ok {
		value := system
		if _, isStr := system.(string); !isStr {
			texts := []any{}
			for _, block := range msgParts(system, true) {
				texts = append(texts, msgText(block))
			}
			value = texts
			if len(texts) == 0 {
				value = ""
			}
		}
		converted = append(converted, host.Obj{{Key: "role", Value: "system"}, {Key: "content", Value: value}})
	}
	messages, ok := get(b, "messages").([]any)
	if !ok || len(messages) == 0 {
		fail("invalid_request")
	}
	pending, seen := map[string]bool{}, map[string]bool{}
	for _, message := range messages {
		m := msgKeys(message, set("role", "content"), false)
		role := get(m, "role")
		if role != "user" && role != "assistant" || len(pending) > 0 && role != "user" {
			fail("invalid_request")
		}
		parts, calls := []any{}, []any{}
		for _, block := range msgParts(get(m, "content"), false) {
			if _, ok := block.(host.Obj); !ok {
				fail("invalid_request")
			}
			switch kind := str(get(block, "type"), "invalid_request", true); kind {
			case "tool_use":
				msgKeys(block, set("type", "id", "name", "input"), true)
				identity := str(get(block, "id"), "invalid_request", true)
				input, ok := get(block, "input").(host.Obj)
				if role != "assistant" || seen[identity] || !ok {
					fail("invalid_request")
				}
				toolName := name(get(block, "name"), "invalid_request")
				calls = append(calls, host.Obj{{Key: "id", Value: identity}, {Key: "type", Value: "function"},
					{Key: "function", Value: host.Obj{{Key: "name", Value: toolName}, {Key: "arguments", Value: jsonText(input, "invalid_request")}}}})
				pending[identity], seen[identity] = true, true
			case "tool_result":
				identity := str(get(block, "tool_use_id"), "invalid_request", true)
				if role != "user" || !pending[identity] || len(parts) > 0 {
					fail("invalid_request")
				}
				result := msgResult(block)
				delete(pending, identity)
				converted = append(converted, host.Obj{{Key: "role", Value: "tool"}, {Key: "tool_call_id", Value: identity}, {Key: "content", Value: result}})
			default:
				if len(pending) > 0 && role == "user" {
					fail("invalid_request")
				}
				var part host.Obj
				switch kind {
				case "text":
					part = msgText(block)
					if len(calls) > 0 {
						// Chat places content before tool_calls; never reorder later text.
						fail("unsupported_request")
					}
				case "image":
					if role != "user" {
						fail("unsupported_request")
					}
					part = msgImage(block)
				default:
					fail("unsupported_request")
				}
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 || len(calls) > 0 {
			item := host.Obj{{Key: "role", Value: role}, {Key: "content", Value: nil}}
			if len(parts) > 0 {
				item.Set("content", parts)
			}
			if len(calls) > 0 {
				item.Set("tool_calls", calls)
			}
			converted = append(converted, item)
		}
	}
	if len(pending) > 0 {
		fail("invalid_request")
	}
	out.Set("messages", converted)
	return out, nil
}
