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
