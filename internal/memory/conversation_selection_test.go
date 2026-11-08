package memory

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestConversationSelectionRanksReturnedEvidenceNotBatchMetadata(t *testing.T) {
	rows := []ScoredHit{
		{Hit: Hit{ID: "metadata", Text: "Avery: copper garden notes.", Summary: "copper pump gasket", keywords: []string{"pump", "gasket"}}, Score: 1},
		{Hit: Hit{ID: "evidence", Text: "Avery: The copper pump gasket cracked during testing."}, Score: .98},
	}
	before := slices.Clone(rows)
	got := selectConversationCandidates("copper pump gasket", rows, nil, nil, 1)
	if len(got) != 1 || got[0].ID != "evidence" || got[0].Text != rows[1].Text || !reflect.DeepEqual(rows, before) {
		t.Fatal("actual evidence did not outrank batch metadata, or input changed", got)
	}
}

func TestConversationSelectionMakesRoomForNewEvidence(t *testing.T) {
	dup := "Avery: The copper pump gasket cracked during pressure testing."
	fresh := "Rowan: The copper pump gasket replacement passed the final inspection."
	rows := []ScoredHit{
		{Hit: Hit{ID: "first", Text: dup}, Score: 1},
		{Hit: Hit{ID: "repeat", Text: dup}, Score: .99},
		{Hit: Hit{ID: "new", Text: fresh}, Score: .98},
	}
	got := selectConversationCandidates("copper pump gasket", rows, nil, nil, 2)
	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "new" {
		t.Fatal("repeated excerpt spent both slots", got)
	}
	for _, alreadyShown := range []bool{false, true} {
		var memories, existing []Hit
		if alreadyShown {
			existing = []Hit{{Text: dup}}
		} else {
			memories = []Hit{{Text: dup}}
		}
		got = selectConversationCandidates("copper pump gasket", rows, memories, existing, 1)
		if len(got) != 1 || got[0].ID != "new" {
			t.Fatal("ignored already delivered evidence", alreadyShown, got)
		}
	}
}

func TestConversationSelectionPreservesQualificationAndVectorOnlyEvidence(t *testing.T) {
	plain := conversationPassageOf("The copper pump gasket passed the pressure test")
	for _, text := range []string{
		"The copper pump gasket did not pass the pressure test",
		"Not the copper pump gasket passed the pressure test",
		"The copper pump gasket passed the pressure test but failed the next inspection",
		"The copper valve gasket passed the pressure test",
	} {
		if conversationPassageRepeated(conversationPassageOf(text), plain) || conversationPassageRepeated(plain, conversationPassageOf(text)) {
			t.Fatal("changed fact classified as repeated", text)
		}
	}
	if conversationPassageRepeated(conversationPassageOf("The copper pump remained active for 2 days"), conversationPassageOf("The copper pump remained active for 3 days")) {
		t.Fatal("quantity difference erased by duplicate normalization")
	}
	rows := []ScoredHit{{Hit: Hit{ID: "semantic", Text: "The machine was restored successfully."}, Score: 1}}
	got := selectConversationCandidates("copper pump repair", rows, nil, nil, 5)
	if len(got) != 1 || got[0].ID != "semantic" {
		t.Fatal("discarded vector-only candidate", got)
	}
	if got := selectConversationCandidates("copper pump", rows, nil, nil, 0); len(got) != 0 {
		t.Fatal("ignored remaining limit", got)
	}
}

// Deliberately stale search metadata forces conversationTurns to re-read the
// source. The fixture uses no database, model, or external service.
type conversationSelectionRepo struct {
	Repository
	hits []Hit
}

func (r conversationSelectionRepo) SearchCollection(context.Context, string, SearchInput, []float64, string) ([][]Hit, error) {
	return [][]Hit{nil, nil, slices.Clone(r.hits)}, nil
}

func TestConversationSelectionServiceRequiresReturnedLexicalEvidence(t *testing.T) {
	f := newFixture(t)
	r := conversationBatch(f, "Avery: unrelated garden discussion.", "", 0, func(r *Record) { r.Summary = "copper pump gasket" })
	f.svc.Repo = conversationSelectionRepo{Repository: f.repo, hits: []Hit{{ID: r.ID, Collection: "sources", Text: "copper pump gasket", Reasons: []string{reasonTextSearch}}}}
	in := SearchInput{Scope: scopeA, Query: "copper pump gasket", Modes: []string{"fts"}, Include: []string{"sources"},
		space: &SpaceConfig{CaptureDensity: captureDensityConversation, RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
	var out ContextBundle
	f.svc.conversationTurns(testCtx, f.agent, in, colMemories, &out)
	if len(out.Conversation) != 0 {
		t.Fatal("omitted summary bypassed excerpt relevance threshold", out.Conversation)
	}
}

func TestConversationSelectionServiceOptInAndSourceBoundaries(t *testing.T) {
	f := newFixture(t)
	var records []Record
	for i := range 5 {
		records = append(records, conversationBatch(f, fmt.Sprintf("Avery: copper gardening notes and ordinary morning discussion number %d.", i), fmt.Sprintf("selection-%d", i), 0))
	}
	late := strings.Repeat("ordinary unrelated discussion. ", 30) + "\nRowan: The copper pump gasket cracked during pressure testing.\n<private>hidden pressure location</private>"
	good := conversationBatch(f, late, "selection-good", 0)
	records = append(records, good)
	for _, change := range []func(*Record){
		func(r *Record) { r.Sensitivity = "restricted" },
		func(r *Record) { r.ContentErased = true },
		func(r *Record) { r.PrivacyQuarantined = true },
	} {
		records = append(records, conversationBatch(f, "Secret: copper pump gasket hidden evidence.", "", 0, change))
	}
	var hits []Hit
	for _, r := range records {
		hits = append(hits, Hit{ID: r.ID, Collection: "sources", Text: "copper pump gasket", Reasons: []string{reasonVector}})
	}
	f.svc.Repo = conversationSelectionRepo{Repository: f.repo, hits: hits}
	in := SearchInput{Scope: scopeA, Query: "copper pump gasket", Modes: []string{"vector"}, Include: []string{"sources"}, ConversationContext: true,
		space: &SpaceConfig{CaptureDensity: captureDensityConversation, RecallQuality: &SpaceRecallQuality{}}}
	var baseline ContextBundle
	f.svc.conversationTurns(testCtx, f.agent, in, colMemories, &baseline)
	if len(baseline.Conversation) != 5 || hitByID(baseline.Conversation, good.ID) != nil {
		t.Fatal("default source order changed", baseline.Conversation)
	}
	in.space.RecallQuality.LocalExpand = true
	var improved ContextBundle
	f.svc.conversationTurns(testCtx, f.agent, in, colMemories, &improved)
	if len(improved.Conversation) != 5 || improved.Conversation[0].ID != good.ID {
		t.Fatal("failed to inspect and select evidence beyond first five batches", improved.Conversation)
	}
	for _, h := range improved.Conversation {
		if strings.Contains(h.Text, "hidden") || utf8.RuneCountInString(h.Text) > sessionLaneRunes || h.Authority != AuthorityDataOnly || h.Origin != conversationSourceKind {
			t.Fatal("redaction, excerpt budget or authority changed", h)
		}
		if _, ok := improved.conversationRecords[Ref{Collection: "sources", ID: h.ID, Revision: h.Revision}]; !ok {
			t.Fatal("missing exact revision attribution", h)
		}
		for _, blocked := range records[6:] {
			if h.ID == blocked.ID {
				t.Fatal("stale metadata bypassed source revalidation", h)
			}
		}
	}
	if len(improved.conversationRecords) != 5 || !strings.Contains(redactSessionPrivate(good.Text), improved.Conversation[0].Text) {
		t.Fatal("selected record accounting or verbatim window changed", improved)
	}
	in.space.RecallQuality.LocalExpand = false
	var restored ContextBundle
	f.svc.conversationTurns(testCtx, f.agent, in, colMemories, &restored)
	if !reflect.DeepEqual(restored.Conversation, baseline.Conversation) {
		t.Fatal("disabling did not restore the baseline", restored.Conversation, baseline.Conversation)
	}
	in.space.RecallQuality.LocalExpand = true
	for _, mutate := range []func(*SearchInput){
		func(q *SearchInput) { q.Destination = "external:test" },
		func(q *SearchInput) { q.contextView = &ContextInvocationView{} },
		func(q *SearchInput) { q.IncludeInactive = true },
	} {
		q := in
		mutate(&q)
		var out ContextBundle
		f.svc.conversationTurns(testCtx, f.agent, q, colMemories, &out)
		if len(out.Conversation) != 0 {
			t.Fatal("selection crossed excluded context", out.Conversation)
		}
	}
}
