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
	s.conversationTurnsWithCandidates(ctx, token, in, collection, out, false)
}

type conversationCandidatePool struct {
	rows    []ScoredHit
	records map[string]Record
}

// Collection and materialization stay separate so joint selection can reuse the
// exact authorized excerpts while preserving the ordinary baseline selection.
func (s *Service) collectConversationCandidates(ctx context.Context, token string, in SearchInput, collection string, out *ContextBundle, all bool) *conversationCandidatePool {
	if !conversationLaneOn(in, collection) || !s.probeAllowed(ctx, token, "read:sources", in.Scope) {
		return nil
	}
	in.optionalFailures = &out.Degraded
	rows, err := s.sideLane(ctx, "sources", in, SearchCriteria{Kinds: []string{conversationSourceKind}})
	if err != nil {
		out.Degraded = append(out.Degraded, "conversation")
		return nil
	}
	terms := searchQueryTerms(in.Query)
	localSelection := localExpandOn(in, collection)
	pool := &conversationCandidatePool{records: map[string]Record{}}
	for _, row := range rows {
		if !all && !localSelection && len(pool.rows)+len(out.Conversation) == conversationLaneLimit(in.Query) {
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
		if localSelection {
			text = conversationQueryExcerpt(in.Query, h.Text, sessionLaneRunes)
		} else {
			text = runeCutString(redactSessionPrivate(h.Text), sessionLaneRunes)
		}
		h.Origin, h.Text, h.Reasons, h.Authority, h.CollectedAt = conversationSourceKind, text, []string{conversationSourceKind}, AuthorityDataOnly, r.CollectedAt
		lexicalHit := h
		if localSelection {
			lexicalHit = Hit{Text: text} // omitted batch metadata is not returned evidence
		}
		if slices.Contains(row.Hit.Reasons, reasonVector) || lexicallyRelevant(terms, lexicalHit) {
			pool.rows = append(pool.rows, ScoredHit{Hit: h, Score: row.Score})
			pool.records[r.ID] = r
		}
	}
	return pool
}

func (s *Service) conversationTurnsWithCandidates(ctx context.Context, token string, in SearchInput, collection string, out *ContextBundle, all bool) *conversationCandidatePool {
	earlier := earlierStateIntent(in.Query) && !localExpandOn(in, collection)
	pool := s.collectConversationCandidates(ctx, token, in, collection, out, all || earlier)
	if pool == nil {
		return nil
	}
	limit := conversationLaneLimit(in.Query) - len(out.Conversation)
	var chosen []Hit
	if localExpandOn(in, collection) {
		chosen = selectConversationCandidates(in.Query, pool.rows, out.Items, out.Conversation, limit)
	} else {
		rows := pool.rows
		if earlier {
			// The earliest of the twice-the-lane best turns first: a question about an earlier
			// state is answered by what was said before the newer state, which memories may never have stated.
			rows = slices.Clone(rows[:min(2*conversationLaneHits, len(rows))])
			slices.SortStableFunc(rows, func(a, b ScoredHit) int { return conversationTime(a.Hit).Compare(conversationTime(b.Hit)) })
		}
		for _, row := range rows[:min(max(limit, 0), len(rows))] {
			chosen = append(chosen, row.Hit)
		}
	}
	s.materializeConversationCandidates(ctx, token, in, out, pool, chosen)
	return pool
}

// conversationTime is when a conversation batch was said; batches without a time sort last.
func conversationTime(h Hit) time.Time {
	if h.CollectedAt == nil {
		return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return *h.CollectedAt
}

func (s *Service) materializeConversationCandidates(ctx context.Context, token string, in SearchInput, out *ContextBundle, pool *conversationCandidatePool, chosen []Hit) {
	if in.ConversationContext || out.evidenceSelectionFenced {
		out.conversationRecords = map[Ref]Record{}
	}
	selected := map[string]Record{}
	for _, h := range chosen {
		r := pool.records[h.ID]
		out.Conversation = append(out.Conversation, h)
		selected[r.ID] = r
		if in.ConversationContext || out.evidenceSelectionFenced {
			out.conversationRecords[Ref{Collection: "sources", ID: r.ID, Revision: r.Revision}] = r
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
		s.appendStoredNeighbors(ctx, token, in, h, r, store, selected, spare)
		return
	}
	if r.ConversationOrder != nil {
		return
	} // explicit ordering needs the exact-slot reader, never a time guess
	rows, at := s.sameInstantConversationBatches(ctx, in, r)
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
		spare = s.appendNeighborWithin(ctx, token, in, h, other, spare)
	}
}

func (s *Service) appendStoredNeighbors(ctx context.Context, token string, in SearchInput, h *Hit, r Record, store conversationNeighborStore, selected map[string]Record, spare int) {
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
		spare = s.appendNeighborWithin(ctx, token, in, h, other, spare)
	}
}

// sameInstantConversationBatches lists r's conversation batches captured at its
// instant in creation order and r's position among them, or -1 when unknown.
func (s *Service) sameInstantConversationBatches(ctx context.Context, in SearchInput, r Record) ([]Record, int) {
	end := r.CollectedAt.Add(time.Millisecond)
	rows, err := s.Repo.ListSources(ctx, SourceListInput{Scope: in.Scope, Kind: conversationSourceKind, OriginProduct: r.OriginProduct, CollectedFrom: r.CollectedAt, CollectedTo: &end, Limit: conversationNeighborScan + 1})
	// A truncated list cannot say which batch is next; skip rather than guess.
	if err != nil || len(rows) > conversationNeighborScan {
		return nil, -1
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
	return rows, slices.IndexFunc(rows, func(other Record) bool { return other.ID == r.ID })
}

// appendNeighborWithin appends neighbor to h and returns the spare room left.
func (s *Service) appendNeighborWithin(ctx context.Context, token string, in SearchInput, h *Hit, neighbor Record, spare int) int {
	before := utf8.RuneCountInString(h.Text)
	s.appendConversationNeighbor(ctx, token, in, h, neighbor, spare)
	return spare - (utf8.RuneCountInString(h.Text) - before)
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
