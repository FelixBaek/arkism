package gateway

import (
	"regexp"

	"github.com/FelixBaek/arkism/internal/host"
)

// Responses request -> Chat request.

func emptyArray(value any, code string) {
	list, ok := value.([]any)
	if !ok {
		fail(code)
	}
	if len(list) > 0 {
		fail("unsupported_field")
	}
}

func token(value any, code string, pattern *regexp.Regexp, limit int) string {
	s := str(value, code, true)
	if host.Len(s) > limit || !pattern.MatchString(s) {
		fail(code)
	}
	return s
}

var (
	includePattern  = regexp.MustCompile(`^[a-z0-9._-]+$`)
	cacheKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
)

// include accepts Codex's single string or the short list the Responses schema allows.
func include(value any) {
	items, ok := value.([]any)
	if s, isStr := value.(string); isStr {
		items, ok = []any{s}, true
	}
	if !ok || len(items) > 8 {
		fail("invalid_request")
	}
	for _, item := range items {
		token(item, "invalid_request", includePattern, 64)
	}
}

func reasoning(value any) {
	o := keys(value, set("effort", "summary"), "invalid_request")
	if v, ok := o.Get("effort"); ok && !set("minimal", "low", "medium", "high", "xhigh")[str(v, "invalid_request", false)] {
		fail("invalid_request")
	}
	if v, ok := o.Get("summary"); ok && !set("auto", "concise", "detailed")[str(v, "invalid_request", false)] {
		fail("invalid_request")
	}
}

// clientMetadata holds Codex turn metadata; one value is a JSON blob of about 700 characters,
// so the 512-character metadata limit does not apply.
func clientMetadata(value any) {
	o, ok := value.(host.Obj)
	if !ok || len(o) > 32 {
		fail("invalid_request")
	}
	for _, kv := range o {
		if host.Len(str(kv.Key, "invalid_request", true)) > 64 || host.Len(str(kv.Value, "invalid_request", true)) > 4096 {
			fail("invalid_request")
		}
	}
}

// textFormat builds the Chat response_format. Messages cannot express it, so it stays out of
// the final Messages self-check and a non-Chat target rejects the key.
func textFormat(text any) host.Obj {
	keys(text, set("format"), "invalid_request")
	format, ok := get(text, "format").(host.Obj)
	if !ok {
		fail("invalid_request")
	}
	kind := str(get(format, "type"), "invalid_request", true)
	if kind == "text" || kind == "json_object" {
		keys(format, set("type"), "invalid_request")
		if kind == "text" {
			return nil
		}
		return host.Obj{{Key: "type", Value: "json_object"}}
	}
	if kind != "json_schema" {
		fail("unsupported_field")
	}
	keys(format, set("type", "name", "schema", "strict"), "invalid_request")
	schemaName := name(get(format, "name"), "invalid_request")
	schema, ok := get(format, "schema").(host.Obj)
	if !ok {
		fail("invalid_request")
	}
	jsonShape(schema, 0)
	strict, ok := getd(format, "strict", false).(bool)
	if !ok {
		fail("invalid_request")
	}
	if strict {
		fail("unsupported_field")
	}
	// The caller's schema object is never shared, rewritten or normalized.
	return host.Obj{{Key: "type", Value: "json_schema"}, {Key: "json_schema", Value: host.Obj{
		{Key: "name", Value: schemaName}, {Key: "schema", Value: deepcopy(schema, "invalid_request")}, {Key: "strict", Value: false}}}}
}

func chatContent(value any, role string) any {
	if s, ok := value.(string); ok {
		return str(s, "invalid_content", false)
	}
	list, ok := value.([]any)
	if !ok {
		fail("invalid_content")
	}
	parts := []any{}
	for _, part := range list {
		p, ok := part.(host.Obj)
		if !ok {
			fail("invalid_content")
		}
		switch kind := str(get(p, "type"), "invalid_content", true); {
		case kind == "input_text" || kind == "output_text" && role == "assistant":
			allowed := set("type", "text")
			if kind == "output_text" {
				allowed["annotations"], allowed["logprobs"] = true, true
			}
			keys(p, allowed, "invalid_content")
			for _, key := range []string{"annotations", "logprobs"} {
				if v, ok := p.Get(key); ok {
					emptyArray(v, "invalid_content")
				}
			}
			parts = append(parts, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: str(get(p, "text"), "invalid_content", false)}})
		case kind == "refusal" && role == "assistant":
			keys(p, set("type", "refusal"), "invalid_content")
			parts = append(parts, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: str(get(p, "refusal"), "invalid_content", false)}})
		case kind == "input_image" && role == "user":
			keys(p, set("type", "image_url", "detail"), "invalid_image")
			image := host.Obj{{Key: "url", Value: str(get(p, "image_url"), "invalid_image", true)}}
			if detail, ok := p.Get("detail"); ok {
				image.Set("detail", str(detail, "invalid_image", true))
			}
			parts = append(parts, host.Obj{{Key: "type", Value: "image_url"}, {Key: "image_url", Value: image}})
		default:
			fail("unsupported_field")
		}
	}
	var result any = parts
	if len(parts) == 0 {
		result = ""
	}
	content(result, role == "user")
	return result
}

func history(item host.Obj, tool bool) {
	if id, ok := item.Get("id"); ok {
		// Historical item ids are metadata; only call_id identifies a Chat call.
		str(id, "invalid_request", true)
	}
	if v, ok := item.Get("status"); ok {
		status := str(v, "invalid_request", true)
		if !set("completed", "in_progress", "incomplete")[status] {
			fail("invalid_request")
		}
		if tool && status != "completed" || status == "in_progress" {
			fail("unsupported_field")
		}
	}
}

// functionKeys are carried by a Responses function tool and every function in a namespace.
var functionKeys = set("type", "name", "description", "parameters", "strict")

func functionTool(tool any) host.Obj {
	t := keys(tool, functionKeys, "invalid_tools")
	if str(get(t, "type"), "invalid_tools", true) != "function" {
		fail("unsupported_field")
	}
	strict, ok := t.Get("strict")
	if !ok {
		fail("unsupported_field")
	}
	if b, ok := strict.(bool); !ok {
		fail("invalid_tools")
	} else if b {
		fail("unsupported_field")
	}
	if _, ok := get(t, "parameters").(host.Obj); !ok {
		fail("invalid_tools")
	}
	function := host.Obj{}
	for _, kv := range t {
		if kv.Key != "type" {
			function = append(function, host.KV{Key: kv.Key, Value: deepcopy(kv.Value, "invalid_request")})
		}
	}
	return host.Obj{{Key: "type", Value: "function"}, {Key: "function", Value: function}}
}

// namespaceTools flattens a Codex tool group: Chat has no groups, so the children keep their own
// names and the group name and description are dropped.
func namespaceTools(tool any) []host.Obj {
	t := keys(tool, set("type", "name", "description", "tools"), "invalid_tools")
	name(get(t, "name"), "invalid_tools")
	if description, ok := t.Get("description"); ok {
		str(description, "invalid_tools", false)
	}
	children, ok := get(t, "tools").([]any)
	if !ok || len(children) < 1 || len(children) > 128 {
		fail("invalid_tools")
	}
	out := make([]host.Obj, len(children))
	for i, child := range children {
		out[i] = functionTool(child)
	}
	return out
}

// webSearchTools accepts only the no-external-access form: search runs outside this gateway.
func webSearchTools(tool any) []host.Obj {
	keys(tool, set("type", "external_web_access"), "invalid_tools")
	if get(tool, "external_web_access") != false {
		fail("unsupported_field")
	}
	return nil
}

var responsesKeys = set("model", "input", "instructions", "store", "tools", "tool_choice",
	"parallel_tool_calls", "temperature", "top_p", "max_output_tokens",
	"stream", "previous_response_id", "conversation", "background",
	"metadata", "include", "reasoning", "prompt_cache_key", "client_metadata",
	"text", "truncation")

// ResponsesToChat returns an independent, validated Chat request without changing body
// (to_chat).
func ResponsesToChat(body any) (out host.Obj, err error) {
	defer catch(&err)
	jsonText(body, "invalid_request")
	jsonShape(body, 0)
	b := keys(body, responsesKeys, "invalid_request")
	responseFormat := responsesDropped(b)

	// The local default is explicit so the response can report what was sent.
	out = host.Obj{{Key: "model", Value: str(get(b, "model"), "invalid_request", true)}, {Key: "parallel_tool_calls", Value: true}}
	for _, key := range []string{"parallel_tool_calls", "temperature", "top_p", "stream"} {
		if v, ok := b.Get(key); ok {
			if v == nil {
				fail("invalid_option")
			}
			out.Set(key, v)
		}
	}
	if v, ok := b.Get("max_output_tokens"); ok {
		if v == nil {
			fail("invalid_option")
		}
		out.Set("max_completion_tokens", v)
	}
	if get(b, "stream") == true {
		out.Set("stream_options", host.Obj{{Key: "include_usage", Value: true}})
	}
	if v, ok := b.Get("tools"); ok {
		out.Set("tools", responsesTools(v))
	}
	if choice, ok := b.Get("tool_choice"); ok {
		out.Set("tool_choice", responsesToolChoice(choice))
	}

	messages := []any{}
	if v, ok := b.Get("instructions"); ok {
		messages = append(messages, host.Obj{{Key: "role", Value: "system"}, {Key: "content", Value: str(v, "invalid_request", false)}})
	}
	switch items := get(b, "input").(type) {
	case string:
		messages = append(messages, host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: str(items, "invalid_content", false)}})
	case []any:
		for _, item := range items {
			messages = responsesItem(item, messages)
		}
		if len(items) == 0 {
			fail("invalid_messages")
		}
	default:
		fail("invalid_messages")
	}
	out.Set("messages", messages)
	// The self-check covers the Messages-convertible part; response_format is a Chat-only key
	// added afterwards, which a Messages target must keep rejecting.
	toMessages(out)
	if responseFormat != nil {
		out.Set("response_format", responseFormat)
	}
	return out, nil
}

// responsesDropped validates the fields that never reach Chat and returns the text format, the
// one of them that does.
func responsesDropped(b host.Obj) host.Obj {
	store, ok := b.Get("store")
	if !ok {
		fail("unsupported_field")
	}
	if s, ok := store.(bool); !ok {
		fail("invalid_request")
	} else if s {
		fail("unsupported_field")
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		if v := get(b, key); v != nil {
			_, isStr := v.(string)
			_, isObj := v.(host.Obj)
			if !isStr && !(key == "conversation" && isObj) {
				fail("invalid_request")
			}
			fail("unsupported_field")
		}
	}
	if v := get(b, "background"); v != nil {
		if background, ok := v.(bool); !ok {
			fail("invalid_request")
		} else if background {
			fail("unsupported_field")
		}
	}
	if v, ok := b.Get("metadata"); ok {
		responsesMetadata(v)
	}
	// Fields with no Chat equivalent are validated and then dropped, never forwarded.
	if v, ok := b.Get("include"); ok {
		include(v)
	}
	if v, ok := b.Get("reasoning"); ok {
		reasoning(v)
	}
	if v, ok := b.Get("prompt_cache_key"); ok {
		token(v, "invalid_request", cacheKeyPattern, 128)
	}
	if v, ok := b.Get("client_metadata"); ok {
		clientMetadata(v)
	}
	var responseFormat host.Obj
	if v, ok := b.Get("text"); ok {
		responseFormat = textFormat(v)
	}
	if v, ok := b.Get("truncation"); ok && str(v, "invalid_request", false) != "disabled" {
		fail("unsupported_field")
	}
	return responseFormat
}

func responsesMetadata(v any) {
	metadata, ok := v.(host.Obj)
	if !ok || len(metadata) > 16 {
		fail("invalid_request")
	}
	for _, kv := range metadata {
		if host.Len(str(kv.Key, "invalid_request", false)) > 64 || host.Len(str(kv.Value, "invalid_request", false)) > 512 {
			fail("invalid_request")
		}
	}
}

func responsesTools(v any) []any {
	list, ok := v.([]any)
	if !ok {
		fail("invalid_tools")
	}
	tools, names := []any{}, map[string]bool{}
	for _, tool := range list {
		t, ok := tool.(host.Obj)
		if !ok {
			fail("invalid_tools")
		}
		var flattened []host.Obj
		switch kind := get(t, "type"); kind {
		case "function":
			flattened = []host.Obj{functionTool(t)}
		case "namespace":
			flattened = namespaceTools(t)
		case "web_search":
			flattened = webSearchTools(t)
		default:
			// Other tool types keep the function key rule, then stay unsupported; a missing
			// or non-string type is a malformed tool.
			keys(t, functionKeys, "invalid_tools")
			str(kind, "invalid_tools", true)
			fail("unsupported_field")
		}
		for _, item := range flattened {
			// Flattened names must stay unique, or the model cannot tell the tools apart.
			toolName := name(get(get(item, "function"), "name"), "invalid_tools")
			if names[toolName] {
				fail("invalid_tools")
			}
			names[toolName] = true
			tools = append(tools, item)
		}
	}
	return tools
}

func responsesToolChoice(choice any) any {
	switch c := choice.(type) {
	case string:
		if !set("auto", "none", "required")[c] {
			fail("unsupported_field")
		}
		return c
	case host.Obj:
		keys(c, set("type", "name"), "invalid_tool_choice")
		if str(get(c, "type"), "invalid_tool_choice", true) != "function" {
			fail("unsupported_field")
		}
		return host.Obj{{Key: "type", Value: "function"}, {Key: "function", Value: host.Obj{
			{Key: "name", Value: str(get(c, "name"), "invalid_tool_choice", true)}}}}
	default:
		fail("invalid_tool_choice")
	}
	return nil
}

func responsesItem(item any, messages []any) []any {
	it, ok := item.(host.Obj)
	if !ok {
		fail("invalid_messages")
	}
	switch kind := str(getd(it, "type", "message"), "invalid_messages", true); kind {
	case "message":
		keys(it, set("type", "role", "content", "id", "status"), "invalid_messages")
		history(it, false)
		role := str(get(it, "role"), "invalid_messages", true)
		if !set("system", "developer", "user", "assistant")[role] {
			fail("invalid_messages")
		}
		messages = append(messages, host.Obj{{Key: "role", Value: role}, {Key: "content", Value: chatContent(get(it, "content"), role)}})
	case "function_call":
		keys(it, set("type", "id", "status", "call_id", "name", "arguments"), "invalid_tools")
		history(it, true)
		callID := str(get(it, "call_id"), "invalid_tool_sequence", true)
		callName := str(get(it, "name"), "invalid_tools", true)
		call := host.Obj{{Key: "type", Value: "function"}, {Key: "id", Value: callID},
			{Key: "function", Value: host.Obj{{Key: "name", Value: callName},
				{Key: "arguments", Value: str(get(it, "arguments"), "invalid_tool_arguments", false)}}}}
		if n := len(messages); n > 0 && has(messages[n-1].(host.Obj), "tool_calls") {
			last := messages[n-1].(host.Obj)
			last.Set("tool_calls", append(get(last, "tool_calls").([]any), call))
		} else {
			messages = append(messages, host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: nil}, {Key: "tool_calls", Value: []any{call}}})
		}
	case "function_call_output":
		keys(it, set("type", "id", "status", "call_id", "output"), "invalid_tools")
		history(it, true)
		callID := str(get(it, "call_id"), "invalid_tool_sequence", true)
		messages = append(messages, host.Obj{{Key: "role", Value: "tool"}, {Key: "tool_call_id", Value: callID},
			{Key: "content", Value: chatContent(get(it, "output"), "tool")}})
	default:
		fail("unsupported_field")
	}
	return messages
}
