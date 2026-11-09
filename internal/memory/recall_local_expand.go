package memory

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Internal candidate budgets, independent of the caller's final output budget.
// Three ordinary lanes can contribute at most 150 distinct original candidates.
const localRecallCandidateLimit = 50

// Local expansion only separates an explicit adjacent "Name and Name" pair.
// Question words, conditions, negation, tense and punctuation remain intact:
// removing scaffolding from a vector query would change its retrieval intent.
// It never invents facts, synonyms, names or dates. It is opt-in
// independently of model expansion and reuses the ordinary authorization gates.
func localRecallQueries(query string) []string {
	var out []string
	add := func(q string) {
		if len(strings.Fields(q)) < 2 || utf8.RuneCountInString(q) > recallExpandRunes || strings.EqualFold(q, strings.TrimSpace(query)) || slices.Contains(out, q) {
			return
		}
		out = append(out, q)
	}
	// Track original byte positions rather than reconstructing normalized terms.
	// Each result removes only the conjunction and the other name. In particular,
	// a comma or question mark following the right name stays in both results.
	words := strings.Fields(query)
	starts := make([]int, len(words))
	cursor := 0
	for i, word := range words {
		starts[i] = cursor + strings.Index(query[cursor:], word)
		cursor = starts[i] + len(word)
	}
	for i := 1; i+1 < len(words); i++ {
		if !strings.EqualFold(words[i], "and") {
			continue
		}
		left := words[i-1]
		right := strings.TrimRightFunc(words[i+1], unicode.IsPunct)
		lr, _ := utf8.DecodeRuneInString(left)
		rr, _ := utf8.DecodeRuneInString(right)
		if !unicode.IsUpper(lr) || !unicode.IsUpper(rr) || strings.TrimRightFunc(left, unicode.IsPunct) != left {
			continue
		}
		// Only unambiguous single-word names here. Possessives or punctuation
		// inside a name need a parser; leave such questions unchanged instead
		// of accidentally dropping their grammatical meaning.
		nameBreak := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsMark(r) }
		if strings.ContainsFunc(left, nameBreak) || strings.ContainsFunc(right, nameBreak) {
			continue
		}
		a, b := searchQueryTerms(left), searchQueryTerms(right)
		if len(a) != 1 || len(b) != 1 {
			continue
		}
		add(query[:starts[i-1]+len(left)] + query[starts[i+1]+len(right):])
		add(query[:starts[i-1]] + query[starts[i+1]:])
		break // One explicit pair; never combinatorial expansion.
	}
	return out
}

func localExpandOn(in SearchInput, collection string) bool {
	return in.space.recallQuality().LocalExpand && collection == colMemories && !in.IncludeInactive && in.RelatedTo == nil && in.contextView == nil
}

func (s *Service) expandRecallLocally(ctx context.Context, token string, in *SearchInput, d Decision, collection string, run *Ref, out *ContextBundle) {
	queries := localRecallQueries(in.Query)
	if len(queries) == 0 {
		in.expandStatus = "local_unchanged"
		return
	}
	if s.searchExpansions(ctx, token, in, d, collection, run, out, queries) != nil {
		in.expandStatus = "local_failed"
		return
	}
	in.expandStatus, in.expandQueries = "local_applied", queries
}
