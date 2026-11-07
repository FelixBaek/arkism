package skills

import (
	"encoding/json"
	"github.com/FelixBaek/arkism/internal/host"
	"strings"
	"testing"
)

func TestRecallContextOptionsReachMCPAndCLI(t *testing.T) {
	swapRouter(t)
	packet := `{"authority":"data_only_untrusted","items":[],"conversation":[{"text":"upstairs room","authority":"data_only_untrusted"}]}`
	reply, _ := json.Marshal(map[string]any{"items": []any{}, "conversation": []any{map[string]any{"text": "upstairs room"}}, "context_data": packet, "tokens_used": 41, "token_encoding": "cl100k_base", "degraded": []any{"graph_unavailable"}})
	calls := fakeRequest(t, string(reply))
	args := `{"query":"meeting location","conversation_context":true,"whole_items":true,"best_effort_lanes":true,"budget_tokens":3000,"token_encoding":"cl100k_base"}`
	var stdout strings.Builder
	routerMain(nil, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":`+args+`}}`+"\n"), &stdout, nil)
	var got struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(stdout.String()), &got) != nil || got.Result.IsError {
		t.Fatal(stdout.String())
	}
	if got.Result.Structured["context_data"] != packet || got.Result.Structured["tokens_used"] != float64(41) || got.Result.Structured["result"] != nil {
		t.Fatal("final consumer lost packet", stdout.String())
	}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/search" {
		t.Fatal(*calls)
	}
	sent := (*calls)[0].payload.(map[string]any)
	for _, key := range []string{"conversation_context", "whole_items", "best_effort_lanes"} {
		if sent[key] != true {
			t.Fatal(key, sent)
		}
	}
	n, _ := host.Int(sent["budget_tokens"])
	if n != 3000 || sent["token_encoding"] != "cl100k_base" {
		t.Fatal(sent)
	}
}
func TestRecallContextOptionsRejectBeforeRequest(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[]}`)
	for _, raw := range []string{
		`{"conversation_context":true}`, `{"whole_items":"true"}`, `{"best_effort_lanes":1}`,
		`{"budget_tokens":0,"token_encoding":"cl100k_base"}`, `{"budget_tokens":10.5,"token_encoding":"cl100k_base"}`,
		`{"budget_tokens":true,"token_encoding":"cl100k_base"}`, `{"budget_tokens":3000,"token_encoding":"unknown"}`, `{"token_encoding":"cl100k_base"}`,
	} {
		if _, err := recall("query", nil, nil, j(t, raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if _, err := recall("", []any{"memory"}, nil, j(t, `{"whole_items":true}`)); err == nil {
		t.Fatal("ids ignored option")
	}
	if len(*calls) != 0 {
		t.Fatal("invalid request reached server", *calls)
	}
	opts, err := recallSearchOptions(j(t, `{"conversation_context":false,"whole_items":false,"best_effort_lanes":false}`))
	if err != nil || len(opts) != 0 {
		t.Fatal("default changed", opts, err)
	}
}
func TestRecallPackedResponseValidation(t *testing.T) {
	swapRouter(t)
	opts := j(t, `{"budget_tokens":3000,"token_encoding":"cl100k_base"}`)
	for _, reply := range []string{
		`{"items":[]}`, `{"items":[],"context_data":"{}","tokens_used":1,"token_encoding":"cl100k_base"}`,
		`{"items":[],"context_data":"{\"authority\":\"data_only_untrusted\",\"items\":[]}","tokens_used":3001,"token_encoding":"cl100k_base"}`,
		`{"items":[],"context_data":"{\"authority\":\"data_only_untrusted\",\"items\":[]}","tokens_used":30,"token_encoding":"o200k_base"}`,
	} {
		fakeRequest(t, reply)
		if _, err := recall("query", nil, nil, opts); err == nil {
			t.Fatal("accepted invalid packet", reply)
		}
	}
}
