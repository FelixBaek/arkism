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
	// Link is the memory_relations row of a causes/caused_by link.
	Link *Ref `json:"link,omitempty"`
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
	causal, e := s.causalLineage(ctx, scope, id)
	if e != nil {
		return MemoryLineage{}, e
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
		if e = followCausalRevisions(ctx, tx, scope, causal); e != nil {
			return e
		}
		out.Links = append(out.Links, causal...)
		for i := range out.Links {
			if e = s.resolveLineageLink(ctx, tx, token, scope, d, &out.Links[i]); e != nil {
				return e
			}
		}
		return audit(ctx, tx, scope, d.PrincipalID, "read:memory_lineage", id)
	})
	if e != nil {
		return MemoryLineage{}, e
	}
	return out, nil
}

func (s *Service) resolveLineageLink(ctx context.Context, tx Tx, token string, scope Scope, d Decision, link *LineageLink) error {
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
		return nil
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
		return nil
	}
	if err != nil {
		return err
	}
	if target.Revision != ref.Revision {
		link.State = "changed"
		return nil
	}
	err = checkSources(ctx, tx, target)
	if errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) {
		link.State = "invalid_sources"
		return nil
	}
	if err != nil {
		return err
	}
	link.State = "available"
	link.Record = &target
	return nil
}

// followCausalRevisions points causal links at each memory's current revision: a
// link names the memory, not the wording it had when the link was made.
func followCausalRevisions(ctx context.Context, tx Tx, scope Scope, links []LineageLink) error {
	for i := range links {
		m, e := tx.Get(ctx, colMemories, scope, links[i].Reference.ID)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return e
		}
		links[i].Reference.Revision = m.Revision
	}
	return nil
}
