package memory

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Exact immediate egress check for consolidation, separate from activation.
// Candidate/conflicted content remains untrusted data and cannot approve itself.
type ModelInputTransferInput struct {
	Scope       Scope  `json:"scope"`
	Refs        []Ref  `json:"refs"`
	Destination string `json:"destination"`
	Content     string `json:"content"`
}

func modelInputReadable(col string, r Record) bool {
	if r.ContentErased || r.PrivacyQuarantined || r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now()) {
		return false
	}
	switch col {
	case "sources":
		return live(r)
	case "memory_candidates":
		return r.Status == "candidate" || r.Status == "conflicted"
	case "memories":
		return live(r) || r.Status == "conflicted"
	}
	return false
}
func (s *Service) AuthorizeModelInput(ctx context.Context, token string, in ModelInputTransferInput) (ModelArchiveTransferOutput, error) {
	if !validScope(in.Scope) || len(in.Refs) < 1 || len(in.Refs) > 16 || in.Destination != "external:anthropic" || strings.TrimSpace(in.Content) == "" || len(in.Content) > 100000 {
		return ModelArchiveTransferOutput{}, ErrInvalid
	}
	actor, e := s.authorizeArchiveModelContent(ctx, token, in.Scope, "", "private", in.Content)
	if e != nil {
		return ModelArchiveTransferOutput{}, e
	}
	if actor.Role != roleService {
		return ModelArchiveTransferOutput{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	refs := append([]Ref(nil), in.Refs...)
	seen := map[Ref]bool{}
	checked := []Ref{}
	headers := []Record{}
	for i := 0; i < len(refs); i++ {
		if len(refs) > 128 {
			return ModelArchiveTransferOutput{}, ErrInvalid
		}
		ref := refs[i]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		more, parents, err := s.modelInputRef(ctx, token, in, ref)
		if err != nil {
			return ModelArchiveTransferOutput{}, err
		}
		headers = append(headers, more...)
		refs = append(refs, parents...)
		checked = append(checked, ref)
	}
	for _, h := range headers {
		if h.Sensitivity == "restricted" {
			return ModelArchiveTransferOutput{}, ErrDenied
		}
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, ref := range checked {
			if ref.Collection != "sources" {
				if err := tx.FenceRelationMember(ctx, in.Scope, ref.Collection, ref.ID); err != nil {
					return err
				}
			}
			r, err := tx.Get(ctx, ref.Collection, in.Scope, ref.ID)
			if err != nil {
				return err
			}
			if r.Revision != ref.Revision || !modelInputReadable(ref.Collection, r) {
				return ErrConflict
			}
			if ref.Collection != "sources" {
				if err = checkSources(ctx, tx, r); err != nil {
					return err
				}
				if err = checkRelations(ctx, tx, r); err != nil {
					return err
				}
			}
			if ref.Collection == "sources" {
				if err = tx.FenceSearchRead(ctx, in.Scope, ref.Collection, ref.ID, ref.Revision); err != nil {
					return err
				}
			}
		}
		if err := s.fenceSourceArchives(ctx, tx, headers); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "transfer:model_input", ContentHash(in.Content))
	})
	if e != nil {
		return ModelArchiveTransferOutput{}, e
	}
	return ModelArchiveTransferOutput{Authorized: true, ContentHash: ContentHash(in.Content)}, nil
}

// modelInputRef authorizes one model input record and returns its archive
// headers and the parent sources to follow.
func (s *Service) modelInputRef(ctx context.Context, token string, in ModelInputTransferInput, ref Ref) ([]Record, []Ref, error) {
	if ref.Collection != "sources" && ref.Collection != "memory_candidates" && ref.Collection != "memories" || ref.Revision < 1 || !validRunLabel(ref.ID, 256) {
		return nil, nil, ErrInvalid
	}
	r, err := s.Get(ctx, token, ref.Collection, in.Scope, ref.ID)
	if err != nil {
		return nil, nil, err
	}
	if r.Revision != ref.Revision || !modelInputReadable(ref.Collection, r) {
		return nil, nil, ErrConflict
	}
	if ref.Collection == "sources" {
		_, err = s.authorizeArchiveModelContent(ctx, token, in.Scope, r.ID, r.Sensitivity, recordContent(r))
	} else {
		_, err = s.authorizeTransfer(ctx, token, "transfer:model_input", in.Scope, r.ID, r.Sensitivity, in.Destination, recordContent(r), r.Sources)
	}
	if err != nil {
		return nil, nil, err
	}
	var headers []Record
	for start := 0; start < len(r.ArchiveMessages); start += 8 {
		more, _, err := s.validateArchiveMessageRefs(ctx, token, in.Scope, r.ArchiveMessages[start:min(start+8, len(r.ArchiveMessages))], "", "")
		if err != nil {
			return nil, nil, err
		}
		headers = append(headers, more...)
	}
	var parents []Ref
	for _, src := range r.Sources {
		parent, err := s.Get(ctx, token, "sources", in.Scope, src.SourceID)
		if err != nil {
			return nil, nil, err
		}
		parents = append(parents, Ref{Collection: "sources", ID: parent.ID, Revision: parent.Revision})
	}
	return headers, parents, nil
}
func modelInputRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/model-input/authorize", func(w http.ResponseWriter, r *http.Request) {
		var in ModelInputTransferInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.AuthorizeModelInput(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
