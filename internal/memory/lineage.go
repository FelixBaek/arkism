package memory

import (
	"context"
	"errors"
)

type LineageLink struct {
	Relation  string  `json:"relation"`
	Reference Ref     `json:"reference"`
	State     string  `json:"state"`
	Record    *Record `json:"record,omitempty"`
}
type MemoryLineage struct {
	Memory    Ref           `json:"memory"`
	Status    string        `json:"status"`
	Links     []LineageLink `json:"links"`
	Authority string        `json:"authority"`
}

func (s *Service) MemoryLineage(ctx context.Context, token string, scope Scope, id string) (MemoryLineage, error) {
	if id == "" || len(id) > 256 {
		return MemoryLineage{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:memories", scope, id, "")
	if e != nil {
		return MemoryLineage{}, e
	}
	if s.Repo == nil {
		return MemoryLineage{}, ErrUnavailable
	}
	var out MemoryLineage
	// shortcut: bounded one-hop walk; preauthorize with revision fences if policy latency grows.
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = MemoryLineage{}
		r, e := tx.Get(ctx, "memories", scope, id)
		if e != nil {
			return e
		}
		if r.PrivacyQuarantined || r.ContentErased {
			return ErrNotFound // same answer as Get while a privacy deletion is pending
		}
		if len(r.Supersedes) > 64 {
			return ErrInvalid
		}
		out = MemoryLineage{Memory: Ref{Collection: "memories", ID: id, Revision: r.Revision}, Status: r.Status, Links: []LineageLink{}, Authority: AuthorityDataOnly}
		if r.Proposal != nil {
			out.Links = append(out.Links, LineageLink{Relation: "proposal", Reference: *r.Proposal})
		}
		if r.SupersededBy != nil {
			out.Links = append(out.Links, LineageLink{Relation: "superseded_by", Reference: *r.SupersededBy})
		}
		for _, ref := range r.Supersedes {
			out.Links = append(out.Links, LineageLink{Relation: "supersedes", Reference: ref})
		}
		for i := range out.Links {
			link := &out.Links[i]
			ref := link.Reference
			col := "memories"
			if link.Relation == "proposal" {
				col = "memory_candidates"
			}
			if ref.Collection != col || ref.ID == "" || len(ref.ID) > 256 || ref.Revision < 1 {
				return ErrInvalid
			}
			if containsSecret(ref.ID) {
				return ErrSecretDetected
			}
			who, err := s.authorize(ctx, token, "read:"+col, scope, ref.ID, "")
			if errors.Is(err, ErrDenied) {
				link.State = "hidden"
				continue
			}
			if err != nil {
				return err
			}
			if who.PrincipalID != d.PrincipalID || who.Role != d.Role {
				return ErrDenied
			}
			target, err := tx.Get(ctx, col, scope, ref.ID)
			if errors.Is(err, ErrNotFound) {
				link.State = "unavailable"
				continue
			}
			if err != nil {
				return err
			}
			if target.Revision != ref.Revision {
				link.State = "changed"
				continue
			}
			err = checkSources(ctx, tx, target)
			if errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) {
				link.State = "invalid_sources"
				continue
			}
			if err != nil {
				return err
			}
			link.State = "available"
			link.Record = &target
		}
		return audit(ctx, tx, scope, d.PrincipalID, "read:memory_lineage", id)
	})
	if e != nil {
		return MemoryLineage{}, e
	}
	return out, nil
}
