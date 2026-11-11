//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func causalAnswerText(t *testing.T, links ...causalDraft) string {
	t.Helper()
	raw, err := json.Marshal(causalAnswer{Links: links})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func lineageRelations(t *testing.T, f *fixture, id string) []string {
	t.Helper()
	l, err := f.svc.MemoryLineage(testCtx, f.human, scopeA, id)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, link := range l.Links {
		if link.Relation == causalKind || link.Relation == causalCausedBy {
			if link.State != "available" || link.Link == nil {
				t.Fatalf("causal lineage link %+v", link)
			}
			out = append(out, link.Relation+":"+link.Reference.ID)
		}
	}
	return out
}

func TestCausalLinkHumanCreateValidationAndRead(t *testing.T) {
	f := newFixture(t)
	a := f.activeMemory("the staging deploy script lost its staging flag")
	b := f.activeMemory("the staging deploy pushed to production by mistake")
	in := CausalLinkInput{Scope: scopeA, CauseID: a.ID, EffectID: b.ID, Reason: "without the flag the script targets production"}
	if _, err := f.svc.LinkCausal(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent link: %v", err)
	}
	for _, bad := range []CausalLinkInput{
		{Scope: scopeA, CauseID: a.ID, EffectID: a.ID},
		{Scope: scopeA, CauseID: a.ID, EffectID: b.ID, Reason: "see <private>notes</private>"},
	} {
		if _, err := f.svc.LinkCausal(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if _, err := f.svc.LinkCausal(testCtx, f.human, CausalLinkInput{Scope: scopeB, CauseID: a.ID, EffectID: b.ID}); err == nil {
		t.Fatal("memories of another scope must not be linked")
	}
	link, err := f.svc.LinkCausal(testCtx, f.human, in)
	if err != nil || link.Status != statusActive || link.Origin != originHuman || link.Cause.ID != a.ID || link.Effect.ID != b.ID || link.ApprovedBy == "" {
		t.Fatalf("%+v %v", link, err)
	}
	if _, err := f.svc.LinkCausal(testCtx, f.human, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	// The reverse of a standing link is refused, so two memories never cause each other.
	if _, err := f.svc.LinkCausal(testCtx, f.human, CausalLinkInput{Scope: scopeA, CauseID: b.ID, EffectID: a.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reverse link: %v", err)
	}
	if got := lineageRelations(t, f, a.ID); !slices.Equal(got, []string{"causes:" + b.ID}) {
		t.Fatalf("lineage %v", got)
	}
	// With no status the list shows the links that stand, not only the review queue.
	if all, err := f.svc.ListCausalLinks(testCtx, f.human, scopeA, "", "", 0); err != nil || len(all.Items) != 1 || all.Items[0].ID != link.ID {
		t.Fatalf("default list %+v %v", all, err)
	}
	g, err := f.svc.MemoryGraph(testCtx, f.human, MemoryGraphQuery{Scope: scopeA, MemoryID: a.ID, Depth: 1})
	if err != nil || !slices.ContainsFunc(g.Edges, func(e MemoryGraphEdge) bool { return e.Type == GraphCauses && e.From == a.ID && e.To == b.ID }) {
		t.Fatalf("graph edges %+v %v", g.Edges, err)
	}
	if _, err := f.svc.Create(testCtx, f.agent, colMemoryRelations, CreateInput{Scope: scopeA, Kind: causalKind, Text: "self approved", Relations: []Ref{link.Cause, link.Effect}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic create of a causal row: %v", err)
	}
	// Rejecting an active link removes it from every read; a person may revive it.
	if _, err := f.svc.ReviewCausalLink(testCtx, f.human, link.ID, CausalReviewInput{Scope: scopeA, ExpectedRevision: link.Revision, Action: "reject"}); err != nil {
		t.Fatal(err)
	}
	if got := lineageRelations(t, f, a.ID); len(got) != 0 {
		t.Fatalf("after reject %v", got)
	}
	if again, err := f.svc.LinkCausal(testCtx, f.human, in); err != nil || again.Status != statusActive || again.Revision != link.Revision+2 {
		t.Fatalf("revive %+v %v", again, err)
	}
}

func TestCausalProposalNeverActiveUntilApproved(t *testing.T) {
	if _, err := newFixture(t).svc.ProposeCausalLinks(testCtx, testPrincipalToken("tok-human"), CausalProposeInput{Scope: scopeA}); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("no model: %v", err)
	}
	f, llm := wikiFixture(t)
	a := f.activeMemory("the cache server ran out of memory overnight")
	b := f.activeMemory("search answered slowly all morning")
	c := f.activeMemory("lunch orders close at noon")
	if _, err := f.svc.ProposeCausalLinks(testCtx, f.agent, CausalProposeInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent propose: %v", err)
	}
	llm.answers = []string{causalAnswerText(t,
		causalDraft{CauseID: a.ID, EffectID: b.ID, Reason: "no cache, slow search", Confidence: 0.7},
		causalDraft{CauseID: a.ID, EffectID: "not-in-group", Reason: "x", Confidence: 0.5},
		causalDraft{CauseID: c.ID, EffectID: c.ID, Reason: "x", Confidence: 0.5},
		causalDraft{CauseID: b.ID, EffectID: c.ID, Reason: "x", Confidence: 3},
	)}
	out, err := f.svc.ProposeCausalLinks(testCtx, f.human, CausalProposeInput{Scope: scopeA})
	if err != nil || len(out.Proposed) != 1 || out.Calls != 1 || len(out.Rejected) != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	p := out.Proposed[0]
	if p.Status != "candidate" || p.Origin != originModel || p.Generator == nil || p.ApprovedBy != "" {
		t.Fatalf("%+v", p)
	}
	// A proposal is never read as a link.
	if got := lineageRelations(t, f, a.ID); len(got) != 0 {
		t.Fatalf("proposal visible in lineage: %v", got)
	}
	if ids := causalHitIDs(t, f, "overnight"); slices.Contains(ids, b.ID) {
		t.Fatal("a proposal must not expand recall")
	}
	queue, err := f.svc.ListCausalLinks(testCtx, f.human, scopeA, "", "", 0)
	if err != nil || len(queue.Items) != 1 || queue.Items[0].ID != p.ID {
		t.Fatalf("%+v %v", queue, err)
	}
	// The same pair is not proposed twice.
	llm.answers = append(llm.answers, causalAnswerText(t, causalDraft{CauseID: a.ID, EffectID: b.ID, Reason: "again", Confidence: 0.7}))
	if again, err := f.svc.ProposeCausalLinks(testCtx, f.human, CausalProposeInput{Scope: scopeA}); err != nil || len(again.Proposed) != 0 || again.Rejected[0].Code != "conflict" {
		t.Fatalf("%+v %v", again, err)
	}
	review := CausalReviewInput{Scope: scopeA, ExpectedRevision: p.Revision, Action: "approve"}
	if _, err := f.svc.ReviewCausalLink(testCtx, f.agent, p.ID, review); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent approve: %v", err)
	}
	if _, err := f.svc.ReviewCausalLink(testCtx, f.human, p.ID, CausalReviewInput{Scope: scopeA, ExpectedRevision: 9, Action: "approve"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	approved, err := f.svc.ReviewCausalLink(testCtx, f.human, p.ID, review)
	if err != nil || approved.Status != statusActive || approved.ApprovedBy == "" {
		t.Fatalf("%+v %v", approved, err)
	}
	if got := lineageRelations(t, f, b.ID); !slices.Equal(got, []string{"caused_by:" + a.ID}) {
		t.Fatalf("lineage %v", got)
	}
	if ids := causalHitIDs(t, f, "overnight"); !slices.Contains(ids, b.ID) {
		t.Fatalf("approved link expands recall: %v", ids)
	}
}

// causalHitIDs returns the ids recall found through the causal lane.
func causalHitIDs(t *testing.T, f *fixture, query string) []string {
	t.Helper()
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query, Limit: 5, ExpandCausal: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, h := range out.Items {
		if slices.Contains(h.Reasons, reasonCausal) {
			ids = append(ids, h.ID)
		}
	}
	return ids
}

func TestCausalNeighborsAreOneHopAndBounded(t *testing.T) {
	f := newFixture(t)
	hub := f.activeMemory("the hub cause")
	effects := []string{}
	for i := range 8 {
		e := f.activeMemory("an effect number " + string(rune('a'+i)))
		effects = append(effects, e.ID)
		if _, err := f.svc.LinkCausal(testCtx, f.human, CausalLinkInput{Scope: scopeA, CauseID: hub.ID, EffectID: e.ID}); err != nil {
			t.Fatal(err)
		}
	}
	// A second hop is never followed.
	far := f.activeMemory("two hops away")
	if _, err := f.svc.LinkCausal(testCtx, f.human, CausalLinkInput{Scope: scopeA, CauseID: effects[0], EffectID: far.ID}); err != nil {
		t.Fatal(err)
	}
	ids, err := f.svc.causalNeighbors(testCtx, scopeA, []string{hub.ID}, 5)
	if err != nil || len(ids) != 5 || slices.Contains(ids, hub.ID) || slices.Contains(ids, far.ID) {
		t.Fatalf("%v %v", ids, err)
	}
	if got := lineageRelations(t, f, hub.ID); len(got) != 8 {
		t.Fatalf("hub lineage %v", got)
	}
}

func TestCausalListPagesAndHidesUnreadableLinks(t *testing.T) {
	f := newFixture(t)
	a := f.activeMemory("the build cache was cleared")
	b := f.activeMemory("the next build took an hour")
	c := f.activeMemory("a private note about the build")
	ab, err := f.svc.LinkCausal(testCtx, f.human, CausalLinkInput{Scope: scopeA, CauseID: a.ID, EffectID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	ac, err := f.svc.LinkCausal(testCtx, f.human, CausalLinkInput{Scope: scopeA, CauseID: a.ID, EffectID: c.ID, Reason: "the cleared cache is in the note"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.svc.ListCausalLinks(testCtx, f.human, scopeA, statusActive, "", 1)
	if err != nil || len(first.Items) != 1 || first.NextCursor != "1" {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := f.svc.ListCausalLinks(testCtx, f.human, scopeA, statusActive, first.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("%+v %v", second, err)
	}
	if _, err := f.svc.ListCausalLinks(testCtx, f.human, scopeA, statusActive, "x", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cursor: %v", err)
	}
	// A reader who may not read c sees neither the link nor its reason, and cannot review it.
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: c.ID}
	page, err := f.svc.ListCausalLinks(testCtx, f.human, scopeA, statusActive, "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != ab.ID {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err := f.svc.ReviewCausalLink(testCtx, f.human, ac.ID, CausalReviewInput{Scope: scopeA, ExpectedRevision: ac.Revision, Action: "reject"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("reject of an unreadable link: %v", err)
	}
}
