package memory

import (
	"context"
	"net/url"
	"slices"
)

const sourceDerivedCountLimit = 1000

type DerivedCountInput struct {
	Scope      Scope
	Collection string
}
type SourceDerivedCount struct {
	Source     Ref    `json:"source"`
	Collection string `json:"collection"`
	Count      int    `json:"count"`
	Authority  string `json:"authority"`
}

func normalizeDerivedCount(in DerivedCountInput) (DerivedCountInput, error) {
	normalized, e := normalizeDerivedList(DerivedListInput{Scope: in.Scope, Collection: in.Collection})
	in.Collection = normalized.Collection
	return in, e
}
func parseDerivedCountQuery(raw string) (DerivedCountInput, error) {
	q, e := url.ParseQuery(raw)
	if e != nil || q.Has("limit") || q.Has("cursor") {
		return DerivedCountInput{}, ErrInvalid
	}
	in, e := parseDerivedListQuery(raw)
	return DerivedCountInput{Scope: in.Scope, Collection: in.Collection}, e
}

func (s *Service) CountSourceDerived(ctx context.Context, token, sourceID string, in DerivedCountInput) (SourceDerivedCount, error) {
	in, e := normalizeDerivedCount(in)
	if e != nil {
		return SourceDerivedCount{}, e
	}
	if !validRunLabel(sourceID, 256) {
		return SourceDerivedCount{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "read:sources", in.Scope, sourceID, "")
	if e != nil {
		return SourceDerivedCount{}, e
	}
	checkRead := func(id string) error {
		d, e := s.authorize(ctx, token, "read:"+in.Collection, in.Scope, id, "")
		if e != nil {
			return e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return ErrDenied
		}
		return nil
	}
	if e = checkRead(""); e != nil {
		return SourceDerivedCount{}, e
	}
	if s.Repo == nil {
		return SourceDerivedCount{}, ErrUnavailable
	}
	source, e := s.Repo.Get(ctx, "sources", in.Scope, sourceID)
	if e != nil {
		return SourceDerivedCount{}, e
	}
	if !live(source) {
		return SourceDerivedCount{}, ErrNotFound
	}
	refs, e := s.Repo.SourceDerivedReferences(ctx, sourceID, in)
	if e != nil {
		return SourceDerivedCount{}, e
	}
	// shortcut: fail closed above 1000 records, like evaluation summaries; use an explicit bulk protocol if larger exact counts are needed.
	if len(refs) > sourceDerivedCountLimit {
		return SourceDerivedCount{}, ErrUnavailable
	}
	for _, ref := range refs {
		if ref.Collection != in.Collection || !validRunLabel(ref.ID, 256) || ref.Revision < 1 {
			return SourceDerivedCount{}, ErrUnavailable
		}
		if e = checkRead(ref.ID); e != nil {
			return SourceDerivedCount{}, e
		}
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		src, e := tx.Get(ctx, "sources", in.Scope, sourceID)
		if e != nil {
			return e
		}
		if !live(src) {
			return ErrNotFound
		}
		if src.Revision != source.Revision || src.Sensitivity != source.Sensitivity {
			return ErrConflict
		}
		actual, e := tx.SourceDerivedReferences(ctx, sourceID, in)
		if e != nil {
			return e
		}
		if !slices.Equal(actual, refs) {
			return ErrConflict
		}
		sources := map[string]bool{sourceID: true}
		for _, ref := range actual {
			r, e := tx.Get(ctx, in.Collection, in.Scope, ref.ID)
			if e != nil {
				return e
			}
			if r.Scope != in.Scope || r.Revision != ref.Revision || r.ContentErased || !directlyDerived(r, sourceID) {
				return ErrConflict
			}
			metadata, e := sourceMetadata(ctx, tx, in.Scope, r.Sources)
			if e != nil {
				return e
			}
			for _, src := range metadata {
				sources[src.ID] = true
			}
			if e = tx.FenceRelationMember(ctx, in.Scope, in.Collection, r.ID); e != nil {
				return e
			}
		}
		ids := make([]string, 0, len(sources))
		for id := range sources {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if e = tx.FenceSource(ctx, in.Scope, id); e != nil {
				return e
			}
		}
		if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "read:sources", sourceID); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:source_derived_count:"+in.Collection, sourceID)
	})
	if e != nil {
		return SourceDerivedCount{}, e
	}
	return SourceDerivedCount{Source: Ref{Collection: "sources", ID: sourceID, Revision: source.Revision}, Collection: in.Collection, Count: len(refs), Authority: AuthorityDataOnly}, nil
}
