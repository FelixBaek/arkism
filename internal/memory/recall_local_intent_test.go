package memory

import (
	"slices"
	"strings"
	"testing"
)

func TestLocalRecallQueriesPreserveQuestionIntent(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"When did Rowan and Avery move, if the permit was not delayed?", []string{"When did Rowan move, if the permit was not delayed?", "When did Avery move, if the permit was not delayed?"}},
		{"How often would Rowan and Avery cycle if it did not rain?", []string{"How often would Rowan cycle if it did not rain?", "How often would Avery cycle if it did not rain?"}},
		{"Which tools would Rowan and Avery not replace before 2025?", []string{"Which tools would Rowan not replace before 2025?", "Which tools would Avery not replace before 2025?"}},
		{"What happened to Rowan and Avery?", []string{"What happened to Rowan?", "What happened to Avery?"}},
		{"Rowan and Avery did not replace the pump.", []string{"Rowan did not replace the pump.", "Avery did not replace the pump."}},
		{"When\tdid Rowan  and\nAvery, if eligible, apply?", []string{"When\tdid Rowan, if eligible, apply?", "When\tdid Avery, if eligible, apply?"}},
		{"What did Rowan and Avery repair with Casey and Drew?", []string{"What did Rowan repair with Casey and Drew?", "What did Avery repair with Casey and Drew?"}},
		{"When did Élise and Áron move?", []string{"When did Élise move?", "When did Áron move?"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got := localRecallQueries(tc.query); !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLocalRecallQueriesDoNotRewriteSimpleOrAmbiguousQuestions(t *testing.T) {
	for _, query := range []string{
		"When did Rowan move?",
		"How often would Rowan cycle if it did not rain?",
		"What did Rowan repair?",
		"What activities do rowan and avery enjoy?",
		"What are Rowan and Avery's preferences?",
		"When and How are these events related?",
		"어제 배포 서버와 백업 서버 상태는?",
		"What did Rowan and Avery say about " + strings.Repeat("the unusually detailed requirement ", 10) + "?",
	} {
		if got := localRecallQueries(query); len(got) != 0 {
			t.Fatalf("unexpected query rewrite: %q -> %q", query, got)
		}
	}
}

func TestLocalRecallIntentServiceEmbedsOnlyOriginalOrExplicitPair(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		on          bool
		status      string
		queries     []string
	}{
		{"simple-temporal", "When did Rowan repair the pump?", true, "local_unchanged", nil},
		{"simple-conditional", "How often would Rowan repair the pump if it did not leak?", true, "local_unchanged", nil},
		{"pair", "When would Rowan and Avery repair the pump if it did not leak?", true, "local_applied", []string{"When would Rowan repair the pump if it did not leak?", "When would Avery repair the pump if it did not leak?"}},
		{"default-off", "When would Rowan and Avery repair the pump if it did not leak?", false, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			mem := f.activeMemory("Rowan and Avery repair the pump only if it did not leak.", "pump")
			if tc.on {
				setLocalExpansionForTest(t, f, true)
			}
			f.emb.calls = nil
			out := f.search(f.agent, SearchInput{Scope: scopeA, Query: tc.query, Modes: []string{"exact", "fts", "vector"}, Limit: 10, Explain: true})
			wantCalls := []string{"query:" + tc.query}
			for _, query := range tc.queries {
				wantCalls = append(wantCalls, "query:"+query)
			}
			if !slices.Equal(f.emb.calls, wantCalls) {
				t.Fatalf("embedder inputs=%q, want %q", f.emb.calls, wantCalls)
			}
			if out.Explain == nil || out.Explain.Expand != tc.status || !slices.Equal(out.Explain.ExpandQueries, tc.queries) || hitByID(out.Items, mem.ID) == nil {
				t.Fatal("intent-preserving search lost original evidence or expansion contract", out.Items, out.Explain)
			}
		})
	}
}
