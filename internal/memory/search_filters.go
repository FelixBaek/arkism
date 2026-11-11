package memory

import (
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
)

type SearchCriteria struct {
	history bool           // Local memory search only; never forwarded to the hub.
	pending bool           // Candidates waiting for review (provisional recall, recall_quality.go); local only.
	listing bool           // The question asks for every instance (recall_aggregate.go): the vector lane keeps its tail; local only.
	Kinds   []string       `json:"kinds,omitempty"`
	Filters *SearchFilters `json:"filters,omitempty"`
	// ProjectKey is the caller's project, e.g. the git repository; it only narrows the
	// Scope's results: project-reach memories of other keys, or of any key when empty, are left out.
	ProjectKey string `json:"project_key,omitempty"`
}
type SearchFilters struct {
	Statuses []string `json:"statuses,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// TagsMatch is how Tags match (tagsMatch): any_strict (default), any, all, all_strict or exact.
	TagsMatch string `json:"tags_match,omitempty"`
	// TagGroups is a boolean tag filter: every group must hold, and a group holds when any
	// of its tests does (TagTest.holds). A test may nest and/or groups. It applies on top of Tags.
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

// TagTest is one test in a tag group: a leaf (Tags matched as Match says, tagsMatch) or a
// nested group (every test of And, or any test of Or), negated by Not. Resolve "fuzzy" lets a
// leaf's tags also match the space's tags spelled alike (tag_resolve.go). A Meta leaf compares
// a metadata value instead of naming tags.
type TagTest struct {
	Tags    []string  `json:"tags,omitempty"`
	Match   string    `json:"match,omitempty"`
	Resolve string    `json:"resolve,omitempty"`
	Not     bool      `json:"not,omitempty"`
	And     []TagTest `json:"and,omitempty"`
	Or      []TagTest `json:"or,omitempty"`
	Meta    *MetaTest `json:"meta,omitempty"`
}

// MetaTest compares the value a memory holds for one metadata key (its meta:<key>=<value>
// tag, claimLabelTags). A memory without the key fails every op. gt, gte, lt and lte compare
// as numbers when Value is a plain decimal (then only decimal values pass) and as bytes
// otherwise, which orders ISO dates.
type MetaTest struct {
	Key    string   `json:"key"`
	Op     string   `json:"op,omitempty"` // eq (default), ne, in, gt, gte, lt, lte, contains, exists
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"` // in only
}

var (
	metaTestOps = []string{"", "eq", "ne", "in", "gt", "gte", "lt", "lte", "contains", "exists"}
	// metaDecimal is shared with the SQL form (pgMetaTest), so both sides call the same values numbers.
	metaDecimal = regexp.MustCompile(metaDecimalPattern)
)

const (
	metaDecimalPattern = `^-?[0-9]{1,18}(\.[0-9]{1,18})?$`
	metaTestValuesMax  = 16
)

func (m MetaTest) prefix() string { return "meta:" + m.Key + "=" }

func (m MetaTest) valid() bool {
	okValue := func(v string) bool { return validRunLabel(v, 80) && !containsSecret(v) }
	if !claimMetaKey.MatchString(m.Key) || !slices.Contains(metaTestOps, m.Op) {
		return false
	}
	switch m.Op {
	case "exists":
		return m.Value == "" && len(m.Values) == 0
	case "in":
		return m.Value == "" && len(m.Values) > 0 && len(m.Values) <= metaTestValuesMax && !slices.ContainsFunc(m.Values, func(v string) bool { return !okValue(v) })
	}
	return len(m.Values) == 0 && okValue(m.Value)
}

// holds: some value of the key passes the op.
func (m MetaTest) holds(have []string) bool {
	return slices.ContainsFunc(have, func(tag string) bool {
		value, ok := strings.CutPrefix(tag, m.prefix())
		return ok && m.passes(value)
	})
}

func (m MetaTest) passes(value string) bool {
	switch m.Op {
	case "exists":
		return true
	case "", "eq":
		return value == m.Value
	case "ne":
		return value != m.Value
	case "in":
		return slices.Contains(m.Values, value)
	case "contains":
		return strings.Contains(value, m.Value)
	}
	order := strings.Compare(value, m.Value)
	if metaDecimal.MatchString(m.Value) {
		if !metaDecimal.MatchString(value) {
			return false
		}
		a, _ := new(big.Rat).SetString(value)
		b, _ := new(big.Rat).SetString(m.Value)
		order = a.Cmp(b)
	}
	return map[string]bool{"gt": order > 0, "gte": order >= 0, "lt": order < 0, "lte": order <= 0}[m.Op]
}

const (
	tagGroupsMax     = 8  // groups, and tests per group or nested and/or
	tagGroupDepthMax = 5  // a group's tests are depth 1; each and/or adds one
	tagGroupNodesMax = 64 // tests in all groups together, nested ones included
)

// localeTag is the shape of a request body's locale: a language, then optional script or region parts.
var localeTag = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8}){0,3}$`)

// knownSearchKinds refuses a memory search for a kind no memory can have, so a
// typo is not read as "nothing found". Other collections keep their open kinds.
func knownSearchKinds(collection string, kinds []string) error {
	if collection != colMemories {
		return nil
	}
	for _, k := range kinds {
		if !slices.Contains(memoryKinds, k) {
			return invalidf("kinds: unknown kind %q; use one of %s", recallLabel(k, 64), strings.Join(memoryKinds, ", "))
		}
	}
	return nil
}

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
	nodes := 0
	for _, group := range groups {
		if !validTagTests(group, 1, &nodes) {
			return false
		}
	}
	return true
}

// validTagTests checks 1..tagGroupsMax tests at depth, counting every test into nodes.
func validTagTests(tests []TagTest, depth int, nodes *int) bool {
	if len(tests) == 0 || len(tests) > tagGroupsMax || depth > tagGroupDepthMax {
		return false
	}
	for _, t := range tests {
		if *nodes++; *nodes > tagGroupNodesMax || !validTagTest(t, depth, nodes) {
			return false
		}
	}
	return true
}

// validMetaLeaf: a meta test stands alone in its node.
func validMetaLeaf(t TagTest) bool {
	return len(t.Tags) == 0 && len(t.And) == 0 && len(t.Or) == 0 && t.Match == "" && t.Resolve == "" && t.Meta.valid()
}

// validTagTest: exactly one of tags, and, or, meta; match and resolve only on a tag leaf.
func validTagTest(t TagTest, depth int, nodes *int) bool {
	if t.Meta != nil {
		return validMetaLeaf(t)
	}
	switch {
	case len(t.Tags) > 0:
		return len(t.And) == 0 && len(t.Or) == 0 && validTags(t.Tags) && !slices.ContainsFunc(t.Tags, containsSecret) &&
			(t.Match == "" || slices.Contains(tagsMatchModes, t.Match)) && (t.Resolve == "" || t.Resolve == "exact" || t.Resolve == tagResolveFuzzy)
	case len(t.And) > 0 && len(t.Or) == 0 && t.Match == "" && t.Resolve == "":
		return validTagTests(t.And, depth+1, nodes)
	case len(t.Or) > 0 && len(t.And) == 0 && t.Match == "" && t.Resolve == "":
		return validTagTests(t.Or, depth+1, nodes)
	}
	return false
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
		if !slices.ContainsFunc(group, func(t TagTest) bool { return t.holds(have) }) {
			return false
		}
	}
	return true
}

// holds evaluates the test on a memory's tags. A fuzzy leaf not yet resolved matches literally.
func (t TagTest) holds(have []string) bool {
	ok := false
	switch {
	case len(t.And) > 0:
		ok = !slices.ContainsFunc(t.And, func(c TagTest) bool { return !c.holds(have) })
	case len(t.Or) > 0:
		ok = slices.ContainsFunc(t.Or, func(c TagTest) bool { return c.holds(have) })
	case t.Meta != nil:
		ok = t.Meta.holds(have)
	default:
		ok = tagsMatch(t.Match, t.Tags, have)
	}
	return ok != t.Not
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
