package memory

import (
	"errors"
	"slices"
	"testing"
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
