package memory

import (
	"context"
	"slices"
	"strings"
)

// Fuzzy tag leaves. A tag_groups leaf with resolve "fuzzy" treats its tags as spellings:
// before the search runs, each is matched against the space's active-memory tags by
// trigram similarity, and the leaf is rewritten into ordinary leaves over the tags that
// matched, so the SQL and the in-memory filter only ever see exact tags.
//
//   - any / any_strict: one leaf over the union of every token's matches.
//   - all / all_strict: an and of one any-leaf per token (some spelling of each token).
//   - exact: an or over the combinations of one match per token, each an exact leaf;
//     combinations with fewer distinct tags than tokens are dropped.
//
// A token with no similar tag keeps itself, so a fuzzy leaf never widens past what the
// literal leaf would match. A rewrite that would leave the tag_groups bounds is refused
// (ErrInvalid) rather than cut, since cutting would silently change which memories match.

const (
	tagResolveFuzzy    = "fuzzy"
	tagFuzzySimilarity = 0.45 // trigram similarity a spelling needs; keeps mango/mongo (0.33) apart
	tagFuzzyPerToken   = 8    // most similar tags one token expands to
)

// resolveFuzzyTags rewrites the fuzzy leaves of in's tag groups (no-op without any).
// Callers run it after the search grant, so a refused caller reads no tag vocabulary.
func (s *Service) resolveFuzzyTags(ctx context.Context, in *SearchInput) error {
	f := in.Filters
	if f == nil || !slices.ContainsFunc(f.TagGroups, hasFuzzyLeaf) {
		return nil
	}
	tags := []string{}
	if st, err := s.opsStore(); err == nil {
		rows, err := st.OpsCounts(ctx, opsCountQuery{Scope: in.Scope, Collection: colMemories, By: "tag", Statuses: []string{statusActive}})
		if err != nil {
			return err
		}
		for _, r := range rows {
			tags = append(tags, opsText(r.Key))
		}
	}
	vocab := tagVocab(tags) // at most opsCountRows tags, each split into trigrams once
	copied := *f
	copied.TagGroups = make([][]TagTest, len(f.TagGroups))
	for i, group := range f.TagGroups {
		copied.TagGroups[i] = resolveTagTests(group, vocab)
	}
	if !validTagGroups(copied.TagGroups) {
		return ErrInvalid
	}
	in.Filters = &copied
	return nil
}

func hasFuzzyLeaf(tests []TagTest) bool {
	return slices.ContainsFunc(tests, func(t TagTest) bool {
		return t.Resolve == tagResolveFuzzy || hasFuzzyLeaf(t.And) || hasFuzzyLeaf(t.Or)
	})
}

func resolveTagTests(tests []TagTest, vocab []tagSpelling) []TagTest {
	out := make([]TagTest, len(tests))
	for i, t := range tests {
		switch {
		case len(t.And) > 0:
			t.And = resolveTagTests(t.And, vocab)
		case len(t.Or) > 0:
			t.Or = resolveTagTests(t.Or, vocab)
		case t.Resolve == tagResolveFuzzy:
			t = fuzzyLeaf(t, vocab)
		}
		out[i] = t
	}
	return out
}

// fuzzyLeaf is the rewrite described above; Not stays on the rewritten test.
func fuzzyLeaf(t TagTest, vocab []tagSpelling) TagTest {
	spellings := make([][]string, len(t.Tags))
	for i, token := range t.Tags {
		spellings[i] = similarTags(token, vocab)
	}
	switch t.Match {
	case "all", "all_strict":
		mode := "any_strict"
		if t.Match == "all" {
			mode = "any"
		}
		and := make([]TagTest, len(spellings))
		for i, tags := range spellings {
			and[i] = TagTest{Tags: tags, Match: mode}
		}
		return TagTest{And: and, Not: t.Not}
	case "exact":
		or := []TagTest{}
		for _, combo := range tagCombos(spellings, tagGroupsMax+1) {
			or = append(or, TagTest{Tags: combo, Match: "exact"})
		}
		if len(or) == 0 {
			return TagTest{Tags: t.Tags, Match: "exact", Not: t.Not}
		}
		return TagTest{Or: or, Not: t.Not} // more than tagGroupsMax combos fails validation
	}
	union := []string{}
	for _, tags := range spellings {
		for _, tag := range tags {
			if !slices.Contains(union, tag) {
				union = append(union, tag)
			}
		}
	}
	return TagTest{Tags: union, Match: t.Match, Not: t.Not}
}

// tagCombos is up to max picks of one tag per token with no tag picked twice.
func tagCombos(spellings [][]string, limit int) [][]string {
	out := [][]string{{}}
	for _, tags := range spellings {
		next := [][]string{}
		for _, combo := range out {
			for _, tag := range tags {
				if !slices.Contains(combo, tag) && len(next) < limit {
					next = append(next, append(slices.Clone(combo), tag))
				}
			}
		}
		out = next
	}
	return out
}

// similarTags is token itself plus the vocabulary tags at least tagFuzzySimilarity alike,
// most similar first, at most tagFuzzyPerToken. A namespace ("lang:go") must match exactly
// and only the value is compared: a shared namespace is not a shared spelling.
func similarTags(token string, vocab []tagSpelling) []string {
	ns, value := splitTagNamespace(token)
	want := trigramSet(value)
	type scored struct {
		tag string
		sim float64
	}
	found := []scored{}
	for _, v := range vocab {
		if v.tag == token || !strings.EqualFold(v.ns, ns) {
			continue
		}
		if sim := trigramSimilarity(want, v.tri); sim >= tagFuzzySimilarity {
			found = append(found, scored{v.tag, sim})
		}
	}
	slices.SortStableFunc(found, func(a, b scored) int {
		switch {
		case a.sim > b.sim:
			return -1
		case a.sim < b.sim:
			return 1
		}
		return strings.Compare(a.tag, b.tag)
	})
	out := []string{token}
	for _, f := range found[:min(len(found), tagFuzzyPerToken-1)] {
		out = append(out, f.tag)
	}
	return out
}

// tagSpelling is a vocabulary tag with its namespace and value trigrams, computed once per
// search rather than once per fuzzy token.
type tagSpelling struct {
	tag, ns string
	tri     map[string]bool
}

func tagVocab(tags []string) []tagSpelling {
	out := make([]tagSpelling, len(tags))
	for i, tag := range tags {
		ns, value := splitTagNamespace(tag)
		out[i] = tagSpelling{tag, ns, trigramSet(value)}
	}
	return out
}

func splitTagNamespace(tag string) (string, string) {
	if ns, value, ok := strings.Cut(tag, ":"); ok && value != "" {
		return ns, value
	}
	return "", tag
}

