package memory

import (
	"context"
	"slices"
)

const deletionLedgerLimit = 1000

var deletionLedgerKinds = []string{"source_deletion", memoryRelationErasureKind, memoryEvaluationErasureKind, candidateEvaluationErasureKind, workDeletionKind}

type DeletionTarget struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
}
type DeletionLedger struct {
	Version         int                       `json:"version"`
	Scope           map[string]string         `json:"scope"`
	Targets         []DeletionTarget          `json:"targets"`
	PrivacyRequests *[]RecoveryPrivacyRequest `json:"privacy_requests,omitempty"`
}

// shortcut: bounded full-scope export; add snapshot-aware pagination if a
// scope exceeds 1000 receipts. Never return a silently truncated ledger.

func (s *Service) ExportDeletions(ctx context.Context, token string, scope Scope) (DeletionLedger, error) {
	return s.exportDeletions(ctx, token, scope, 1)
}

func (s *Service) ExportRecoveryDeletions(ctx context.Context, token string, scope Scope) (DeletionLedger, error) {
	return s.exportDeletions(ctx, token, scope, 2)
}

func (s *Service) exportDeletions(ctx context.Context, token string, scope Scope, version int) (DeletionLedger, error) {
	if !validScope(scope) {
		return DeletionLedger{}, ErrInvalid
	}
	who, e := s.authorize(ctx, token, "export:deletions", scope, "", "")
	if e != nil {
		return DeletionLedger{}, e
	}
	if who.Role != "human" {
		return DeletionLedger{}, ErrDenied
	}
	if s.Repo == nil {
		return DeletionLedger{}, ErrUnavailable
	}
	var out DeletionLedger
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		check := func(action, id string) error {
			reader, err := s.authorize(ctx, token, action, scope, id, "")
			if err != nil {
				return err
			}
			if reader.PrincipalID != who.PrincipalID || reader.Role != "human" {
				return ErrDenied
			}
			return nil
		}
		var err error
		out, err = buildDeletionLedger(ctx, tx, scope, version, check)
		if err != nil {
			return err
		}
		return audit(ctx, tx, scope, who.PrincipalID, "export:deletions", "")
	})
	if e != nil {
		return DeletionLedger{}, e
	}
	return out, nil
}

// Data validation shared by the scoped, audited API and the offline DB operator.
// A callback is required; only the privileged read-only snapshot passes its
// already established database authorization instead of per-record API policy.
type ledgerReadCheck func(action, id string) error

func buildDeletionLedger(ctx context.Context, tx Tx, scope Scope, version int, check ledgerReadCheck) (DeletionLedger, error) {
	if !validScope(scope) || check == nil || (version != 1 && version != 2) {
		return DeletionLedger{}, ErrInvalid
	}
	out := DeletionLedger{Version: version, Scope: map[string]string{"workspace_id": scope.WorkspaceID, "project_id": scope.ProjectID, "user_id": scope.UserID, "agent_id": scope.AgentID}, Targets: []DeletionTarget{}}
	rows, e := tx.DeletionRecords(ctx, scope)
	if e != nil {
		return DeletionLedger{}, e
	}
	if len(rows) > deletionLedgerLimit {
		return DeletionLedger{}, ErrUnavailable
	}
	seen := map[DeletionTarget]bool{}
	// As in deletion preview, row policies run within this bounded snapshot.
	for _, r := range rows {
		if !validDeletionLedgerRow(r, scope) {
			return DeletionLedger{}, ErrUnavailable
		}
		if e = check("read:deletion", r.Target.ID); e != nil {
			return DeletionLedger{}, e
		}
		if r.Kind == workDeletionKind {
			e = verifyWorkPrivacyImpact(ctx, tx, scope, PrivacyImpact{Records: []Ref{*r.Target}})
		} else {
			e = erasedTargetAbsent(ctx, tx, r)
		}
		if e != nil {
			return DeletionLedger{}, e
		}
		target := DeletionTarget{Collection: r.Target.Collection, ID: r.Target.ID}
		if seen[target] {
			return DeletionLedger{}, ErrUnavailable
		}
		seen[target] = true
		out.Targets = append(out.Targets, target)
	}
	if version == 2 {
		requests, err := recoveryPrivacyRequests(ctx, tx, scope, check)
		if err != nil {
			return DeletionLedger{}, err
		}
		out.PrivacyRequests = &requests
	}
	return out, nil
}
func validDeletionLedgerRow(r Record, scope Scope) bool {
	if r.Scope != scope || !slices.Contains(deletionLedgerKinds, r.Kind) || r.Target == nil || r.Revision < 1 || !validRunLabel(r.Target.ID, 256) {
		return false
	}
	switch r.Kind {
	case "source_deletion":
		return r.Target.Collection == "sources" && r.ID == sourceDeletionID(scope, r.Target.ID)
	case workDeletionKind:
		return validWorkDeletionReceipt(r, scope)
	}
	return validSourceErasureJob(r) && slices.Contains([]string{JobStatusPending, JobStatusRunning, JobStatusRetry, JobStatusDone, JobStatusFailed}, r.Status)
}
