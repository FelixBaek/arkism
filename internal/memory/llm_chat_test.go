package memory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMChatMessagesProtocol(t *testing.T) {
	stop := "end_turn"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model, System string
			MaxTokens     int `json:"max_tokens"`
			Messages      []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "m" || req.System != "sys" || req.MaxTokens != 64 || len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hi" {
			t.Errorf("request = %+v", req)
		}
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != llmMessagesVersion || r.Header.Get("Authorization") != "" {
			t.Errorf("headers = %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "m-2026", "stop_reason": stop,
			"content": []any{map[string]any{"type": "text", "text": "hello "}, map[string]any{"type": "text", "text": "there"}},
			"usage":   map[string]any{"input_tokens": 5, "output_tokens": 3}})
	}))
	defer server.Close()
	llm := &ChatWikiLLM{Config: LLMConfig{Endpoint: server.URL, APIKey: "k", Model: "m", Protocol: llmProtocolMessages}}
	text, usage, err := llm.Complete(testCtx, "sys", "hi", 64)
	if err != nil || text != "hello there" || usage.Provider != wikiProvider || usage.Model != "m" || usage.TotalTokens != 8 {
		t.Fatalf("%q %+v %v", text, usage, err)
	}
	stop = "max_tokens"
	reply, err := llm.Config.chat(testCtx, chatRequest{op: "t", provider: "skill_llm", system: "sys", user: "hi", maxTokens: 64})
	if err != nil || reply.complete {
		t.Fatalf("a cut-off answer is not complete: %+v %v", reply, err)
	}
	if _, err := (LLMConfig{Endpoint: server.URL, APIKey: "k", Model: "m", Protocol: "other"}).chat(testCtx, chatRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown protocol: %v", err)
	}
}

// An image goes as a content part in either protocol; a keyless, lenient request
// (the routing picker) works without a key and books one request when usage is malformed.
func TestLLMChatImagePartsKeylessAndLenientUsage(t *testing.T) {
	var got map[string]any
	var auth string
	usage := map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, auth = nil, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		out := map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "ok"}}}}
		if usage != nil {
			out["usage"] = usage
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	cfg := LLMConfig{Endpoint: server.URL, APIKey: "k", Model: "m"}
	if _, err := cfg.chat(testCtx, chatRequest{provider: "p", system: "s", user: "read", imageMIME: "image/png", image: []byte("PNG"), maxTokens: 9}); err != nil {
		t.Fatal(err)
	}
	user := got["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if len(user) != 2 || user[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,UE5H" || auth != "Bearer k" {
		t.Fatalf("chat image part: %v", user)
	}
	if _, err := (LLMConfig{Endpoint: server.URL, Model: "m"}).chat(testCtx, chatRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a key is required unless the request is keyless", err)
	}
	usage = map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 2}
	if _, err := cfg.chat(testCtx, chatRequest{provider: "p", maxTokens: 9}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("malformed usage is refused by default", err)
	}
	reply, err := (LLMConfig{Endpoint: server.URL, Model: "m"}).chat(testCtx, chatRequest{provider: "chat", maxTokens: 9, keyless: true, lenientUsage: true})
	if err != nil || reply.text != "ok" || auth != "" || reply.usage != (ProviderUsage{Provider: "chat", Model: "m", Requests: 1}) {
		t.Fatalf("keyless lenient: %+v %v %q", reply, err, auth)
	}
	msgs := LLMConfig{Endpoint: server.URL, APIKey: "k", Model: "m", Protocol: llmProtocolMessages}
	_, _ = msgs.chat(testCtx, chatRequest{provider: "p", user: "read", imageMIME: "image/jpeg", image: []byte("J"), maxTokens: 9})
	part := got["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if src := part["source"].(map[string]any); part["type"] != "image" || src["media_type"] != "image/jpeg" || src["data"] != "Sg==" {
		t.Fatalf("messages image part: %v", part)
	}
}
