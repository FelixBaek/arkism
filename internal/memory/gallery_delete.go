package memory

import (
	"context"
	"net/http"
	"time"
)

// Gallery delete and 보관함: prompts and design systems are deleted for good
// through the generic DELETE /v1/{collection}/{id} (human only, revision CAS,
// audited "delete:<collection>"), archived or not. Their sample images ride
// inside the record as data URLs, so the record is the only thing to erase:
// no blob or artifact points at them. The 보관함 lists the archived ones and
// 되살리기 puts one back on the list.

func galleryCollection(col string) bool { return col == colPrompts || col == colDesignSystems }

// ListArchivedGallery lists the archived prompts or design systems of a scope.
func (s *Service) ListArchivedGallery(ctx context.Context, token, col string, scope Scope) ([]Record, error) {
	if !galleryCollection(col) || !validScope(scope) {
		return nil, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+col, scope, "", ""); err != nil {
		return nil, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, col, 500)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, r := range rows {
		if r.Status == "archived" && (r.ImagePrompt != nil || r.DesignSystem != nil) {
			out = append(out, r)
		}
	}
	return out, nil
}

// RestoreGallery puts an archived prompt or design system back on its list.
func (s *Service) RestoreGallery(ctx context.Context, token, col, id string, in ChangeInput) (Record, error) {
	if !galleryCollection(col) || !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+col, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, col, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Revision != in.ExpectedRevision || r.Status != "archived" {
			return ErrConflict
		}
		prev := r.Revision
		r.Status = statusActive
		r.Revision++
		r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if err := tx.Replace(ctx, col, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "restore:"+col, id)
	})
	return out, err
}

// galleryArchiveRoutes: GET /v1/{prompts|design_systems}/archived and
// POST /v1/{prompts|design_systems}/{id}/restore.
func galleryArchiveRoutes(mux routeRegistrar, s *Service) {
	for _, col := range []string{colPrompts, colDesignSystems} {
		mux.HandleFunc("GET /v1/"+col+"/archived", func(w http.ResponseWriter, r *http.Request) {
			items, e := s.ListArchivedGallery(r.Context(), bearer(r), col, scopeQuery(r))
			respond(w, map[string]any{"items": items}, e)
		})
		mux.HandleFunc("POST /v1/"+col+"/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
			var in ChangeInput
			if !decode(w, r, &in) {
				return
			}
			out, e := s.RestoreGallery(r.Context(), bearer(r), col, r.PathValue("id"), in)
			respond(w, out, e)
		})
	}
}
