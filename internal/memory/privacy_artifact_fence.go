package memory

import (
	"context"
	"strings"
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

// SECURITY DEFINER changes current_user, so check the original SQL role (SET
// ROLE, or the session login when no role is set), never the function owner.
// Normal application callers must pass the token permission checks below.
const artifactPrivilegedInvoker = `EXISTS (SELECT 1 FROM pg_catalog.pg_roles
 WHERE rolname=COALESCE(NULLIF(current_setting('role',true),'none'),session_user::text)
 AND (rolsuper OR rolbypassrls OR rolname='arkism_init'))`

func (p *Postgres) initArtifactPrivacyRLS(ctx context.Context) error {
	// Only code-owned kind constants enter these function definitions.
	kinds := "ARRAY['" + strings.Join(erasureJobKinds, "','") + "']::text[]"
	pairs := strings.NewReplacer("$1::text[]", kinds, "$2", "'"+privacyDeletionKind+"'", "memory_records", "arkism.memory_records").Replace(artifactPrivacyPairs)
	statements := []string{
		`CREATE OR REPLACE FUNCTION arkism.artifact_gc_held(blob_digest text) RETURNS boolean
 LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $body$
 BEGIN
  IF NOT (arkism.has_any_permission('storage:internal') OR ` + artifactPrivilegedInvoker + `) THEN
   RAISE EXCEPTION 'artifact GC permission denied' USING ERRCODE='42501';
  END IF;
  RETURN EXISTS (SELECT 1` + pairs + `
   AND a->>'digest'=blob_digest
   AND (p.doc#>>'{privacy_deletion,state}'='held' OR p.doc#>>'{privacy_deletion,legal_hold_status}'='held'));
 END $body$`,
		`CREATE OR REPLACE FUNCTION arkism.artifact_cleanup_pending(org text,project text,subject_id text,agent text,job_id text,blob_digest text) RETURNS boolean
 LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $body$
 BEGIN
  IF NOT (arkism.has_permission(org,project,'db:read') OR ` + artifactPrivilegedInvoker + `) THEN
   RAISE EXCEPTION 'artifact hold permission denied' USING ERRCODE='42501';
  END IF;
  RETURN EXISTS (SELECT 1 FROM arkism.memory_records j
   CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(j.doc#>'{source_memory_erasure,pending_artifacts}','null'::jsonb),'[]'::jsonb)) a
   JOIN arkism.memory_records b ON b.collection='blobs' AND b.id=a->>'digest'
    AND b.workspace_id='__memory_blobs__' AND b.project_id='__memory_blobs__' AND b.user_id='' AND b.agent_id='' AND b.status='pending'
   WHERE j.collection='jobs' AND j.kind=ANY(` + kinds + `) AND j.id=job_id
    AND j.workspace_id=org AND j.project_id=project AND j.user_id=subject_id AND j.agent_id=agent AND a->>'digest'=blob_digest);
 END $body$`,
		`REVOKE ALL ON FUNCTION arkism.artifact_gc_held(text),arkism.artifact_cleanup_pending(text,text,text,text,text,text) FROM PUBLIC`,
		`DO $$ DECLARE role_name text; BEGIN
  FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role','arkism_app'] LOOP
   IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
    EXECUTE format('GRANT EXECUTE ON FUNCTION arkism.artifact_gc_held(text),arkism.artifact_cleanup_pending(text,text,text,text,text,text) TO %I',role_name);
   END IF;
  END LOOP;
 END $$`,
	}
	for _, statement := range statements {
		if _, err := p.q().Exec(ctx, statement); err != nil {
			return pgErr(err)
		}
	}
	return nil
}
