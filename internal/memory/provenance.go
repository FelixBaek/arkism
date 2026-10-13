package memory

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Generator struct {
	Model           string `json:"model" bson:"model"`
	PromptVersion   string `json:"prompt_version" bson:"prompt_version"`
	PipelineVersion string `json:"pipeline_version" bson:"pipeline_version"`
}

func validGenerator(g *Generator) bool {
	if g == nil {
		return false
	}
	for _, v := range []string{g.Model, g.PromptVersion, g.PipelineVersion} {
		if v == "" || len(v) > 256 || strings.TrimSpace(v) != v || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return false
		}
	}
	return !containsSecret(structuredText(g))
}
func requiresGenerator(role, col string) bool {
	return (role == "agent" || role == "pipeline" || role == "service") && (col == "memory_candidates" || col == "skill_versions" || col == colReflections)
}

var originProductPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func validSourceOrigin(product, ref string, at *time.Time, now time.Time) bool {
	return originProductPattern.MatchString(product) && ref != "" && len(ref) <= 4096 && strings.TrimSpace(ref) == ref && strings.IndexFunc(ref, unicode.IsControl) < 0 && at != nil && storableTime(at.Truncate(time.Millisecond)) && !at.After(now)
}

var sourceHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func sourceReference(r Record, locator string) SourceRef {
	if r.Text == "" {
		return SourceRef{SourceID: r.ID, Locator: locator}
	}
	return SourceRef{SourceID: r.ID, Locator: locator, Span: &SourceSpan{ContentHash: ContentHash(r.Text), End: utf8.RuneCountInString(r.Text)}}
}

func validateSourceSpan(span *SourceSpan, hash string, length int) error {
	if span == nil {
		return nil
	}
	if span.Start < 0 || span.End <= span.Start || span.End > 64000 || !sourceHashPattern.MatchString(span.ContentHash) {
		return ErrInvalid
	}
	if span.ContentHash != hash {
		return ErrConflict
	}
	if span.End > length {
		return ErrInvalid
	}
	return nil
}
