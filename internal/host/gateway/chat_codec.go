package gateway

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"iter"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Pure Chat <-> Messages codec. No credentials, network or
// gateway state; failures are fixed public codes.

// Limits shared by every translator. Variables so tests can shrink them.
var (
	MaxFrameBytes        = 1024 * 1024
	MaxToolArgumentBytes = 1024 * 1024
)

// Clock and id source; tests replace them for byte-exact goldens.
var (
	now    = time.Now
	newHex = uuidHex
)

var codes = set("invalid_request", "unsupported_field", "invalid_messages", "invalid_content",
	"invalid_image", "unsupported_image_detail", "invalid_tools", "invalid_tool_choice",
	"invalid_tool_arguments", "invalid_tool_sequence", "invalid_option",
	"invalid_response", "invalid_usage", "invalid_stream", "truncated_stream",
	"stream_frame_too_large", "tool_arguments_too_large", "upstream_stream_error",
	"upstream_error", "invalid_error_status",
	"login_unavailable", "claude_subscription_requires_cli", "input_too_large",
	"upstream_interrupted", "upstream_invalid_response", "model_busy", "model_rate_limited")

// Code is a fixed public failure code (Python's ValueError(code)); never upstream text.
type Code string

func (c Code) Error() string { return string(c) }

// emitError carries a consumer's emit failure through the panic-based unwinding.
type emitError struct{ err error }

func fail(code string) { panic(Code(code)) }

// catch turns a fail or emit panic back into the returned error at an exported boundary.
func catch(err *error) {
	if r := recover(); r != nil {
		switch x := r.(type) {
		case Code:
			*err = x
		case emitError:
			*err = x.err
		default:
			panic(r)
		}
	}
}

// rethrow re-raises an error returned by a nested exported call.
func rethrow(err error) {
	if c, ok := err.(Code); ok {
		panic(c)
	}
	if err != nil {
		panic(emitError{err})
	}
}

// emitter adapts a consumer callback; its error stops the stream and is returned as is.
func emitter(emit func([]byte) error) func([]byte) {
	return func(frame []byte) {
		if err := emit(frame); err != nil {
			panic(emitError{err})
		}
	}
}

func keys(value any, allowed map[string]bool, code string) host.Obj {
	o, ok := value.(host.Obj)
	if !ok {
		fail(code)
	}
	for _, kv := range o {
		if !allowed[kv.Key] {
			fail("unsupported_field")
		}
	}
	return o
}

func str(value any, code string, nonempty bool) string {
	s, ok := value.(string)
	if !ok || (nonempty && s == "") || !utf8.ValidString(s) {
		fail(code)
	}
	return s
}

// integer is _integer; Python ints beyond int64 are refused here.
func integer(value any, code string, minimum int64) int64 {
	n, ok := host.Int(value)
	if !ok || !isInt(value) || n < minimum {
		fail(code)
	}
	return n
}

// jsonText is _json: compact, non-ASCII, finite, valid UTF-8.
func jsonText(value any, code string) string {
	if !validJSON(value, 0) {
		fail(code)
	}
	return dumps(value)
}

func loads(value, code string) any {
	parsed, err := parse(value, true)
	if err != nil {
		fail(code)
	}
	jsonText(parsed, code)
	return parsed
}

func arguments(value any) host.Obj {
	parsed, ok := loads(str(value, "invalid_tool_arguments", false), "invalid_tool_arguments").(host.Obj)
	if !ok {
		fail("invalid_tool_arguments")
	}
	return parsed
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func name(value any, code string) string {
	s := str(value, code, true)
	if !namePattern.MatchString(s) {
		fail(code)
	}
	return s
}

// jsonShape is _json_shape: object keys must be valid text and values JSON types.
func jsonShape(value any, depth int) {
	if depth > maxNesting*2 {
		fail("invalid_request")
	}
	switch x := value.(type) {
	case host.Obj:
		for _, kv := range x {
			str(kv.Key, "invalid_request", false)
			jsonShape(kv.Value, depth+1)
		}
	case []any:
		for _, item := range x {
			jsonShape(item, depth+1)
		}
	case nil, string, bool, json.Number, float64, int, int64:
	default:
		fail("invalid_request")
	}
}

// responseFormat only validates Chat structured output: Messages cannot express it, so the key
// never reaches the Anthropic request and the caller's schema is never shared or rewritten.
func responseFormat(value any) {
	if _, ok := value.(host.Obj); !ok {
		fail("invalid_request")
	}
	kind := str(get(value, "type"), "invalid_request", true)
	if kind == "json_object" {
		keys(value, set("type"), "invalid_request")
		return
	}
	if kind != "json_schema" {
		fail("unsupported_field")
	}
	keys(value, set("type", "json_schema"), "invalid_request")
	format, ok := get(value, "json_schema").(host.Obj)
	if !ok {
		fail("invalid_request")
	}
	keys(format, set("name", "schema", "strict"), "invalid_request")
	name(get(format, "name"), "invalid_request")
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
}

var imageHeaders = set("data:image/png;base64", "data:image/jpeg;base64", "data:image/gif;base64", "data:image/webp;base64")

var base64Pattern = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

// content is _content: Chat message content as Messages blocks.
func content(value any, images bool) []any {
	if s, ok := value.(string); ok {
		return []any{host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: s}}}
	}
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		fail("invalid_content")
	}
	result := []any{}
	for _, part := range parts {
		if _, ok := part.(host.Obj); !ok {
			fail("invalid_content")
		}
		kind := str(get(part, "type"), "invalid_content", false)
		switch {
		case kind == "text":
			keys(part, set("type", "text"), "invalid_content")
			result = append(result, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: str(get(part, "text"), "invalid_content", false)}})
		case kind == "image_url" && images:
			result = append(result, imagePart(part))
		default:
			fail("unsupported_field")
		}
	}
	return result
}

// imagePart converts one image_url content part to a Messages image block.
func imagePart(part any) host.Obj {
	keys(part, set("type", "image_url"), "invalid_image")
	image := keys(get(part, "image_url"), set("url", "detail"), "invalid_image")
	if detail, ok := image.Get("detail"); ok && detail != "auto" {
		fail("unsupported_image_detail")
	}
	url := str(get(image, "url"), "invalid_image", true)
	for _, r := range url {
		if isSpace(r) || r < 32 {
			fail("invalid_image")
		}
	}
	var source host.Obj
	if strings.HasPrefix(url, "data:") {
		header, data, found := strings.Cut(url, ",")
		if !found || !imageHeaders[header] || !base64Pattern.MatchString(data) {
			fail("invalid_image")
		}
		if decoded, err := base64.StdEncoding.DecodeString(data); err != nil || len(decoded) == 0 {
			fail("invalid_image")
		}
		source = host.Obj{{Key: "type", Value: "base64"}, {Key: "media_type", Value: header[5 : len(header)-7]}, {Key: "data", Value: data}}
	} else {
		if !httpURL(url) {
			fail("invalid_image")
		}
		source = host.Obj{{Key: "type", Value: "url"}, {Key: "url", Value: url}}
	}
	return host.Obj{{Key: "type", Value: "image"}, {Key: "source", Value: source}}
}

// isSpace is Python's str.isspace for one character.
func isSpace(r rune) bool {
	switch {
	case r >= 0x9 && r <= 0xd, r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// nfkcSeparators are the non-ASCII characters whose NFKC form contains / ? # @ or :, which
// urlsplit refuses inside a netloc.
var nfkcSeparators = map[rune]bool{0x2047: true, 0x2048: true, 0x2049: true, 0x2100: true, 0x2101: true,
	0x2105: true, 0x2106: true, 0x2a74: true, 0xfe13: true, 0xfe16: true, 0xfe55: true, 0xfe56: true,
	0xfe5f: true, 0xfe6b: true, 0xff03: true, 0xff0f: true, 0xff1a: true, 0xff1f: true, 0xff20: true}

var ipvFuture = regexp.MustCompile(`^v[a-fA-F0-9]+\..+$`)

// httpURL reports whether urlsplit(url) parses with an http(s) scheme, a hostname, no
// credentials and a valid port, as the Python check required. url has no whitespace.
func httpURL(url string) bool {
	scheme, rest := "", url
	if i := strings.IndexByte(url, ':'); i > 0 && isAlpha(url[0]) &&
		strings.Trim(url[:i], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+-.") == "" {
		scheme, rest = strings.ToLower(url[:i]), url[i+1:]
	}
	if scheme != "http" && scheme != "https" || !strings.HasPrefix(rest, "//") {
		return false
	}
	netloc := rest[2:]
	if i := strings.IndexAny(netloc, "/?#"); i >= 0 {
		netloc = netloc[:i]
	}
	open, closed := strings.Contains(netloc, "["), strings.Contains(netloc, "]")
	if open != closed {
		return false
	}
	at := strings.LastIndexByte(netloc, '@')
	hostport := netloc[at+1:]
	if open && !bracketHost(hostport) {
		return false
	}
	for _, r := range netloc {
		if nfkcSeparators[r] {
			return false
		}
	}
	if at >= 0 {
		user, password, _ := strings.Cut(netloc[:at], ":")
		if user != "" || password != "" {
			return false
		}
	}
	return hostPort(hostport)
}

// bracketHost reports whether a netloc with brackets holds a valid IPv6 or IPvFuture literal.
func bracketHost(hostport string) bool {
	host := hostport
	if before, bracketed, ok := strings.Cut(hostport, "["); ok {
		if before != "" {
			return false
		}
		var after string
		host, after, _ = strings.Cut(bracketed, "]")
		if after != "" && !strings.HasPrefix(after, ":") {
			return false
		}
	} else {
		host, _, _ = strings.Cut(hostport, ":")
	}
	if strings.HasPrefix(host, "v") {
		if !ipvFuture.MatchString(host) {
			return false
		}
	} else if addr, err := netip.ParseAddr(host); err != nil || addr.Is4() {
		return false
	}
	return true
}

// hostPort reports whether hostport has a hostname and a valid port.
func hostPort(hostport string) bool {
	var hostname, port string
	if _, bracketed, ok := strings.Cut(hostport, "["); ok {
		var after string
		hostname, after, _ = strings.Cut(bracketed, "]")
		_, port, _ = strings.Cut(after, ":")
	} else {
		hostname, port, _ = strings.Cut(hostport, ":")
	}
	if hostname == "" {
		return false
	}
	if port != "" {
		if strings.Trim(port, "0123456789") != "" {
			return false
		}
		port = strings.TrimLeft(port, "0")
		if len(port) > 5 || port > "65535" && len(port) == 5 {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

var toMessagesKeys = set("model", "messages", "tools", "tool_choice", "parallel_tool_calls",
	"max_tokens", "max_completion_tokens", "temperature", "top_p", "stop",
	"stream", "stream_options", "n", "response_format")

// CodecToMessages validates a Chat request and returns an independent Anthropic Messages request
// (to_messages).
func CodecToMessages(body any) (out host.Obj, err error) {
	defer catch(&err)
	return toMessages(body), nil
}

func toMessages(value any) host.Obj {
	body := keys(value, toMessagesKeys, "invalid_request")
	jsonText(body, "invalid_request")
	if format, ok := body.Get("response_format"); ok {
		responseFormat(format)
	}
	out := host.Obj{{Key: "model", Value: str(get(body, "model"), "invalid_request", true)}}
	requestOptions(body, &out)
	streamOptions(body, &out)
	stopSequences(body, &out)
	names := codecTools(body, &out)
	choice := toolChoice(body, &out, names)
	parallelToolCalls(body, &out, names, choice)
	codecMessages(body, &out)
	return out
}

// requestOptions validates n and copies the token limit and sampling options.
func requestOptions(body host.Obj, out *host.Obj) {
	if n := get(body, "n"); n != nil {
		if count, ok := host.Int(n); !ok || !isInt(n) || count != 1 {
			fail("invalid_option")
		}
	}
	var limits []any
	var values []int64
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if limit := get(body, key); limit != nil {
			limits, values = append(limits, limit), append(values, integer(limit, "invalid_option", 1))
		}
	}
	if len(limits) == 2 && values[0] != values[1] {
		fail("invalid_option")
	}
	if len(limits) > 0 {
		out.Set("max_tokens", limits[0])
	} else {
		out.Set("max_tokens", 4096)
	}
	for _, key := range []string{"temperature", "top_p"} {
		if v := get(body, key); v != nil {
			if !isNumber(v) || !(number(v) >= 0 && number(v) <= 1) {
				fail("invalid_option")
			}
			out.Set(key, v)
		}
	}
}

func streamOptions(body host.Obj, out *host.Obj) {
	if v, ok := body.Get("stream"); ok {
		if _, ok := v.(bool); !ok {
			fail("invalid_option")
		}
		out.Set("stream", v)
	}
	if options := get(body, "stream_options"); options != nil {
		o := keys(options, set("include_usage"), "invalid_option")
		include, has := o.Get("include_usage")
		if _, isBool := include.(bool); get(body, "stream") != true || has && !isBool {
			fail("invalid_option")
		}
		// include_usage controls the downstream codec, never the Messages request.
	}
}

func stopSequences(body host.Obj, out *host.Obj) {
	if stop := get(body, "stop"); stop != nil {
		stops, ok := stop.([]any)
		if s, isStr := stop.(string); isStr {
			stops, ok = []any{s}, true
		}
		if !ok || len(stops) < 1 || len(stops) > 4 {
			fail("invalid_option")
		}
		sequences := make([]any, len(stops))
		for i, item := range stops {
			sequences[i] = str(item, "invalid_option", true)
		}
		out.Set("stop_sequences", sequences)
	}
}

// codecTools converts the tools and returns their names.
func codecTools(body host.Obj, out *host.Obj) map[string]bool {
	names := map[string]bool{}
	if v, ok := body.Get("tools"); ok {
		list, ok := v.([]any)
		if !ok {
			fail("invalid_tools")
		}
		tools := []any{}
		for _, tool := range list {
			keys(tool, set("type", "function"), "invalid_tools")
			if get(tool, "type") != "function" {
				fail("invalid_tools")
			}
			function := keys(get(tool, "function"), set("name", "description", "parameters", "strict"), "invalid_tools")
			if strict := get(function, "strict"); strict != nil && strict != false {
				fail("unsupported_field")
			}
			toolName := name(get(function, "name"), "invalid_tools")
			if names[toolName] {
				fail("invalid_tools")
			}
			names[toolName] = true
			schema, ok := getd(function, "parameters", host.Obj{{Key: "type", Value: "object"}, {Key: "properties", Value: host.Obj{}}}).(host.Obj)
			if !ok {
				fail("invalid_tools")
			}
			converted := host.Obj{{Key: "name", Value: toolName}, {Key: "input_schema", Value: deepcopy(schema, "invalid_request")}}
			if description, ok := function.Get("description"); ok {
				converted.Set("description", str(description, "invalid_tools", false))
			}
			tools = append(tools, converted)
		}
		out.Set("tools", tools)
	}
	return names
}

func toolChoice(body host.Obj, out *host.Obj, names map[string]bool) any {
	choice := get(body, "tool_choice")
	if choice != nil {
		if s, ok := choice.(string); ok && (s == "auto" || s == "none" || s == "required") {
			kind := s
			if s == "required" {
				kind = "any"
			}
			out.Set("tool_choice", host.Obj{{Key: "type", Value: kind}})
		} else if o, ok := choice.(host.Obj); ok {
			keys(o, set("type", "function"), "invalid_tool_choice")
			keys(get(o, "function"), set("name"), "invalid_tool_choice")
			chosen := name(get(get(o, "function"), "name"), "invalid_tool_choice")
			if get(o, "type") != "function" || !names[chosen] {
				fail("invalid_tool_choice")
			}
			out.Set("tool_choice", host.Obj{{Key: "type", Value: "tool"}, {Key: "name", Value: chosen}})
		} else {
			fail("invalid_tool_choice")
		}
		if len(names) == 0 && choice != "none" {
			fail("invalid_tool_choice")
		}
	}
	return choice
}

func parallelToolCalls(body host.Obj, out *host.Obj, names map[string]bool, choice any) {
	if parallel, ok := body.Get("parallel_tool_calls"); ok {
		p, ok := parallel.(bool)
		if !ok {
			fail("invalid_option")
		}
		if len(names) > 0 && choice != "none" {
			tc, ok := out.Get("tool_choice")
			if !ok {
				tc = host.Obj{{Key: "type", Value: "auto"}}
			}
			o := tc.(host.Obj)
			o.Set("disable_parallel_tool_use", !p)
			out.Set("tool_choice", o)
		}
	}
}

func codecMessages(body host.Obj, out *host.Obj) {
	messages, ok := get(body, "messages").([]any)
	if !ok || len(messages) == 0 {
		fail("invalid_messages")
	}
	type turn struct {
		role    string
		content []any
	}
	var system []any
	var converted []*turn
	pending, seen := map[string]bool{}, map[string]bool{}
	for _, message := range messages {
		m := keys(message, set("role", "content", "tool_calls", "tool_call_id"), "invalid_messages")
		role := messageRole(m, pending)
		if role == "system" || role == "developer" {
			if len(converted) > 0 {
				fail("invalid_messages")
			}
			system = append(system, content(get(m, "content"), false)...)
			continue
		}
		var blocks []any
		if role == "tool" {
			blocks = toolResult(m, pending)
			role = "user"
		} else {
			blocks = turnBlocks(m, role, pending, seen)
		}
		if n := len(converted); n > 0 && converted[n-1].role == role {
			converted[n-1].content = append(converted[n-1].content, blocks...)
		} else {
			converted = append(converted, &turn{role, blocks})
		}
	}
	if len(converted) == 0 || len(pending) > 0 {
		if len(pending) > 0 {
			fail("invalid_tool_sequence")
		}
		fail("invalid_messages")
	}
	list := make([]any, len(converted))
	for i, t := range converted {
		list[i] = host.Obj{{Key: "role", Value: t.role}, {Key: "content", Value: t.content}}
	}
	out.Set("messages", list)
	if len(system) > 0 {
		out.Set("system", system)
	}
}

// messageRole validates where message m may appear and returns its role.
func messageRole(m host.Obj, pending map[string]bool) string {
	role, _ := get(m, "role").(string)
	if !set("system", "developer", "user", "assistant", "tool")[role] {
		fail("invalid_messages")
	}
	if has(m, "tool_calls") && role != "assistant" || has(m, "tool_call_id") && role != "tool" {
		fail("invalid_tool_sequence")
	}
	if len(pending) > 0 && role != "tool" {
		fail("invalid_tool_sequence")
	}
	return role
}

// toolResult converts a tool message to a tool_result block.
func toolResult(m host.Obj, pending map[string]bool) []any {
	callID := str(get(m, "tool_call_id"), "invalid_tool_sequence", true)
	if !pending[callID] {
		fail("invalid_tool_sequence")
	}
	delete(pending, callID)
	return []any{host.Obj{{Key: "type", Value: "tool_result"}, {Key: "tool_use_id", Value: callID},
		{Key: "content", Value: content(get(m, "content"), false)}}}
}

// turnBlocks converts a user or assistant message, with any tool calls, to content blocks.
func turnBlocks(m host.Obj, role string, pending, seen map[string]bool) []any {
	var blocks []any
	calls := get(m, "tool_calls")
	if get(m, "content") == nil && truthy(calls) {
		blocks = []any{}
	} else {
		blocks = content(get(m, "content"), role == "user")
	}
	if calls != nil {
		list, ok := calls.([]any)
		if !ok || len(list) == 0 {
			fail("invalid_tools")
		}
		for _, call := range list {
			keys(call, set("id", "type", "function"), "invalid_tools")
			function := keys(get(call, "function"), set("name", "arguments"), "invalid_tools")
			callID := str(get(call, "id"), "invalid_tool_sequence", true)
			if get(call, "type") != "function" || seen[callID] {
				fail("invalid_tool_sequence")
			}
			seen[callID], pending[callID] = true, true
			toolName := name(get(function, "name"), "invalid_tools")
			blocks = append(blocks, host.Obj{{Key: "type", Value: "tool_use"}, {Key: "id", Value: callID},
				{Key: "name", Value: toolName}, {Key: "input", Value: arguments(get(function, "arguments"))}})
		}
	}
	return blocks
}

func codecUsage(value any) host.Obj {
	o, ok := value.(host.Obj)
	if !ok {
		fail("invalid_usage")
	}
	prompt := integer(get(o, "input_tokens"), "invalid_usage", 0)
	completion := integer(get(o, "output_tokens"), "invalid_usage", 0)
	cached := integer(getd(o, "cache_read_input_tokens", 0), "invalid_usage", 0)
	prompt += cached + integer(getd(o, "cache_creation_input_tokens", 0), "invalid_usage", 0)
	result := host.Obj{{Key: "prompt_tokens", Value: prompt}, {Key: "completion_tokens", Value: completion},
		{Key: "total_tokens", Value: prompt + completion}}
	if cached != 0 {
		result.Set("prompt_tokens_details", host.Obj{{Key: "cached_tokens", Value: cached}})
	}
	if details := get(o, "output_tokens_details"); details != nil {
		if _, ok := details.(host.Obj); !ok {
			fail("invalid_usage")
		}
		if reasoning := get(details, "reasoning_tokens"); reasoning != nil {
			if integer(reasoning, "invalid_usage", 0) > completion {
				fail("invalid_usage")
			}
			result.Set("completion_tokens_details", host.Obj{{Key: "reasoning_tokens", Value: reasoning}})
		}
	}
	return result
}

var finishReasons = map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length",
	"tool_use": "tool_calls", "refusal": "content_filter", "model_context_window_exceeded": "length"}

func finish(reason any) string {
	s, _ := reason.(string)
	mapped, ok := finishReasons[s]
	if !ok {
		fail("invalid_response")
	}
	return mapped
}

// CodecResponse converts a complete Messages response to a Chat completion and never exposes
// thinking blocks (response).
func CodecResponse(body any, model string) (out host.Obj, err error) {
	defer catch(&err)
	if get(body, "type") != "message" || get(body, "role") != "assistant" {
		fail("invalid_response")
	}
	jsonText(body, "invalid_response")
	identity := str(get(body, "id"), "invalid_response", true)
	str(model, "invalid_response", true)
	blocks, ok := get(body, "content").([]any)
	if !ok {
		fail("invalid_response")
	}
	var text []string
	calls, ids := []any{}, map[string]bool{}
	for _, block := range blocks {
		if _, ok := block.(host.Obj); !ok {
			fail("invalid_response")
		}
		switch kind := str(get(block, "type"), "invalid_response", false); kind {
		case "text":
			text = append(text, str(get(block, "text"), "invalid_response", false))
		case "tool_use":
			callID := str(get(block, "id"), "invalid_response", true)
			input, ok := get(block, "input").(host.Obj)
			if ids[callID] || !ok {
				fail("invalid_response")
			}
			ids[callID] = true
			calls = append(calls, host.Obj{{Key: "id", Value: callID}, {Key: "type", Value: "function"},
				{Key: "function", Value: host.Obj{{Key: "name", Value: name(get(block, "name"), "invalid_response")},
					{Key: "arguments", Value: jsonText(input, "invalid_response")}}}})
		case "thinking", "redacted_thinking":
		default:
			fail("invalid_response")
		}
	}
	reason := finish(get(body, "stop_reason"))
	if reason == "tool_calls" && len(calls) == 0 {
		fail("invalid_response")
	}
	message := host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: nil}}
	if text != nil {
		message.Set("content", strings.Join(text, ""))
	}
	if len(calls) > 0 {
		message.Set("tool_calls", calls)
	}
	return host.Obj{{Key: "id", Value: identity}, {Key: "object", Value: "chat.completion"},
		{Key: "created", Value: now().Unix()}, {Key: "model", Value: model},
		{Key: "choices", Value: []any{host.Obj{{Key: "index", Value: 0}, {Key: "message", Value: message}, {Key: "finish_reason", Value: reason}}}},
		{Key: "usage", Value: codecUsage(get(body, "usage"))}}, nil
}

// SSEFrames splits upstream bytes into server-sent events and calls fn with each event name
// ("" when absent) and joined data. The pending frame stays bounded even when one read holds
// many frames (sse_frames).
func SSEFrames(chunks iter.Seq2[[]byte, error], fn func(event, data string) error) (err error) {
	defer catch(&err)
	sseFrames(chunks, func(event, data string) {
		if err := fn(event, data); err != nil {
			panic(emitError{err})
		}
	})
	return nil
}

func sseFrames(chunks iter.Seq2[[]byte, error], fn func(event, data string)) {
	var pending []byte
	lineStart := 0
	for chunk, err := range chunks {
		if err != nil {
			fail("upstream_stream_error")
		}
		for offset := 0; offset < len(chunk); {
			newline := bytes.IndexByte(chunk[offset:], '\n')
			end := len(chunk)
			if newline >= 0 {
				newline += offset
				end = newline + 1
			}
			if len(pending)+end-offset > MaxFrameBytes {
				fail("stream_frame_too_large")
			}
			pending = append(pending, chunk[offset:end]...)
			offset = end
			if newline < 0 {
				continue
			}
			if line := string(pending[lineStart:]); line != "\n" && line != "\r\n" {
				lineStart = len(pending)
				continue
			}
			if !utf8.Valid(pending) {
				fail("invalid_stream")
			}
			frame := string(pending)
			pending, lineStart = pending[:0], 0
			event, hasData := "", false
			var data []string
			for _, row := range strings.Split(frame, "\n") {
				row = strings.TrimSuffix(row, "\r")
				key, value, sep := strings.Cut(row, ":")
				value = strings.TrimPrefix(value, " ")
				switch key {
				case "event":
					event = value
				case "data":
					if !sep {
						value = ""
					}
					data, hasData = append(data, value), true
				}
			}
			if event == "error" {
				fail("upstream_stream_error")
			}
			if hasData {
				fn(event, strings.Join(data, "\n"))
			}
		}
	}
	if len(pending) > 0 {
		fail("truncated_stream")
	}
}

// frames is _frames: SSE frames as validated Messages event objects.
func frames(chunks iter.Seq2[[]byte, error], fn func(host.Obj)) {
	sseFrames(chunks, func(event, data string) {
		payload, ok := loads(data, "invalid_stream").(host.Obj)
		if !ok {
			fail("invalid_stream")
		}
		if get(payload, "type") == "error" {
			fail("upstream_stream_error")
		}
		if event != "" && get(payload, "type") != event {
			fail("invalid_stream")
		}
		fn(payload)
	})
}

type codecBlock struct {
	kind      string
	closed    bool
	toolIndex int
	initial   host.Obj
	parts     []string
	size      int
}

// CodecStream translates Messages SSE to Chat SSE frames, calling emit for each; DONE is emitted
// only after a validated EOF (stream).
func CodecStream(chunks iter.Seq2[[]byte, error], model string, includeUsage bool, emit func([]byte) error) (err error) {
	defer catch(&err)
	codecStream(chunks, model, includeUsage, emitter(emit))
	return nil
}

func codecStream(chunks iter.Seq2[[]byte, error], model string, includeUsage bool, yield func([]byte)) {
	str(model, "invalid_response", true)
	var identity, reason string
	var usage host.Obj
	var blocks []*codecBlock
	ids := map[string]bool{}
	toolCount := 0
	stopped, deltaStarted := false, false
	created := now().Unix()

	emit := func(delta host.Obj, finish any, finalUsage host.Obj) []byte {
		choices := []any{}
		if finalUsage == nil {
			choices = []any{host.Obj{{Key: "index", Value: 0}, {Key: "delta", Value: delta}, {Key: "finish_reason", Value: finish}}}
		}
		result := host.Obj{{Key: "id", Value: identity}, {Key: "object", Value: "chat.completion.chunk"},
			{Key: "created", Value: created}, {Key: "model", Value: model}, {Key: "choices", Value: choices}}
		if includeUsage {
			if finalUsage == nil {
				result.Set("usage", nil)
			} else {
				result.Set("usage", finalUsage)
			}
		}
		return []byte("data: " + jsonText(result, "invalid_stream") + "\n\n")
	}
	toolDelta := func(index int, function host.Obj) host.Obj {
		return host.Obj{{Key: "tool_calls", Value: []any{host.Obj{{Key: "index", Value: index}, {Key: "function", Value: function}}}}}
	}
	open := func() bool {
		for _, block := range blocks {
			if !block.closed {
				return true
			}
		}
		return false
	}

	startMessage := func(event host.Obj) {
		if identity != "" {
			fail("invalid_stream")
		}
		message, ok := get(event, "message").(host.Obj)
		if c, isList := get(message, "content").([]any); !ok || get(message, "type") != "message" ||
			get(message, "role") != "assistant" || !isList || len(c) != 0 || get(message, "stop_reason") != nil {
			fail("invalid_stream")
		}
		identity = str(get(message, "id"), "invalid_stream", true)
		usage, _ = clone(get(message, "usage")).(host.Obj)
		codecUsage(get(message, "usage"))
		yield(emit(host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: ""}}, nil, nil))
	}
	startBlock := func(event host.Obj, index int64) {
		block, ok := get(event, "content_block").(host.Obj)
		if index != int64(len(blocks)) || !ok {
			fail("invalid_stream")
		}
		state := &codecBlock{kind: str(get(block, "type"), "invalid_stream", false)}
		blocks = append(blocks, state)
		switch state.kind {
		case "text":
			if text := str(get(block, "text"), "invalid_stream", false); text != "" {
				yield(emit(host.Obj{{Key: "content", Value: text}}, nil, nil))
			}
		case "tool_use":
			callID := str(get(block, "id"), "invalid_stream", true)
			toolName := name(get(block, "name"), "invalid_stream")
			input, ok := get(block, "input").(host.Obj)
			if ids[callID] || !ok {
				fail("invalid_stream")
			}
			jsonText(input, "invalid_stream")
			ids[callID] = true
			state.toolIndex, state.initial = toolCount, input
			toolCount++
			yield(emit(host.Obj{{Key: "tool_calls", Value: []any{host.Obj{{Key: "index", Value: state.toolIndex},
				{Key: "id", Value: callID}, {Key: "type", Value: "function"},
				{Key: "function", Value: host.Obj{{Key: "name", Value: toolName}, {Key: "arguments", Value: ""}}}}}}}, nil, nil))
		case "thinking", "redacted_thinking":
		default:
			fail("invalid_stream")
		}
	}
	blockDelta := func(event host.Obj, state *codecBlock) {
		delta, ok := get(event, "delta").(host.Obj)
		if !ok {
			fail("invalid_stream")
		}
		deltaType := str(get(delta, "type"), "invalid_stream", false)
		switch {
		case state.kind == "text" && deltaType == "text_delta":
			yield(emit(host.Obj{{Key: "content", Value: str(get(delta, "text"), "invalid_stream", false)}}, nil, nil))
		case state.kind == "tool_use" && deltaType == "input_json_delta":
			part := str(get(delta, "partial_json"), "invalid_stream", false)
			state.size += len(part)
			if state.size > MaxToolArgumentBytes {
				fail("tool_arguments_too_large")
			}
			if len(state.initial) > 0 {
				fail("invalid_stream")
			}
			if part != "" {
				state.parts = append(state.parts, part)
			}
			yield(emit(toolDelta(state.toolIndex, host.Obj{{Key: "arguments", Value: part}}), nil, nil))
		case state.kind == "thinking" && (deltaType == "thinking_delta" || deltaType == "signature_delta"):
		default:
			fail("invalid_stream")
		}
	}
	blockEvent := func(kind string, event host.Obj) {
		if deltaStarted {
			fail("invalid_stream")
		}
		index := integer(get(event, "index"), "invalid_stream", 0)
		if kind == "content_block_start" {
			startBlock(event, index)
			return
		}
		if index >= int64(len(blocks)) || blocks[index].closed {
			fail("invalid_stream")
		}
		state := blocks[index]
		if kind == "content_block_stop" {
			state.closed = true
			if state.kind == "tool_use" {
				if len(state.parts) > 0 {
					arguments(strings.Join(state.parts, ""))
				} else {
					yield(emit(toolDelta(state.toolIndex, host.Obj{{Key: "arguments", Value: jsonText(state.initial, "invalid_stream")}}), nil, nil))
				}
			}
			return
		}
		blockDelta(event, state)
	}
	messageDelta := func(event host.Obj) {
		if open() {
			fail("invalid_stream")
		}
		deltaStarted = true
		delta, ok1 := get(event, "delta").(host.Obj)
		update, ok2 := get(event, "usage").(host.Obj)
		if !ok1 || !ok2 {
			fail("invalid_stream")
		}
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
			if v, ok := update.Get(key); ok {
				value := integer(v, "invalid_usage", 0)
				previous, _ := host.Int(getd(usage, key, 0))
				if value < previous {
					fail("invalid_usage")
				}
				usage.Set(key, value)
			}
		}
		if !has(update, "output_tokens") {
			fail("invalid_usage")
		}
		if details, ok := update.Get("output_tokens_details"); ok {
			usage.Set("output_tokens_details", clone(details))
		}
		codecUsage(usage)
		if stop := get(delta, "stop_reason"); stop != nil {
			if reason != "" {
				fail("invalid_stream")
			}
			reason = finish(stop)
		}
	}

	frames(chunks, func(event host.Obj) {
		kind := str(get(event, "type"), "invalid_stream", false)
		if stopped {
			fail("invalid_stream")
		}
		if kind == "ping" {
			return
		}
		if kind == "message_start" {
			startMessage(event)
			return
		}
		if identity == "" {
			fail("invalid_stream")
		}
		switch kind {
		case "content_block_start", "content_block_delta", "content_block_stop":
			blockEvent(kind, event)
		case "message_delta":
			messageDelta(event)
		case "message_stop":
			if !deltaStarted || reason == "" || open() {
				fail("truncated_stream")
			}
			if reason == "tool_calls" && toolCount == 0 {
				fail("invalid_stream")
			}
			stopped = true
		default:
			fail("invalid_stream")
		}
	})
	if !stopped {
		fail("truncated_stream")
	}
	yield(emit(host.Obj{}, reason, nil))
	if includeUsage {
		yield(emit(host.Obj{}, nil, codecUsage(usage)))
	}
	yield([]byte("data: [DONE]\n\n"))
}

var errorTypes = map[int]string{401: "authentication_error", 403: "permission_error", 429: "rate_limit_error"}

// CodecError builds the OpenAI error envelope with a safe fixed message, never upstream text
// (error).
func CodecError(message string, status int) (out host.Obj, err error) {
	defer catch(&err)
	if status < 400 || status > 599 {
		fail("invalid_error_status")
	}
	kind, ok := errorTypes[status]
	if !ok {
		kind = "api_error"
		if status < 500 {
			kind = "invalid_request_error"
		}
	}
	code := message
	if !codes[message] {
		code = "upstream_error"
		if status < 500 {
			code = "invalid_request"
		}
	}
	return host.Obj{{Key: "error", Value: host.Obj{{Key: "message", Value: code}, {Key: "type", Value: kind},
		{Key: "param", Value: nil}, {Key: "code", Value: code}}}}, nil
}

// Exported forms of the codec helpers the gateway and provider bridges call.

// Loads is _loads: strict JSON (no duplicate keys, NaN or Infinity) or the given code.
func Loads(value, code string) (out any, err error) {
	defer catch(&err)
	return loads(value, code), nil
}

// JSON is _json: compact non-ASCII JSON text of a valid value, or the given code.
func JSON(value any, code string) (out string, err error) {
	defer catch(&err)
	return jsonText(value, code), nil
}

// Keys is _keys: value must be an object (else code) with only allowed keys (else
// unsupported_field).
func Keys(value any, allowed []string, code string) (out host.Obj, err error) {
	defer catch(&err)
	return keys(value, set(allowed...), code), nil
}

// Arguments is _arguments: tool-call argument text that must decode to a JSON object.
func Arguments(value any) (out host.Obj, err error) {
	defer catch(&err)
	return arguments(value), nil
}

// uuidHex is uuid.uuid4().hex.
func uuidHex() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}
