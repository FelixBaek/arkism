package memory

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestContextBuildHTTP(t *testing.T) {
	for _, encoding := range []string{"cl100k_base", "o200k_base"} {
		for _, count := range []int{0, 3, 8} {
			t.Run(fmt.Sprintf("%s/%d", encoding, count), func(t *testing.T) {
				f := newFixture(t)
				for i := 0; i < 10; i++ {
					f.activeMemory(fmt.Sprintf("context needle 한글 %d", i), "needle")
				}
				in := map[string]any{"scope": scopeA, "task": "needle", "token_encoding": encoding, "modes": []string{"fts"}, "budget_tokens": 64000}
				want := 6
				if count != 0 {
					in["max_items"] = count
					want = count
				}
				w := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(in)))
				req.Header.Set("Authorization", "Bearer "+f.human)
				Handler(f.svc).ServeHTTP(w, req)
				var out ContextPack
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != want || out.Partial || out.Notice == "" {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				codec, _ := tokenizer.Get(tokenizer.Encoding(encoding))
				n, e := codec.Count(out.ContextData)
				if e != nil || n != out.TokensUsed || n > 64000 || out.Authority != AuthorityDataOnly {
					t.Fatal("budget or authority", e)
				}
				var data ContextBundle
				if json.Unmarshal([]byte(out.ContextData), &data) != nil || structuredText(data.Items) != structuredText(out.Items) {
					t.Fatal("payload mismatch")
				}
				receipt, e := f.repo.Get(testCtx, "jobs", scopeA, out.ContextID)
				if e != nil || receipt.Usage == nil || len(receipt.Usage.Items) != want || receipt.Usage.Purpose != "answer" {
					t.Fatal("receipt mismatch", e)
				}
				for i, h := range out.Items {
					if h.Authority != AuthorityDataOnly || h.Status != "active" || h.Scope != scopeA || len(h.Sources) == 0 || receipt.Usage.Items[i].Memory.ID != h.ID {
						t.Fatal("provenance or usage")
					}
				}
				delete(in, "budget_tokens")
				in["max_items"] = nil
				req = httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(in)))
				req.Header.Set("Authorization", "Bearer "+f.human)
				w = httptest.NewRecorder()
				Handler(f.svc).ServeHTTP(w, req)
				out = ContextPack{}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.TokensUsed > 2000 || len(out.Items) > 6 {
					t.Fatal("default budget", w.Code)
				}
			})
		}
	}
}

func TestContextBuildInvalidHTTP(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"max_items", 0}, {"max_items", 2}, {"max_items", 9}, {"budget_tokens", 0}, {"budget_tokens", -1}, {"budget_tokens", 64001},
		{"token_encoding", ""}, {"token_encoding", "unknown"}, {"task", " "}, {"task", strings.Repeat("한", 1001)},
		{"destination", "elsewhere"}, {"modes", []string{"hub"}}, {"query", "needle"}, {"limit", 3}, {"project_id", "project"}, {"agent_id", "codex"},
	} {
		t.Run(fmt.Sprint(tc.key, tc.value), func(t *testing.T) {
			f := newFixture(t)
			called := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error { called = true; return nil }}
			in := map[string]any{"scope": scopeA, "task": "needle", "token_encoding": "o200k_base", "modes": []string{"fts"}}
			in[tc.key] = tc.value
			req := httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code != 400 || called || len(f.repo.rows("jobs")) != 0 {
				t.Fatal("invalid input reached side effects", w.Code, called)
			}
		})
	}
}

func TestContextBuildBoundaries(t *testing.T) {
	for _, mode := range []string{"denied", "unavailable", "restricted", "changed", "audit", "actor", "tiny"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := f.activeMemory("needle context body")
			in := ContextBuildInput{Scope: scopeA, Task: "needle", TokenEncoding: "o200k_base", Modes: []string{"fts"}}
			if mode == "restricted" {
				m.Sensitivity = "restricted"
				f.repo.store[colMemories][recKey(m.Scope, m.ID)] = m
				in.Destination = "external:test"
			}
			if mode == "audit" {
				f.repo.failAudit = true
			}
			if mode == "tiny" {
				n := 1
				in.BudgetTokens = &n
			}
			changed := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
				if a.Action == "context:read" && a.ResourceID == m.ID && !changed {
					changed = true
					switch mode {
					case "denied":
						d.Allowed = false
						return nil
					case "unavailable":
						return ErrUnavailable
					case "changed":
						m.Text = "changed"
						return f.repo.Replace(testCtx, colMemories, m, m.Revision)
					case "actor":
						d.PrincipalID = "other"
					}
				}
				return nil
			}}
			before := len(f.repo.rows("jobs"))
			out, e := f.svc.BuildContext(testCtx, f.human, in)
			if mode == "denied" || mode == "unavailable" || mode == "restricted" {
				if mode != "restricted" && !slices.Contains(out.Degraded, "context_content_filtered") {
					t.Fatal("missing policy filter marker")
				}
				if e != nil || !out.Partial || len(out.Items) != 0 || strings.Contains(out.ContextData, m.Text) {
					t.Fatal("filtered context", e, out)
				}
				receipt, e := f.repo.Get(testCtx, "jobs", scopeA, out.ContextID)
				if e != nil || len(receipt.Usage.Items) != 0 {
					t.Fatal("filtered receipt", e)
				}
			} else if e == nil || out.ContextID != "" || out.ContextData != "" || len(out.Items) != 0 || len(f.repo.rows("jobs")) != before {
				t.Fatal("failed build leaked data or receipt", e)
			}
		})
	}
}
