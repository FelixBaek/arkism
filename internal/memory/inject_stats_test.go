package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tiktoken-go/tokenizer"
)

func TestCountTokensRoute(t *testing.T) {
	svc := &Service{Repo: newFakeRepo(), Auth: &fakeAuth{actors: map[string]fakeActor{
		"reader": actor("reader", "human", false, "read"),
		"writer": actor("writer", "human", false, "write"),
	}}}
	h := handler(svc, 5*time.Second)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u"}
	call := func(token string, body any) (int, TokenCount) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/v1/context/tokens", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out TokenCount
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	text := "Arkism 기억 목차\n- [user] 보고는 짧게 🔑"
	code, out := call("reader", map[string]any{"scope": scope, "text": text})
	codec, _ := tokenizer.Get(tokenizer.Cl100kBase)
	want, _ := codec.Count(text)
	if code != http.StatusOK || out.Tokens != want || out.Chars != len([]rune(text)) || out.TokenEncoding != "cl100k_base" {
		t.Fatal(code, out, want)
	}
	if code, out = call("reader", map[string]any{"scope": scope, "text": "", "token_encoding": "o200k_base"}); code != http.StatusOK || out.Tokens != 0 || out.TokenEncoding != "o200k_base" {
		t.Fatal("empty text", code, out)
	}
	for name, body := range map[string]map[string]any{
		"encoding": {"scope": scope, "text": "x", "token_encoding": "p50k_base"},
		"too long": {"scope": scope, "text": strings.Repeat("가 ", tokenCountMaxChars/2+1)},
		"long run": {"scope": scope, "text": strings.Repeat("a", 5000)},
		"unknown":  {"scope": scope, "text": "x", "extra": 1},
	} {
		if code, _ := call("reader", body); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
			t.Fatal(name, code)
		}
	}
	if code, _ := call("writer", map[string]any{"scope": scope, "text": "x"}); code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Fatal("no read right", code)
	}
	if code, _ := call("nobody", map[string]any{"scope": scope, "text": "x"}); code < 400 {
		t.Fatal("unknown token", code)
	}
}

func TestValidMemoryUseWindows(t *testing.T) {
	ok := MemoryUseStats{Days: 30, Sessions: 2, Injections: 3, Used: 1, Items: []MemoryUseStat{{ID: "m1", Injected: 3, Used: 1}}, InjectSessions: 2, InjectChars: 900}
	if !validMemoryUse(nil) || !validMemoryUse(&ok) {
		t.Fatal("valid window refused")
	}
	for name, mut := range map[string]func(*MemoryUseStats){
		"days":           func(u *MemoryUseStats) { u.Days = 91 },
		"negative used":  func(u *MemoryUseStats) { u.Items[0].Used = -1 },
		"estimated":      func(u *MemoryUseStats) { u.Estimated = -1 },
		"unreceipted":    func(u *MemoryUseStats) { u.Items[0].Unreceipted = -1 },
		"chars no sess":  func(u *MemoryUseStats) { u.InjectSessions = 0 },
		"negative chars": func(u *MemoryUseStats) { u.InjectChars = -1 },
	} {
		u := ok
		u.Items = append([]MemoryUseStat(nil), ok.Items...)
		mut(&u)
		if validMemoryUse(&u) {
			t.Fatal(name)
		}
	}
	report := AgentToolsReport{Scope: Scope{WorkspaceID: "w", ProjectID: "p"}, Servers: []AgentToolEntry{}, Plugins: []AgentToolEntry{}, Results: []AgentToolResult{}, MemoryUse30: &ok}
	if !validAgentToolsReport(report) {
		t.Fatal("report with a 30-day window refused")
	}
	bad := ok
	bad.Used = -1
	report.MemoryUse30 = &bad
	if validAgentToolsReport(report) {
		t.Fatal("bad 30-day window accepted")
	}
}
