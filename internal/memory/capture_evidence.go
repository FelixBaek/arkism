package memory

import (
	"slices"
	"strings"
)

// Explicit evidence is immutable. A different repository or source is a new
// review candidate, never permission to add evidence to an approved memory.
func captureEvidenceKey(base string, c CaptureClaim) string {
	repo := c.Repo
	if c.Kind == "user" {
		repo = ""
	}
	if repo == "" && len(c.SourceRefs) == 0 {
		return base
	}
	return "capture-" + ContentHash(base+"\n"+structuredText(struct {
		Repo    string `json:"repo"`
		Sources []Ref  `json:"sources,omitempty"`
	}{repo, orderedCaptureSources(c.SourceRefs)}))
}

func orderedCaptureSources(refs []Ref) []Ref {
	refs = append([]Ref(nil), refs...)
	slices.SortFunc(refs, func(a, b Ref) int { return strings.Compare(structuredText(a), structuredText(b)) })
	return refs
}

func captureLegacyEvidenceMatches(candidate Record, c CaptureClaim) bool {
	repo := ""
	if c.Kind != "user" {
		repo = c.Repo
	}
	var repos []string
	for _, tag := range candidate.Tags {
		if strings.HasPrefix(tag, "repo:") {
			repos = append(repos, strings.TrimPrefix(tag, "repo:"))
		}
	}
	if repo == "" && len(repos) != 0 || repo != "" && (len(repos) != 1 || repos[0] != repo) {
		return false
	}
	return slices.Equal(orderedCaptureSources(candidate.Relations), orderedCaptureSources(c.SourceRefs))
}
