package memory

import (
	"context"
	"strings"
)

// Service-created source cleanup must retain its canonical privacy parent.
// A memory cleanup may sit between that parent and this derived source.
func derivedSourceErasureParent(ctx context.Context, tx Tx, job Record) (string, error) {
	if job.CreatedByType == roleHuman {
		return job.ID, nil
	}
	seen := map[string]bool{job.ID: true}
	parentID := job.ErasureParentID
	for len(seen) < 64 && parentID != "" && !seen[parentID] {
		seen[parentID] = true
		parent, err := tx.Get(ctx, colJobs, job.Scope, parentID)
		if err != nil {
			return "", err
		}
		if parent.Kind == privacyDeletionKind && parent.PrivacyDeletion != nil && strings.HasPrefix(parent.ID, "privacy-deletion-") && sourceHashPattern.MatchString(strings.TrimPrefix(parent.ID, "privacy-deletion-")) {
			return parent.ID, nil
		}
		if !validSourceErasureJob(parent) {
			return "", ErrConflict
		}
		parentID = parent.ErasureParentID
	}
	return "", ErrConflict
}
