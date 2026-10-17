package memory

import (
	"context"
)

// A detached artifact still has provenance in its durable cleanup intent. Use
// that provenance until the bytes are removed and the intent is acknowledged.
// Both queries run in Serializable transactions: GC reads the hold and writes
// the blob marker; hold reads the marker and writes the privacy request.
const artifactPrivacyPairs = `
 FROM memory_records j
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(j.doc#>'{source_memory_erasure,pending_artifacts}', 'null'::jsonb), '[]'::jsonb)) a
 JOIN memory_records p ON p.collection='jobs' AND p.kind=$2
  AND p.workspace_id=j.workspace_id AND p.project_id=j.project_id
  AND p.user_id=j.user_id AND p.agent_id=j.agent_id
 WHERE j.collection='jobs' AND j.kind=ANY($1::text[])
 AND (p.doc#>>'{privacy_deletion,target_type}'='subject'
  OR (j.doc#>>'{target,collection}'='sources' AND (p.doc#>'{privacy_deletion,impact,source_event_ids}') ? (j.doc#>>'{target,id}'))
  OR EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(p.doc#>'{privacy_deletion,impact,records}', 'null'::jsonb), '[]'::jsonb)) r
   WHERE (r->>'collection'=j.doc#>>'{target,collection}' AND r->>'id'=j.doc#>>'{target,id}')
      OR (r->>'collection'='artifacts' AND r->>'id'=a->>'id')))
`

func (p *Postgres) CheckArtifactPrivacyHold(ctx context.Context, digest string) error {
	if p.tx == nil || !sourceHashPattern.MatchString(digest) {
		return ErrUnavailable
	}
	var blocked bool
	err := p.q().QueryRow(ctx, `SELECT arkism.artifact_gc_held($1)`, digest).Scan(&blocked)
	if err != nil {
		return pgErr(err)
	}
	if blocked {
		return ErrConflict
	}
	return nil
}

func (p *Postgres) checkArtifactHoldBoundary(ctx context.Context, scope Scope, id string) error {
	if p.tx == nil {
		return ErrUnavailable
	}
	var blocked bool
	err := p.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1`+artifactPrivacyPairs+`
 AND p.id=$3 AND p.workspace_id=$4 AND p.project_id=$5 AND p.user_id=$6 AND p.agent_id=$7
 AND arkism.artifact_cleanup_pending(j.workspace_id,j.project_id,j.user_id,j.agent_id,j.id,a->>'digest'))`, erasureJobKinds, privacyDeletionKind, id, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID).Scan(&blocked)
	if err != nil {
		return pgErr(err)
	}
	if blocked {
		return ErrConflict
	}
	return nil
}
