package memory

// One chat call to the server's configured model (SKILL_LLM_*), in one of three
// wire protocols: the common chat-completions one (default), the messages one
// (LLMConfig.Protocol "messages": POST {endpoint} with x-api-key and a version
// header, {model, max_tokens, system, messages} answered by {content: [{type, text}],
// stop_reason, usage}) or the responses one (Protocol "responses": POST {endpoint}
// {model, instructions, input, max_output_tokens, store:false} answered by
// {status, output: [{type:"message", content: [{type:"output_text", text}]}], usage}).
// Analysis, drafting and wiki/derivation calls share it. Named presets
// (ApplyLLMPreset) fill the endpoint and auth for common local servers, proxies
// and this host's model gateway, so no host needs its own copy of this client.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
)

const (
	llmProtocolMessages  = "messages"
	llmProtocolResponses = "responses"
	llmMessagesVersion   = "2023-06-01"
)

// LLMConfig is the server model: endpoint URL, key, model and wire protocol
// ("" chat completions, "messages", "responses"). KeyHeader, when set, carries the
// key in that header instead of the protocol's own; Keyless allows an empty key for
// a loopback endpoint (a model server on this machine).
type LLMConfig struct {
	Endpoint, APIKey, Model, Protocol string
	KeyHeader                         string
	Keyless                           bool
}

type chatRequest struct {
	op, provider string // dependency name for errors, provider name for the ledger
	system, user string
	imageMIME    string // with image: one picture sent before the user text
	image        []byte
	maxTokens    int
	jsonOut      bool // ask for a JSON object where the protocol has a switch for it
	timeout      time.Duration
	// keyless allows an empty key (a model on this computer); lenientUsage books one
	// request without token counts when the reply's usage is missing or malformed.
	keyless, lenientUsage bool
}

type chatReply struct {
	text     string
	usage    ProviderUsage
	complete bool // the model stopped on its own, not at the token limit
}

// ValidLLMProtocol reports whether p names a supported wire protocol.
func ValidLLMProtocol(p string) bool {
	return p == "" || p == llmProtocolMessages || p == llmProtocolResponses
}

// usable reports whether the config can make a call for req.
func (c LLMConfig) usable(req chatRequest) bool {
	if c.Model == "" || !ValidLLMProtocol(c.Protocol) || validateEndpoint(c.Endpoint) != nil {
		return false
	}
	if c.APIKey != "" || req.keyless {
		return true
	}
	return c.Keyless && loopbackURL(c.Endpoint)
}

func loopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && isLoopbackHost(parsed.Hostname())
}

func (c LLMConfig) chat(ctx context.Context, req chatRequest) (chatReply, error) {
	if !c.usable(req) {
		return chatReply{}, ErrUnavailable
	}
	encoded, err := json.Marshal(c.payload(req))
	if err != nil || len(encoded) > defaultRequestMax {
		return chatReply{}, ErrInvalid
	}
	client := providerClient(nil, req.timeout)
	if c.APIKey == "" && !req.keyless {
		// A keyless preset: the connection itself, after DNS, must stay on this machine.
		client.Transport = httpguard.LoopbackClient(req.timeout).Transport
	}
	body, status, err := postBody(ctx, client, c.Endpoint, c.header(), encoded, defaultResponseMax)
	if err != nil {
		return chatReply{}, err
	}
	if status != http.StatusOK {
		return chatReply{}, dependencyStatusError(req.op, status)
	}
	var reply chatReply
	var ok bool
	switch c.Protocol {
	case llmProtocolMessages:
		reply, ok = parseMessagesReply(body)
	case llmProtocolResponses:
		reply, ok = parseResponsesReply(body)
	default:
		reply, ok = parseChatReply(body)
	}
	if !ok {
		return chatReply{}, ErrUnavailable
	}
	reply.usage.Provider, reply.usage.Model = req.provider, c.Model
	if reply.usage, ok = req.checkUsage(reply.usage); !ok {
		return chatReply{}, ErrUnavailable
	}
	return reply, nil
}

func (c LLMConfig) header() http.Header {
	header := http.Header{"Accept": {"application/json"}}
	switch {
	case c.APIKey == "":
	case c.KeyHeader != "":
		header.Set(c.KeyHeader, c.APIKey)
	case c.Protocol == llmProtocolMessages:
		header.Set("X-Api-Key", c.APIKey)
	default:
		header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.Protocol == llmProtocolMessages {
		header.Set("Anthropic-Version", llmMessagesVersion)
	}
	return header
}

func dataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func (c LLMConfig) payload(req chatRequest) map[string]any {
	switch c.Protocol {
	case llmProtocolMessages:
		user := any(req.user)
		if req.image != nil {
			user = []map[string]any{{"type": "image", "source": map[string]string{"type": "base64", "media_type": req.imageMIME, "data": base64.StdEncoding.EncodeToString(req.image)}},
				{"type": "text", "text": req.user}}
		}
		return map[string]any{"model": c.Model, "temperature": 0, "max_tokens": req.maxTokens, "system": req.system,
			"messages": []map[string]any{{"role": "user", "content": user}}}
	case llmProtocolResponses:
		content := []map[string]any{{"type": "input_text", "text": req.user}}
		if req.image != nil {
			content = append(content, map[string]any{"type": "input_image", "image_url": dataURL(req.imageMIME, req.image)})
		}
		// No temperature: reasoning models on this protocol refuse it. store:false keeps
		// nothing on the provider side between calls.
		payload := map[string]any{"model": c.Model, "instructions": req.system, "max_output_tokens": req.maxTokens, "store": false,
			"input": []map[string]any{{"role": "user", "content": content}}}
		if req.jsonOut {
			payload["text"] = map[string]any{"format": map[string]string{"type": "json_object"}}
		}
		return payload
	}
	user := any(req.user)
	if req.image != nil {
		user = []map[string]any{{"type": "text", "text": req.user},
			{"type": "image_url", "image_url": map[string]string{"url": dataURL(req.imageMIME, req.image)}}}
	}
	payload := map[string]any{"model": c.Model, "temperature": 0, "max_tokens": req.maxTokens, "messages": []map[string]any{
		{"role": "system", "content": req.system},
		{"role": "user", "content": user},
	}}
	if req.jsonOut {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	return payload
}

func parseChatReply(body []byte) (chatReply, bool) {
	var response struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
			Details          struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Choices) != 1 {
		return chatReply{}, false
	}
	usage := ProviderUsage{Requests: 1, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, TotalTokens: response.Usage.TotalTokens,
		CachedInputTokens: response.Usage.Details.CachedTokens}
	return chatReply{text: response.Choices[0].Message.Content, usage: usage, complete: response.Choices[0].FinishReason == "stop"}, true
}

func parseMessagesReply(body []byte) (chatReply, bool) {
	usage, stop, text, ok := parseDraftResponse(body, "")
	return chatReply{text: text, usage: usage, complete: stop == "end_turn"}, ok
}

func parseResponsesReply(body []byte) (chatReply, bool) {
	var response struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
			Details      struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.Status != "completed" && response.Status != "incomplete" {
		return chatReply{}, false
	}
	var text strings.Builder
	for _, item := range response.Output {
		for _, part := range item.Content {
			if item.Type == "message" && part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	usage := ProviderUsage{Requests: 1, InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, TotalTokens: response.Usage.TotalTokens,
		CachedInputTokens: response.Usage.Details.CachedTokens}
	return chatReply{text: text.String(), usage: usage, complete: response.Status == "completed"}, true
}

// checkUsage refuses a reply whose usage is malformed, unless the request is lenient.
func (req chatRequest) checkUsage(u ProviderUsage) (ProviderUsage, bool) {
	if validProviderUsage(u) {
		return u, true
	}
	return ProviderUsage{Provider: u.Provider, Model: u.Model, Requests: 1}, req.lenientUsage
}

// LLM presets: a named kind of server whose address and auth are known, so an
// operator sets one name (SKILL_LLM_PRESET) instead of a URL and key shape.
// "local" is a single-model inference server on this machine (the common
// style on port 8080), "runner" a local multi-model runner on 11434, "proxy" a
// chat-completions proxy gateway on 4000 (it routes to many hosted models), and
// "gateway" this host's own model gateway, which reaches the subscription
// accounts signed in on this host (the caller passes its address and token).
var llmPresets = map[string]struct {
	base      string
	keyHeader string
	model     string
}{
	"local":   {base: "http://127.0.0.1:8080/v1", model: "local"},
	"runner":  {base: "http://127.0.0.1:11434/v1"},
	"proxy":   {base: "http://127.0.0.1:4000/v1"},
	"gateway": {keyHeader: "x-arkism-gateway-token", model: "group/auto"},
}

// llmProtocolPath is the path each protocol answers under a base URL.
var llmProtocolPath = map[string]string{"": "/chat/completions", llmProtocolMessages: "/messages", llmProtocolResponses: "/responses"}

// ApplyLLMPreset completes c from a preset name and an optional base URL: the
// endpoint becomes base + the protocol's path unless c.Endpoint is already set
// (setting both is an error), presets on loopback need no key, and an empty model
// takes the preset's default. gatewayBase is the host gateway's base URL, used by
// the "gateway" preset only.
func ApplyLLMPreset(c LLMConfig, preset, base, gatewayBase string) (LLMConfig, error) {
	if !ValidLLMProtocol(c.Protocol) {
		return c, fmt.Errorf(`SKILL_LLM_PROTOCOL must be empty, "messages" or "responses": %w`, ErrInvalid)
	}
	if preset != "" {
		p, ok := llmPresets[preset]
		if !ok {
			return c, fmt.Errorf("SKILL_LLM_PRESET %q is not local, runner, proxy or gateway: %w", preset, ErrInvalid)
		}
		if base == "" {
			base = p.base
			if preset == "gateway" {
				base = strings.TrimRight(gatewayBase, "/") + "/v1"
			}
		}
		c.KeyHeader, c.Keyless = p.keyHeader, preset != "gateway"
		if c.Model == "" {
			c.Model = p.model
		}
	}
	if preset == "gateway" && !loopbackURL(base) {
		// The gateway token is this host's own secret: it never goes to another machine.
		return c, fmt.Errorf("SKILL_LLM_PRESET=gateway needs a loopback base URL: %w", ErrInvalid)
	}
	if base != "" {
		if c.Endpoint != "" {
			return c, fmt.Errorf("set SKILL_LLM_ENDPOINT_URL or a base URL, not both: %w", ErrInvalid)
		}
		c.Endpoint = strings.TrimRight(base, "/") + llmProtocolPath[c.Protocol]
		if err := validateEndpoint(c.Endpoint); err != nil {
			return c, fmt.Errorf("server model base url: %w", err)
		}
	}
	return c, nil
}

// Configured reports whether c can make calls: a model, and a key unless keyless.
func (c LLMConfig) Configured() bool { return c.usable(chatRequest{}) }
