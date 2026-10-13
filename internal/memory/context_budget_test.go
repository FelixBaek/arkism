package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestContextBudgetPacking(t *testing.T) {
	for _, encoding := range []string{"cl100k_base", "o200k_base"} {
		t.Run(encoding, func(t *testing.T) {
			short := Hit{ID: "short", Text: "안녕하세요 🔑 é <>&", Authority: AuthorityDataOnly, Sources: []SourceRef{{SourceID: "proof"}}}
			small := ContextBundle{Items: []Hit{short}}
			if e := packContext(testCtx, &small, 64000, encoding); e != nil {
				t.Fatal(e)
			}
			long := short
			long.ID = "large"
			long.Sources[0].Locator = strings.Repeat("metadata ", 2000)
			// Do not share source backing arrays between trials.
			short.Sources = []SourceRef{{SourceID: "proof"}}
			out := ContextBundle{Items: []Hit{long, short, short}}
			if e := packContext(testCtx, &out, small.TokensUsed, encoding); e != nil {
				t.Fatal(e)
			}
			if len(out.Items) != 1 || out.Items[0].ID != "short" || out.TokensUsed != small.TokensUsed {
				t.Fatal("metadata or exact bound not enforced", out)
			}
			var decoded struct {
				Authority string `json:"authority"`
				Items     []Hit  `json:"items"`
			}
			if e := json.Unmarshal([]byte(out.ContextData), &decoded); e != nil || decoded.Authority != AuthorityDataOnly || !reflect.DeepEqual(decoded.Items, out.Items) {
				t.Fatal("data serialization differs", e)
			}
			codec, _ := tokenizer.Get(tokenizer.Encoding(encoding))
			if n, e := codec.Count(out.ContextData); e != nil || n != out.TokensUsed {
				t.Fatal(n, e)
			}
			out = ContextBundle{Items: []Hit{short}}
			if e := packContext(testCtx, &out, small.TokensUsed-1, encoding); e != nil || len(out.Items) != 0 {
				t.Fatal("one token overflow", out, e)
			}
			if e := packContext(testCtx, &out, 1, encoding); !errors.Is(e, ErrInvalid) {
				t.Fatal(e)
			}
		})
	}
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	if e := packContext(ctx, &ContextBundle{}, 64000, "o200k_base"); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestSearchBudgetUsageAndFailures(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("budget 안녕하세요 기억", "budget")
	in := SearchInput{Scope: scopeA, Query: "budget", BudgetTokens: 64000, TokenEncoding: "o200k_base"}
	full, e := f.svc.Search(testCtx, f.human, in)
	if e != nil || len(full.Items) != 1 || full.ContextData == "" {
		t.Fatal(full, e)
	}
	in.BudgetTokens = full.TokensUsed - 1
	out, e := f.svc.Search(testCtx, f.human, in)
	if e != nil || len(out.Items) != 0 || out.TokensUsed > in.BudgetTokens {
		t.Fatal(out, e)
	}
	receipt, e := f.repo.Get(testCtx, "jobs", scopeA, out.ContextID)
	if e != nil || len(receipt.Usage.Items) != 0 || strings.Contains(structuredText(receipt), m.Text) {
		t.Fatal("usage not bounded", receipt, e)
	}
	before := len(f.repo.rows("jobs"))
	for _, invalid := range []SearchInput{
		{Scope: scopeA, Query: "budget", BudgetTokens: 1, TokenEncoding: "o200k_base"},
		{Scope: scopeA, Query: "budget", BudgetTokens: 10},
		{Scope: scopeA, Query: "budget", TokenEncoding: "o200k_base"},
		{Scope: scopeA, Query: "budget", BudgetTokens: 10, TokenEncoding: "unknown"},
		{Scope: scopeA, Query: "budget", BudgetTokens: 64001, TokenEncoding: "o200k_base"},
	} {
		out, e = f.svc.Search(testCtx, f.human, invalid)
		if !errors.Is(e, ErrInvalid) || out.ContextData != "" || len(f.repo.rows("jobs")) != before {
			t.Fatal(out, e)
		}
	}
	in.BudgetTokens = 64000
	f.repo.failAudit = true
	out, e = f.svc.Search(testCtx, f.human, in)
	if !errors.Is(e, ErrUnavailable) || out.ContextData != "" || out.TokensUsed != 0 || len(f.repo.rows("jobs")) != before {
		t.Fatal("audit failure leaked context", out, e)
	}
	f.repo.failAudit = false
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: m.ID}
	out, e = f.svc.Search(testCtx, f.human, in)
	if e != nil || len(out.Items) != 0 || strings.Contains(out.ContextData, m.Text) {
		t.Fatal("denied data leaked", out, e)
	}
}

// Golden counts from Python tiktoken 0.14.0 encode_ordinary, independently run
// on server. Special-token-looking source text remains ordinary data.
func TestContextTokenizerGolden(t *testing.T) {
	texts := []string{"Hello, world!", "안녕하세요. 기억과 스킬을 관리합니다.", "<|endoftext|> 🔑 é 中文"}
	for encoding, want := range map[string][]int{"cl100k_base": {4, 18, 13}, "o200k_base": {4, 11, 12}} {
		codec, e := tokenizer.Get(tokenizer.Encoding(encoding))
		if e != nil {
			t.Fatal(e)
		}
		for i, text := range texts {
			if n, e := codec.Count(text); e != nil || n != want[i] {
				t.Fatal(encoding, i, n, e)
			}
		}
	}
}

func TestContextBudgetWorkBounds(t *testing.T) {
	for _, text := range []string{strings.Repeat("가", 64000), strings.Repeat("a", 64000), strings.Repeat(" ", 64000)} {
		out := ContextBundle{Items: []Hit{{Text: text}}}
		if e := packContext(testCtx, &out, 64000, "o200k_base"); !errors.Is(e, ErrUnavailable) || out.ContextData != "" {
			t.Fatal("unbounded tokenization", e)
		}
	}
	if !boundedTokenRuns(strings.Repeat("a", 4096)) || boundedTokenRuns(strings.Repeat("a", 4097)) {
		t.Fatal("run boundary")
	}
}
