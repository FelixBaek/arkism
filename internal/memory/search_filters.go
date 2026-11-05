package memory

import (
	"slices"
	"time"
)

type SearchCriteria struct {
	history bool           // Local memory search only; never forwarded to the hub.
	Kinds   []string       `json:"kinds,omitempty"`
	Filters *SearchFilters `json:"filters,omitempty"`
	// ProjectKey (ST-824) is the caller's project, e.g. the git repository; it only narrows the
	// Scope's results: project-reach memories of other keys, or of any key when empty, are left out.
	ProjectKey string `json:"project_key,omitempty"`
}
type SearchFilters struct {
	Statuses []string `json:"statuses,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// TagsMatch is how Tags match (tagsMatch): any_strict (default), any, all, all_strict or exact.
	TagsMatch string `json:"tags_match,omitempty"`
	// TagGroups is a boolean tag filter: every group must hold, and a group holds when any
	// of its tests does (tagsMatch, negated by Not). It applies on top of Tags.
	TagGroups   [][]TagTest `json:"tag_groups,omitempty"`
	CreatedFrom *time.Time  `json:"created_from,omitempty"`
	CreatedTo   *time.Time  `json:"created_to,omitempty"`
	UpdatedFrom *time.Time  `json:"updated_from,omitempty"`
	UpdatedTo   *time.Time  `json:"updated_to,omitempty"`
	// Exact-search lanes for identity, model and file (step 2). Names
	// and models match the recorded values verbatim; a file filter matches the
	// artifact digest or any source span content hash. A hit that cannot show
	// the filtered value (for example a hub result) is excluded, never guessed.
	CreatedBy       []string `json:"created_by,omitempty"`
	GeneratorModels []string `json:"generator_models,omitempty"`
	FileDigests     []string `json:"file_digests,omitempty"`
}

// TagTest is one test in a tag group: Tags matched as Match says (tagsMatch), or not.
type TagTest struct {
	Tags  []string `json:"tags"`
	Match string   `json:"match,omitempty"`
	Not   bool     `json:"not,omitempty"`
}

const tagGroupsMax = 8 // groups, and tests per group

func normalizeSearchCriteria(in SearchCriteria) (SearchCriteria, error) {
	if len(in.Kinds) > 16 || !validTags(in.Kinds) {
		return in, ErrInvalid
	}
	for _, kind := range in.Kinds {
		if !validRunLabel(kind, 64) || containsSecret(kind) {
			return in, ErrInvalid
		}
	}
	if !validProjectKey(in.ProjectKey) {
		return in, ErrInvalid
	}
	in.Kinds = slices.Clone(in.Kinds)
	if in.Filters == nil {
		return in, nil
	}
	f := *in.Filters
	in.Filters = &f
	if !normalizeFilterLabels(&f) || !normalizeFilterIdentities(&f) || !normalizeFilterTimes(&f) {
		return in, ErrInvalid
	}
	if f.empty() {
		in.Filters = nil
	}
	return in, nil
}

func (f SearchFilters) empty() bool {
	return len(f.Statuses) == 0 && len(f.Tags) == 0 && len(f.TagGroups) == 0 && len(f.CreatedBy) == 0 && len(f.GeneratorModels) == 0 && len(f.FileDigests) == 0 &&
		f.CreatedFrom == nil && f.CreatedTo == nil && f.UpdatedFrom == nil && f.UpdatedTo == nil
}

func normalizeFilterLabels(f *SearchFilters) bool {
	if len(f.Statuses) > 5 {
		return false
	}
	for i, status := range f.Statuses {
		if !slices.Contains(memorySearchStatuses, status) || slices.Contains(f.Statuses[:i], status) {
			return false
		}
	}
	f.Statuses = slices.Clone(f.Statuses)
	if !validTags(f.Tags) || f.TagsMatch != "" && (len(f.Tags) == 0 || !slices.Contains(tagsMatchModes, f.TagsMatch)) {
		return false
	}
	for _, tag := range f.Tags {
		if containsSecret(tag) {
			return false
		}
	}
	f.Tags = slices.Clone(f.Tags)
	return validTagGroups(f.TagGroups)
}

func validTagGroups(groups [][]TagTest) bool {
	if len(groups) > tagGroupsMax {
		return false
	}
	for _, group := range groups {
		if len(group) == 0 || len(group) > tagGroupsMax {
			return false
		}
		for _, test := range group {
			if len(test.Tags) == 0 || !validTags(test.Tags) || test.Match != "" && !slices.Contains(tagsMatchModes, test.Match) || slices.ContainsFunc(test.Tags, containsSecret) {
				return false
			}
		}
	}
	return true
}

func normalizeFilterIdentities(f *SearchFilters) bool {
	for _, values := range [][]string{f.CreatedBy, f.GeneratorModels} {
		if len(values) > 16 {
			return false
		}
		for j, value := range values {
			if !validRunLabel(value, 256) || containsSecret(value) || slices.Contains(values[:j], value) {
				return false
			}
		}
	}
	if len(f.FileDigests) > 16 {
		return false
	}
	for i, digest := range f.FileDigests {
		if !sourceHashPattern.MatchString(digest) || slices.Contains(f.FileDigests[:i], digest) {
			return false
		}
	}
	f.CreatedBy = slices.Clone(f.CreatedBy)
	f.GeneratorModels = slices.Clone(f.GeneratorModels)
	f.FileDigests = slices.Clone(f.FileDigests)
	return true
}

func normalizeFilterTimes(f *SearchFilters) bool {
	for _, field := range []**time.Time{&f.CreatedFrom, &f.CreatedTo, &f.UpdatedFrom, &f.UpdatedTo} {
		if *field != nil {
			at := (**field).UTC().Truncate(time.Millisecond)
			if at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
				return false
			}
			*field = &at
		}
	}
	for _, pair := range [][2]*time.Time{{f.CreatedFrom, f.CreatedTo}, {f.UpdatedFrom, f.UpdatedTo}} {
		if pair[0] != nil && pair[1] != nil && !pair[0].Before(*pair[1]) {
			return false
		}
	}
	return true
}
func generatorModel(r Record) string {
	if r.Generator == nil {
		return ""
	}
	return r.Generator.Model
}
func recordFileDigests(r Record) []string {
	var out []string
	if r.Artifact != nil && r.Artifact.Digest != "" {
		out = append(out, r.Artifact.Digest)
	}
	for _, ref := range r.Sources {
		if ref.Span != nil && ref.Span.ContentHash != "" {
			out = append(out, ref.Span.ContentHash)
		}
	}
	return out
}
func hitFileDigests(h Hit) []string {
	var out []string
	for _, ref := range h.Sources {
		if ref.Span != nil && ref.Span.ContentHash != "" {
			out = append(out, ref.Span.ContentHash)
		}
	}
	return out
}
func (c SearchCriteria) matchesIdentity(createdBy, model string, digests []string) bool {
	if c.Filters == nil {
		return true
	}
	f := c.Filters
	if len(f.CreatedBy) > 0 && !slices.Contains(f.CreatedBy, createdBy) {
		return false
	}
	if len(f.GeneratorModels) > 0 && !slices.Contains(f.GeneratorModels, model) {
		return false
	}
	if len(f.FileDigests) > 0 && !slices.ContainsFunc(digests, func(digest string) bool { return slices.Contains(f.FileDigests, digest) }) {
		return false
	}
	return true
}
func (c SearchCriteria) matches(kind string, tags []string, created, updated time.Time) bool {
	if len(c.Kinds) > 0 && !slices.Contains(c.Kinds, kind) {
		return false
	}
	if c.Filters == nil {
		return true
	}
	f := c.Filters
	if !f.tagsHold(tags) {
		return false
	}
	for _, bound := range []struct {
		at       time.Time
		from, to *time.Time
	}{{created, f.CreatedFrom, f.CreatedTo}, {updated, f.UpdatedFrom, f.UpdatedTo}} {
		if (bound.from != nil || bound.to != nil) && bound.at.IsZero() {
			return false
		}
		if bound.from != nil && bound.at.Before(*bound.from) || bound.to != nil && !bound.at.Before(*bound.to) {
			return false
		}
	}
	return true
}
func (c SearchCriteria) matchesRecord(r Record) bool {
	return reachVisible(memoryReach(r), r.ReachProject, c.ProjectKey) && c.matchesStatus(r.Status) && c.matches(r.Kind, r.Tags, r.CreatedAt, r.UpdatedAt) &&
		c.matchesIdentity(r.CreatedBy, generatorModel(r), recordFileDigests(r))
}
func (c SearchCriteria) matchesHit(h Hit) bool {
	created := time.Time{}
	if h.CreatedAt != nil {
		created = *h.CreatedAt
	}
	return reachVisible(h.Reach, h.ReachProject, c.ProjectKey) && c.matchesStatus(h.Status) && c.matches(h.Kind, h.Tags, created, h.UpdatedAt) &&
		c.matchesIdentity(h.CreatedBy, h.GeneratorModel, hitFileDigests(h))
}

var tagsMatchModes = []string{"any", "all", "any_strict", "all_strict", "exact"}

// tagsMatch: any/any_strict need one requested tag, all/all_strict every one, exact the
// same set. any and all also let untagged memories through; the strict modes and exact
// do not. Empty mode is any_strict, the behaviour before modes existed.
func tagsMatch(mode string, want, have []string) bool {
	if len(have) == 0 {
		return mode == "any" || mode == "all"
	}
	has := func(tag string) bool { return slices.Contains(have, tag) }
	switch mode {
	case "all", "all_strict":
		return !slices.ContainsFunc(want, func(tag string) bool { return !has(tag) })
	case "exact":
		return !slices.ContainsFunc(want, func(tag string) bool { return !has(tag) }) && !slices.ContainsFunc(have, func(tag string) bool { return !slices.Contains(want, tag) })
	}
	return slices.ContainsFunc(want, has)
}

// tagsHold: the tags pass Tags (as TagsMatch says) and every tag group.
func (f *SearchFilters) tagsHold(tags []string) bool {
	return (len(f.Tags) == 0 || tagsMatch(f.TagsMatch, f.Tags, tags)) && tagGroupsMatch(f.TagGroups, tags)
}

func tagGroupsMatch(groups [][]TagTest, have []string) bool {
	for _, group := range groups {
		if !slices.ContainsFunc(group, func(t TagTest) bool { return tagsMatch(t.Match, t.Tags, have) != t.Not }) {
			return false
		}
	}
	return true
}

var memorySearchStatuses = []string{"active", "superseded", "conflicted", "rejected", "expired"}

func historySearchable(r Record) bool {
	return !r.ContentErased && !r.PrivacyQuarantined && r.ApprovedBy != "" && slices.Contains(memorySearchStatuses, r.Status)
}

func verifiedMemory(r Record) bool {
	return (r.VerificationStatus == VerificationVerified || r.VerificationStatus == "" && r.ApprovedBy != "") && r.ApprovedBy != "" && (effectiveSourceEventID(r) != "" || r.TraceID != "")
}
func (c SearchCriteria) matchesStatus(status string) bool {
	return c.Filters == nil || len(c.Filters.Statuses) == 0 || slices.Contains(c.Filters.Statuses, status)
}

// Identity, model and file conditions narrow the store-side candidate set
// before the lane limit; the rehydrated-record recheck still decides.
