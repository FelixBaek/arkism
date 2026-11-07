package memory

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func setCaptureDensity(t *testing.T, f *fixture, density string) {
	t.Helper()
	cur, err := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: cur.Revision, CaptureDensity: &density}, false); err != nil {
		t.Fatal(err)
	}
}

func conversationTurn(f *fixture, text string) Record {
	return f.create(f.agent, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: conversationSourceKind, Text: text}))
}

func TestConversationLaneReturnsTurnsOnlyWhenTheSpaceKeepsConversations(t *testing.T) {
	f := newFixture(t)
	turn := conversationTurn(f, "[2023-05-08] Caroline: I went to the LGBTQ support group yesterday.")
	note := f.create(f.agent, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "notes on the LGBTQ support group"}))
	query := SearchInput{Scope: scopeA, Query: "LGBTQ support group", Modes: []string{"exact", "fts"}, Limit: 5, Include: []string{"sources"}}
	if out := f.search(f.agent, query); len(out.Conversation) != 0 {
		t.Fatal("conversation lane ran for a default space", out.Conversation)
	}
	setCaptureDensity(t, f, captureDensityConversation)
	out := f.search(f.agent, query)
	if len(out.Conversation) != 1 || out.Conversation[0].ID != turn.ID || out.Conversation[0].Origin != conversationSourceKind ||
		out.Conversation[0].Authority != AuthorityDataOnly || out.Conversation[0].CollectedAt == nil || hitByID(out.Items, turn.ID) != nil || hitByID(out.Conversation, note.ID) != nil {
		t.Fatal("conversation turn missing, mislabelled or mixed with memories", out.Conversation)
	}
	without := query
	without.Include = nil
	if out := f.search(f.agent, without); len(out.Conversation) != 0 {
		t.Fatal("turns came back without include sources")
	}
	external := query
	external.Destination = "external:test"
	if out := f.search(f.agent, external); len(out.Conversation) != 0 {
		t.Fatal("turns went to an external destination")
	}
	limited := testPrincipalToken("tok-agent-no-sources")
	f.auth.actors[limited] = actor("agent-3", "agent", true, allVerbs...)
	f.auth.actors[limited].verbs["!read:sources"] = true
	if out := f.search(limited, query); len(out.Conversation) != 0 {
		t.Fatal("turns reached a caller without read:sources")
	}
	if out := f.search(f.agent, SearchInput{Scope: scopeA, Query: "quarterly budget forecast", Modes: []string{"exact", "fts"}, Limit: 5, Include: []string{"sources"}}); len(out.Conversation) != 0 {
		t.Fatal("an unrelated turn came back", out.Conversation)
	}
}

func TestConversationTurnsAreEmbeddedOnlyForTheConversationProfile(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	sourceJob := func(id string) bool {
		return slices.ContainsFunc(f.embeddingJobs(), func(j Record) bool { return j.Target.Collection == "sources" && j.Target.ID == id })
	}
	before := conversationTurn(f, "[2023-05-08] Melanie: I painted a sunrise over the lake.")
	if sourceJob(before.ID) {
		t.Fatal("turn embedded before the space kept conversations")
	}
	setCaptureDensity(t, f, captureDensityConversation)
	doc := f.create(f.agent, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "a document source"}))
	turn := conversationTurn(f, "[2023-05-09] Melanie: I ran a charity race for mental health.")
	if sourceJob(doc.ID) || !sourceJob(turn.ID) {
		t.Fatal("embedding jobs: document", sourceJob(doc.ID), "turn", sourceJob(turn.ID))
	}
	for range 4 {
		if err := f.svc.WorkOnce(testCtx, f.work); err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if got := f.get(f.human, "sources", scopeA, turn.ID); got.Embedding == nil {
		t.Fatal("turn was not embedded")
	}
	out := f.search(f.agent, SearchInput{Scope: scopeA, Query: "Melanie charity race mental health", Modes: []string{"vector"}, Limit: 5, Include: []string{"sources"}})
	if len(out.Conversation) == 0 || out.Conversation[0].ID != turn.ID {
		t.Fatal("vector lane missed the turn", out.Conversation)
	}
}

func TestCaptureDensityAcceptsConversation(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	eff, err := f.svc.spaceSettings(testCtx, scopeA)
	if err != nil || eff.CaptureDensity != captureDensityConversation {
		t.Fatal(err, eff.CaptureDensity)
	}
}

func conversationBatch(f *fixture, text, session string, order int, change ...func(*Record)) Record {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	r := Record{ID: ID(), Scope: scopeA, Kind: conversationSourceKind, Status: statusActive, Revision: 1, Text: text, OriginProduct: "brain", SourceRef: session, CollectedAt: &at, CreatedAt: at.Add(time.Duration(order) * time.Second), UpdatedAt: at}
	for _, mutate := range change {
		mutate(&r)
	}
	if err := f.repo.Insert(testCtx, "sources", r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestConversationNeighborPreservesRankedTextAndBudget(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	anchor := conversationBatch(f, "Rowan: copper repair needs a new washer.", "session-alpha", 0)
	neighbor := conversationBatch(f, "Avery: The inlet valve is upstairs. <private>hidden location</private>\n"+strings.Repeat("가", 700)+"\nAvery: Use the brass coupling.", "session-alpha", 1)
	query := SearchInput{Scope: scopeA, Query: "copper repair", Modes: []string{"fts"}, Limit: 10, Include: []string{"sources"}}
	out := f.search(f.agent, query)
	if len(out.Items) != 0 || len(out.Conversation) != 1 {
		t.Fatal("neighbor became a ranked hit", out)
	}
	h := out.Conversation[0]
	if h.ID != anchor.ID || !strings.HasPrefix(h.Text, anchor.Text+"\nAvery:") || strings.Contains(h.Text, "hidden location") || utf8.RuneCountInString(h.Text) > sessionLaneRunes || !strings.Contains(h.Text, "brass coupling.") {
		t.Fatal("ranked excerpt, privacy or rune budget changed", h)
	}
	if !slices.ContainsFunc(h.Sources, func(ref SourceRef) bool {
		return ref.SourceID == neighbor.ID && ref.Span != nil && ref.Span.ContentHash == ContentHash(neighbor.Text)
	}) {
		t.Fatal("neighbor attribution missing", h.Sources)
	}
}

func TestConversationNeighborNeverRepeatsRankedBatches(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	a := conversationBatch(f, "Rowan: copper repair needs a washer.", "session-alpha", 0)
	b := conversationBatch(f, "Avery: copper repair also needs a wrench.", "session-alpha", 1)
	out := f.search(f.agent, SearchInput{Scope: scopeA, Query: "copper repair", Modes: []string{"fts"}, Limit: 10, Include: []string{"sources"}})
	if len(out.Conversation) != 2 {
		t.Fatal(out.Conversation)
	}
	for _, h := range out.Conversation {
		if h.Text != a.Text && h.Text != b.Text {
			t.Fatal("ranked batches were repeated", h.Text)
		}
	}
}

func TestConversationNeighborIsolation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Record)
	}{
		{"other scope", func(r *Record) { r.Scope.ProjectID = "other-project" }},
		{"other kind", func(r *Record) { r.Kind = "document" }},
		{"other conversation", func(r *Record) { r.SourceRef = "session-beta" }},
		{"other origin", func(r *Record) { r.OriginProduct = "notes" }},
		{"other time", func(r *Record) { at := r.CollectedAt.Add(time.Hour); r.CollectedAt = &at }},
		{"expired", func(r *Record) { r.Status = "expired" }},
		{"erased", func(r *Record) { r.ContentErased = true }},
		{"quarantined", func(r *Record) { r.PrivacyQuarantined = true }},
		{"restricted", func(r *Record) { r.Sensitivity = "restricted" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			a := conversationBatch(f, "Rowan: copper repair needs a washer.", "session-alpha", 0)
			conversationBatch(f, "Avery: The inlet valve is upstairs.", "session-alpha", 1, tc.mutate)
			h := HitFromRecord("sources", a)
			f.svc.completeConversationTurn(testCtx, f.agent, SearchInput{Scope: scopeA}, &h, map[string]Record{a.ID: a}, sessionLaneRunes-utf8.RuneCountInString(h.Text))
			if h.Text != a.Text {
				t.Fatal("unavailable neighbor disclosed", h.Text)
			}
		})
	}
	f := newFixture(t)
	a := conversationBatch(f, "Rowan: copper repair needs a washer.", "session-alpha", 0)
	b := conversationBatch(f, "Avery: The inlet valve is upstairs.", "session-alpha", 1)
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: b.ID}
	h := HitFromRecord("sources", a)
	f.svc.completeConversationTurn(testCtx, f.agent, SearchInput{Scope: scopeA}, &h, map[string]Record{a.ID: a}, sessionLaneRunes-utf8.RuneCountInString(h.Text))
	if h.Text != a.Text {
		t.Fatal("denied neighbor disclosed", h.Text)
	}
}

func TestConversationNeighborRejectsStaleCandidate(t *testing.T) {
	f := newFixture(t)
	a := conversationBatch(f, "Rowan: copper repair needs a washer.", "session-alpha", 0)
	b := conversationBatch(f, "Avery: The inlet valve is upstairs.", "session-alpha", 1)
	f.editRecord("sources", b.ID, func(r *Record) { r.Status = "expired" })
	h := HitFromRecord("sources", a)
	f.svc.appendConversationNeighbor(testCtx, f.agent, SearchInput{Scope: scopeA}, &h, b, sessionLaneRunes-utf8.RuneCountInString(h.Text))
	if h.Text != a.Text {
		t.Fatal("stale list candidate disclosed", h.Text)
	}
}

func TestConversationNeighborBoundedFallback(t *testing.T) {
	f := newFixture(t)
	a := conversationBatch(f, "Rowan: copper repair needs a washer.", "session-alpha", 0)
	for i := range 100 {
		conversationBatch(f, "Avery: neighboring context.", "session-alpha", i+1)
	}
	h := HitFromRecord("sources", a)
	f.svc.completeConversationTurn(testCtx, f.agent, SearchInput{Scope: scopeA}, &h, map[string]Record{a.ID: a}, sessionLaneRunes-utf8.RuneCountInString(h.Text))
	if h.Text != a.Text {
		t.Fatal("partial conversation window was grouped", h.Text)
	}
}

func TestConversationNeighborExcerptKeepsCompleteLines(t *testing.T) {
	text := "A: " + strings.Repeat("가", 30) + "\nB: yes\nC: no"
	if got := conversationNeighborExcerpt(text, 12); got != "B: yes\nC: no" {
		t.Fatal("short complete turns did not fit", got)
	}
	for _, left := range []int{-1, 0, 4} {
		if got := conversationNeighborExcerpt(text, left); got != "" {
			t.Fatal("partial turn returned", left, got)
		}
	}
}

func TestConversationNeighborSharesOnlyUnusedLaneBudget(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	first := "Rowan: copper repair " + strings.Repeat("가", 479)
	second := "Avery: copper repair " + strings.Repeat("나", 479)
	a := conversationBatch(f, first, "session-alpha", 0)
	neighbor := "Rowan: " + strings.Repeat("다", 140)
	conversationBatch(f, neighbor, "session-alpha", 1)
	b := conversationBatch(f, second, "session-alpha", 2)
	out := f.search(f.agent, SearchInput{Scope: scopeA, Query: "copper repair", Modes: []string{"fts"}, Limit: 10, Include: []string{"sources"}})
	if len(out.Conversation) != 2 {
		t.Fatal(out.Conversation)
	}
	total, grouped := 0, false
	for _, h := range out.Conversation {
		total += utf8.RuneCountInString(h.Text)
		original := map[string]string{a.ID: first, b.ID: second}[h.ID]
		if original == "" || !strings.HasPrefix(h.Text, original) {
			t.Fatal("ranked text changed", h)
		}
		grouped = grouped || strings.Contains(h.Text, neighbor)
	}
	if !grouped || total > len(out.Conversation)*sessionLaneRunes {
		t.Fatal("complete neighbor did not fit the shared budget", grouped, total)
	}
}
