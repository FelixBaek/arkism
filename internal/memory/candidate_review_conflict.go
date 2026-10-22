package memory

import (
	"context"
	"strings"
)

// A review can resolve one existing candidate/memory conflict atomically.
// References are taken before any mutation and bound to the operation receipt.
type CandidateReviewConflict struct {
	ID               string `json:"id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Memory           Ref    `json:"memory"`
	Resolution       string `json:"resolution"`
	Reason           string `json:"reason"`
}

func validCandidateReviewConflict(id string, in CandidateReviewInput) bool {
	c := in.Conflict
	if in.OperationID == "" || c.ID == "" || len(c.ID) > 256 || c.ExpectedRevision < 1 || c.Memory.Collection != colMemories || c.Memory.ID == "" || c.Memory.ID == id || len(c.Memory.ID) > 256 || c.Memory.Revision < 1 || strings.TrimSpace(c.Reason) == "" || len(c.Reason) > 2000 {
		return false
	}
	if in.Action == "reject" {
		return c.Resolution == "keep_old"
	}
	return c.Resolution == "keep_both" || c.Resolution == "replace_old"
}

func (s *Service) resolveReviewConflict(ctx context.Context, tx Tx, candidate Record, in CandidateReviewInput, actor Decision) (Record, *Record, error) {
	c := in.Conflict
	if candidate.ConflictID != c.ID {
		return Record{}, nil, ErrConflict
	}
	rel, err := tx.Get(ctx, "memory_relations", in.Scope, c.ID)
	if err != nil {
		return Record{}, nil, err
	}
	if rel.PrivacyQuarantined || rel.ContentErased || rel.Revision != c.ExpectedRevision || len(rel.Relations) != 2 {
		return Record{}, nil, ErrConflict
	}
	candidateRef := Ref{Collection: "memory_candidates", ID: candidate.ID, Revision: in.ExpectedRevision}
	candidateIndex := -1
	for i, ref := range rel.Relations {
		if ref == candidateRef {
			candidateIndex = i
		}
	}
	if candidateIndex < 0 || rel.Relations[1-candidateIndex] != c.Memory {
		return Record{}, nil, ErrConflict
	}
	old, err := tx.Get(ctx, colMemories, in.Scope, c.Memory.ID)
	if err != nil {
		return Record{}, nil, err
	}
	if old.ContentErased || old.PrivacyQuarantined || !s.memoryReviewerAllowed(actor, in.Scope, old.Kind) {
		return Record{}, nil, ErrDenied
	}
	resolution := reviewConflictResolution(in.Action, candidateIndex)
	resolved, err := applyConflictAs(ctx, tx, ConflictInput{Scope: in.Scope, Memories: rel.Relations, Action: "resolve", Reason: c.Reason, ConflictID: c.ID, ExpectedRevision: c.ExpectedRevision, Resolution: resolution}, actor.PrincipalID, actor.Role, -1)
	if err != nil {
		return Record{}, nil, err
	}
	next, replaced := resolvedReviewRows(resolved, candidate.ID, old.ID, c.Resolution == "replace_old")
	if next.ID == "" {
		return Record{}, nil, ErrConflict
	}
	return next, replaced, nil
}

// reviewConflictResolution keeps the other side when the candidate is rejected.
func reviewConflictResolution(action string, candidateIndex int) string {
	if action != "reject" {
		return "keep_both"
	}
	if candidateIndex == 0 {
		return "keep_second"
	}
	return "keep_first"
}

func resolvedReviewRows(resolved ConflictOutput, candidateID, oldID string, replace bool) (Record, *Record) {
	var next Record
	var replaced *Record
	for _, row := range resolved.Memories {
		if row.ID == candidateID {
			next = row
		}
		if row.ID == oldID && replace {
			clone := row
			replaced = &clone
		}
	}
	return next, replaced
}

func finishReviewReplacement(ctx context.Context, tx Tx, old, next *Record, in CandidateReviewInput, actor Decision) (Record, error) {
	// The next record must not depend on the revision we are retiring.
	for _, ref := range next.Relations {
		if ref.Collection == colMemories && ref.ID == old.ID {
			return Record{}, ErrConflict
		}
	}
	oldRevision := old.Revision
	old.Status = "superseded"
	old.Revision++
	old.UpdatedAt = next.UpdatedAt
	relation, err := linkSupersession(old, next, in.Conflict.Reason, actor.PrincipalID, next.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	relation.CreatedByType = actor.Role
	if err = tx.Replace(ctx, colMemories, *old, oldRevision); err != nil {
		return Record{}, err
	}
	// Keep the original conflict's historical endpoints and attach its replacement.
	rel, err := tx.Get(ctx, "memory_relations", in.Scope, in.Conflict.ID)
	if err != nil {
		return Record{}, err
	}
	revision := rel.Revision
	detail := *rel.Conflict
	detail.Replacement = &Ref{Collection: "memory_relations", ID: relation.ID, Revision: relation.Revision}
	detail.Resolution = "candidate_replaces_memory"
	rel.Conflict = &detail
	rel.Revision++
	rel.UpdatedAt = next.UpdatedAt
	if err = tx.Replace(ctx, "memory_relations", rel, revision); err != nil {
		return Record{}, err
	}
	return relation, nil
}
