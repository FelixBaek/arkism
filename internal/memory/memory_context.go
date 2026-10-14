package memory

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// MemoryContext describes when evidence is useful. It never grants access or
// changes scope, trust, lifecycle dates, or the authority of retrieved data.
type MemoryContext struct {
	Domains       []string   `json:"domains" bson:"domains"`
	Applicability string     `json:"applicability,omitempty" bson:"applicability,omitempty"`
	EvidenceKind  string     `json:"evidence_kind" bson:"evidence_kind"`
	Uncertainty   string     `json:"uncertainty,omitempty" bson:"uncertainty,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty" bson:"observed_at,omitempty"`
	ValidFrom     string     `json:"valid_from,omitempty" bson:"valid_from,omitempty"`
	SemanticKind  string     `json:"semantic_kind" bson:"semantic_kind"`
}

func validMemoryContext(c *MemoryContext, now time.Time) bool {
	if c == nil {
		return true
	}
	if len(c.Domains) < 1 || len(c.Domains) > 2 || !slices.Contains([]string{"user_statement", "tool_observation", "agent_statement", "inference"}, c.EvidenceKind) || !slices.Contains([]string{"fact", "preference", "correction", "lesson", "failure_recovery"}, c.SemanticKind) {
		return false
	}
	seen := map[string]bool{}
	for _, d := range c.Domains {
		if (d != "personal" && d != "work") || seen[d] {
			return false
		}
		seen[d] = true
	}
	for _, v := range []struct {
		s   string
		max int
	}{{c.Applicability, 2000}, {c.Uncertainty, 512}, {c.ValidFrom, 256}} {
		if !utf8.ValidString(v.s) || utf8.RuneCountInString(v.s) > v.max || strings.ContainsRune(v.s, '\x00') {
			return false
		}
	}
	return c.ObservedAt == nil || storableTime(*c.ObservedAt) && !c.ObservedAt.After(now)
}

func copyMemoryContext(c *MemoryContext) *MemoryContext {
	if c == nil {
		return nil
	}
	out := *c
	out.Domains = append([]string(nil), c.Domains...)
	slices.Sort(out.Domains)
	if c.ObservedAt != nil {
		at := c.ObservedAt.UTC().Truncate(time.Millisecond)
		out.ObservedAt = &at
	}
	return &out
}

// Observation timestamps vary across sessions and do not create new claims.
// Domain, condition and evidence distinctions must survive deduplication.
func memoryContextKey(c *MemoryContext) string {
	if c == nil {
		return ""
	}
	out := copyMemoryContext(c)
	out.ObservedAt = nil
	return structuredText(out)
}

// Evidence labels are checked against canonical sources, not model confidence.
// Returned archive headers are fenced in the candidate creation transaction.
func (s *Service) validateMemoryContextEvidence(ctx context.Context, token string, scope Scope, refs []SourceRef, c *MemoryContext) ([]Record, error) {
	if c == nil || c.EvidenceKind == "inference" {
		return nil, nil
	}
	if len(refs) == 0 {
		return nil, ErrInvalid
	}
	var headers []Record
	for _, ref := range refs {
		source, err := s.Get(ctx, token, "sources", scope, ref.SourceID)
		if err != nil {
			return nil, err
		}
		if !live(source) || source.ContentErased {
			return nil, ErrConflict
		}
		role := ""
		switch c.EvidenceKind {
		case "tool_observation":
			if source.Kind != "tool_result" {
				return nil, ErrInvalid
			}
		case "user_statement":
			if len(source.ArchiveMessages) == 0 && source.TrustLevel == "user_authored" {
				continue
			}
			role = "user"
		case "agent_statement":
			if len(source.ArchiveMessages) == 0 && source.CreatedByType == roleAgent {
				continue
			}
			role = "assistant"
		}
		if role != "" {
			if len(source.ArchiveMessages) == 0 {
				return nil, ErrInvalid
			}
			more, _, err := s.validateArchiveMessageRefs(ctx, token, scope, source.ArchiveMessages, "", role)
			if err != nil {
				return nil, err
			}
			headers = append(headers, more...)
		}
	}
	return headers, nil
}
