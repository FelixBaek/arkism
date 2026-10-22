package gateway

import (
	"iter"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Native Gemini pages -> Chat. Completion is committed only
// after a real EOF.

// MaxOutput bounds the total upstream pages and the emitted Chat output.
var MaxOutput = 16 << 20

var (
	googleFilters = set("SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII")
	googleCounts  = []string{"promptTokenCount", "candidatesTokenCount", "totalTokenCount",
		"thoughtsTokenCount", "cachedContentTokenCount"}
)

// googlePages validates each page and its size before handing it on.
func googlePages(pages iter.Seq2[any, error], streaming bool, fn func(host.Obj)) {
	total := 0
	for page, err := range pages {
		if err != nil {
			fail("upstream_stream_error")
		}
		o, ok := page.(host.Obj)
		if !ok || has(o, "error") {
			fail("invalid_response")
		}
		size := len(jsonText(o, "invalid_response"))
		total += size
		if total > MaxOutput {
			fail("upstream_too_large")
		}
		if streaming && size > MaxFrameBytes {
			fail("stream_frame_too_large")
		}
		fn(o)
	}
}

func googleUsage(value any) (map[string]int64, host.Obj) {
	o, ok := value.(host.Obj)
	if !ok {
		fail("invalid_usage")
	}
	counts := map[string]int64{}
	for i, key := range googleCounts {
		v := get(o, key)
		if i >= 3 {
			v = getd(o, key, 0)
		}
		counts[key] = integer(v, "invalid_usage", 0)
	}
	prompt, candidates, total := counts["promptTokenCount"], counts["candidatesTokenCount"], counts["totalTokenCount"]
	thoughts, cached := counts["thoughtsTokenCount"], counts["cachedContentTokenCount"]
	if total != prompt+candidates+thoughts || cached > prompt {
		fail("invalid_usage")
	}
	result := host.Obj{{Key: "prompt_tokens", Value: prompt}, {Key: "completion_tokens", Value: candidates + thoughts},
		{Key: "total_tokens", Value: total}}
	if has(o, "thoughtsTokenCount") {
		result.Set("completion_tokens_details", host.Obj{{Key: "reasoning_tokens", Value: thoughts}})
	}
	if has(o, "cachedContentTokenCount") {
		result.Set("prompt_tokens_details", host.Obj{{Key: "cached_tokens", Value: cached}})
	}
	return counts, result
}

type completion struct {
	model, identity                     string
	created                             int64
	parts, calls                        []any
	text                                []string
	ids                                 map[string]bool
	reason                              string
	usage                               host.Obj
	counts                              map[string]int64
	seen, blocked, usageEnd, finalUsage bool
	onComplete                          func(message host.Obj, parts []any)
}

func newCompletion(model string, onComplete func(host.Obj, []any)) *completion {
	str(model, "invalid_response", true)
	if onComplete == nil {
		fail("invalid_option")
	}
	return &completion{model: model, identity: "chatcmpl_" + newHex(), created: now().Unix(),
		ids: map[string]bool{}, onComplete: onComplete}
}

func (c *completion) accept(page host.Obj) []string {
	if c.usageEnd {
		fail("invalid_response")
	}
	candidates, ok := getd(page, "candidates", []any{}).([]any)
	if !ok || len(candidates) > 1 {
		fail("invalid_response")
	}
	feedback, ok := getd(page, "promptFeedback", host.Obj{}).(host.Obj)
	if !ok {
		fail("invalid_response")
	}
	if v, ok := feedback.Get("blockReason"); ok {
		block := str(v, "invalid_response", true)
		if !(googleFilters[block] || block == "OTHER" || block == "IMAGE_SAFETY") || c.seen || c.blocked || len(candidates) > 0 {
			fail("invalid_response")
		}
		c.blocked, c.reason = true, "content_filter"
	}
	if c.blocked && len(candidates) > 0 {
		fail("invalid_response")
	}
	var visible []string
	if len(candidates) > 0 {
		visible = c.candidate(candidates[0])
	}
	if v, ok := page.Get("usageMetadata"); ok {
		c.acceptUsage(v, len(candidates) == 0)
	} else if len(candidates) == 0 && !has(feedback, "blockReason") {
		fail("invalid_response")
	}
	return visible
}

// candidate validates the only candidate and returns its visible texts.
func (c *completion) candidate(value any) []string {
	if c.reason != "" {
		fail("invalid_response")
	}
	candidate, ok := value.(host.Obj)
	if !ok || integer(getd(candidate, "index", 0), "invalid_response", 0) != 0 {
		fail("invalid_response")
	}
	c.seen = true
	var visible []string
	if v, ok := candidate.Get("content"); ok {
		content, ok := v.(host.Obj)
		parts, isList := get(content, "parts").([]any)
		if !ok || get(content, "role") != "model" || !isList {
			fail("invalid_response")
		}
		for _, kv := range content {
			if kv.Key != "role" && kv.Key != "parts" {
				fail("invalid_response")
			}
		}
		for _, part := range parts {
			if text, ok := c.part(part); ok {
				visible = append(visible, text)
			}
		}
	} else if !has(candidate, "finishReason") {
		fail("invalid_response")
	}
	if v, ok := candidate.Get("finishReason"); ok {
		switch reason := str(v, "invalid_response", true); {
		case reason == "STOP":
			c.reason = "stop"
			if len(c.calls) > 0 {
				c.reason = "tool_calls"
			}
		case reason == "MAX_TOKENS":
			c.reason = "length"
		case googleFilters[reason]:
			c.reason = "content_filter"
		default:
			fail("invalid_response")
		}
	}
	return visible
}

func (c *completion) acceptUsage(v any, usageOnly bool) {
	counts, usage := googleUsage(v)
	if c.counts != nil {
		for _, key := range googleCounts {
			if counts[key] < c.counts[key] {
				fail("invalid_usage")
			}
		}
	}
	c.counts, c.usage = counts, usage
	if c.reason != "" {
		c.finalUsage = true
	}
	if usageOnly {
		if c.reason == "" {
			fail("invalid_response")
		}
		c.usageEnd = true
	}
}

// part validates one native part, records it and returns its visible text, if any.
func (c *completion) part(value any) (string, bool) {
	part, ok := value.(host.Obj)
	if !ok || len(part) == 0 {
		fail("invalid_response")
	}
	for _, kv := range part {
		if !set("text", "functionCall", "thought", "thoughtSignature")[kv.Key] {
			fail("invalid_response")
		}
	}
	if !has(part, "text") && !has(part, "functionCall") && !has(part, "thoughtSignature") ||
		has(part, "text") && has(part, "functionCall") {
		fail("invalid_response")
	}
	if v, ok := part.Get("thought"); ok {
		if _, isBool := v.(bool); !isBool {
			fail("invalid_response")
		}
	}
	if v, ok := part.Get("thoughtSignature"); ok {
		str(v, "invalid_response", true)
	}
	text, visible := "", false
	if v, ok := part.Get("text"); ok {
		value := str(v, "invalid_response", false)
		if getd(part, "thought", false) == false {
			if len(c.calls) > 0 {
				fail("invalid_response")
			}
			c.text = append(c.text, value)
			text, visible = value, true
		}
	}
	if v, ok := part.Get("functionCall"); ok {
		c.functionCall(v)
	}
	c.parts = append(c.parts, clone(part))
	return text, visible
}

func (c *completion) functionCall(v any) {
	function, ok := v.(host.Obj)
	if !ok || len(c.calls) >= MaxToolCalls {
		fail("invalid_response")
	}
	for _, kv := range function {
		if !set("id", "name", "args")[kv.Key] {
			fail("invalid_response")
		}
	}
	toolName := name(get(function, "name"), "invalid_response")
	if id, ok := function.Get("id"); ok {
		identity := str(id, "invalid_response", true)
		if c.ids[identity] {
			fail("invalid_response")
		}
		c.ids[identity] = true
	}
	args, ok := get(function, "args").(host.Obj)
	if !ok {
		fail("invalid_tool_arguments")
	}
	encoded := jsonText(args, "invalid_tool_arguments")
	if len(encoded) > MaxToolArgumentBytes {
		fail("tool_arguments_too_large")
	}
	arguments(encoded)
	c.calls = append(c.calls, host.Obj{{Key: "id", Value: "mggoogle_" + newHex()}, {Key: "type", Value: "function"},
		{Key: "function", Value: host.Obj{{Key: "name", Value: toolName}, {Key: "arguments", Value: encoded}}}})
}

func (c *completion) finish() host.Obj {
	if c.reason == "" || !(c.seen || c.blocked) {
		fail("truncated_stream")
	}
	if c.reason == "content_filter" && len(c.calls) > 0 {
		fail("invalid_response")
	}
	if !c.finalUsage {
		fail("invalid_usage")
	}
	message := host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: nil}}
	if len(c.text) > 0 || len(c.calls) == 0 {
		message.Set("content", strings.Join(c.text, ""))
	}
	if len(c.calls) > 0 {
		message.Set("tool_calls", c.calls)
	}
	result := host.Obj{{Key: "id", Value: c.identity}, {Key: "object", Value: "chat.completion"}, {Key: "created", Value: c.created},
		{Key: "model", Value: c.model}, {Key: "choices", Value: []any{host.Obj{{Key: "index", Value: 0}, {Key: "message", Value: message},
			{Key: "finish_reason", Value: c.reason}}}}, {Key: "usage", Value: c.usage}}
	if len(jsonText(result, "invalid_response")) > MaxOutput {
		fail("upstream_too_large")
	}
	return result
}

func (c *completion) commit(result host.Obj) {
	message := get(get(result, "choices").([]any)[0], "message")
	c.onComplete(clone(message).(host.Obj), clone(c.parts).([]any))
}

// GoogleResponse validates native Gemini pages and returns the Chat completion; onComplete gets
// independent copies of the final message and the native parts once, after EOF (response).
func GoogleResponse(pages iter.Seq2[any, error], model string, onComplete func(message host.Obj, parts []any)) (out host.Obj, err error) {
	defer catch(&err)
	c := newCompletion(model, onComplete)
	googlePages(pages, false, func(page host.Obj) { c.accept(page) })
	result := c.finish()
	c.commit(result)
	return result, nil
}

// GoogleStream emits text early; tool, finish and usage frames and DONE follow full validation
// and the onComplete call (stream).
func GoogleStream(pages iter.Seq2[any, error], model string, onComplete func(message host.Obj, parts []any), emit func([]byte) error) (err error) {
	defer catch(&err)
	yield := emitter(emit)
	c := newCompletion(model, onComplete)
	emitted := 0
	frame := func(delta host.Obj, finish any, usage host.Obj) []byte {
		body := host.Obj{{Key: "id", Value: c.identity}, {Key: "object", Value: "chat.completion.chunk"},
			{Key: "created", Value: c.created}, {Key: "model", Value: model}}
		if usage != nil {
			body = append(body, host.KV{Key: "choices", Value: []any{}}, host.KV{Key: "usage", Value: usage})
		} else {
			body = append(body, host.KV{Key: "choices", Value: []any{host.Obj{{Key: "index", Value: 0}, {Key: "delta", Value: delta}, {Key: "finish_reason", Value: finish}}}})
		}
		raw := []byte("data: " + jsonText(body, "invalid_stream") + "\n\n")
		if len(raw) > MaxFrameBytes {
			fail("stream_frame_too_large")
		}
		emitted += len(raw)
		if emitted > MaxOutput {
			fail("upstream_too_large")
		}
		return raw
	}
	started := false
	googlePages(pages, true, func(page host.Obj) {
		visible := c.accept(page)
		if !started {
			started = true
			yield(frame(host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: ""}}, nil, nil))
		}
		for _, text := range visible {
			yield(frame(host.Obj{{Key: "content", Value: text}}, nil, nil))
		}
	})
	result := c.finish()
	var final [][]byte
	for i, call := range c.calls {
		item := append(host.Obj{{Key: "index", Value: i}}, call.(host.Obj)...)
		final = append(final, frame(host.Obj{{Key: "tool_calls", Value: []any{item}}}, nil, nil))
	}
	last := host.Obj{}
	if len(c.calls) == 0 && strings.Join(c.text, "") == "" {
		last = host.Obj{{Key: "content", Value: ""}}
	}
	final = append(final, frame(last, c.reason, nil), frame(host.Obj{}, nil, c.usage))
	if emitted+len("data: [DONE]\n\n") > MaxOutput {
		fail("upstream_too_large")
	}
	c.commit(result)
	for _, f := range final {
		yield(f)
	}
	yield([]byte("data: [DONE]\n\n"))
	return nil
}
