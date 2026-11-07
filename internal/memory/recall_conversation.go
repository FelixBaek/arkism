package memory

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The conversation lane: a space with capture_density conversation keeps each captured batch of
// turns as conversation sources (speaker and date on every line), so a recall that asks for
// sources still finds what was said when no reviewed memory states it. The turns come back in
// their own conversation list, never ranked among memories, because nobody reviewed them.

const (
	conversationSourceKind = "conversation"
	conversationLaneHits   = 5
)

// conversationSource: only sources of kind conversation are embedded and searched.
func conversationSource(col string, r Record) bool {
	return col == "sources" && r.Kind == conversationSourceKind
}

// conversationLaneOn: a memory recall with a query that includes sources, in a space that keeps
// conversations, not bound for an outside destination or a frozen context view.
func conversationLaneOn(in SearchInput, collection string) bool {
	return collection == colMemories && !in.IncludeInactive && in.RelatedTo == nil && in.contextView == nil && !strings.HasPrefix(in.Destination, "external:") &&
		in.space != nil && in.space.CaptureDensity == captureDensityConversation && slices.Contains(in.Include, "sources") && strings.TrimSpace(in.Query) != ""
}

// conversationTurns adds up to conversationLaneHits conversation sources the query is about to a
// memory recall that includes sources, when its space keeps conversations and the caller may read
// sources. Like the provisional lane it stays out of results bound for an outside destination and
// frozen context views; a lexical match must hold the query's topic.
// A lane search failure marks the recall degraded instead of failing it.
func (s *Service) conversationTurns(ctx context.Context, token string, in SearchInput, collection string, out *ContextBundle) {
	if !conversationLaneOn(in, collection) || !s.probeAllowed(ctx, token, "read:sources", in.Scope) {
		return
	}
	in.optionalFailures = &out.Degraded
	rows, err := s.sideLane(ctx, "sources", in, SearchCriteria{Kinds: []string{conversationSourceKind}})
	if err != nil {
		out.Degraded = append(out.Degraded, "conversation")
		return
	}
	if in.ConversationContext {
		out.conversationRecords = map[Ref]Record{}
	}
	terms := searchQueryTerms(in.Query)
	selected := map[string]Record{}
	for _, row := range rows {
		if len(out.Conversation) == conversationLaneHits {
			break
		}
		r, err := s.Repo.Get(ctx, "sources", in.Scope, row.Hit.ID)
		if err != nil || !conversationSource("sources", r) || !live(r) || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "restricted" {
			continue
		}
		if _, err := s.authorize(ctx, token, "read:sources", in.Scope, r.ID, structuredText(r)); err != nil {
			continue
		}
		h := HitFromRecord("sources", r)
		var text string
		if in.space.recallQuality().LocalExpand {
			text = conversationQueryExcerpt(in.Query, h.Text, sessionLaneRunes)
		} else {
			text = runeCutString(redactSessionPrivate(h.Text), sessionLaneRunes)
		}
		h.Origin, h.Text, h.Reasons, h.Authority, h.CollectedAt = conversationSourceKind, text, []string{conversationSourceKind}, AuthorityDataOnly, r.CollectedAt
		if slices.Contains(row.Hit.Reasons, reasonVector) || lexicallyRelevant(terms, h) {
			out.Conversation = append(out.Conversation, h)
			selected[r.ID] = r
			if in.ConversationContext {
				out.conversationRecords[Ref{Collection: "sources", ID: r.ID, Revision: r.Revision}] = r
			}
		}
	}
	spare := len(out.Conversation) * sessionLaneRunes
	for _, h := range out.Conversation {
		spare -= utf8.RuneCountInString(h.Text)
	}
	in.conversationRecords = out.conversationRecords
	for i := range out.Conversation {
		before := utf8.RuneCountInString(out.Conversation[i].Text)
		s.completeConversationTurn(ctx, token, in, &out.Conversation[i], selected, spare)
		spare -= utf8.RuneCountInString(out.Conversation[i].Text) - before
	}
}

// completeConversationTurn fills only unused excerpt space with an adjacent
// captured batch from the same conversation. The ranked batches and their text
// stay intact, and the lane shares only the unused character budget of its ranked batches.
// conversationNeighborScan is how many batches captured at the same instant are read to find a neighbour.
const conversationNeighborScan = 99 // one more is read to see truncation; source lists cap at 100

func (s *Service) completeConversationTurn(ctx context.Context, token string, in SearchInput, h *Hit, selected map[string]Record, spare int) {
	r := selected[h.ID]
	// A selected middle window is not the end of the batch. Joining a neighbor
	// across omitted turns could hide a correction or qualification.
	if h.Text != redactSessionPrivate(r.Text) {
		return
	}
	if r.SourceRef == "" || r.CollectedAt == nil || spare <= 1 {
		return
	}
	if store, ok := s.Repo.(conversationNeighborStore); ok {
		neighbors, err := store.ConversationNeighbors(ctx, r)
		if err != nil || len(neighbors) > 2 {
			return
		}
		for _, other := range neighbors {
			if other.ID == r.ID || other.Scope != r.Scope || other.SourceRef != r.SourceRef || other.OriginProduct != r.OriginProduct || !adjacentConversation(r, other) {
				continue
			}
			if _, shown := selected[other.ID]; shown {
				continue
			}
			before := utf8.RuneCountInString(h.Text)
			s.appendConversationNeighbor(ctx, token, in, h, other, spare)
			spare -= utf8.RuneCountInString(h.Text) - before
		}
		return
	}
	if r.ConversationOrder != nil {
		return
	} // explicit ordering needs the exact-slot reader, never a time guess
	end := r.CollectedAt.Add(time.Millisecond)
	rows, err := s.Repo.ListSources(ctx, SourceListInput{Scope: in.Scope, Kind: conversationSourceKind, OriginProduct: r.OriginProduct, CollectedFrom: r.CollectedAt, CollectedTo: &end, Limit: conversationNeighborScan + 1})
	// A truncated list cannot say which batch is next; skip rather than guess.
	if err != nil || len(rows) > conversationNeighborScan {
		return
	}
	rows = slices.DeleteFunc(rows, func(other Record) bool {
		return other.SourceRef != r.SourceRef || other.OriginProduct != r.OriginProduct || other.ConversationOrder != nil
	})
	slices.SortFunc(rows, func(a, b Record) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	at := slices.IndexFunc(rows, func(other Record) bool { return other.ID == r.ID })
	if at < 0 {
		return
	}
	for _, pos := range []int{at + 1, at - 1} {
		if pos < 0 || pos >= len(rows) {
			continue
		}
		other := rows[pos]
		if _, shown := selected[other.ID]; shown {
			continue
		}
		before := utf8.RuneCountInString(h.Text)
		s.appendConversationNeighbor(ctx, token, in, h, other, spare)
		spare -= utf8.RuneCountInString(h.Text) - before
	}
}

func (s *Service) appendConversationNeighbor(ctx context.Context, token string, in SearchInput, h *Hit, neighbor Record, spare int) {
	// Re-read the list candidate and authorize it independently before showing it.
	r, err := s.Repo.Get(ctx, "sources", in.Scope, neighbor.ID)
	if err != nil || r.Revision != neighbor.Revision || !conversationSource("sources", r) || !live(r) || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "restricted" {
		return
	}
	if _, err = s.authorize(ctx, token, "read:sources", in.Scope, r.ID, structuredText(r)); err != nil {
		return
	}
	left := spare - 1
	text := conversationNeighborExcerpt(r.Text, left)
	if text == "" {
		return
	}
	if in.conversationRecords != nil {
		in.conversationRecords[Ref{Collection: "sources", ID: r.ID, Revision: r.Revision}] = r
	}
	h.Text += "\n" + text
	h.Sources = append(slices.Clone(h.Sources), sourceReference(r, "conversation_neighbor"))
}

// A reply cut mid-turn can omit its fact or qualification. Fit whole lines only;
// a long turn does not prevent a shorter adjacent turn from using the spare room.
func conversationNeighborExcerpt(text string, left int) string {
	var lines []string
	for _, line := range strings.Split(redactSessionPrivate(text), "\n") {
		n := utf8.RuneCountInString(line)
		if strings.TrimSpace(line) == "" || n > left {
			continue
		}
		lines = append(lines, line)
		left -= n + 1
	}
	return strings.Join(lines, "\n")
}
