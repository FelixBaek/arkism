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
	// Properly capitalized interior words are anchors. A second query retains
	// the predicate to avoid embedding every answer around a frequently named
	// person; candidates still undergo all source, scope and visibility checks.
	anchors := map[string]bool{}
	for i, w := range strings.Fields(query) {
		r, _ := utf8.DecodeRuneInString(w)
		if i > 0 && unicode.IsUpper(r) {
			for _, t := range SearchTokens(w) {
				anchors[t] = true
			}
		}
	}
	var predicate []string
	for _, t := range terms {
		if !anchors[t] {
			predicate = append(predicate, t)
		}
	}
	add(predicate)
	// Explicit coordinated clauses can supply complementary evidence. Short
	// fragments stay out; at most four queries share the normal expansion bound.
	for _, clause := range strings.Split(query, " and ") {
		if len(out) >= recallExpandMax {
			break
		}
		if clause != query {
			add(searchQueryTerms(clause))
		}
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
