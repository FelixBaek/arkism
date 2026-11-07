package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func excerptEligibilityFixture() (string, string) {
	// Each complete turn fits, but two do not. A rare one-concept turn has
	// more inverse-frequency weight than the two common concepts together.
	common := "Avery: copper turbine " + strings.Repeat("ordinary detail ", 22)
	rare := "Rowan: overhaul " + strings.Repeat("ordinary detail ", 22)
	return strings.Repeat(common+"\n", 20) + rare, common
}

func TestConversationExcerptPrefersEligibleWindow(t *testing.T) {
	text, want := excerptEligibilityFixture()
	got := conversationQueryExcerpt("copper turbine overhaul", text, sessionLaneRunes)
	if got != want || !lexicallyRelevant(searchQueryTerms("copper turbine overhaul"), Hit{Text: got}) {
		t.Fatalf("eligible complete turn lost: %q", got)
	}
	if utf8.RuneCountInString(got) > sessionLaneRunes || !strings.Contains(text, got) {
		t.Fatal("excerpt exceeded budget or changed source text")
	}
}

func TestPostgresConversationExcerptDeliversEligibleWindow(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	setCaptureDensity(t, f, captureDensityConversation)
	setLocalExpansionForTest(t, f, true)
	text, want := excerptEligibilityFixture()
	source := conversationTurn(f, text)
	out := f.search(f.agent, SearchInput{Scope: scopeA, Query: "copper turbine overhaul", Modes: []string{"fts"}, Include: []string{"sources"}})
	if len(out.Conversation) != 1 || out.Conversation[0].ID != source.ID || out.Conversation[0].Text != want {
		t.Fatalf("eligible conversation missing: %+v", out.Conversation)
	}
}

func TestConversationExcerptEligibilityCountsAliasesOnce(t *testing.T) {
	common := "Avery: 복구 일정 " + strings.Repeat("ordinary detail ", 22)
	rare := "Rowan: 백업 사본 " + strings.Repeat("ordinary detail ", 22)
	text := strings.Repeat(common+"\n", 20) + rare
	got := conversationQueryExcerpt("백업 사본 복구 일정", text, sessionLaneRunes)
	if got != common || !lexicallyRelevant(searchQueryTerms("백업 사본 복구 일정"), Hit{Text: got}) {
		t.Fatalf("alias spellings counted as separate concepts: %q", got)
	}
}

func TestConversationExcerptNoEligibleWindowPreservesFallback(t *testing.T) {
	common := "Avery: copper " + strings.Repeat("ordinary detail ", 22)
	rare := "Rowan: overhaul " + strings.Repeat("ordinary detail ", 22)
	text := strings.Repeat(common+"\n", 20) + rare
	if got := conversationQueryExcerpt("copper turbine overhaul", text, sessionLaneRunes); got != rare {
		t.Fatalf("no eligible window changed the fallback: %q", got)
	}
}

func TestConversationExcerptRanksEligibleWindowsByExistingScore(t *testing.T) {
	common := "Avery: copper turbine " + strings.Repeat("ordinary detail ", 22)
	rare := "Rowan: copper overhaul " + strings.Repeat("ordinary detail ", 22)
	text := strings.Repeat(common+"\n", 20) + rare
	if got := conversationQueryExcerpt("copper turbine overhaul", text, sessionLaneRunes); got != rare {
		t.Fatalf("eligible windows lost their existing relevance order: %q", got)
	}
}
