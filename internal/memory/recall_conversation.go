package memory

import (
	"context"
	"slices"
	"strings"
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
	rows, err := s.sideLane(ctx, "sources", in, SearchCriteria{Kinds: []string{conversationSourceKind}})
	if err != nil {
		out.Degraded = append(out.Degraded, "conversation")
		return
	}
	terms := searchQueryTerms(in.Query)
	for _, row := range rows {
		if len(out.Conversation) == conversationLaneHits {
			break
		}
		r, err := s.Repo.Get(ctx, "sources", in.Scope, row.Hit.ID)
		if err != nil || !conversationSource("sources", r) || !live(r) || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "restricted" {
			continue
		}
		h := HitFromRecord("sources", r)
		h.Origin, h.Text, h.Reasons, h.Authority, h.CollectedAt = conversationSourceKind, redactSessionPrivate(runeCutString(h.Text, sessionLaneRunes)), []string{conversationSourceKind}, AuthorityDataOnly, r.CollectedAt
		if slices.Contains(row.Hit.Reasons, reasonVector) || lexicallyRelevant(terms, h) {
			out.Conversation = append(out.Conversation, h)
		}
	}
}
