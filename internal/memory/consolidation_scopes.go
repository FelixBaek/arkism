package memory

// Observation scopes: the space's consolidation block (reflect_models_settings.go)
// splits approved memories into tag scopes, consolidates each scope on its own
// and tags the proposals with the scope. A strategy that claims a scope supplies
// its mission, its observation cap and its source token budget. With no block the
// run keeps one untagged scope, as before.

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const obsReviewTagPrefix = "review:"

// obsScope is one tag scope of a consolidation run.
type obsScope struct {
	tags     []string
	members  []Record
	strategy *ConsolidationStrategy
	limit    int // -1 unlimited
	mission  string
}

// globMatch matches * (any run) and ? (one character) without compiling anything:
// strategy globs run once per scope and tag, and the preview takes globs from the caller.
func globMatch(glob, tag string) bool {
	g, t := []rune(glob), []rune(tag)
	gi, ti, star, mark := 0, 0, -1, 0
	for ti < len(t) {
		switch {
		case gi < len(g) && (g[gi] == '?' || g[gi] == t[ti]):
			gi, ti = gi+1, ti+1
		case gi < len(g) && g[gi] == '*':
			star, mark, gi = gi, ti, gi+1
		case star >= 0:
			mark++
			gi, ti = star+1, mark
		default:
			return false
		}
	}
	for gi < len(g) && g[gi] == '*' {
		gi++
	}
	return gi == len(g)
}

func anyGlob(globs []string, tag string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return globMatch(g, tag) })
}

// matches: "all" when every glob matches some tag, "exact" when also every tag matches some glob. Untagged scopes never match.
func (r ObservationTagRule) matches(tags []string) bool {
	if len(tags) == 0 || slices.ContainsFunc(r.Tags, func(g string) bool { return !slices.ContainsFunc(tags, func(t string) bool { return globMatch(g, t) }) }) {
		return false
	}
	return r.TagsMatch != "exact" || !slices.ContainsFunc(tags, func(t string) bool { return !anyGlob(r.Tags, t) })
}

// strategyFor is the first strategy one of whose rules claims tags.
func (c *SpaceConsolidation) strategyFor(tags []string) (int, *ConsolidationStrategy) {
	if c == nil {
		return -1, nil
	}
	for i := range c.Strategies {
		if slices.ContainsFunc(c.Strategies[i].Scopes, func(r ObservationTagRule) bool { return r.matches(tags) }) {
			return i, &c.Strategies[i]
		}
	}
	return -1, nil
}

// limitFor: the claiming strategy's cap, then the first scope limit covering tags exactly, then the space cap; -1 unlimited.
func (c *SpaceConsolidation) limitFor(tags []string, st *ConsolidationStrategy) int {
	switch {
	case c == nil:
		return -1
	case st != nil && st.MaxObservationsPerScope != nil:
		return *st.MaxObservationsPerScope
	}
	for _, l := range c.ScopeLimits {
		if (ObservationTagRule{Tags: l.Scope, TagsMatch: "exact"}).matches(tags) {
			return l.Limit
		}
	}
	if c.MaxObservationsPerScope != nil {
		return *c.MaxObservationsPerScope
	}
	return -1
}

// scopesOf is every observation scope a memory with these tags joins.
func (c *SpaceConsolidation) scopesOf(tags []string) [][]string {
	tags = obsScopeTags(tags)
	if c == nil || len(tags) == 0 && len(c.DefaultScopes) == 0 {
		return [][]string{nil}
	}
	if len(c.DefaultScopes) > 0 {
		out := [][]string{}
		for _, globs := range c.DefaultScopes {
			if (ObservationTagRule{Tags: globs}).matches(tags) {
				out = append(out, slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return !anyGlob(globs, t) }))
			}
		}
		return out
	}
	switch c.DefaultScopeMode {
	case "combined":
		return [][]string{tags}
	case "per_tag":
		out := [][]string{}
		for _, t := range tags {
			out = append(out, []string{t})
		}
		return out
	case "all_combinations":
		return tagSubsets(tags[:min(len(tags), consolidationScopeMax)])
	}
	return [][]string{nil}
}

func tagSubsets(tags []string) [][]string {
	out := [][]string{}
	for mask := 1; mask < 1<<len(tags); mask++ {
		set := []string{}
		for i, t := range tags {
			if mask&(1<<i) != 0 {
				set = append(set, t)
			}
		}
		out = append(out, set)
	}
	return out
}

// obsScopeTags is a record's scope: its tags without review marks, sorted and unique.
func obsScopeTags(tags []string) []string {
	out := slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return strings.HasPrefix(t, obsReviewTagPrefix) })
	slices.Sort(out)
	return slices.Compact(out)
}

func obsScopeKey(tags []string) string { return strings.Join(tags, "\x00") }

// partition splits members into scopes in a stable order, each with its strategy, cap and mission.
func (c *SpaceConsolidation) partition(members []Record) []obsScope {
	byKey := map[string]*obsScope{}
	for _, m := range members {
		for _, tags := range c.scopesOf(m.Tags) {
			k := obsScopeKey(tags)
			if byKey[k] == nil && len(byKey) >= consolidationScopesMax {
				continue // shortcut: scopes past the cap are left out of this run; raise the cap if spaces need more
			}
			if byKey[k] == nil {
				_, st := c.strategyFor(tags)
				byKey[k] = &obsScope{tags: tags, strategy: st, limit: c.limitFor(tags, st), mission: consolidationMission(c, st)}
			}
			byKey[k].members = append(byKey[k].members, m)
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]obsScope, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byKey[k])
	}
	return out
}

func consolidationMission(c *SpaceConsolidation, st *ConsolidationStrategy) string {
	switch {
	case st != nil && st.ObservationsMission != "":
		return st.ObservationsMission
	case c != nil:
		return c.ObservationsMission
	}
	return ""
}

func missionText(mission string) string {
	if strings.TrimSpace(mission) == "" {
		return ""
	}
	return "\nMISSION for these observations, set by a person. It says what is worth stating; the rules above still apply:\n" + wikiFence("MISSION", mission)
}

// trim keeps the group inside the strategy's source token budgets (about 3 characters a token), never below two members.
func (sc obsScope) trim(group []Record) []Record {
	st := sc.strategy
	if st == nil {
		return group
	}
	out, used := []Record{}, 0
	for _, m := range group {
		if per := st.SourceMaxTokensPerObservation; per > 0 && utf8.RuneCountInString(m.Text) > per*3 {
			m.Text = string([]rune(m.Text)[:per*3])
		}
		n := utf8.RuneCountInString(m.Text)/3 + 1
		if st.SourceMaxTokens > 0 && used+n > st.SourceMaxTokens && len(out) >= 2 {
			break
		}
		used += n
		out = append(out, m)
	}
	return out
}

// inScope is the existing observations of one scope.
func inScope(existing []Record, tags []string) []Record {
	key := obsScopeKey(tags)
	return slices.DeleteFunc(slices.Clone(existing), func(r Record) bool { return obsScopeKey(obsScopeTags(r.Tags)) != key })
}

func liveObservations(rows []Record) int {
	return len(liveRecords(rows))
}

// obsAsk is one group a run asks the model about.
type obsAsk struct {
	in    ObservationConsolidateInput
	group []Record
	n     int
}

// runObservationAsks asks about each group: one after another, or up to parallel at
// once, each with an even share of the run's remaining tokens. Proposals are stored in order.
func (s *Service) runObservationAsks(ctx context.Context, d Decision, job *WikiJob, asks []obsAsk, parallel int, out *ObservationConsolidateResult) error {
	if parallel <= 1 {
		for _, a := range asks {
			drafts, err := s.askObservations(ctx, a.in, job, a.group, "")
			if err := s.storeGroupDrafts(ctx, d, a, drafts, err, out); err != nil {
				return err
			}
		}
		return nil
	}
	drafts, errs, jobs := make([][]observationDraft, len(asks)), make([]error, len(asks)), make([]WikiJob, len(asks))
	share := max((job.MaxTotalTokens-job.Tokens)/int64(max(len(asks), 1)), 0)
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	for i, a := range asks {
		jobs[i] = WikiJob{MaxTotalTokens: share}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			drafts[i], errs[i] = s.askObservations(ctx, a.in, &jobs[i], a.group, "")
		}()
	}
	wg.Wait()
	var first error // every answer already paid for is stored before the first failure is returned
	for i, a := range asks {
		job.Calls, job.Tokens = job.Calls+jobs[i].Calls, job.Tokens+jobs[i].Tokens
		if err := s.storeGroupDrafts(ctx, d, a, drafts[i], errs[i], out); err != nil && first == nil {
			first = err
		}
	}
	return first
}
