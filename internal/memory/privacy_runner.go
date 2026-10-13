package memory

// Arkism runs no external agent runtime: workflow runs keep no checkpoints, writes, blobs or
// journals outside this database. A deletion's runtime step therefore has nothing to purge, and a
// deployment that says so (MEMORY_PRIVACY_NO_AGENT_RUNTIME=true, which the desktop's built-in
// Arkism sets) records that step itself: verified, every expected run absent. The worker's
// WorkOnce then purges the derived records and verifies, as for any deletion.

import (
	"context"
	"errors"
	"slices"
)

// privacyPendingScopes lists the scopes that have a deletion waiting for its runtime step.
type privacyPendingScopes interface {
	PrivacyRuntimeScopes(ctx context.Context) ([]Scope, error)
}

// RunPrivacyRuntimeStep records the runtime step of every waiting deletion and returns how many.
func (s *Service) RunPrivacyRuntimeStep(ctx context.Context, token string) (int, error) {
	lister, ok := s.Repo.(privacyPendingScopes)
	if !ok {
		return 0, ErrUnavailable
	}
	scopes, err := lister.PrivacyRuntimeScopes(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, scope := range scopes {
		for range 50 {
			action, err := s.ClaimPrivacyDeletion(ctx, token, PrivacyScopeInput{Scope: scope})
			if errors.Is(err, ErrNotFound) {
				break
			}
			if err != nil {
				return done, err
			}
			if _, err := s.RecordPrivacyRuntimeReceipt(ctx, token, action.DeletionRequestID, PrivacyRuntimeReceiptInput{
				Scope: action.Scope, ExpectedRevision: action.Revision, LeaseToken: action.LeaseToken, Receipt: noRuntimeReceipt(action),
			}); err != nil {
				return done, err
			}
			done++
		}
	}
	return done, nil
}

// noRuntimeReceipt is the honest receipt where no agent runtime exists: nothing matched, every
// expected run absent, nothing counted. A held deletion stays held (not verified).
func noRuntimeReceipt(action PrivacyRuntimeAction) PrivacyRuntimeReceipt {
	held := action.HoldStatus == privacyHoldHeld
	expected := slices.Clone(action.ExpectedRunIDs)
	if expected == nil {
		expected = []string{}
	}
	return PrivacyRuntimeReceipt{RequestHash: action.RequestHash, Quarantined: true, Verified: !held, Held: held,
		ExpectedRunIDs: expected, MatchedRunIDs: []string{}, AffectedRunIDs: []string{}, AbsentRunIDs: slices.Clone(expected), Errors: []string{}}
}
