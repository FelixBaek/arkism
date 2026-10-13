package gateway

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Gemini generateContent request -> Chat request.

var geminiAliases = map[string]string{
	"systemInstruction": "system_instruction", "generationConfig": "generation_config",
	"toolConfig": "tool_config", "inlineData": "inline_data", "mimeType": "mime_type",
	"functionCall": "function_call", "functionResponse": "function_response",
	"functionDeclarations": "function_declarations", "parametersJsonSchema": "parameters_json_schema",
	"functionCallingConfig": "function_calling_config", "allowedFunctionNames": "allowed_function_names",
	"topP": "top_p", "maxOutputTokens": "max_output_tokens", "stopSequences": "stop_sequences",
	"candidateCount": "candidate_count", "anyOf": "any_of", "propertyOrdering": "property_ordering",
	"minItems": "min_items", "maxItems": "max_items", "minProperties": "min_properties",
	"maxProperties": "max_properties", "minLength": "min_length", "maxLength": "max_length",
}

// Wire normalizes the declared camelCase wire fields of one object, accepting their
// snake_case aliases; user-owned JSON objects are never passed here (_wire).
func Wire(value any, fields []string, code string) (out host.Obj, err error) {
	defer catch(&err)
	return wire(value, fields, code), nil
}

func wire(value any, fields []string, code string) host.Obj {
	allowed, aliases := set(fields...), map[string]string{}
	for _, key := range fields {
		if alias, ok := geminiAliases[key]; ok {
			aliases[alias] = key
			allowed[alias] = true
		}
	}
	o := keys(value, allowed, code)
	out := host.Obj{}
	for _, kv := range o {
		key := kv.Key
		if canonical, ok := aliases[key]; ok {
			key = canonical
		}
		if has(out, key) {
			fail(code)
		}
		out = append(out, host.KV{Key: key, Value: kv.Value})
	}
	return out
}

var (
	schemaFields = []string{"type", "format", "title", "description", "nullable", "enum", "properties", "required",
		"items", "anyOf", "propertyOrdering", "minItems", "maxItems", "minProperties",
		"maxProperties", "minLength", "maxLength", "pattern", "example", "default", "minimum", "maximum"}
	schemaTypes  = set("STRING", "NUMBER", "INTEGER", "BOOLEAN", "ARRAY", "OBJECT", "NULL")
	schemaCounts = set("minItems", "maxItems", "minProperties", "maxProperties", "minLength", "maxLength")
)

func geminiSchema(input any) host.Obj {
	value := wire(input, schemaFields, "invalid_tools")
	out := host.Obj{}
	for _, kv := range value {
		key, item := kv.Key, kv.Value
		switch {
		case key == "type":
			kind := str(item, "invalid_tools", false)
			if !schemaTypes[kind] {
				fail("invalid_tools")
			}
			out.Set(key, strings.ToLower(kind))
		case key == "properties":
			properties, ok := item.(host.Obj)
			if !ok {
				fail("invalid_tools")
			}
			converted := host.Obj{}
			for _, p := range properties {
				converted.Set(p.Key, geminiSchema(p.Value))
			}
			out.Set(key, converted)
		case key == "items":
			out.Set(key, geminiSchema(item))
		case key == "anyOf":
			list, ok := item.([]any)
			if !ok || len(list) == 0 {
				fail("invalid_tools")
			}
			converted := make([]any, len(list))
			for i, schema := range list {
				converted[i] = geminiSchema(schema)
			}
			out.Set(key, converted)
		case schemaCounts[key]:
			// Proto int64 fields may arrive as decimal strings; JSON Schema needs numbers.
			if s, ok := item.(string); ok {
				if s == "" || len(s) > 19 || strings.Trim(s, "0123456789") != "" {
					fail("invalid_tools")
				}
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					fail("invalid_tools") // shortcut: Python accepts 19-digit values above int64 here
				}
				item = n
			}
			integer(item, "invalid_tools", 0)
			out.Set(key, item)
		case key == "format" || key == "title" || key == "description" || key == "pattern":
			out.Set(key, str(item, "invalid_tools", false))
		case key == "enum" || key == "required" || key == "propertyOrdering":
			list, ok := item.([]any)
			if !ok {
				fail("invalid_tools")
			}
			converted := make([]any, len(list))
			for i, entry := range list {
				converted[i] = str(entry, "invalid_tools", false)
			}
			out.Set(key, converted)
		case key == "minimum" || key == "maximum":
			if !isNumber(item) {
				fail("invalid_tools")
			}
			out.Set(key, item)
		case key == "example":
			out.Set("examples", []any{deepcopy(item, "invalid_request")})
		case key == "default":
			out.Set(key, deepcopy(item, "invalid_request"))
		}
	}
	if v, ok := value.Get("nullable"); ok {
		nullable, ok := v.(bool)
		if !ok {
			fail("invalid_tools")
		}
		if nullable {
			// Wrap all constraints so enum or anyOf cannot accidentally exclude null.
			out = host.Obj{{Key: "anyOf", Value: []any{out, host.Obj{{Key: "type", Value: "null"}}}}}
		} else if !has(out, "type") {
			out.Set("not", host.Obj{{Key: "type", Value: "null"}})
		}
	}
	return out
}

// geminiTools adds tools and tool_choice to *out.
func geminiTools(body host.Obj, out *host.Obj) {
	names := map[string]bool{}
	if v, ok := body.Get("tools"); ok {
		list, ok := v.([]any)
		if !ok {
			fail("invalid_tools")
		}
		tools := []any{}
		for _, tool := range list {
			t := wire(tool, []string{"functionDeclarations"}, "invalid_tools")
			declarations, ok := get(t, "functionDeclarations").([]any)
			if !ok || len(declarations) == 0 {
				fail("invalid_tools")
			}
			for _, d := range declarations {
				declaration := wire(d, []string{"name", "description", "parameters", "parametersJsonSchema"}, "invalid_tools")
				toolName := name(get(declaration, "name"), "invalid_tools")
				if names[toolName] || has(declaration, "parameters") && has(declaration, "parametersJsonSchema") {
					fail("invalid_tools")
				}
				names[toolName] = true
				function := host.Obj{{Key: "name", Value: toolName}}
				if description, ok := declaration.Get("description"); ok {
					function.Set("description", str(description, "invalid_tools", false))
				}
				if parameters, ok := declaration.Get("parameters"); ok {
					function.Set("parameters", geminiSchema(parameters))
				} else if schema, ok := declaration.Get("parametersJsonSchema"); ok {
					if _, ok := schema.(host.Obj); !ok {
						fail("invalid_tools")
					}
					function.Set("parameters", deepcopy(schema, "invalid_request"))
				} else {
					function.Set("parameters", host.Obj{{Key: "type", Value: "object"}, {Key: "properties", Value: host.Obj{}}})
				}
				tools = append(tools, host.Obj{{Key: "type", Value: "function"}, {Key: "function", Value: function}})
			}
		}
		out.Set("tools", tools)
	}
	toolConfig, ok := body.Get("toolConfig")
	if !ok {
		return
	}
	config := wire(toolConfig, []string{"functionCallingConfig"}, "invalid_tool_choice")
	calling, ok := config.Get("functionCallingConfig")
	if !ok {
		return
	}
	config = wire(calling, []string{"mode", "allowedFunctionNames"}, "invalid_tool_choice")
	mode := str(getd(config, "mode", "AUTO"), "invalid_tool_choice", false)
	choices := map[string]string{"AUTO": "auto", "NONE": "none", "ANY": "required"}
	if _, ok := choices[mode]; !ok {
		fail("unsupported_field")
	}
	if mode != "AUTO" || len(names) > 0 {
		out.Set("tool_choice", choices[mode])
	}
	if v, ok := config.Get("allowedFunctionNames"); ok {
		list, ok := v.([]any)
		if !ok || len(list) == 0 {
			fail("invalid_tool_choice")
		}
		allowed := map[string]bool{}
		for _, item := range list {
			allowed[name(item, "invalid_tool_choice")] = true
		}
		for chosen := range allowed {
			if !names[chosen] {
				fail("invalid_tool_choice")
			}
		}
		if len(allowed) != len(list) {
			fail("invalid_tool_choice")
		}
		if mode != "ANY" {
			fail("unsupported_field")
		}
		if len(list) == 1 {
			out.Set("tool_choice", host.Obj{{Key: "type", Value: "function"}, {Key: "function", Value: host.Obj{{Key: "name", Value: list[0]}}}})
		} else {
			kept := []any{}
			for _, tool := range get(*out, "tools").([]any) {
				if allowed[get(get(tool, "function"), "name").(string)] {
					kept = append(kept, tool)
				}
			}
			out.Set("tools", kept)
		}
	}
}

type geminiPart struct {
	kind string
	data any
}

type geminiContent struct {
	role  string
	parts []geminiPart
}

func geminiContents(value any) ([]geminiContent, map[string]bool) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		fail("invalid_messages")
	}
	var contents []geminiContent
	reserved := map[string]bool{}
	for _, c := range list {
		keys(c, set("role", "parts"), "invalid_messages")
		role := str(getd(c.(host.Obj), "role", "user"), "invalid_messages", false)
		if role != "user" && role != "model" && role != "" {
			fail("invalid_messages")
		}
		parts, ok := get(c, "parts").([]any)
		if !ok || len(parts) == 0 {
			fail("invalid_content")
		}
		var normalized []geminiPart
		for _, p := range parts {
			part := wire(p, []string{"text", "inlineData", "functionCall", "functionResponse"}, "invalid_content")
			if len(part) != 1 {
				fail("invalid_content")
			}
			kind, data := part[0].Key, part[0].Value
			if kind == "functionCall" || kind == "functionResponse" {
				allowed := set("id", "name", "response")
				if kind == "functionCall" {
					allowed = set("id", "name", "args")
				}
				d := keys(data, allowed, "invalid_tools")
				if id, ok := d.Get("id"); ok {
					reserved[str(id, "invalid_tool_sequence", true)] = true
				}
			}
			normalized = append(normalized, geminiPart{kind, data})
		}
		if role == "" {
			role = "user"
		}
		contents = append(contents, geminiContent{role, normalized})
	}
	return contents, reserved
}

var generationFields = []struct{ key, target string }{{"temperature", "temperature"}, {"topP", "top_p"},
	{"maxOutputTokens", "max_completion_tokens"}, {"stopSequences", "stop"}, {"candidateCount", "n"}}

// GeminiToChat returns an independent, validated Chat request for the path model (to_chat).
func GeminiToChat(input any, model string, stream bool) (out host.Obj, err error) {
	defer catch(&err)
	jsonText(input, "invalid_request")
	jsonShape(input, 0)
	body := wire(input, []string{"model", "contents", "systemInstruction", "tools", "toolConfig", "generationConfig"}, "invalid_request")
	model = str(model, "invalid_request", true)
	if v, ok := body.Get("model"); ok && strings.TrimPrefix(str(v, "invalid_request", true), "models/") != model {
		fail("invalid_request")
	}
	out = host.Obj{{Key: "model", Value: model}, {Key: "stream", Value: stream}, {Key: "parallel_tool_calls", Value: true}}
	if stream {
		out.Set("stream_options", host.Obj{{Key: "include_usage", Value: true}})
	}
	if v, ok := body.Get("generationConfig"); ok {
		config := wire(v, []string{"temperature", "topP", "maxOutputTokens", "stopSequences", "candidateCount"}, "invalid_option")
		for _, f := range generationFields {
			item, ok := config.Get(f.key)
			if !ok {
				continue
			}
			if item == nil {
				fail("invalid_option")
			}
			if f.key == "stopSequences" {
				list, ok := item.([]any)
				if !ok {
					fail("invalid_option")
				}
				stops := make([]any, len(list))
				for i, s := range list {
					stops[i] = str(s, "invalid_option", true)
				}
				item = stops
			}
			out.Set(f.target, item)
		}
	}
	geminiTools(body, &out)
	messages := []any{}
	if system, ok := body.Get("systemInstruction"); ok {
		s := keys(system, set("role", "parts"), "invalid_content")
		if role, ok := s.Get("role"); ok {
			str(role, "invalid_content", false) // Google ignores this role.
		}
		parts, ok := get(s, "parts").([]any)
		if !ok || len(parts) == 0 {
			fail("invalid_content")
		}
		text := []any{}
		for _, part := range parts {
			keys(part, set("text"), "invalid_content")
			text = append(text, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: str(get(part, "text"), "invalid_content", false)}})
		}
		messages = append(messages, host.Obj{{Key: "role", Value: "system"}, {Key: "content", Value: text}})
	}
	contents, reserved := geminiContents(get(body, "contents"))
	pending, seen := map[string]string{}, map[string]bool{}
	var order []string // pending insertion order, for matching by name
	for contentIndex, c := range contents {
		if len(pending) > 0 && c.role != "user" {
			fail("invalid_tool_sequence")
		}
		text, calls := []any{}, []any{}
		for partIndex, p := range c.parts {
			switch p.kind {
			case "functionCall":
				if c.role != "model" {
					fail("invalid_tool_sequence")
				}
				callName := name(get(p.data, "name"), "invalid_tools")
				args, ok := getd(p.data.(host.Obj), "args", host.Obj{}).(host.Obj)
				if !ok {
					fail("invalid_tool_arguments")
				}
				arguments := jsonText(args, "invalid_tool_arguments")
				if len(arguments) > MaxToolArgumentBytes {
					fail("tool_arguments_too_large")
				}
				var identity string
				if id, ok := p.data.(host.Obj).Get("id"); ok {
					identity = id.(string)
				} else {
					base := fmt.Sprintf("gemini_call_%d_%d", contentIndex, partIndex)
					identity = base
					for suffix := 1; reserved[identity] || seen[identity]; suffix++ {
						identity = fmt.Sprintf("%s_%d", base, suffix)
					}
				}
				if seen[identity] {
					fail("invalid_tool_sequence")
				}
				seen[identity] = true
				pending[identity] = callName
				order = append(order, identity)
				calls = append(calls, host.Obj{{Key: "id", Value: identity}, {Key: "type", Value: "function"},
					{Key: "function", Value: host.Obj{{Key: "name", Value: callName}, {Key: "arguments", Value: arguments}}}})
			case "functionResponse":
				if c.role != "user" || len(text) > 0 {
					fail("invalid_tool_sequence")
				}
				callName := name(get(p.data, "name"), "invalid_tools")
				identity, _ := get(p.data, "id").(string)
				if get(p.data, "id") == nil {
					var matches []string
					for _, key := range order {
						if pendingName, ok := pending[key]; ok && pendingName == callName {
							matches = append(matches, key)
						}
					}
					if len(matches) != 1 {
						fail("invalid_tool_sequence")
					}
					identity = matches[0]
				}
				if pendingName, ok := pending[identity]; !ok || pendingName != callName {
					fail("invalid_tool_sequence")
				}
				response, ok := get(p.data, "response").(host.Obj)
				if !ok {
					fail("invalid_tool_arguments")
				}
				messages = append(messages, host.Obj{{Key: "role", Value: "tool"}, {Key: "tool_call_id", Value: identity},
					{Key: "content", Value: jsonText(response, "invalid_tool_arguments")}})
				delete(pending, identity)
			default:
				if len(pending) > 0 && c.role == "user" {
					fail("invalid_tool_sequence")
				}
				if len(calls) > 0 {
					// Chat cannot keep text or image parts after a function call.
					fail("unsupported_field")
				}
				if p.kind == "text" {
					text = append(text, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: str(p.data, "invalid_content", false)}})
					continue
				}
				if c.role != "user" {
					fail("unsupported_field")
				}
				data := wire(p.data, []string{"mimeType", "data"}, "invalid_image")
				mime := str(get(data, "mimeType"), "invalid_image", true)
				if !set("image/png", "image/jpeg", "image/gif", "image/webp")[mime] {
					fail("unsupported_field")
				}
				url := "data:" + mime + ";base64," + str(get(data, "data"), "invalid_image", true)
				image := host.Obj{{Key: "type", Value: "image_url"}, {Key: "image_url", Value: host.Obj{{Key: "url", Value: url}}}}
				content([]any{image}, true)
				text = append(text, image)
			}
		}
		if len(text) > 0 || len(calls) > 0 {
			role := "user"
			if c.role == "model" {
				role = "assistant"
			}
			item := host.Obj{{Key: "role", Value: role}, {Key: "content", Value: nil}}
			if len(text) > 0 {
				item.Set("content", text)
			}
			if len(calls) > 0 {
				item.Set("tool_calls", calls)
			}
			messages = append(messages, item)
		}
	}
	out.Set("messages", messages)
	toMessages(out)
	return out, nil
}
