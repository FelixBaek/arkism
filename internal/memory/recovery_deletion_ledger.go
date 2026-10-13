package memory

import (
	"context"
	"slices"
	"time"
)

// RecoveryPrivacyRequest is evidence, not a command or proof of erasure.
// Keep holds visible and never export reasons, actor details or runtime data.
type RecoveryPrivacyRequest struct {
	ID              string    `json:"id"`
	Revision        int64     `json:"revision"`
	TargetType      string    `json:"target_type"`
	TargetIDs       []string  `json:"target_ids"`
	State           string    `json:"state"`
	LegalHoldStatus string    `json:"legal_hold_status"`
	RequestedAt     time.Time `json:"requested_at"`
}

type recoveryDeletionReader interface {
	RecoveryPrivacyRecords(context.Context, Scope) ([]Record, error)
}

func (p *Postgres) RecoveryPrivacyRecords(ctx context.Context, scope Scope) ([]Record, error) {
	if checkScope(scope) != nil {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colJobs, scope, "kind=$6", []any{privacyDeletionKind}, "id ASC", deletionLedgerLimit+1)
}

func recoveryPrivacyRequests(ctx context.Context, tx Tx, scope Scope, check ledgerReadCheck) ([]RecoveryPrivacyRequest, error) {
	reader, ok := tx.(recoveryDeletionReader)
	if !ok {
		return nil, ErrUnavailable
	}
	rows, err := reader.RecoveryPrivacyRecords(ctx, scope)
	if err != nil {
		return nil, err
	}
	if len(rows) > deletionLedgerLimit {
		return nil, ErrUnavailable
	}
	out := make([]RecoveryPrivacyRequest, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		pd := row.PrivacyDeletion
		if row.Scope != scope || row.Kind != privacyDeletionKind || row.Revision < 1 || pd == nil || seen[row.ID] {
			return nil, ErrUnavailable
		}
		request := PrivacyDeletionRequest{Scope: scope, TargetType: pd.TargetType, TargetIDs: slices.Clone(pd.TargetIDs), IdempotencyKey: pd.IdempotencyKey, Reason: pd.Reason, RetentionPolicyRef: pd.RetentionPolicyRef}
		_, knownState := privacyStateTransitions[pd.State]
		knownState = knownState || pd.State == privacyCompleted || pd.State == privacyCancelled || pd.State == privacyFailed
		if validatePrivacyRequest(&request) != nil || row.ID != privacyJobID(scope, pd.RequesterID, pd.IdempotencyKey) || !slices.Contains([]string{"human", "service"}, pd.RequesterType) || pd.RequesterID == "" || !knownState || !slices.Contains([]string{privacyHoldNone, privacyHoldHeld, privacyHoldReleased}, pd.LegalHoldStatus) || pd.RequestedAt.IsZero() || pd.RequestedAt.After(time.Now()) {
			return nil, ErrUnavailable
		}
		if err = check("read:privacy_deletion", row.ID); err != nil {
			return nil, err
		}
		seen[row.ID] = true
		out = append(out, RecoveryPrivacyRequest{ID: row.ID, Revision: row.Revision, TargetType: pd.TargetType, TargetIDs: request.TargetIDs, State: pd.State, LegalHoldStatus: pd.LegalHoldStatus, RequestedAt: pd.RequestedAt})
	}
	return out, nil
}
