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

func TestLLMChatResponsesProtocol(t *testing.T) {
	var got map[string]any
	status := "completed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "output": []any{
			map[string]any{"type": "reasoning", "content": []any{map[string]any{"type": "output_text", "text": "hidden"}}},
			map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "hel"}, map[string]any{"type": "output_text", "text": "lo"}}}},
			"usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6}})
	}))
	defer server.Close()
	cfg := LLMConfig{Endpoint: server.URL, APIKey: "k", Model: "m", Protocol: llmProtocolResponses}
	reply, err := cfg.chat(testCtx, chatRequest{provider: "p", system: "sys", user: "hi", imageMIME: "image/png", image: []byte("PNG"), maxTokens: 9, jsonOut: true})
	if err != nil || reply.text != "hello" || !reply.complete || reply.usage != (ProviderUsage{Provider: "p", Model: "m", Requests: 1, InputTokens: 4, OutputTokens: 2, TotalTokens: 6}) {
		t.Fatalf("%+v %v", reply, err)
	}
	content := got["input"].([]any)[0].(map[string]any)["content"].([]any)
	if got["instructions"] != "sys" || got["max_output_tokens"] != float64(9) || got["store"] != false || got["temperature"] != nil ||
		content[0].(map[string]any)["text"] != "hi" || content[1].(map[string]any)["image_url"] != "data:image/png;base64,UE5H" ||
		got["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("request = %v", got)
	}
	status = "incomplete"
	if reply, err = cfg.chat(testCtx, chatRequest{provider: "p", user: "hi", maxTokens: 9}); err != nil || reply.complete {
		t.Fatalf("a cut-off answer is not complete: %+v %v", reply, err)
	}
	status = "failed"
	if _, err = cfg.chat(testCtx, chatRequest{provider: "p", user: "hi", maxTokens: 9}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed status: %v", err)
	}
}

func TestApplyLLMPreset(t *testing.T) {
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header
		if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "ok"}}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer server.Close()
	// Gateway: the host gateway's base, its token in the gateway header, the auto group by default.
	gw, err := ApplyLLMPreset(LLMConfig{APIKey: "tok"}, "gateway", "", server.URL)
	if err != nil || gw.Endpoint != server.URL+"/v1/chat/completions" || gw.Model != "group/auto" || gw.Keyless || !gw.Configured() {
		t.Fatalf("gateway preset: %+v %v", gw, err)
	}
	if _, err := gw.chat(testCtx, chatRequest{provider: "p", user: "x", maxTokens: 9}); err != nil || header.Get("x-arkism-gateway-token") != "tok" || header.Get("Authorization") != "" {
		t.Fatalf("gateway call: %v %v", err, header)
	}
	if (LLMConfig{Endpoint: gw.Endpoint, Model: "m", KeyHeader: gw.KeyHeader}).Configured() {
		t.Fatal("the gateway preset needs its token")
	}
	// A local preset pointed at another loopback base works without a key.
	local, err := ApplyLLMPreset(LLMConfig{}, "local", server.URL+"/v1/", "")
	if err != nil || local.Model != "local" || !local.Keyless || !local.Configured() {
		t.Fatalf("local preset: %+v %v", local, err)
	}
	if _, err := local.chat(testCtx, chatRequest{provider: "p", user: "x", maxTokens: 9}); err != nil || header.Get("Authorization") != "" {
		t.Fatalf("keyless call: %v %v", err, header)
	}
	// A base URL takes the protocol's path; presets keep their default addresses.
	resp, err := ApplyLLMPreset(LLMConfig{APIKey: "k", Model: "m", Protocol: llmProtocolResponses}, "", server.URL+"/v1", "")
	if err != nil || resp.Endpoint != server.URL+"/v1/responses" {
		t.Fatalf("base url: %+v %v", resp, err)
	}
	if c, _ := ApplyLLMPreset(LLMConfig{Model: "q"}, "ollama", "", ""); c.Endpoint != "http://127.0.0.1:11434/v1/chat/completions" || c.Model != "q" {
		t.Fatalf("ollama preset: %+v", c)
	}
	if c, _ := ApplyLLMPreset(LLMConfig{Protocol: llmProtocolMessages}, "proxy", "", ""); c.Endpoint != "http://127.0.0.1:4000/v1/messages" {
		t.Fatalf("proxy preset: %+v", c)
	}
	// Keyless never reaches a remote host; bad names and double endpoints stop startup.
	remote, _ := ApplyLLMPreset(LLMConfig{}, "proxy", "https://models.example.com/v1", "")
	if remote.Configured() {
		t.Fatal("keyless remote endpoint must not be usable")
	}
	for _, bad := range []struct {
		c            LLMConfig
		preset, base string
	}{{LLMConfig{}, "other", ""}, {LLMConfig{Endpoint: server.URL}, "", server.URL}, {LLMConfig{Protocol: "other"}, "", ""}, {LLMConfig{}, "", "http://models.example.com/v1"}} {
		if _, err := ApplyLLMPreset(bad.c, bad.preset, bad.base, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}
