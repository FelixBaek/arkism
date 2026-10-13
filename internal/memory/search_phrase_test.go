package memory

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func exerciseExactWholeKeyword(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	for _, term := range []string{"Claude Code", "Voyage Context 4", "docs/My Notes.md", "홍 길동", "a+b(c)", "file[1].md"} {
		t.Run(term, func(t *testing.T) {
			m := f.activeMemory("approved reference", term)
			// Text does not contain the term: only the stored literal keyword can match.
			in := SearchInput{Scope: scopeA, Query: "  " + strings.ToUpper(term) + "  ", Modes: []string{"exact"}, Limit: 1}
			req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			var out ContextBundle
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 1 || out.Items[0].ID != m.ID || !slices.Equal(out.Items[0].Reasons, []string{reasonExactKeyword}) {
				t.Fatal(w.Code, w.Body.String())
			}
			if len(f.emb.calls) != 0 {
				t.Fatal("unselected provider")
			}
			in.Scope = scopeB
			out, e := f.svc.Search(testCtx, f.human, in)
			if e != nil || len(out.Items) != 0 {
				t.Fatal("scope escape", e, out)
			}
		})
	}
}
func TestExactWholeKeyword(t *testing.T) { exerciseExactWholeKeyword(t, newFakeRepo()) }

func TestExactWholeKeywordBoundaries(t *testing.T) {
	for _, query := range []string{"", "   ", "Claude Code", "  Claude  Code  ", "a+b(c)", "file[1].md", strings.Repeat("a ", maxKeywordTokens+2), strings.Repeat("가", 84) + " abc", strings.Repeat("가", 84) + " abcd"} {
		got := exactQueryKeywords(query)
		words := tokenize(query)
		whole := strings.TrimSpace(query)
		if len(got) > maxKeywordTokens+1 || !slices.Equal(got[:len(words)], words) {
			t.Fatal("word lane changed", query, got)
		}
		expected := strings.ToLower(whole)
		if whole != "" && len(whole) <= maxKeywordLength && !slices.Contains(got, expected) {
			t.Fatal("missing bounded whole keyword", query, got)
		}
		if len(got) != len(words) && (!slices.Equal(got[len(words):], []string{expected}) || len(whole) > maxKeywordLength) {
			t.Fatal("extra keywords", got)
		}
	}
	f := newFixture(t)
	m := f.activeMemory("reference", "Claude  Code")
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "Claude Code", Modes: []string{"exact"}})
	if e != nil || len(out.Items) != 0 {
		t.Fatal("internal spaces collapsed", e, out)
	}
	out, e = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "Claude  Code", Modes: []string{"exact"}})
	if e != nil || len(out.Items) != 1 || out.Items[0].ID != m.ID {
		t.Fatal("literal spaces", e, out)
	}
}
