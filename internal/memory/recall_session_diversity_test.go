package memory

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func sessionRow(id, event, text string, score float64, vector ...float64) ScoredHit {
	r := diverseRow(id, score, vector...)
	r.Text, r.Tags = text, []string{"capture", "event_id:" + event}
	return r
}

func rowIDs(rows []ScoredHit) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// One capture event restating one event fills the head no longer: a second telling from the
// same event (similarity 0.88, under the capture merge bar) gives way to another conversation's
// fact, and a telling that adds a figure is not a second telling.
func TestSelectDiverseSameSessionDominance(t *testing.T) {
	rows := []ScoredHit{
		sessionRow("a", "e1", "The speaker trains for a charity run", 1, 1, 0),
		sessionRow("a-retold", "e1", "The speaker trains for the charity run", 0.85, 0.88, 0.475),
		sessionRow("other", "e2", "The speaker volunteers at the shelter", 0.8, 0, 1),
	}
	if got := rowIDs(selectDiverse(slices.Clone(rows), 2)); !slices.Equal(got, []string{"a", "other"}) {
		t.Fatal("a retelling from the same event kept over another conversation's fact", got)
	}
	// The same similarity across two different captures is not a retelling: plain score order.
	apart := slices.Clone(rows)
	apart[1].Tags = []string{"capture", "event_id:e3"}
	if got := rowIDs(selectDiverse(apart, 2)); !slices.Equal(got, []string{"a", "a-retold"}) {
		t.Fatal("rows of different captures were penalised below the capture merge bar", got)
	}
	// A session tag groups rows the same way an event id does.
	session := slices.Clone(rows)
	session[0].Tags, session[1].Tags = []string{"session:s1"}, []string{"session:s1"}
	if got := rowIDs(selectDiverse(session, 2)); !slices.Equal(got, []string{"a", "other"}) {
		t.Fatal("same session retelling kept", got)
	}
	// Another fact of the same conversation (few of its words in the pick) is not a retelling.
	other := slices.Clone(rows)
	other[1].Text = "Their favourite novel is on the shelf in the hallway"
	if got := rowIDs(selectDiverse(other, 2)); !slices.Equal(got, []string{"a", "a-retold"}) {
		t.Fatal("a different fact of the same session was penalised", got)
	}
	// Different scopes never group.
	scoped := slices.Clone(rows)
	scoped[1].Scope = Scope{WorkspaceID: "other"}
	if got := rowIDs(selectDiverse(scoped, 2)); !slices.Equal(got, []string{"a", "a-retold"}) {
		t.Fatal("rows of another scope were grouped", got)
	}

	// A telling that adds a figure is never penalised as a retelling of the pick.
	figure := []ScoredHit{
		sessionRow("timed", "e1", "The speaker recently finished a 5K in 35 minutes", 1, 1, 0),
		sessionRow("pace", "e1", "The speaker kept a pace of 420 seconds per kilometre", 0.85, 0.88, 0.475),
		sessionRow("other", "e2", "The speaker volunteers at the shelter", 0.8, 0, 1),
	}
	if got := rowIDs(selectDiverse(figure, 2)); !slices.Equal(got, []string{"timed", "pace"}) {
		t.Fatal("a telling with a new figure was penalised", got)
	}
}

func TestRetells(t *testing.T) {
	thin, rich := tokenSet(searchQueryTerms("The speaker recently ran a 5K")), tokenSet(searchQueryTerms("The speaker recently finished a 5K in 35 minutes"))
	met, park := tokenSet(searchQueryTerms("Deborah met her new neighbor Anna on 2023-01-31")), tokenSet(searchQueryTerms("Deborah met Anna at yoga in the park on 2023-01-31"))
	camaro := tokenSet(searchQueryTerms("The speaker is adding wire details to the 1/24 scale '69 Camaro model"))
	if retells(park, met) || retells(rich, thin) || !retells(tokenSet(searchQueryTerms("Deborah met Anna")), met) || retells(map[string]bool{}, met) ||
		!retells(tokenSet(searchQueryTerms("The speaker owns a 1/24 scale '69 Camaro model")), camaro) ||
		retells(tokenSet(searchQueryTerms("Evan finds nature healing")), tokenSet(searchQueryTerms("Evan finds that nature brings peace and clarity"))) {
		t.Fatal("retells")
	}
}

func TestEarlierStateIntent(t *testing.T) {
	for _, q := range []string{
		"Where do I initially keep my old sneakers?", "What used to be my favourite band?",
		"Originally, which city did we plan?", "At first, what color was the car?",
		"처음에 운동화를 어디에 뒀지?", "원래 회의는 몇 시였어?", "最初はどこに住んでいましたか", "我以前住在哪里", "¿Dónde vivía al principio?",
		"Où habitais-je au début ?", "Wo habe ich ursprünglich gewohnt?",
	} {
		if !earlierStateIntent(q) {
			t.Error("missed", q)
		}
	}
	for _, q := range []string{
		"Where do I keep my old sneakers now?", "How much faster did I finish the 5K run?", "Which user owns the account?",
		"운동화를 어디에 뒀지?", "Où est la gare ?", "Initiallyx is not a word", "",
	} {
		if earlierStateIntent(q) {
			t.Error("false intent", q)
		}
	}
}

// A question about an earlier state takes the earliest of the best conversation turns, so the
// turn said before the newer state reaches the result; other questions keep the score order.
func TestConversationLaneEarlierStateTakesOlderTurns(t *testing.T) {
	f := newFixture(t)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var hits []Hit
	var old Record
	for i := range 7 {
		at := day.AddDate(0, 0, 10-i) // later batches score higher and were said more recently
		r := conversationBatch(f, fmt.Sprintf("Avery: the sneakers go on the shoe rack, note %d.", i), fmt.Sprintf("sneakers-%d", i), 0, func(r *Record) { r.CollectedAt = &at })
		if i == 6 {
			old = r
		}
		hits = append(hits, Hit{ID: r.ID, Collection: "sources", Text: r.Text, Reasons: []string{reasonVector}})
	}
	f.svc.Repo = conversationSelectionRepo{Repository: f.repo, hits: hits}
	space := &SpaceConfig{CaptureDensity: captureDensityConversation, RecallQuality: &SpaceRecallQuality{}}
	run := func(query string) []Hit {
		var out ContextBundle
		f.svc.conversationTurns(testCtx, f.agent, SearchInput{Scope: scopeA, Query: query, Modes: []string{"vector"}, Include: []string{"sources"}, space: space}, colMemories, &out)
		return out.Conversation
	}
	plain := run("Where do I keep my old sneakers?")
	if len(plain) != conversationLaneHits || hitByID(plain, old.ID) != nil || plain[0].ID != hits[0].ID {
		t.Fatal("score order changed for a plain question", plain)
	}
	earlier := run("Where did I initially keep my old sneakers?")
	if len(earlier) != conversationLaneHits || earlier[0].ID != old.ID {
		t.Fatal("the earliest turn did not come first for an earlier-state question", earlier)
	}
	for i := 1; i < len(earlier); i++ {
		if earlier[i].CollectedAt.Before(*earlier[i-1].CollectedAt) {
			t.Fatal("earlier-state turns out of time order", earlier)
		}
	}
}

// Recency no longer discounts the older memory when the question asks about an earlier state.
func TestRecallEarlierStateSkipsRecency(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	older, newer := now.AddDate(0, -6, 0), now.AddDate(0, 0, -1)
	rows := func() []ScoredHit {
		return []ScoredHit{
			{Hit: Hit{ID: "old", Kind: "episodic", EffectiveAt: &older}, Score: 1},
			{Hit: Hit{ID: "new", Kind: "episodic", EffectiveAt: &newer}, Score: 0.9},
		}
	}
	if got := decayByRecency(rows(), now, nil, 2); got[0].ID != "new" {
		t.Fatal("recency no longer applies to plain questions", got)
	}
	if got := decayByRecency(rows(), time.Time{}, nil, 2); got[0].ID != "old" || got[0].Score != 1 {
		t.Fatal("the zero clock still discounted age", got)
	}
}
