package memory

import (
	"slices"
)

// A conflict cannot disappear while its surviving members still carry its ID.
// Retain only the control fields needed for a later explicit human resolution.
func conflictControlRecord(r Record) (Record, error) {
	if r.Kind != "conflicts_with" || r.Status != "active" || r.Conflict == nil || len(r.Relations) != 2 || r.Revision < 1 || !validRunLabel(r.ID, 256) {
		return Record{}, ErrUnavailable
	}
	hold := r.Conflict.Hold
	if hold != "both" && hold != "first" && hold != "second" {
		return Record{}, ErrUnavailable
	}
	for _, ref := range r.Relations {
		if (ref.Collection != "memories" && ref.Collection != "memory_candidates") || !validRunLabel(ref.ID, 256) || ref.Revision < 1 {
			return Record{}, ErrUnavailable
		}
	}
	if r.Relations[0].ID == r.Relations[1].ID {
		return Record{}, ErrUnavailable
	}
	return Record{ID: r.ID, Scope: r.Scope, Kind: r.Kind, Status: r.Status, Revision: r.Revision, ContentErased: true, Relations: slices.Clone(r.Relations), Conflict: &ConflictDetails{Hold: hold}, Sources: []SourceRef{}, Sensitivity: r.Sensitivity, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CreatedBy: r.CreatedBy, CreatedByType: r.CreatedByType}, nil
}
