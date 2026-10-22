package subscription

// Anthropic messages over Codex OAuth.
//
// No login, refresh, redirect, retry, model discovery, or credential export occurs.
// The Codex backend has no max_tokens setting: the required Anthropic field is
// validated but not forwarded. Sampling/stop controls and semantic extensions
// without an equivalent are rejected. Cache hints/metadata have no wire effect.
// Text deltas are forwarded immediately; tool arguments wait for complete JSON
// at item completion. Callers must stop the iterator on disconnect and must never
// retry/fail over after receiving its first chunk.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"iter"
	"math"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Size limits in bytes (MaxEvent counts characters, as Python's len did).
var (
	MaxInput  = 8 * 1024 * 1024
	MaxOutput = 16 * 1024 * 1024
	MaxEvent  = 8 * 1024 * 1024
)

var codexCodes = map[string]int{
	"invalid_request": 400, "unsupported_request": 400, "input_too_large": 413,
	"auth_missing": 401, "auth_expired": 401, "cli_unavailable": 503,
	"upstream_auth": 401, "upstream_forbidden": 403, "upstream_rate_limit": 429,
	"upstream_error": 502, "upstream_protocol": 502, "upstream_truncated": 502,
	"output_too_large": 502, "deadline_exceeded": 504,
}

var (
	accountRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
	versionRe = regexp.MustCompile(`^codex(?:-cli)? ([0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?)[ \t\n\r\f\v]*$`)
	base64Re  = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

	// Test seams.
	version = cliVersion
	newID   = func() string {
		b := make([]byte, 16)
		rand.Read(b)
		b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80 // uuid4
		return hex.EncodeToString(b)
	}
	versions sync.Map // executable -> version; failures are not cached
)

// CodexError carries only a fixed code and status across the provider boundary.
type CodexError struct {
	Code       string
	Status     int
	RetryAfter float64 // seconds; < 0 means none
}

func (e *CodexError) Error() string { return e.Code }

// NewCodexError maps unknown codes to upstream_error and keeps retryAfter only within 0 < x <= 604800.
func NewCodexError(code string, retryAfter float64) *CodexError {
	if _, ok := codexCodes[code]; !ok {
		code = "upstream_error"
	}
	if !(retryAfter > 0 && retryAfter <= 604800) {
		retryAfter = -1
	}
	return &CodexError{code, codexCodes[code], retryAfter}
}

func fail(code string) { panic(NewCodexError(code, -1)) }

func compact(v any) string {
	s, err := host.Dumps(v, host.DumpOpts{Compact: true, NoASCII: true})
	if err != nil {
		fail("invalid_request")
	}
	return s
}

// Redact replaces token in every string of v, keys included.
func Redact(v any, token string) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, token, "***")
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = Redact(item, token)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[strings.ReplaceAll(k, token, "***")] = Redact(item, token)
		}
		return out
	}
	return v
}

func checkKeys(v any, allowed ...string) map[string]any {
	obj, ok := v.(map[string]any)
	if !ok {
		fail("invalid_request")
	}
	for k := range obj {
		if !slices.Contains(allowed, k) {
			fail("unsupported_request")
		}
	}
	return obj
}

func str(v any, empty bool) string {
	s, ok := v.(string)
	if !ok || (!empty && s == "") {
		fail("invalid_request")
	}
	return s
}

func get(obj map[string]any, key string, def any) any {
	if v, ok := obj[key]; ok {
		return v
	}
	return def
}

func parts(content any) []any {
	switch x := content.(type) {
	case string:
		return []any{map[string]any{"type": "text", "text": x}}
	case []any:
		return x
	}
	fail("invalid_request")
	return nil
}

// imageURLAllowed mirrors Python's urlsplit checks: https, a hostname, and no user name or
// password. A malformed bracketed host panics invalid_request, as urlsplit's ValueError did.
func imageURLAllowed(raw string) bool {
	s := strings.TrimLeftFunc(raw, func(r rune) bool { return r <= ' ' })
	s = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(s)
	scheme, rest, ok := strings.Cut(s, ":")
	if !ok || strings.ToLower(scheme) != "https" {
		return false
	}
	if rest, ok = strings.CutPrefix(rest, "//"); !ok {
		return false
	}
	netloc := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		netloc = rest[:i]
	}
	if strings.Contains(netloc, "[") != strings.Contains(netloc, "]") {
		fail("invalid_request")
	}
	userinfo, hostinfo := "", netloc
	i := strings.LastIndex(netloc, "@")
	if i >= 0 {
		userinfo, hostinfo = netloc[:i], netloc[i+1:]
	}
	if before, bracketed, found := strings.Cut(hostinfo, "["); found {
		inner, port, _ := strings.Cut(bracketed, "]")
		ip, err := netip.ParseAddr(inner)
		if before != "" || (port != "" && !strings.HasPrefix(port, ":")) || ((err != nil || !ip.Is6()) && !ipvFutureRe.MatchString(inner)) {
			fail("invalid_request")
		}
		hostinfo = inner
	} else {
		hostinfo, _, _ = strings.Cut(hostinfo, ":")
	}
	name, password, _ := strings.Cut(userinfo, ":")
	return hostinfo != "" && name == "" && password == ""
}

func translatePart(v any, assistant bool) map[string]any {
	block, ok := v.(map[string]any)
	if !ok {
		fail("invalid_request")
	}
	if block["type"] == "text" {
		checkKeys(block, "type", "text", "cache_control")
		kind := "input_text"
		if assistant {
			kind = "output_text"
		}
		return map[string]any{"type": kind, "text": str(block["text"], true)}
	}
	if block["type"] != "image" || assistant {
		fail("unsupported_request")
	}
	checkKeys(block, "type", "source", "cache_control")
	source, ok := block["source"].(map[string]any)
	if !ok {
		fail("invalid_request")
	}
	var url string
	switch source["type"] {
	case "base64":
		checkKeys(source, "type", "media_type", "data")
		media, _ := source["media_type"].(string)
		if !slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, media) {
			fail("unsupported_request")
		}
		data := str(source["data"], false)
		if _, err := base64.StdEncoding.DecodeString(data); err != nil || !base64Re.MatchString(data) {
			fail("invalid_request")
		}
		url = "data:" + media + ";base64," + data
	case "url":
		checkKeys(source, "type", "url")
		url = str(source["url"], false)
		if !imageURLAllowed(url) {
			fail("unsupported_request")
		}
	default:
		fail("unsupported_request")
	}
	return map[string]any{"type": "input_image", "image_url": url}
}

// TranslateRequest turns an Anthropic messages body into a Codex responses request without
// mutating it; unsupported semantics fail closed with a *CodexError.
func TranslateRequest(body, model any) (out map[string]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*CodexError); ok {
				err = e
			} else {
				err = NewCodexError("invalid_request", -1)
			}
		}
	}()
	return translate(body, model), nil
}

func translate(v, model any) map[string]any {
	if len(compact(v)) > MaxInput {
		fail("input_too_large")
	}
	body := checkKeys(v, "model", "messages", "system", "tools", "tool_choice", "stream", "max_tokens", "metadata")
	str(model, false)
	if s, ok := body["stream"]; ok {
		if _, isBool := s.(bool); !isBool {
			fail("invalid_request")
		}
	}
	if m, ok := body["max_tokens"]; ok {
		if n, isInt := host.Int(m); !isInt || n <= 0 {
			fail("invalid_request")
		}
	}
	var instructions []string
	for _, block := range parts(get(body, "system", "")) {
		part := translatePart(block, false)
		if part["type"] != "input_text" {
			fail("unsupported_request")
		}
		instructions = append(instructions, part["text"].(string))
	}
	out := map[string]any{"model": model, "instructions": strings.Join(instructions, "\n"), "stream": true, "store": false}
	out["input"] = translateMessages(body)
	names := translateTools(body, out)
	if raw, ok := body["tool_choice"]; ok {
		translateToolChoice(out, raw, names)
	}
	if len(compact(out)) > MaxInput {
		fail("input_too_large")
	}
	return out
}

func translateMessages(body map[string]any) []any {
	input := []any{}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		fail("invalid_request")
	}
	calls, results := map[string]bool{}, map[string]bool{}
	for _, raw := range messages {
		input = translateMessage(input, raw, calls, results)
	}
	return input
}

func translateMessage(input []any, raw any, calls, results map[string]bool) []any {
	current := -1 // index in input of this message's open text message
	message := checkKeys(raw, "role", "content")
	role := message["role"]
	if role != "user" && role != "assistant" {
		fail("unsupported_request")
	}
	for _, item := range parts(message["content"]) {
		block, ok := item.(map[string]any)
		if !ok {
			fail("invalid_request")
		}
		switch block["type"] {
		case "tool_use":
			input = append(input, translateToolUse(block, role, calls))
		case "tool_result":
			input = append(input, translateToolResult(block, role, calls, results))
		default:
			part := translatePart(block, role == "assistant")
			// Keep adjacent parts together, but never cross message boundaries.
			if current >= 0 && current == len(input)-1 {
				last := input[current].(map[string]any)
				last["content"] = append(last["content"].([]any), part)
			} else {
				current = len(input)
				input = append(input, map[string]any{"type": "message", "role": role, "content": []any{part}})
			}
		}
	}
	return input
}

func translateToolUse(block map[string]any, role any, calls map[string]bool) map[string]any {
	checkKeys(block, "type", "id", "name", "input", "cache_control")
	call := str(block["id"], false)
	args, isObj := block["input"].(map[string]any)
	if role != "assistant" || calls[call] || !isObj {
		fail("invalid_request")
	}
	calls[call] = true
	name := str(block["name"], false)
	return map[string]any{"type": "function_call", "call_id": call, "name": name, "arguments": compact(args)}
}

func translateToolResult(block map[string]any, role any, calls, results map[string]bool) map[string]any {
	checkKeys(block, "type", "tool_use_id", "content", "is_error", "cache_control")
	call := str(block["tool_use_id"], false)
	if role != "user" || !calls[call] || results[call] {
		fail("invalid_request")
	}
	isError, has := block["is_error"]
	if _, isBool := isError.(bool); has && !isBool {
		fail("invalid_request")
	}
	results[call] = true
	var output any
	if text, isStr := get(block, "content", "").(string); isStr {
		output = text
	} else {
		converted := []any{}
		for _, p := range parts(block["content"]) {
			converted = append(converted, translatePart(p, false))
		}
		output = converted
	}
	if isError == true {
		marked := []any{map[string]any{"type": "input_text", "text": "[tool_result is_error=true]"}}
		if text, isStr := output.(string); isStr {
			output = append(marked, map[string]any{"type": "input_text", "text": text})
		} else {
			output = append(marked, output.([]any)...)
		}
	}
	return map[string]any{"type": "function_call_output", "call_id": call, "output": output}
}

// translateTools sets out["tools"] and returns the declared tool names.
func translateTools(body, out map[string]any) map[string]bool {
	tools, ok := get(body, "tools", []any{}).([]any)
	if !ok {
		fail("invalid_request")
	}
	names := map[string]bool{}
	var converted []any
	for _, raw := range tools {
		tool := checkKeys(raw, "type", "name", "description", "input_schema", "cache_control")
		if get(tool, "type", "custom") != "custom" {
			fail("unsupported_request")
		}
		name := str(tool["name"], false)
		schema, isObj := tool["input_schema"].(map[string]any)
		if names[name] || !isObj {
			fail("invalid_request")
		}
		names[name] = true
		converted = append(converted, map[string]any{"type": "function", "name": name, "strict": false,
			"description": str(get(tool, "description", ""), true), "parameters": schema})
	}
	if len(tools) > 0 {
		out["tools"] = converted
	}
	return names
}

func translateToolChoice(out map[string]any, raw any, names map[string]bool) {
	choice := checkKeys(raw, "type", "name", "disable_parallel_tool_use")
	kind := choice["type"]
	_, named := choice["name"]
	name, nameStr := choice["name"].(string)
	switch {
	case (kind == "auto" || kind == "none" || kind == "any") && !named:
		out["tool_choice"] = kind
		if kind == "any" {
			out["tool_choice"] = "required"
		}
	case kind == "tool" && nameStr && names[name]:
		out["tool_choice"] = map[string]any{"type": "function", "name": name}
	case kind == "tool" && !nameStr && named && isContainer(choice["name"]):
		fail("invalid_request") // an unhashable name raised TypeError
	default:
		fail("unsupported_request")
	}
	if (kind == "tool" || kind == "any") && len(names) == 0 {
		fail("invalid_request")
	}
	if v, ok := choice["disable_parallel_tool_use"]; ok {
		b, isBool := v.(bool)
		if !isBool {
			fail("invalid_request")
		}
		out["parallel_tool_calls"] = !b
	}
}

func isContainer(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return true
	}
	return false
}

// cliVersion runs `executable --version`; only the installed executable supplies the version.
func cliVersion(executable string, timeout time.Duration) string {
	if v, ok := versions.Load(executable); ok {
		return v.(string)
	}
	out, _, code, err := runBounded(exec.Command(executable, "--version"), false, false, 4096, timeout)
	m := versionRe.FindSubmatch(out)
	if err != nil || code != 0 || m == nil {
		fail("cli_unavailable")
	}
	versions.Store(executable, string(m[1]))
	return string(m[1])
}

func codexAuth(configDir string) (string, string) {
	login, token := Login("codex", configDir)
	if token == "" {
		if login["status"] == "expired" {
			fail("auth_expired")
		}
		fail("auth_missing")
	}
	account, _ := login["account_id"].(string)
	if !accountRe.MatchString(account) || account == token {
		fail("auth_missing")
	}
	return token, account
}

// sseReader yields Server-Sent Events lazily, bounded in bytes and event size.
type sseReader struct {
	body        io.Reader
	length      int64 // Content-Length, < 0 when unknown
	r           *codexRun
	raw         []byte // undecoded tail (an incomplete UTF-8 sequence)
	text        string
	lines       []string
	total, size int
	eof         bool
	buf         []byte
}

// next returns the next event, done=true for [DONE], or ok=false at a clean end.
func (s *sseReader) next() (event map[string]any, done, ok bool) {
	for {
		if line, rest, found := strings.Cut(s.text, "\n"); found {
			s.text = rest
			line = strings.TrimRight(line, "\r")
			s.size += utf8.RuneCountInString(line)
			if s.size > MaxEvent {
				fail("output_too_large")
			}
			switch {
			case line == "":
				lines := s.lines
				s.lines, s.size = nil, 0
				if len(lines) == 0 {
					continue
				}
				raw := strings.Join(lines, "\n")
				if raw == "[DONE]" {
					return nil, true, true
				}
				v, err := StrictJSON([]byte(raw))
				obj, isObj := v.(map[string]any)
				if _, typed := obj["type"].(string); err != nil || !isObj || !typed {
					fail("upstream_protocol")
				}
				return obj, false, true
			case strings.HasPrefix(line, "data:"):
				s.lines = append(s.lines, strings.TrimPrefix(line[5:], " "))
			case !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, "id:") && !strings.HasPrefix(line, "retry:"):
				fail("upstream_protocol")
			}
			continue
		}
		if utf8.RuneCountInString(s.text)+s.size > MaxEvent {
			fail("output_too_large")
		}
		if s.eof {
			if s.truncated() {
				fail("upstream_truncated")
			}
			return nil, false, false
		}
		s.read()
	}
}

// truncated reports leftover or missing bytes at end of body.
func (s *sseReader) truncated() bool {
	return s.text != "" || len(s.lines) > 0 || s.size > 0 || (s.length >= 0 && int64(s.total) != s.length)
}

func (s *sseReader) read() {
	s.r.remaining()
	n, err := s.body.Read(s.buf)
	s.total += n
	if s.total > MaxOutput {
		fail("output_too_large")
	}
	s.raw = append(s.raw, s.buf[:n]...)
	if err == io.EOF {
		s.eof = true
	} else if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) && s.r.ctx.Err() == nil {
			fail("upstream_truncated")
		}
		s.r.netFail(err)
	}
	// Strict incremental UTF-8: keep an unfinished sequence for the next read.
	valid := 0
	for valid < len(s.raw) {
		r, size := utf8.DecodeRune(s.raw[valid:])
		if r == utf8.RuneError && size == 1 {
			if !s.eof && !utf8.FullRune(s.raw[valid:]) {
				break
			}
			fail("upstream_protocol")
		}
		valid += size
	}
	s.text += string(s.raw[:valid])
	s.raw = append(s.raw[:0], s.raw[valid:]...)
}

type textState struct {
	index                                int
	kind                                 string
	chunks                               []string
	delta, hasFinal, added               bool
	final, tail                          string
	started, stopped, textDone, partDone bool
}

type itemMeta struct {
	id, kind     string
	contentIndex int
}

type codexRun struct {
	ctx      context.Context
	deadline time.Time
	armed    bool // the request is under way, so an expired deadline explains any failure
	token    string
	stream   bool
	message  map[string]any
	content  []any
	items    map[int][]any
	meta     map[int]*itemMeta
	parts    map[[2]int]*textState
	callIDs  map[string]bool
	pending  map[int]bool
	partial  map[int]bool
}

func (r *codexRun) remaining() time.Duration {
	left := time.Until(r.deadline)
	if left <= 0 {
		fail("deadline_exceeded")
	}
	return left
}

func (r *codexRun) netFail(err error) {
	var ne net.Error
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		fail("deadline_exceeded")
	}
	fail("upstream_error")
}

func sse(kind string, fields map[string]any) []byte {
	fields["type"] = kind
	return []byte("event: " + kind + "\ndata: " + compact(fields) + "\n\n")
}

func index(v any) int {
	n, ok := host.Int(v)
	if !ok || n < 0 || n > math.MaxInt32 {
		fail("upstream_protocol")
	}
	return int(n)
}

// identity records an output item's id and type, refusing changes.
func (r *codexRun) identity(i int, id, kind any) {
	m := r.meta[i]
	if m == nil {
		m = &itemMeta{contentIndex: -1}
		r.meta[i] = m
	}
	for _, field := range []struct {
		value any
		known *string
	}{{id, &m.id}, {kind, &m.kind}} {
		if field.value == nil {
			continue
		}
		s, ok := field.value.(string)
		if !ok || s == "" || (*field.known != "" && *field.known != s) {
			fail("upstream_protocol")
		}
		*field.known = s
	}
}

// content converts a finished output item into Anthropic blocks.
func content(v any, allowIncomplete bool) []any {
	item, ok := v.(map[string]any)
	if !ok {
		fail("upstream_protocol")
	}
	if status := get(item, "status", "completed"); status != "completed" && !(allowIncomplete && item["type"] == "message" && status == "incomplete") {
		fail("upstream_protocol")
	}
	switch item["type"] {
	case "reasoning":
		// Internal provider reasoning is not an Anthropic thinking/signature block.
		return []any{}
	case "function_call":
		return toolUse(item)
	}
	list, isList := item["content"].([]any)
	if item["type"] != "message" || item["role"] != "assistant" || !isList {
		fail("upstream_protocol")
	}
	out := []any{}
	for _, raw := range list {
		part, ok := raw.(map[string]any)
		if !ok || (part["type"] != "output_text" && part["type"] != "refusal") {
			fail("upstream_protocol")
		}
		field := "refusal"
		if part["type"] == "output_text" {
			field = "text"
		}
		value, isStr := part[field].(string)
		if !isStr || truthy(part["annotations"]) {
			fail("upstream_protocol")
		}
		out = append(out, map[string]any{"type": "text", "text": value})
	}
	return out
}

// toolUse converts a finished function_call item into a tool_use block.
func toolUse(item map[string]any) []any {
	for _, k := range []string{"call_id", "name", "arguments"} {
		if s, ok := item[k].(string); !ok || s == "" {
			fail("upstream_protocol")
		}
	}
	v, err := StrictJSON([]byte(item["arguments"].(string)))
	args, isObj := v.(map[string]any)
	if err != nil || !isObj {
		fail("upstream_protocol")
	}
	return []any{map[string]any{"type": "tool_use", "id": item["call_id"], "name": item["name"], "input": args}}
}

func asMessage(part any) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "content": []any{part}}
}

func (r *codexRun) textPart(i int, contentIndex any, kind string) *textState {
	ci, ok := host.Int(contentIndex)
	if !ok || ci < 0 || ci > math.MaxInt32 {
		fail("upstream_protocol")
	}
	r.identity(i, nil, "message")
	r.meta[i].contentIndex = max(r.meta[i].contentIndex, int(ci))
	key := [2]int{i, int(ci)}
	state := r.parts[key]
	if state == nil {
		state = &textState{index: len(r.content), kind: kind}
		r.parts[key] = state
		r.content = append(r.content, map[string]any{"type": "text", "text": ""})
	}
	if state.kind != kind {
		fail("upstream_protocol")
	}
	return state
}

func (r *codexRun) startPart(state *textState) [][]byte {
	if r.stream && !state.started {
		state.started = true
		return [][]byte{sse("content_block_start", map[string]any{"index": state.index, "content_block": map[string]any{"type": "text", "text": ""}})}
	}
	return nil
}

// safeText redacts the token, holding back a tail that could be the start of one.
func (r *codexRun) safeText(state *textState, value string, final bool) string {
	value = strings.ReplaceAll(state.tail+value, r.token, "***")
	keep := 0
	if !final {
		for n := min(len(r.token)-1, len(value)); n > 0; n-- {
			if strings.HasSuffix(value, r.token[:n]) {
				keep = n
				break
			}
		}
	}
	state.tail = value[len(value)-keep:]
	return value[:len(value)-keep]
}

func (r *codexRun) textDelta(state *textState, text string) []byte {
	return sse("content_block_delta", map[string]any{"index": state.index, "delta": map[string]any{"type": "text_delta", "text": text}})
}

func (r *codexRun) finishPart(i int, contentIndex, part any, stop bool) [][]byte {
	block := content(asMessage(part), false)[0].(map[string]any)
	state := r.textPart(i, contentIndex, part.(map[string]any)["type"].(string))
	value := block["text"].(string)
	if state.hasFinal {
		if value != state.final {
			fail("upstream_protocol")
		}
	} else {
		if state.delta && value != strings.Join(state.chunks, "") {
			fail("upstream_protocol")
		}
		state.final, state.hasFinal, state.chunks = value, true, nil
		r.content[state.index] = block
	}
	chunks := r.startPart(state)
	if r.stream && !state.delta && !state.stopped && !state.textDone {
		chunks = append(chunks, r.textDelta(state, r.safeText(state, value, true)))
	} else if r.stream && state.tail != "" {
		chunks = append(chunks, r.textDelta(state, r.safeText(state, "", true)))
	}
	if stop && !state.stopped {
		state.stopped = true
		if r.stream {
			chunks = append(chunks, sse("content_block_stop", map[string]any{"index": state.index}))
		}
	}
	return chunks
}

func (r *codexRun) finishItem(i int, raw any, allowIncomplete bool) [][]byte {
	converted := content(raw, allowIncomplete)
	item := raw.(map[string]any)
	if item["status"] == "incomplete" {
		r.partial[i] = true
	}
	r.identity(i, item["id"], item["type"])
	if _, seen := r.items[i]; seen {
		fail("upstream_protocol")
	}
	var list []any
	if item["type"] == "message" {
		list = item["content"].([]any)
	}
	if r.meta[i].contentIndex >= len(list) {
		fail("upstream_protocol")
	}
	var chunks [][]byte
	if item["type"] == "message" {
		for ci, part := range list {
			chunks = append(chunks, r.finishPart(i, ci, part, true)...)
		}
	} else if len(converted) > 0 {
		block := converted[0].(map[string]any)
		id := block["id"].(string)
		if r.callIDs[id] {
			fail("upstream_protocol")
		}
		r.callIDs[id] = true
		if r.stream {
			chunks = append(chunks, r.blocks(Redact(converted, r.token).([]any), len(r.content))...)
		}
		r.content = append(r.content, converted...)
	}
	r.items[i] = converted
	delete(r.pending, i)
	return chunks
}

func (r *codexRun) blocks(blocks []any, offset int) [][]byte {
	var out [][]byte
	for n, raw := range blocks {
		block := raw.(map[string]any)
		var start, delta map[string]any
		if block["type"] == "text" {
			start = map[string]any{"type": "text", "text": ""}
			delta = map[string]any{"type": "text_delta", "text": block["text"]}
		} else {
			start = maps2(block, "input", map[string]any{})
			delta = map[string]any{"type": "input_json_delta", "partial_json": compact(block["input"])}
		}
		i := offset + n
		for _, chunk := range [][]byte{
			sse("content_block_start", map[string]any{"index": i, "content_block": start}),
			sse("content_block_delta", map[string]any{"index": i, "delta": delta}),
			sse("content_block_stop", map[string]any{"index": i}),
		} {
			r.remaining()
			out = append(out, chunk)
		}
	}
	return out
}

// maps2 is {**m, key: value}.
func maps2(m map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[key] = value
	return out
}

var passEvents = []string{"response.created", "response.in_progress", "response.queued",
	"response.output_item.added", "response.content_part.added", "response.content_part.done",
	"response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done",
	"response.function_call_arguments.delta", "response.function_call_arguments.done",
	"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
	"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done"}

// retryAfter reads a 429's Retry-After as seconds, or -1.
func retryAfter(value string) float64 {
	if value != "" && strings.Trim(value, "0123456789") == "" {
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return -1
		}
		return n
	}
	t, err := mail.ParseDate(value)
	if err != nil {
		if t, err = http.ParseTime(value); err != nil {
			return -1
		}
	}
	return float64(t.UnixNano())/1e9 - unixNow()
}

// IterMessages yields Anthropic SSE chunks (body "stream" true) or one JSON message. A failure
// ends the sequence with a nil chunk and a *CodexError; nothing is retried. Stopping early
// closes the connection.
func IterMessages(body map[string]any, configDir, executable string, timeout float64) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		r := &codexRun{}
		var resp *http.Response
		inYield := false
		stopped := errors.New("stopped")
		emit := func(chunk []byte) {
			inYield = true
			ok := yield(chunk, nil)
			inYield = false
			if !ok {
				panic(stopped)
			}
		}
		defer func() {
			if resp != nil {
				resp.Body.Close()
			}
			rec := recover()
			if rec == nil || rec == stopped {
				return
			}
			if inYield {
				panic(rec) // the caller's panic, not ours
			}
			e, ok := rec.(*CodexError)
			if !ok {
				// Provider/parser failures are private.
				e = NewCodexError("upstream_protocol", -1)
			}
			if r.armed && errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
				e = NewCodexError("deadline_exceeded", -1)
			}
			yield(nil, e)
		}()
		if timeout <= 0 || math.IsNaN(timeout) || math.IsInf(timeout, 0) {
			fail("invalid_request")
		}
		r.deadline = time.Now().Add(time.Duration(timeout * float64(time.Second)))
		ctx, cancel := context.WithDeadline(context.Background(), r.deadline)
		defer cancel()
		r.ctx = ctx
		model := requestModel(body)
		request, err := TranslateRequest(body, model)
		if err != nil {
			panic(err)
		}
		token, account := codexAuth(configDir)
		r.token = token
		ver := version(executable, min(2*time.Second, r.remaining()))
		r.remaining()
		resp = r.send(request, account, ver)
		r.remaining()
		checkResponse(resp)
		r.stream = truthy(body["stream"])
		r.message = map[string]any{"id": "msg_" + newID(), "type": "message", "role": "assistant", "model": model,
			"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}
		r.content = []any{}
		r.items, r.meta, r.parts = map[int][]any{}, map[int]*itemMeta{}, map[[2]int]*textState{}
		r.callIDs, r.pending, r.partial = map[string]bool{}, map[int]bool{}, map[int]bool{}
		started, terminal, sawDone := false, "", false
		events := &sseReader{body: resp.Body, length: resp.ContentLength, r: r, buf: make([]byte, 8192)}
		for {
			event, done, ok := events.next()
			if !ok {
				break
			}
			r.remaining()
			if done {
				if terminal == "" {
					fail("upstream_truncated")
				}
				if sawDone {
					fail("upstream_protocol")
				}
				sawDone = true
				continue
			}
			var chunks [][]byte
			chunks, terminal = r.event(event, terminal)
			if r.stream && (len(chunks) > 0 || terminal != "") {
				if !started {
					start := maps2(r.message, "content", []any{})
					start["stop_reason"] = nil
					start["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0}
					emit(sse("message_start", map[string]any{"message": start}))
					started = true
				}
				for _, chunk := range chunks {
					r.remaining()
					emit(chunk)
				}
			}
		}
		if terminal == "" {
			fail("upstream_truncated")
		}
		if terminal == "response.completed" {
			r.message["stop_reason"] = "end_turn"
			if len(r.callIDs) > 0 {
				r.message["stop_reason"] = "tool_use"
			}
		}
		r.remaining()
		if r.stream {
			emit(sse("message_delta", map[string]any{"delta": map[string]any{"stop_reason": r.message["stop_reason"], "stop_sequence": nil}, "usage": r.message["usage"]}))
			r.remaining()
			emit(sse("message_stop", map[string]any{}))
		} else {
			emit([]byte(compact(Redact(maps2(r.message, "content", r.content), token))))
		}
	}
}

func requestModel(body map[string]any) any {
	var model any
	if body != nil {
		model = body["model"]
	}
	if s, ok := model.(string); ok {
		model = strings.TrimPrefix(s, "openai/")
	}
	return model
}

// send posts the request; a returned response's body is the caller's to close.
func (r *codexRun) send(request map[string]any, account, ver string) *http.Response {
	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, CodexBase+"/backend-api/codex/responses", strings.NewReader(compact(request)))
	if err != nil {
		fail("upstream_error")
	}
	for k, v := range map[string]string{"Authorization": "Bearer " + r.token, "chatgpt-account-id": account,
		"OpenAI-Beta": "responses=experimental", "originator": "codex_cli_rs", "User-Agent": "codex_cli_rs/" + ver,
		"version": ver, "Content-Type": "application/json", "Accept": "text/event-stream", "Accept-Encoding": "identity"} {
		req.Header[k] = []string{v} // exact names, as http.client sent them
	}
	r.armed = true
	resp, err := Client.Do(req)
	if err != nil {
		r.netFail(err)
	}
	return resp
}

func checkResponse(resp *http.Response) {
	if resp.StatusCode != http.StatusOK {
		code := map[int]string{401: "upstream_auth", 403: "upstream_forbidden", 429: "upstream_rate_limit"}[resp.StatusCode]
		wait := -1.0
		if resp.StatusCode == http.StatusTooManyRequests {
			wait = retryAfter(resp.Header.Get("Retry-After"))
		}
		panic(NewCodexError(code, wait))
	}
	ctype, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	if strings.ToLower(strings.TrimSpace(ctype)) != "text/event-stream" {
		fail("upstream_protocol")
	}
}

// event handles one upstream event and returns its chunks and the terminal kind seen so far.
func (r *codexRun) event(event map[string]any, terminal string) ([][]byte, string) {
	kind := event["type"].(string)
	if kind == "error" || kind == "response.failed" || truthy(event["error"]) {
		failEvent(event)
	}
	if terminal != "" {
		fail("upstream_protocol")
	}
	var chunks [][]byte
	switch {
	case kind == "response.output_item.done":
		n, isInt := host.Int(event["output_index"])
		if _, known := r.meta[int(n)]; !isInt || n < 0 || n > math.MaxInt32 || (int(n) > len(r.items) && !known) {
			fail("upstream_protocol")
		}
		chunks = r.finishItem(int(n), event["item"], true)
	case kind == "response.completed" || kind == "response.incomplete":
		chunks = r.terminal(event, kind)
		terminal = kind
	case !slices.Contains(passEvents, kind):
		fail("upstream_protocol")
	case kind != "response.created" && kind != "response.in_progress" && kind != "response.queued":
		chunks = r.progress(event, kind)
	}
	return chunks, terminal
}

// failEvent fails with the error an upstream error event reports.
func failEvent(event map[string]any) {
	problem := event["error"]
	if !truthy(problem) {
		problem = nil
		if res, isObj := event["response"].(map[string]any); isObj {
			problem = res["error"]
		}
	}
	if e, isObj := problem.(map[string]any); isObj && (e["code"] == "rate_limit_exceeded" || e["code"] == "usage_limit_reached") {
		fail("upstream_rate_limit")
	}
	fail("upstream_error")
}

// terminal checks a response.completed / response.incomplete event and fills in the message.
func (r *codexRun) terminal(event map[string]any, kind string) [][]byte {
	result, ok := event["response"].(map[string]any)
	incomplete := kind == "response.incomplete"
	if !ok || truthy(result["error"]) || result["status"] != strings.TrimPrefix(kind, "response.") {
		fail("upstream_error")
	}
	r.incomplete(result, incomplete)
	output, ok := get(result, "output", []any{}).([]any)
	if !ok {
		fail("upstream_protocol")
	}
	var chunks [][]byte
	for i, item := range output {
		converted := content(item, incomplete)
		if seen, done := r.items[i]; done {
			obj := item.(map[string]any)
			r.identity(i, obj["id"], obj["type"])
			if host.MustDumps(converted, host.DumpOpts{}) != host.MustDumps(seen, host.DumpOpts{}) {
				fail("upstream_protocol")
			}
		} else {
			chunks = append(chunks, r.finishItem(i, item, incomplete)...)
		}
	}
	if len(output) > 0 && len(output) < len(r.items) {
		fail("upstream_protocol")
	}
	for i := range r.pending {
		if i >= len(output) {
			fail("upstream_truncated")
		}
	}
	for i := range r.items {
		if i >= len(r.items) {
			fail("upstream_truncated")
		}
	}
	r.message["usage"] = usageCounts(result)
	return chunks
}

// incomplete checks incomplete_details and sets the stop reason of an incomplete response.
func (r *codexRun) incomplete(result map[string]any, incomplete bool) {
	if incomplete {
		details, isObj := result["incomplete_details"].(map[string]any)
		if !isObj || (details["reason"] != "max_output_tokens" && details["reason"] != "content_filter") {
			fail("upstream_protocol")
		}
		r.message["stop_reason"] = "refusal"
		if details["reason"] == "max_output_tokens" {
			r.message["stop_reason"] = "max_tokens"
		}
	} else if len(r.partial) > 0 || truthy(result["incomplete_details"]) {
		fail("upstream_protocol")
	}
}

// usageCounts validates a terminal response's token usage.
func usageCounts(result map[string]any) map[string]any {
	usage, ok := result["usage"].(map[string]any)
	if !ok {
		fail("upstream_protocol")
	}
	counts := map[string]any{}
	for _, k := range []string{"input_tokens", "output_tokens"} {
		n, isInt := host.Int(usage[k])
		if !isInt || n < 0 {
			fail("upstream_protocol")
		}
		counts[k] = n
	}
	if details := usage["output_tokens_details"]; details != nil {
		obj, isObj := details.(map[string]any)
		if !isObj {
			fail("upstream_protocol")
		}
		if reasoning := obj["reasoning_tokens"]; reasoning != nil {
			n, isInt := host.Int(reasoning)
			if !isInt || n < 0 || n > counts["output_tokens"].(int64) {
				fail("upstream_protocol")
			}
			counts["output_tokens_details"] = map[string]any{"reasoning_tokens": n}
		}
	}
	return counts
}

// progress handles the streaming events before an item finishes.
func (r *codexRun) progress(event map[string]any, kind string) [][]byte {
	i := index(event["output_index"])
	r.identity(i, event["item_id"], nil)
	if _, done := r.items[i]; done {
		fail("upstream_protocol")
	}
	delta, isStr := event["delta"].(string)
	if strings.HasSuffix(kind, ".delta") && !isStr {
		fail("upstream_protocol")
	}
	r.pending[i] = true
	switch kind {
	case "response.output_item.added":
		item, ok := event["item"].(map[string]any)
		if !ok || (item["type"] != "message" && item["type"] != "function_call" && item["type"] != "reasoning") {
			fail("upstream_protocol")
		}
		if item["type"] == "message" && item["role"] != "assistant" {
			fail("upstream_protocol")
		}
		r.identity(i, item["id"], item["type"])
	case "response.output_text.delta", "response.refusal.delta":
		return r.progressDelta(i, event, kind, delta)
	case "response.output_text.done", "response.refusal.done", "response.content_part.done":
		return r.progressDone(i, event, kind)
	case "response.content_part.added":
		part := event["part"]
		content(asMessage(part), false)
		state := r.textPart(i, event["content_index"], part.(map[string]any)["type"].(string))
		if state.added || state.delta || state.hasFinal {
			fail("upstream_protocol")
		}
		state.added = true
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		r.identity(i, nil, "function_call")
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		r.identity(i, nil, "reasoning")
	}
	return nil
}

// progressDelta streams an output_text or refusal delta.
func (r *codexRun) progressDelta(i int, event map[string]any, kind, delta string) [][]byte {
	partType := "refusal"
	if kind == "response.output_text.delta" {
		partType = "output_text"
	}
	state := r.textPart(i, event["content_index"], partType)
	if state.textDone || state.stopped {
		fail("upstream_protocol")
	}
	state.delta = true
	state.chunks = append(state.chunks, delta)
	chunks := r.startPart(state)
	if r.stream {
		if safe := r.safeText(state, delta, false); safe != "" || delta == "" {
			chunks = append(chunks, r.textDelta(state, safe))
		}
	}
	return chunks
}

// progressDone finishes a text, refusal or content part, each at most once.
func (r *codexRun) progressDone(i int, event map[string]any, kind string) [][]byte {
	part := event["part"]
	switch kind {
	case "response.output_text.done":
		part = map[string]any{"type": "output_text", "text": event["text"]}
	case "response.refusal.done":
		part = map[string]any{"type": "refusal", "refusal": event["refusal"]}
	}
	chunks := r.finishPart(i, event["content_index"], part, kind == "response.content_part.done")
	ci, _ := host.Int(event["content_index"])
	state := r.parts[[2]int{i, int(ci)}]
	flag := &state.textDone
	if kind == "response.content_part.done" {
		flag = &state.partDone
	}
	if *flag {
		fail("upstream_protocol")
	}
	*flag = true
	return chunks
}
