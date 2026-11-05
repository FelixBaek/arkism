package memory

// One chat call to the server's configured model (SKILL_LLM_*), in either of two
// wire protocols: the common chat-completions one (default) or the messages one
// (LLMConfig.Protocol "messages": POST {endpoint} with x-api-key and a version
// header, {model, max_tokens, system, messages} answered by {content: [{type, text}],
// stop_reason, usage}). Analysis, drafting and wiki/derivation calls share it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

const (
	llmProtocolMessages = "messages"
	llmMessagesVersion  = "2023-06-01"
)

// LLMConfig is the server model: endpoint URL, key, model and wire protocol
// ("" chat completions, "messages" the messages protocol).
type LLMConfig struct{ Endpoint, APIKey, Model, Protocol string }

type chatRequest struct {
	op, provider string // dependency name for errors, provider name for the ledger
	system, user string
	imageMIME    string // with image: one picture sent before the user text
	image        []byte
	maxTokens    int
	jsonOut      bool // ask for a JSON object where the protocol has a switch for it
	timeout      time.Duration
}

type chatReply struct {
	text     string
	usage    ProviderUsage
	complete bool // the model stopped on its own, not at the token limit
}

// ValidLLMProtocol reports whether p names a supported wire protocol.
func ValidLLMProtocol(p string) bool { return p == "" || p == llmProtocolMessages }

func (c LLMConfig) chat(ctx context.Context, req chatRequest) (chatReply, error) {
	if c.APIKey == "" || c.Model == "" || !ValidLLMProtocol(c.Protocol) || validateEndpoint(c.Endpoint) != nil {
		return chatReply{}, ErrUnavailable
	}
	client := providerClient(nil, req.timeout)
	if c.Protocol == llmProtocolMessages {
		return c.messagesChat(ctx, client, req)
	}
	user := any(req.user)
	if req.image != nil {
		user = []map[string]any{{"type": "text", "text": req.user},
			{"type": "image_url", "image_url": map[string]string{"url": "data:" + req.imageMIME + ";base64," + base64.StdEncoding.EncodeToString(req.image)}}}
	}
	payload := map[string]any{"model": c.Model, "temperature": 0, "max_tokens": req.maxTokens, "messages": []map[string]any{
		{"role": "system", "content": req.system},
		{"role": "user", "content": user},
	}}
	if req.jsonOut {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	body, status, err := postJSON(ctx, client, c.Endpoint, c.APIKey, payload, defaultRequestMax, defaultResponseMax)
	if err != nil {
		return chatReply{}, err
	}
	if status != http.StatusOK {
		return chatReply{}, dependencyStatusError(req.op, status)
	}
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
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Choices) != 1 {
		return chatReply{}, ErrUnavailable
	}
	usage := ProviderUsage{Provider: req.provider, Model: c.Model, Requests: 1, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, TotalTokens: response.Usage.TotalTokens}
	if !validProviderUsage(usage) {
		return chatReply{}, ErrUnavailable
	}
	return chatReply{text: response.Choices[0].Message.Content, usage: usage, complete: response.Choices[0].FinishReason == "stop"}, nil
}

func (c LLMConfig) messagesChat(ctx context.Context, client *http.Client, req chatRequest) (chatReply, error) {
	user := any(req.user)
	if req.image != nil {
		user = []map[string]any{{"type": "image", "source": map[string]string{"type": "base64", "media_type": req.imageMIME, "data": base64.StdEncoding.EncodeToString(req.image)}},
			{"type": "text", "text": req.user}}
	}
	payload := map[string]any{"model": c.Model, "temperature": 0, "max_tokens": req.maxTokens, "system": req.system,
		"messages": []map[string]any{{"role": "user", "content": user}}}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > defaultRequestMax {
		return chatReply{}, ErrInvalid
	}
	header := http.Header{"Accept": {"application/json"}, "X-Api-Key": {c.APIKey}, "Anthropic-Version": {llmMessagesVersion}}
	body, status, err := postBody(ctx, client, c.Endpoint, header, encoded, defaultResponseMax)
	if err != nil {
		return chatReply{}, err
	}
	if status != http.StatusOK {
		return chatReply{}, dependencyStatusError(req.op, status)
	}
	usage, stop, text, ok := parseDraftResponse(body, "")
	if !ok {
		return chatReply{}, ErrUnavailable
	}
	usage.Provider, usage.Model = req.provider, c.Model
	if !validProviderUsage(usage) {
		return chatReply{}, ErrUnavailable
	}
	return chatReply{text: text, usage: usage, complete: stop == "end_turn"}, nil
}
