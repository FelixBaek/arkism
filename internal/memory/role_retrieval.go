package memory

import (
	"context"
	"slices"
)

// RoleSearchInput selects a server-defined relevance profile, never an identity
// or permission. Every result still passes Search's normal authorization.
type RoleSearchInput struct {
	SearchInput
	Role string `json:"role"`
}

func roleMemoryKinds(role string) []string {
	switch role {
	case "researcher":
		return []string{"semantic", "episodic", "user", "decision", "lesson"}
	case "implementer":
		return []string{"procedural", "user", "decision", "lesson"}
	case "reviewer":
		return []string{"procedural", "semantic", "user", "decision", "lesson"}
	default:
		return nil
	}
}

func (s *Service) SearchForRole(ctx context.Context, token string, in RoleSearchInput) (ContextBundle, error) {
	kinds := roleMemoryKinds(in.Role)
	if kinds == nil || in.Purpose != "" && in.Purpose != "answer" && in.Purpose != "knowledge" {
		return ContextBundle{}, ErrInvalid
	}
	if _, err := normalizeSearchCriteria(in.SearchCriteria); err != nil {
		return ContextBundle{}, err
	}
	if len(in.Kinds) > 0 {
		narrowed := []string{}
		for _, kind := range in.Kinds {
			if slices.Contains(kinds, kind) {
				narrowed = append(narrowed, kind)
			}
		}
		// Empty means unconstrained to Search; reject instead of widening the request.
		if len(narrowed) == 0 {
			return ContextBundle{}, ErrInvalid
		}
		kinds = narrowed
	}
	in.Kinds = kinds
	return s.Search(ctx, token, in.SearchInput)
}
