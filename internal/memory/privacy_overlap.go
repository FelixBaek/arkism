package memory

import (
	"context"
	"encoding/json"
)

type privacyOverlapStore interface {
	CheckPrivacyOverlap(context.Context, Scope, string, string, PrivacyImpact, bool) error
}

func checkPrivacyOverlap(ctx context.Context, tx Tx, scope Scope, id, targetType string, impact PrivacyImpact, holding bool) error {
	store, ok := tx.(privacyOverlapStore)
	if !ok {
		return ErrUnavailable
	}
	return store.CheckPrivacyOverlap(ctx, scope, id, targetType, impact, holding)
}

// Both sides of a hold/claim race read the other request before writing their
// own row. Serializable isolation retries a conflicting ordering. Do not move
// this predicate outside the transaction or use a previously cached result.
func (p *Postgres) CheckPrivacyOverlap(ctx context.Context, scope Scope, id, targetType string, impact PrivacyImpact, holding bool) error {
	if p.tx == nil || checkScope(scope) != nil {
		return ErrUnavailable
	}
	refs, err := json.Marshal(impact.Records)
	if err != nil {
		return ErrInvalid
	}
	var blocked bool
	err = p.q().QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM memory_records WHERE collection='jobs'
 AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4
 AND kind=$5 AND id<>$6
 AND (CASE WHEN $13 THEN status='running' OR
   doc->'privacy_deletion'->>'state' NOT IN ('requested','authorized','discovering_impact','quarantined','held','cancelled')
 ELSE doc->'privacy_deletion'->>'legal_hold_status'='held' OR doc->'privacy_deletion'->>'state'='held' END)
 AND ($7 OR doc->'privacy_deletion'->>'target_type'='subject'
 OR (doc#>'{privacy_deletion,impact,source_event_ids}') ?| $9::text[]
 OR (doc#>'{privacy_deletion,impact,trace_ids}') ?| $10::text[]
 OR (doc#>'{privacy_deletion,impact,thread_ids}') ?| $11::text[]
 OR (doc#>'{privacy_deletion,impact,expected_run_ids}') ?| $12::text[]
 OR EXISTS (SELECT 1
   FROM jsonb_array_elements(COALESCE(NULLIF(doc#>'{privacy_deletion,impact,records}','null'::jsonb),'[]'::jsonb)) a
   JOIN jsonb_array_elements(COALESCE(NULLIF($8::jsonb,'null'::jsonb),'[]'::jsonb)) b
   ON a->>'collection'=b->>'collection' AND a->>'id'=b->>'id'))
)`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, privacyDeletionKind, id, targetType == "subject", string(refs), impact.SourceEventIDs, impact.TraceIDs, impact.ThreadIDs, impact.ExpectedRunIDs, holding).Scan(&blocked)
	if err != nil {
		return pgErr(err)
	}
	if blocked {
		return ErrConflict
	}
	if holding {
		return p.checkArtifactHoldBoundary(ctx, scope, id)
	}
	return nil
}
