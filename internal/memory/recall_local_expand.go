package memory

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Local expansion only removes question scaffolding or separates explicit
// conjunctions. It never invents facts, synonyms, names or dates. It is opt-in
// independently of model expansion and reuses the ordinary authorization gates.
func localRecallQueries(query string) []string {
	var out []string
	add := func(words []string) {
		q := strings.Join(words, " ")
		if len(words) < 2 || utf8.RuneCountInString(q) > recallExpandRunes || strings.EqualFold(q, strings.TrimSpace(query)) || slices.Contains(out, q) {
			return
		}
		out = append(out, q)
	}
	terms := searchQueryTerms(query)
	add(terms)
	// For an explicit "Name and Name" pair, preserve the common predicate
	// with each anchor. Removing all names would mix other people's facts;
	// splitting at "and" alone would drop the predicate from one side.
	words := strings.Fields(query)
	for i := 1; i+1 < len(words); i++ {
		if !strings.EqualFold(words[i], "and") {
			continue
		}
		left, right := words[i-1], words[i+1]
		lr, _ := utf8.DecodeRuneInString(left)
		rr, _ := utf8.DecodeRuneInString(right)
		if i <= 1 || !unicode.IsUpper(lr) || !unicode.IsUpper(rr) {
			continue
		}
		a, b := searchQueryTerms(left), searchQueryTerms(right)
		if len(a) != 1 || len(b) != 1 {
			continue
		}
		var predicate []string
		for _, term := range terms {
			if term != a[0] && term != b[0] {
				predicate = append(predicate, term)
			}
		}
		if len(predicate) == 0 {
			continue
		}
		add(append([]string{a[0]}, predicate...))
		if len(out) < recallExpandMax {
			add(append([]string{b[0]}, predicate...))
		}
		break // One explicit pair; never combinatorial expansion.
	}
	return out[:min(len(out), recallExpandMax)]
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
	vectors, space := s.expansionVectors(ctx, token, *in, d, queries)
	for i, q := range queries {
		sub := *in
		sub.Query, sub.retrievalQuery = q, ""
		lists, err := s.searchLists(ctx, token, collection, sub.retrieval(collection), vectors[i], space)
		if err == nil {
			err = s.cleanSearchLanes(ctx, token, sub, d, collection, Record{}, run, -1, vectors[i], space, map[string]Record{}, out, lists)
		}
		if err != nil {
			in.expanded = nil
			in.expandStatus = "local_failed"
			return
		}
		in.expanded = append(in.expanded, expandedQuery{query: q, lists: lists, vector: vectors[i], space: space})
	}
	in.expandStatus, in.expandQueries = "local_applied", queries
}
