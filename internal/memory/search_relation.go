package memory

import (
	"context"
	"slices"
)

func validSearchRelation(in SearchInput) bool {
	if (in.RelatedTo != nil) != slices.Contains(in.Modes, "relation") {
		return false
	}
	if in.RelatedTo == nil {
		return true
	}
	r := in.RelatedTo
	return slices.Contains([]string{"sources", "memories", "evaluations"}, r.Collection) && validRunLabel(r.ID, 256) && !containsSecret(r.ID) && r.Revision > 0 && (in.Purpose == "" || in.Purpose == "answer") && (in.Destination == "" || in.Destination == "internal")
}

func relationReferences(col string, anchor Record) ([]Ref, error) {
	refs := []Ref{}
	switch col {
	case "sources":
	case colMemories:
		if len(anchor.Supersedes) > 64 {
			return nil, ErrInvalid
		}
		refs = append(refs, anchor.Supersedes...)
		if anchor.SupersededBy != nil {
			refs = append(refs, *anchor.SupersededBy)
		}
	case "evaluations":
		if len(anchor.Relations) > 64 {
			return nil, ErrInvalid
		}
		if anchor.Target != nil && anchor.Target.Collection == colMemories {
			refs = append(refs, *anchor.Target)
		}
		for _, ref := range anchor.Relations {
			if ref.Collection == colMemories {
				refs = append(refs, ref)
			}
		}
	default:
		return nil, ErrInvalid
	}
	for _, ref := range refs {
		if ref.Collection != colMemories || !validRunLabel(ref.ID, 256) || containsSecret(ref.ID) || ref.Revision < 1 {
			return nil, ErrInvalid
		}
	}
	return refs, nil
}

func relationMatches(col string, anchor, candidate Record) bool {
	if anchor.Scope != candidate.Scope {
		return false
	}
	if col == "sources" {
		return directlyDerived(candidate, anchor.ID)
	}
	refs, err := relationReferences(col, anchor)
	return err == nil && slices.Contains(refs, Ref{Collection: colMemories, ID: candidate.ID, Revision: candidate.Revision})
}

func checkRelationAnchor(ctx context.Context, tx Tx, col string, r Record) error {
	if r.ContentErased || col == colMemories && !historySearchable(r) || col != colMemories && !live(r) {
		return ErrConflict
	}
	if _, err := relationReferences(col, r); err != nil {
		return err
	}
	if col == "sources" {
		return nil
	}
	if col == "evaluations" {
		return checkEvaluationEvidence(ctx, tx, r)
	}
	return checkSources(ctx, tx, r)
}

func (s *Service) readRelationAnchor(ctx context.Context, token string, in SearchInput, actor Decision) (Record, error) {
	ref := in.RelatedTo
	who, err := s.authorize(ctx, token, "read:"+ref.Collection, in.Scope, ref.ID, "")
	if err != nil {
		return Record{}, err
	}
	if who.PrincipalID != actor.PrincipalID || who.Role != actor.Role {
		return Record{}, ErrDenied
	}
	r, err := s.Repo.Get(ctx, ref.Collection, in.Scope, ref.ID)
	if err != nil {
		return Record{}, err
	}
	if r.Scope != in.Scope || r.ID != ref.ID || r.Revision != ref.Revision {
		return Record{}, ErrConflict
	}
	who, err = s.authorize(ctx, token, "read:"+ref.Collection, in.Scope, ref.ID, structuredText(r))
	if err != nil {
		return Record{}, err
	}
	if who.PrincipalID != actor.PrincipalID || who.Role != actor.Role {
		return Record{}, ErrDenied
	}
	if err = checkRelationAnchor(ctx, s.Repo, ref.Collection, r); err != nil {
		return Record{}, err
	}
	return r, nil
}
