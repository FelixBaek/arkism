package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// The session-archive lane (space setting recall_session_archives, off by default) lets a
// memory search also show what was said in past sessions: the episodes behind memories, or
// ones never captured. Its hits stay in their own sessions field, never ranked among memories.

const (
	sessionLaneHits  = 3
	sessionLaneTerms = 2
	sessionLaneRunes = 600
)

// attachSessionLane adds the lane to a memory answer search when the space opted in. It needs
// read:session_archives too; without it the lane is skipped, and any other failure is marked degraded.
func (s *Service) attachSessionLane(ctx context.Context, token string, in SearchInput, out *ContextBundle) {
	// Raw messages get no per-message transfer check, so the lane stays out of results bound for an outside destination.
	if in.Purpose != "" && in.Purpose != "answer" || strings.TrimSpace(in.Query) == "" || strings.HasPrefix(in.Destination, "external:") {
		return
	}
	cfg, err := s.spaceSettings(ctx, in.Scope)
	if err != nil || cfg.RecallSessionArchives == nil || !*cfg.RecallSessionArchives {
		return
	}
	seen := map[ArchiveMessageRef]bool{}
	for _, term := range sessionLaneQueryTerms(in.Query) {
		res, err := s.SearchSessionArchiveMessages(ctx, token, ArchiveSearchInput{Scope: in.Scope, Query: term, Limit: sessionLaneHits})
		if errors.Is(err, ErrDenied) {
			return
		}
		if err != nil {
			out.Degraded = append(out.Degraded, "sessions")
			return
		}
		for _, m := range res.Items {
			if len(out.Sessions) < sessionLaneHits && !seen[m.Ref] {
				seen[m.Ref], m.Text = true, runeCutString(m.Text, sessionLaneRunes)
				out.Sessions = append(out.Sessions, m)
			}
		}
	}
}

// sessionLaneQueryTerms are the longest words of the query: archive search matches literal
// text, so a whole question rarely matches, but its most specific words do.
func sessionLaneQueryTerms(q string) []string {
	var words []string
	for _, w := range strings.Fields(q) {
		if n := utf8.RuneCountInString(w); n >= 2 && len(w) <= 256 && !slices.Contains(words, w) {
			words = append(words, w)
		}
	}
	slices.SortStableFunc(words, func(a, b string) int { return utf8.RuneCountInString(b) - utf8.RuneCountInString(a) })
	return words[:min(len(words), sessionLaneTerms)]
}
