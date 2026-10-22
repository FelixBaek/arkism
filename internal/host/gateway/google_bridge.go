package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/FelixBaek/arkism/internal/host"
)

// Portable Chat requests and bounded native Google continuation state.

const bridgePrefix = "mggoogle_"

// Cache bounds; variables so tests can shrink them. bridgeTTL is seconds of inactivity.
var (
	bridgeMaxTurns = 1024
	bridgeMaxBytes = 16 << 20
	bridgeTTL      = 900.0
)

type bridgeTurn struct {
	parts   []any
	model   string
	size    int
	expires float64
}

// bridgeTurns is an LRU: bridgeOrder runs from least to most recently used.
var (
	bridgeLock  sync.Mutex
	bridgeTurns = map[[32]byte]*bridgeTurn{}
	bridgeOrder [][32]byte
)

func bridgeDigest(value any) [32]byte {
	return sha256.Sum256([]byte(host.MustDumps(value, host.DumpOpts{Compact: true, NoASCII: true, SortKeys: true})))
}

// bridgeOwnedCalls reports whether messages carry a Google-owned tool id (owned_calls).
func bridgeOwnedCalls(messages any) bool {
	list, _ := messages.([]any)
	for _, item := range list {
		message, ok := item.(host.Obj)
		if !ok {
			continue
		}
		blocks, _ := get(message, "content").([]any)
		for _, value := range blocks {
			block, ok := value.(host.Obj)
			if !ok {
				continue
			}
			id, ok := block.Get("id")
			if !ok {
				id = getd(block, "tool_use_id", "")
			}
			if s, ok := id.(string); ok && strings.HasPrefix(s, bridgePrefix) {
				return true
			}
		}
	}
	return false
}

// bridgeCanonical drops empty text, merges adjacent text and flattens tool results (_canonical).
func bridgeCanonical(role any, content []any) host.Obj {
	blocks := []any{}
	for _, value := range content {
		block := clone(value).(host.Obj)
		switch get(block, "type") {
		case "text":
			text := get(block, "text").(string)
			if text == "" {
				continue
			}
			if n := len(blocks); n > 0 && get(blocks[n-1], "type") == "text" {
				last := blocks[n-1].(host.Obj)
				last.Set("text", get(last, "text").(string)+text)
				blocks[n-1] = last
				continue
			}
		case "tool_result":
			block.Set("content", []any{host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: bridgeResultText(block)}}})
		}
		blocks = append(blocks, block)
	}
	return host.Obj{{Key: "role", Value: role}, {Key: "content", Value: blocks}}
}

func bridgeResultText(block host.Obj) string {
	var b strings.Builder
	for _, part := range get(block, "content").([]any) {
		b.WriteString(get(part, "text").(string))
	}
	return b.String()
}

// bridgeNormalized is the codec's Messages body with canonical history, plus the context that
// keys every turn (_normalized).
func bridgeNormalized(chat any) (host.Obj, host.Obj, error) {
	body, err := CodecToMessages(chat)
	if err != nil {
		return nil, nil, err
	}
	messages := get(body, "messages").([]any)
	for i, message := range messages {
		messages[i] = bridgeCanonical(get(message, "role"), get(message, "content").([]any))
	}
	context := host.Obj{}
	for _, key := range []string{"model", "system", "tools", "tool_choice"} {
		if value := get(body, key); truthy(value) {
			context.Set(key, clone(value))
		}
	}
	if system, ok := context.Get("system"); ok {
		context.Set("system", get(bridgeCanonical("system", system.([]any)), "content"))
	}
	if choice, ok := get(context, "tool_choice").(host.Obj); ok {
		if !truthy(get(choice, "disable_parallel_tool_use")) {
			choice.Del("disable_parallel_tool_use")
		}
		if len(choice) == 1 && get(choice, "type") == "auto" {
			context.Del("tool_choice")
		} else {
			context.Set("tool_choice", choice)
		}
	}
	return body, context, nil
}

// bridgeHistoryKeys chains one digest per message over owner and context (_history_keys).
func bridgeHistoryKeys(owner string, context host.Obj, history []any) [][32]byte {
	state := bridgeDigest([]any{owner, context})
	keys := make([][32]byte, len(history))
	for i, message := range history {
		state = bridgeDigest([]any{hex.EncodeToString(state[:]), message})
		keys[i] = state
	}
	return keys
}

// bridgeTouch moves key to the most recently used end.
func bridgeTouch(key [32]byte) {
	for i, item := range bridgeOrder {
		if item == key {
			bridgeOrder = append(bridgeOrder[:i], bridgeOrder[i+1:]...)
			break
		}
	}
	bridgeOrder = append(bridgeOrder, key)
}

func bridgePrune() {
	now := googleMono()
	kept := bridgeOrder[:0]
	for _, key := range bridgeOrder {
		if bridgeTurns[key].expires <= now {
			delete(bridgeTurns, key)
		} else {
			kept = append(kept, key)
		}
	}
	bridgeOrder = kept
}

// bridgeLookup returns a copy of a live turn and refreshes its inactivity TTL (_lookup).
func bridgeLookup(key [32]byte) *bridgeTurn {
	bridgeLock.Lock()
	defer bridgeLock.Unlock()
	bridgePrune()
	record, ok := bridgeTurns[key]
	if !ok {
		return nil
	}
	record.expires = googleMono() + bridgeTTL
	bridgeTouch(key)
	copied := *record
	copied.parts = clone(record.parts).([]any)
	return &copied
}

// bridgeContinuationModel finds Google-owned history in a Chat body without rejecting unrelated
// providers' conversations: the error is a *GoogleError, or a Code from the codec for a body it
// cannot read (continuation_model).
func bridgeContinuationModel(chat host.Obj, owner string) (string, error) {
	body, context, err := bridgeNormalized(chat)
	if err != nil {
		return "", err
	}
	messages := get(body, "messages").([]any)
	models := map[string]bool{}
	model := ""
	for i, key := range bridgeHistoryKeys(owner, context, messages) {
		if get(messages[i], "role") != "assistant" {
			continue
		}
		if record := bridgeLookup(key); record != nil {
			models[record.model], model = true, record.model
		} else if bridgeOwnedCalls([]any{messages[i]}) {
			// shortcut: plain text has no portable origin ID; use native Gemini for strict group continuity.
			return "", googleErr("conversation_mismatch", 409, false)
		}
	}
	if len(models) > 1 {
		return "", googleErr("conversation_mismatch", 409, false)
	}
	return model, nil
}

// bridgeParts converts text and base64 image blocks to native parts (_parts).
func bridgeParts(blocks []any) ([]any, error) {
	parts := []any{}
	for _, value := range blocks {
		block := value.(host.Obj)
		switch get(block, "type") {
		case "text":
			parts = append(parts, host.Obj{{Key: "text", Value: get(block, "text")}})
		case "image":
			source := get(block, "source")
			if get(source, "type") != "base64" {
				return nil, googleErr("unsupported_field", 400, false)
			}
			parts = append(parts, host.Obj{{Key: "inlineData", Value: host.Obj{
				{Key: "mimeType", Value: get(source, "media_type")}, {Key: "data", Value: get(source, "data")}}}})
		default:
			return nil, googleErr("unsupported_field", 400, false)
		}
	}
	return parts, nil
}

// googlePrepared is Prepared; pinned is "" when nothing is pinned (None).
type googlePrepared struct {
	pinned  string
	native  host.Obj
	owner   string
	context host.Obj
	history []any
}

var bridgeChoiceModes = map[any]string{"auto": "AUTO", "none": "NONE", "any": "ANY", "tool": "ANY"}

// newGooglePrepared fails only with *GoogleError.
func newGooglePrepared(chat host.Obj, owner string) (*googlePrepared, error) {
	// response_format is Chat-only and the native Gemini request cannot express it. The gateway
	// refuses it per target before egress, but that check does not cover direct callers of this
	// bridge, so fail closed here instead of silently dropping the key and answering unstructured.
	// Deliberately not in bridgeNormalized: the gateway calls bridgeContinuationModel with the
	// original Chat body, which may legitimately carry response_format for a chat target.
	if has(chat, "response_format") {
		return nil, googleErr("unsupported_field", 400, false)
	}
	body, context, err := bridgeNormalized(chat)
	if err != nil {
		return nil, googleErr("unsupported_or_invalid_request", 400, false)
	}
	p := &googlePrepared{owner: owner, context: context, history: get(body, "messages").([]any)}
	config := host.Obj{{Key: "maxOutputTokens", Value: get(body, "max_tokens")}}
	for _, pair := range [][2]string{{"temperature", "temperature"}, {"top_p", "topP"}, {"stop_sequences", "stopSequences"}} {
		if value, ok := body.Get(pair[0]); ok {
			config.Set(pair[1], value)
		}
	}
	native := host.Obj{{Key: "contents", Value: []any{}}, {Key: "generationConfig", Value: config}}
	if system := get(body, "system"); truthy(system) {
		parts, err := bridgeParts(system.([]any))
		if err != nil {
			return nil, err
		}
		native.Set("systemInstruction", host.Obj{{Key: "parts", Value: parts}})
	}
	tools := get(body, "tools")
	if truthy(tools) {
		native.Set("tools", []any{host.Obj{{Key: "functionDeclarations", Value: bridgeDeclarations(tools.([]any))}}})
	}
	if choice := get(body, "tool_choice"); truthy(choice) {
		kind := get(choice, "type")
		if truthy(get(choice, "disable_parallel_tool_use")) && kind != "none" && truthy(tools) {
			return nil, googleErr("unsupported_field", 400, false)
		}
		calling := host.Obj{{Key: "mode", Value: bridgeChoiceModes[kind]}}
		if kind == "tool" {
			calling.Set("allowedFunctionNames", []any{get(choice, "name")})
		}
		native.Set("toolConfig", host.Obj{{Key: "functionCallingConfig", Value: calling}})
	}
	contents := []any{}
	calls := map[any]host.Obj{}
	for i, key := range bridgeHistoryKeys(owner, context, p.history) {
		message := p.history[i]
		blocks := get(message, "content").([]any)
		var content host.Obj
		if get(message, "role") == "assistant" {
			content, err = p.modelContent(key, blocks, calls)
		} else {
			content, err = bridgeUserContent(blocks, calls)
		}
		if err != nil {
			return nil, err
		}
		contents = append(contents, content)
	}
	native.Set("contents", contents)
	prepared, err := googlePrepare(native, "gemini")
	if err != nil {
		return nil, err
	}
	p.native = prepared
	return p, nil
}

func bridgeDeclarations(tools []any) []any {
	declarations := []any{}
	for _, tool := range tools {
		declaration := host.Obj{{Key: "name", Value: get(tool, "name")},
			{Key: "parametersJsonSchema", Value: clone(get(tool, "input_schema"))}}
		if description, ok := tool.(host.Obj).Get("description"); ok {
			declaration.Set("description", description)
		}
		declarations = append(declarations, declaration)
	}
	return declarations
}

// modelContent restores an assistant turn's recorded native parts, pairing its public tool_use
// ids with the native calls in calls.
func (p *googlePrepared) modelContent(key [32]byte, blocks []any, calls map[any]host.Obj) (host.Obj, error) {
	mismatch := googleErr("conversation_mismatch", 409, false)
	record := bridgeLookup(key)
	if record == nil || (p.pinned != "" && p.pinned != record.model) {
		return nil, mismatch
	}
	p.pinned = record.model
	parts := record.parts
	if len(parts) == 0 {
		parts = []any{host.Obj{{Key: "text", Value: ""}}}
	}
	var native, public []host.Obj
	for _, part := range parts {
		if call, ok := asObj(part).Get("functionCall"); ok {
			native = append(native, asObj(call))
		}
	}
	for _, block := range blocks {
		if get(block, "type") == "tool_use" {
			public = append(public, block.(host.Obj))
		}
	}
	if len(native) != len(public) {
		return nil, mismatch
	}
	for j, call := range public {
		calls[get(call, "id")] = native[j]
	}
	return host.Obj{{Key: "role", Value: "model"}, {Key: "parts", Value: parts}}, nil
}

func bridgeUserContent(blocks []any, calls map[any]host.Obj) (host.Obj, error) {
	parts := []any{}
	for _, value := range blocks {
		block := value.(host.Obj)
		if get(block, "type") != "tool_result" {
			converted, err := bridgeParts([]any{block})
			if err != nil {
				return nil, err
			}
			parts = append(parts, converted...)
			continue
		}
		original, ok := calls[get(block, "tool_use_id")]
		if !ok {
			return nil, googleErr("conversation_mismatch", 409, false)
		}
		result := host.Obj{{Key: "name", Value: get(original, "name")},
			{Key: "response", Value: host.Obj{{Key: "output", Value: bridgeResultText(block)}}}}
		if id, ok := original.Get("id"); ok {
			result.Set("id", id)
		}
		parts = append(parts, host.Obj{{Key: "functionResponse", Value: result}})
	}
	if len(parts) == 0 {
		parts = []any{host.Obj{{Key: "text", Value: ""}}}
	}
	return host.Obj{{Key: "role", Value: "user"}, {Key: "parts", Value: parts}}, nil
}

// remember is Prepared.remember, called only after native EOF and public response validation,
// before publishing tools. Failures are *GoogleError, except a Code for a message or parts the
// response translator could not have produced.
func (p *googlePrepared) remember(message host.Obj, parts []any, model string) error {
	blocks := []any{}
	if content := get(message, "content"); truthy(content) {
		blocks = append(blocks, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: content}})
	}
	toolCalls, _ := getd(message, "tool_calls", []any{}).([]any)
	for _, call := range toolCalls {
		function := get(call, "function")
		input, err := Arguments(get(function, "arguments"))
		if err != nil {
			return err
		}
		blocks = append(blocks, host.Obj{{Key: "type", Value: "tool_use"}, {Key: "id", Value: get(call, "id")},
			{Key: "name", Value: get(function, "name")}, {Key: "input", Value: input}})
	}
	history := append(append([]any{}, p.history...), bridgeCanonical("assistant", blocks))
	// Shared normalization merges adjacent roles on subsequent client input.
	if n := len(p.history); n > 0 && get(p.history[n-1], "role") == "assistant" {
		merged := append(append([]any{}, get(p.history[n-1], "content").([]any)...), blocks...)
		history = append(append([]any{}, p.history[:n-1]...), bridgeCanonical("assistant", merged))
		contents := get(p.native, "contents").([]any)
		parts = append(append([]any{}, get(contents[len(contents)-1], "parts").([]any)...), parts...)
	}
	keys := bridgeHistoryKeys(p.owner, p.context, history)
	key := keys[len(keys)-1]
	text, err := JSON(parts, "invalid_response")
	if err != nil {
		return err
	}
	size := len(text)
	if size > bridgeMaxBytes {
		return googleErr("continuation_capacity", 503, true)
	}
	record := &bridgeTurn{parts: clone(parts).([]any), model: model, size: size, expires: googleMono() + bridgeTTL}
	bridgeLock.Lock()
	defer bridgeLock.Unlock()
	bridgePrune()
	if old, ok := bridgeTurns[key]; ok && (old.model != model || !bridgeSame(old.parts, parts)) {
		return googleErr("conversation_collision", 409, true)
	}
	bridgeTurns[key] = record
	bridgeTouch(key)
	total := 0
	for _, item := range bridgeTurns {
		total += item.size
	}
	for len(bridgeTurns) > bridgeMaxTurns || total > bridgeMaxBytes {
		total -= bridgeTurns[bridgeOrder[0]].size
		delete(bridgeTurns, bridgeOrder[0])
		bridgeOrder = bridgeOrder[1:]
	}
	return nil
}

// bridgeSame is Python == on JSON trees, where object key order does not matter.
func bridgeSame(a, b any) bool {
	opts := host.DumpOpts{Compact: true, NoASCII: true, SortKeys: true}
	return host.MustDumps(a, opts) == host.MustDumps(b, opts)
}
