-- Independent schema-only dump from the actual pre-migration Postgres.Init.
-- Source: public commit 575b4da1b1511fd5ee6348146493120fd322824f, PostgreSQL 18.
-- pg_dump --schema-only --no-owner --no-comments --schema arkism --schema extensions
-- Removed psql/version metadata and transaction_timeout; added the schema-filtered
-- vector extension and restored connection settings. Seed data is set by the test.
-- Do not regenerate this fixture from a numbered migration asset.

--
-- PostgreSQL database dump
--



SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: arkism; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA arkism;


--
-- Name: extensions; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA extensions;

CREATE EXTENSION vector WITH SCHEMA extensions;


--
-- Name: artifact_cleanup_pending(text, text, text, text, text, text); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.artifact_cleanup_pending(org text, project text, subject_id text, agent text, job_id text, blob_digest text) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
 BEGIN
  IF NOT (arkism.has_permission(org,project,'db:read') OR EXISTS (SELECT 1 FROM pg_catalog.pg_roles
 WHERE rolname=COALESCE(NULLIF(current_setting('role',true),'none'),session_user::text)
 AND (rolsuper OR rolbypassrls OR rolname='arkism_init'))) THEN
   RAISE EXCEPTION 'artifact hold permission denied' USING ERRCODE='42501';
  END IF;
  RETURN EXISTS (SELECT 1 FROM arkism.memory_records j
   CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(j.doc#>'{source_memory_erasure,pending_artifacts}','null'::jsonb),'[]'::jsonb)) a
   JOIN arkism.memory_records b ON b.collection='blobs' AND b.id=a->>'digest'
    AND b.workspace_id='__memory_blobs__' AND b.project_id='__memory_blobs__' AND b.user_id='' AND b.agent_id='' AND b.status='pending'
   WHERE j.collection='jobs' AND j.kind=ANY(ARRAY['source_memory_erasure','source_record_erasure','source_skill_erasure','source_relation_erasure','source_memory_dependents_erasure','memory_relation_erasure','memory_evaluation_erasure','candidate_evaluation_erasure']::text[]) AND j.id=job_id
    AND j.workspace_id=org AND j.project_id=project AND j.user_id=subject_id AND j.agent_id=agent AND a->>'digest'=blob_digest);
 END $$;


--
-- Name: artifact_gc_held(text); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.artifact_gc_held(blob_digest text) RETURNS boolean
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
 BEGIN
  IF NOT (arkism.has_any_permission('storage:internal') OR EXISTS (SELECT 1 FROM pg_catalog.pg_roles
 WHERE rolname=COALESCE(NULLIF(current_setting('role',true),'none'),session_user::text)
 AND (rolsuper OR rolbypassrls OR rolname='arkism_init'))) THEN
   RAISE EXCEPTION 'artifact GC permission denied' USING ERRCODE='42501';
  END IF;
  RETURN EXISTS (SELECT 1
 FROM arkism.memory_records j
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(j.doc#>'{source_memory_erasure,pending_artifacts}', 'null'::jsonb), '[]'::jsonb)) a
 JOIN arkism.memory_records p ON p.collection='jobs' AND p.kind='privacy_deletion'
  AND p.workspace_id=j.workspace_id AND p.project_id=j.project_id
  AND p.user_id=j.user_id AND p.agent_id=j.agent_id
 WHERE j.collection='jobs' AND j.kind=ANY(ARRAY['source_memory_erasure','source_record_erasure','source_skill_erasure','source_relation_erasure','source_memory_dependents_erasure','memory_relation_erasure','memory_evaluation_erasure','candidate_evaluation_erasure']::text[])
 AND (p.doc#>>'{privacy_deletion,target_type}'='subject'
  OR (j.doc#>>'{target,collection}'='sources' AND (p.doc#>'{privacy_deletion,impact,source_event_ids}') ? (j.doc#>>'{target,id}'))
  OR EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(p.doc#>'{privacy_deletion,impact,records}', 'null'::jsonb), '[]'::jsonb)) r
   WHERE (r->>'collection'=j.doc#>>'{target,collection}' AND r->>'id'=j.doc#>>'{target,id}')
      OR (r->>'collection'='artifacts' AND r->>'id'=a->>'id')))

   AND a->>'digest'=blob_digest
   AND (p.doc#>>'{privacy_deletion,state}'='held' OR p.doc#>>'{privacy_deletion,legal_hold_status}'='held'));
 END $$;


--
-- Name: authorize_principal(text, text, text); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.authorize_principal(org text, project text, requested_action text) RETURNS TABLE(principal_id text, principal_role text, allowed boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
 SELECT p.id,p.role,arkism.has_permission(org,project,requested_action)
 FROM arkism.principals p
 WHERE encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true) LIMIT 1
$$;


--
-- Name: check_recovery_write_gate(); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.check_recovery_write_gate() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $_$
DECLARE raw bytea; gate jsonb;
BEGIN
 SELECT value INTO raw FROM arkism.memory_storage_metadata WHERE key='recovery_write_gate' FOR SHARE;
 IF raw IS NULL THEN RAISE EXCEPTION 'recovery write gate unavailable' USING ERRCODE='55000'; END IF;
 gate := convert_from(raw,'UTF8')::jsonb;
 IF jsonb_typeof(gate)<>'object' OR NOT (gate ?& ARRAY['version','state','generation','changed_at'])
    OR gate-'version'-'state'-'generation'-'changed_at'<>'{}'::jsonb
    OR jsonb_typeof(gate->'version')<>'number' OR gate->>'version'<>'1' OR gate->>'state' NOT IN ('open','paused')
    OR jsonb_typeof(gate->'state')<>'string' OR jsonb_typeof(gate->'generation')<>'string'
    OR gate->>'generation' !~ '^[0-9a-f]{64}$' OR jsonb_typeof(gate->'changed_at')<>'string'
    OR gate->>'changed_at' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$'
    OR (gate->>'changed_at')::timestamptz IS NULL THEN
  RAISE EXCEPTION 'recovery write gate invalid' USING ERRCODE='55000';
 END IF;
 IF gate->>'state'<>'open' THEN RAISE EXCEPTION 'recovery writes paused' USING ERRCODE='55000'; END IF;
 RETURN NULL;
END $_$;


--
-- Name: classifier_write_guard(); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.classifier_write_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
BEGIN
 -- payload is what the service decodes and doc its jsonb copy: both must hold the same change.
 IF NOT EXISTS (SELECT 1 FROM arkism.current_principal() c WHERE c.role='classifier') THEN
  RETURN COALESCE(NEW, OLD);
 END IF;
 IF TG_OP='UPDATE' AND OLD.collection='memory_candidates' AND OLD.status IN ('candidate','conflicted')
    AND (NEW.collection,NEW.id,NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,NEW.status,NEW.kind)
      = (OLD.collection,OLD.id,OLD.workspace_id,OLD.project_id,OLD.user_id,OLD.agent_id,OLD.status,OLD.kind)
    AND NEW.doc - 'classification' - 'revision' - 'updated_at' = OLD.doc - 'classification' - 'revision' - 'updated_at'
    AND NEW.doc->'classification'->>'by' = (SELECT c.id FROM arkism.current_principal() c)
    AND convert_from(NEW.payload,'UTF8')::jsonb = NEW.doc
    AND NEW.embedding::text IS NOT DISTINCT FROM OLD.embedding::text THEN
  RETURN NEW;
 END IF;
 -- The audit row names this classifier and a waiting candidate in its scope that it classified.
 IF TG_OP='INSERT' AND NEW.collection='audit_events' AND NEW.kind='classify:memory_candidates'
    AND convert_from(NEW.payload,'UTF8')::jsonb = NEW.doc
    AND NEW.doc->>'created_by' = (SELECT c.id FROM arkism.current_principal() c)
    AND EXISTS (SELECT 1 FROM arkism.memory_records m WHERE m.collection='memory_candidates' AND m.id=NEW.doc->'target'->>'id'
      AND (m.workspace_id,m.project_id,m.user_id,m.agent_id)=(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id)
      AND m.status IN ('candidate','conflicted') AND m.doc->'classification'->>'by' = NEW.doc->>'created_by') THEN
  RETURN NEW;
 END IF;
 RAISE EXCEPTION 'classifier may only classify memory candidates' USING ERRCODE='AK403';
END $$;


--
-- Name: current_principal(); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.current_principal() RETURNS TABLE(id text, role text, active boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
 SELECT p.id,p.role,p.active FROM arkism.principals p
 WHERE encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true) LIMIT 1
$$;


--
-- Name: has_any_permission(text); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.has_any_permission(requested_action text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
 SELECT EXISTS (
  SELECT 1 FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
 )
$$;


--
-- Name: has_permission(text, text, text); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.has_permission(org text, project text, requested_action text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
 SELECT EXISTS (
  SELECT 1 FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.organization_id=org AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=org AND m.project_id=project AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=org AND x.project_id=project AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
    AND (org NOT IN ('101-internal','__memory_blobs__','__unparsed__') OR p.role='service')
 )
$$;


--
-- Name: permitted_scopes(text); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.permitted_scopes(requested_action text) RETURNS TABLE(org text, project text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
 SELECT DISTINCT m.organization_id, m.project_id FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
    AND (m.organization_id NOT IN ('101-internal','__memory_blobs__','__unparsed__') OR p.role='service')
$$;


--
-- Name: reserve_candidate_quota(text, text, date, integer); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.reserve_candidate_quota(org text, project text, quota_day date, quota_limit integer) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'arkism'
    AS $$
DECLARE next_used integer;
BEGIN
 IF quota_limit < 1 OR quota_limit > 100000 OR
    (NOT (session_user=current_user AND COALESCE(current_setting('role',true),'none') IN ('none','')) AND NOT arkism.has_permission(org,project,'write:memory_candidates')) THEN
  RAISE EXCEPTION 'candidate quota denied' USING ERRCODE='42501';
 END IF;
 INSERT INTO arkism.candidate_daily_usage(day,used) VALUES(quota_day,1)
 ON CONFLICT(day) DO UPDATE SET used=arkism.candidate_daily_usage.used+1
 WHERE arkism.candidate_daily_usage.used < quota_limit RETURNING used INTO next_used;
	 RETURN COALESCE(next_used,0);
END
$$;


--
-- Name: sync_memory_record_graph(); Type: FUNCTION; Schema: arkism; Owner: -
--

CREATE FUNCTION arkism.sync_memory_record_graph() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'arkism', 'pg_catalog'
    AS $$
DECLARE entity_kind text; target_kind text; evidence_id text; link jsonb;
BEGIN
  -- Human graph assertions are invalidated when their canonical memory changes.
  -- Remove isolated assertion nodes too, so erased identifiers do not linger.
  IF TG_OP IN ('UPDATE','DELETE') AND OLD.collection IN ('memories','sources') AND
    (TG_OP='DELETE' OR NEW.doc->'revision' IS DISTINCT FROM OLD.doc->'revision'
    OR NEW.doc->'content_erased' IS DISTINCT FROM OLD.doc->'content_erased'
    OR NEW.doc->'privacy_quarantined' IS DISTINCT FROM OLD.doc->'privacy_quarantined'
    OR NEW.doc->'status' IS DISTINCT FROM OLD.doc->'status') THEN
    DELETE FROM relations r WHERE r.organization_id=OLD.workspace_id AND r.project_id=OLD.project_id
      AND r.user_id=OLD.user_id AND r.agent_id=OLD.agent_id AND starts_with(r.relation_kind,'asserted_')
      AND ((OLD.collection='memories' AND r.metadata->'evidence'->>'id'=OLD.id)
        OR (OLD.collection='sources' AND EXISTS (SELECT 1 FROM memory_records m
          WHERE m.collection='memories' AND m.workspace_id=OLD.workspace_id AND m.project_id=OLD.project_id
          AND m.user_id=OLD.user_id AND m.agent_id=OLD.agent_id AND m.id=r.metadata->'evidence'->>'id'
          AND m.doc->'sources' @> jsonb_build_array(jsonb_build_object('source_id',OLD.id)))));
    DELETE FROM entities e WHERE e.organization_id=OLD.workspace_id AND e.project_id=OLD.project_id
      AND e.user_id=OLD.user_id AND e.agent_id=OLD.agent_id AND starts_with(e.kind,'asserted_')
      AND NOT EXISTS (SELECT 1 FROM relations r WHERE r.organization_id=e.organization_id AND r.project_id=e.project_id
        AND r.user_id=e.user_id AND r.agent_id=e.agent_id AND ((r.source_kind=e.kind AND r.source_id=e.external_id) OR (r.target_kind=e.kind AND r.target_id=e.external_id)));
  END IF;
	IF TG_OP='DELETE' AND OLD.collection='sources' THEN
	  UPDATE source_events SET payload='null'::jsonb,payload_raw=''::bytea,content_erased=true
	    WHERE organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id
	      AND (id=OLD.id OR id IN (SELECT rel.source_id FROM relations rel WHERE rel.organization_id=OLD.workspace_id AND rel.project_id=OLD.project_id AND rel.user_id=OLD.user_id AND rel.agent_id=OLD.agent_id
	        AND rel.source_kind='source_event' AND rel.target_kind='source_event' AND rel.target_id=OLD.id AND rel.relation_kind='context_uses'));
	  UPDATE execution_traces SET payload_raw=''::bytea,content_erased=true
	    WHERE id IN (SELECT trace_id FROM source_events WHERE content_erased=true AND organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id)
	      AND organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id;
	  DELETE FROM relations rel WHERE rel.organization_id=OLD.workspace_id AND rel.project_id=OLD.project_id AND rel.user_id=OLD.user_id AND rel.agent_id=OLD.agent_id
	    AND ((rel.source_kind='source_event' AND (rel.source_id=OLD.id OR rel.source_id IN (SELECT id FROM source_events WHERE content_erased=true AND organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id)))
	      OR (rel.target_kind='source_event' AND (rel.target_id=OLD.id OR rel.target_id IN (SELECT id FROM source_events WHERE content_erased=true AND organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id)))
	      OR (rel.source_kind='trace' AND rel.source_id IN (SELECT id FROM execution_traces WHERE content_erased=true AND organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id))
	      OR (rel.target_kind='trace' AND rel.target_id IN (SELECT id FROM execution_traces WHERE content_erased=true AND organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id)));
	END IF;
	IF TG_OP='UPDATE' AND NEW.collection='sources'
	  AND COALESCE((NEW.doc->>'content_erased')::boolean,false)
	  AND NOT COALESCE((OLD.doc->>'content_erased')::boolean,false) THEN
	  UPDATE source_events SET payload='null'::jsonb,payload_raw=''::bytea,content_erased=true
	    WHERE organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id
	      AND (id=NEW.id OR id IN (SELECT rel.source_id FROM relations rel WHERE rel.organization_id=NEW.workspace_id AND rel.project_id=NEW.project_id AND rel.user_id=NEW.user_id AND rel.agent_id=NEW.agent_id
	        AND rel.source_kind='source_event' AND rel.target_kind='source_event' AND rel.target_id=NEW.id AND rel.relation_kind='context_uses'));
	  UPDATE execution_traces SET payload_raw=''::bytea,content_erased=true
	    WHERE id IN (SELECT trace_id FROM source_events WHERE content_erased=true AND organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id)
	      AND organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id;
	  DELETE FROM relations rel WHERE rel.organization_id=NEW.workspace_id AND rel.project_id=NEW.project_id AND rel.user_id=NEW.user_id AND rel.agent_id=NEW.agent_id
	    AND ((rel.source_kind='source_event' AND (rel.source_id=NEW.id OR rel.source_id IN (SELECT id FROM source_events WHERE content_erased=true AND organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id)))
	      OR (rel.target_kind='source_event' AND (rel.target_id=NEW.id OR rel.target_id IN (SELECT id FROM source_events WHERE content_erased=true AND organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id)))
	      OR (rel.source_kind='trace' AND rel.source_id IN (SELECT id FROM execution_traces WHERE content_erased=true AND organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id))
	      OR (rel.target_kind='trace' AND rel.target_id IN (SELECT id FROM execution_traces WHERE content_erased=true AND organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id)));
	END IF;
  IF TG_OP='UPDATE' AND NEW.kind IS NOT DISTINCT FROM OLD.kind
    AND NEW.doc->'relations' IS NOT DISTINCT FROM OLD.doc->'relations'
    AND NEW.doc->'supersedes' IS NOT DISTINCT FROM OLD.doc->'supersedes'
    AND NEW.doc->'source_event_id' IS NOT DISTINCT FROM OLD.doc->'source_event_id'
    AND NEW.doc->'trace_id' IS NOT DISTINCT FROM OLD.doc->'trace_id' THEN
    RETURN NEW;
  END IF;
  IF TG_OP='DELETE' THEN
    entity_kind := CASE OLD.collection
      WHEN 'memories' THEN CASE WHEN OLD.kind='lesson' THEN 'lesson' ELSE 'memory' END
      WHEN 'memory_candidates' THEN CASE WHEN OLD.kind='lesson' THEN 'lesson' ELSE 'memory' END
      WHEN 'skills' THEN 'skill' WHEN 'skill_versions' THEN 'skill'
      WHEN 'execution_traces' THEN 'trace' WHEN 'agent_runs' THEN 'agent_run' WHEN 'consultations' THEN 'consultation' WHEN 'agent_handoffs' THEN 'handoff' WHEN 'knowledge_sources' THEN 'knowledge_source' WHEN 'knowledge_documents' THEN 'knowledge_document' WHEN 'knowledge_chunks' THEN 'knowledge_chunk' WHEN 'knowledge_bindings' THEN 'knowledge_binding' WHEN 'retrieval_logs' THEN 'retrieval_log' ELSE NULL END;
    IF entity_kind IS NOT NULL AND NOT EXISTS (
      SELECT 1 FROM memory_records sibling WHERE sibling.id=OLD.id
        AND sibling.workspace_id=OLD.workspace_id AND sibling.project_id=OLD.project_id AND sibling.user_id=OLD.user_id AND sibling.agent_id=OLD.agent_id
        AND CASE sibling.collection
          WHEN 'memories' THEN CASE WHEN sibling.kind='lesson' THEN 'lesson' ELSE 'memory' END
          WHEN 'memory_candidates' THEN CASE WHEN sibling.kind='lesson' THEN 'lesson' ELSE 'memory' END
          WHEN 'skills' THEN 'skill' WHEN 'skill_versions' THEN 'skill'
          WHEN 'execution_traces' THEN 'trace' WHEN 'agent_runs' THEN 'agent_run' WHEN 'consultations' THEN 'consultation' WHEN 'agent_handoffs' THEN 'handoff' WHEN 'knowledge_sources' THEN 'knowledge_source' WHEN 'knowledge_documents' THEN 'knowledge_document' WHEN 'knowledge_chunks' THEN 'knowledge_chunk' WHEN 'knowledge_bindings' THEN 'knowledge_binding' WHEN 'retrieval_logs' THEN 'retrieval_log' ELSE NULL END = entity_kind) THEN
      DELETE FROM entities WHERE organization_id=OLD.workspace_id AND project_id=OLD.project_id AND user_id=OLD.user_id AND agent_id=OLD.agent_id AND kind=entity_kind AND external_id=OLD.id;
    END IF;
    RETURN OLD;
  END IF;
  entity_kind := CASE NEW.collection
    WHEN 'memories' THEN CASE WHEN NEW.kind='lesson' THEN 'lesson' ELSE 'memory' END
    WHEN 'memory_candidates' THEN CASE WHEN NEW.kind='lesson' THEN 'lesson' ELSE 'memory' END
    WHEN 'skills' THEN 'skill' WHEN 'skill_versions' THEN 'skill'
    WHEN 'execution_traces' THEN 'trace' WHEN 'agent_runs' THEN 'agent_run' WHEN 'consultations' THEN 'consultation' WHEN 'agent_handoffs' THEN 'handoff' WHEN 'knowledge_sources' THEN 'knowledge_source' WHEN 'knowledge_documents' THEN 'knowledge_document' WHEN 'knowledge_chunks' THEN 'knowledge_chunk' WHEN 'knowledge_bindings' THEN 'knowledge_binding' WHEN 'retrieval_logs' THEN 'retrieval_log' ELSE NULL END;
  IF entity_kind IS NULL THEN RETURN NEW; END IF;
  INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id)
    VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,entity_kind,NEW.id) ON CONFLICT DO NOTHING;
  DELETE FROM relations WHERE organization_id=NEW.workspace_id AND project_id=NEW.project_id AND user_id=NEW.user_id AND agent_id=NEW.agent_id
    AND source_kind=entity_kind AND source_id=NEW.id AND relation_kind IN ('references','supersedes','evidence_from','derived_from');
  evidence_id := NEW.doc->>'source_event_id';
  IF COALESCE(evidence_id,'')<>'' AND EXISTS (
    SELECT 1 FROM memory_records source WHERE source.collection='sources' AND source.id=evidence_id
      AND source.workspace_id=NEW.workspace_id AND source.project_id=NEW.project_id AND source.user_id=NEW.user_id AND source.agent_id=NEW.agent_id) THEN
    INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id)
      VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,'source_event',evidence_id) ON CONFLICT DO NOTHING;
    INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind)
      VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,entity_kind,NEW.id,'source_event',evidence_id,'evidence_from') ON CONFLICT DO NOTHING;
  END IF;
  evidence_id := NEW.doc->>'trace_id';
  IF COALESCE(evidence_id,'')<>'' AND EXISTS (
    SELECT 1 FROM memory_records trace WHERE trace.collection='execution_traces' AND trace.id=evidence_id
      AND trace.workspace_id=NEW.workspace_id AND trace.project_id=NEW.project_id AND trace.user_id=NEW.user_id AND trace.agent_id=NEW.agent_id) THEN
    INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id)
      VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,'trace',evidence_id) ON CONFLICT DO NOTHING;
    INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind)
      VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,entity_kind,NEW.id,'trace',evidence_id,'derived_from') ON CONFLICT DO NOTHING;
  END IF;
  FOR link IN SELECT value FROM jsonb_array_elements(CASE WHEN jsonb_typeof(NEW.doc->'relations')='array' THEN NEW.doc->'relations' ELSE '[]'::jsonb END) LOOP
    target_kind := NULL;
    SELECT CASE target.collection
      WHEN 'memories' THEN CASE WHEN target.kind='lesson' THEN 'lesson' ELSE 'memory' END
      WHEN 'memory_candidates' THEN CASE WHEN target.kind='lesson' THEN 'lesson' ELSE 'memory' END
      WHEN 'skills' THEN 'skill' WHEN 'skill_versions' THEN 'skill'
      WHEN 'execution_traces' THEN 'trace' WHEN 'agent_runs' THEN 'agent_run' WHEN 'consultations' THEN 'consultation' WHEN 'agent_handoffs' THEN 'handoff' WHEN 'knowledge_sources' THEN 'knowledge_source' WHEN 'knowledge_documents' THEN 'knowledge_document' WHEN 'knowledge_chunks' THEN 'knowledge_chunk' WHEN 'knowledge_bindings' THEN 'knowledge_binding' WHEN 'retrieval_logs' THEN 'retrieval_log' ELSE NULL END
      INTO target_kind FROM memory_records target
      WHERE target.collection=link->>'collection' AND target.id=link->>'id'
        AND target.workspace_id=NEW.workspace_id AND target.project_id=NEW.project_id AND target.user_id=NEW.user_id AND target.agent_id=NEW.agent_id;
    IF target_kind IS NOT NULL AND COALESCE(link->>'id','')<>'' THEN
      INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id)
        VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,target_kind,link->>'id') ON CONFLICT DO NOTHING;
      INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind)
        VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,entity_kind,NEW.id,target_kind,link->>'id','references') ON CONFLICT DO NOTHING;
    END IF;
  END LOOP;
  FOR link IN SELECT value FROM jsonb_array_elements(CASE WHEN jsonb_typeof(NEW.doc->'supersedes')='array' THEN NEW.doc->'supersedes' ELSE '[]'::jsonb END) LOOP
    target_kind := NULL;
    SELECT CASE target.collection
      WHEN 'memories' THEN CASE WHEN target.kind='lesson' THEN 'lesson' ELSE 'memory' END
      WHEN 'memory_candidates' THEN CASE WHEN target.kind='lesson' THEN 'lesson' ELSE 'memory' END
      WHEN 'skills' THEN 'skill' WHEN 'skill_versions' THEN 'skill' ELSE NULL END
      INTO target_kind FROM memory_records target
      WHERE target.collection=link->>'collection' AND target.id=link->>'id'
        AND target.workspace_id=NEW.workspace_id AND target.project_id=NEW.project_id AND target.user_id=NEW.user_id AND target.agent_id=NEW.agent_id;
    IF target_kind IS NOT NULL AND COALESCE(link->>'id','')<>'' THEN
      INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id)
        VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,target_kind,link->>'id') ON CONFLICT DO NOTHING;
      INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind)
        VALUES(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id,entity_kind,NEW.id,target_kind,link->>'id','supersedes') ON CONFLICT DO NOTHING;
    END IF;
  END LOOP;
  RETURN NEW;
END $$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: candidate_daily_usage; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.candidate_daily_usage (
    day date NOT NULL,
    used integer NOT NULL,
    CONSTRAINT candidate_daily_usage_used_check CHECK ((used > 0))
);


--
-- Name: code_edges; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.code_edges (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    repo text NOT NULL,
    from_id text NOT NULL,
    to_id text NOT NULL,
    kind text NOT NULL,
    path text NOT NULL,
    line integer NOT NULL
);

ALTER TABLE ONLY arkism.code_edges FORCE ROW LEVEL SECURITY;


--
-- Name: code_repos; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.code_repos (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    repo text NOT NULL,
    commit_sha text DEFAULT ''::text NOT NULL,
    indexed_at timestamp with time zone,
    updated_at timestamp with time zone NOT NULL,
    revision bigint NOT NULL,
    stats jsonb DEFAULT '{}'::jsonb NOT NULL,
    files jsonb DEFAULT '{}'::jsonb NOT NULL
);

ALTER TABLE ONLY arkism.code_repos FORCE ROW LEVEL SECURITY;


--
-- Name: code_symbols; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.code_symbols (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    repo text NOT NULL,
    id text NOT NULL,
    path text NOT NULL,
    lang text NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    qname text NOT NULL,
    signature text DEFAULT ''::text NOT NULL,
    start_line integer NOT NULL,
    end_line integer NOT NULL,
    exported boolean DEFAULT false NOT NULL,
    doc text DEFAULT ''::text NOT NULL,
    search tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, ((((name || ' '::text) || regexp_replace(name, '([a-z0-9])([A-Z])'::text, '\1 \2'::text, 'g'::text)) || ' '::text) || regexp_replace(qname, '[^A-Za-z0-9_]+'::text, ' '::text, 'g'::text)))) STORED
);

ALTER TABLE ONLY arkism.code_symbols FORCE ROW LEVEL SECURITY;


--
-- Name: entities; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.entities (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    kind text NOT NULL,
    external_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY arkism.entities FORCE ROW LEVEL SECURITY;


--
-- Name: execution_traces; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.execution_traces (
    id text NOT NULL,
    organization_id text NOT NULL,
    project_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    thread_id text NOT NULL,
    agent_run_id text NOT NULL,
    langsmith_run_id text,
    status text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    finished_at timestamp with time zone NOT NULL,
    payload_raw bytea NOT NULL,
    sensitivity text NOT NULL,
    inspection jsonb NOT NULL,
    request_hash text NOT NULL,
    created_by text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    content_erased boolean DEFAULT false NOT NULL,
    privacy_quarantined boolean DEFAULT false NOT NULL,
    CONSTRAINT execution_traces_check CHECK ((finished_at >= started_at)),
    CONSTRAINT execution_traces_status_check CHECK ((status = 'completed'::text))
);

ALTER TABLE ONLY arkism.execution_traces FORCE ROW LEVEL SECURITY;


--
-- Name: memory_records; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.memory_records (
    collection text NOT NULL,
    id text NOT NULL,
    workspace_id text NOT NULL,
    project_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    revision bigint NOT NULL,
    status text DEFAULT ''::text NOT NULL,
    kind text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone,
    run_at timestamp with time zone,
    lease_until timestamp with time zone,
    lease_token text DEFAULT ''::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    payload bytea NOT NULL,
    doc jsonb NOT NULL,
    embedding extensions.vector(1024),
    embedding_space text,
    embedding_dimension integer,
    search_tokens tsvector
);

ALTER TABLE ONLY arkism.memory_records FORCE ROW LEVEL SECURITY;


--
-- Name: memory_storage_metadata; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.memory_storage_metadata (
    key text NOT NULL,
    value bytea NOT NULL
);


--
-- Name: organization_memberships; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.organization_memberships (
    organization_id text NOT NULL,
    principal_id text NOT NULL,
    active boolean DEFAULT true NOT NULL
);


--
-- Name: principal_permissions; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.principal_permissions (
    principal_id text NOT NULL,
    organization_id text NOT NULL,
    project_id text NOT NULL,
    action text NOT NULL,
    allowed boolean DEFAULT true NOT NULL
);


--
-- Name: principals; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.principals (
    id text NOT NULL,
    role text NOT NULL,
    token_sha256 bytea NOT NULL,
    active boolean DEFAULT true NOT NULL,
    CONSTRAINT principals_role_check CHECK ((role = ANY (ARRAY['human'::text, 'agent'::text, 'service'::text, 'pipeline'::text, 'classifier'::text]))),
    CONSTRAINT principals_token_sha256_check CHECK ((octet_length(token_sha256) = 32))
);


--
-- Name: project_memberships; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.project_memberships (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    principal_id text NOT NULL,
    active boolean DEFAULT true NOT NULL
);


--
-- Name: relations; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.relations (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    source_kind text NOT NULL,
    source_id text NOT NULL,
    target_kind text NOT NULL,
    target_id text NOT NULL,
    relation_kind text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY arkism.relations FORCE ROW LEVEL SECURITY;


--
-- Name: source_events; Type: TABLE; Schema: arkism; Owner: -
--

CREATE TABLE arkism.source_events (
    id text NOT NULL,
    trace_id text NOT NULL,
    organization_id text NOT NULL,
    project_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    kind text NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    payload jsonb NOT NULL,
    payload_raw bytea NOT NULL,
    sensitivity text NOT NULL,
    inspection jsonb NOT NULL,
    request_hash text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    content_erased boolean DEFAULT false NOT NULL,
    privacy_quarantined boolean DEFAULT false NOT NULL
);

ALTER TABLE ONLY arkism.source_events FORCE ROW LEVEL SECURITY;


--
-- Name: candidate_daily_usage candidate_daily_usage_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.candidate_daily_usage
    ADD CONSTRAINT candidate_daily_usage_pkey PRIMARY KEY (day);


--
-- Name: code_edges code_edges_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.code_edges
    ADD CONSTRAINT code_edges_pkey PRIMARY KEY (organization_id, project_id, repo, from_id, to_id, kind);


--
-- Name: code_repos code_repos_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.code_repos
    ADD CONSTRAINT code_repos_pkey PRIMARY KEY (organization_id, project_id, repo);


--
-- Name: code_symbols code_symbols_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.code_symbols
    ADD CONSTRAINT code_symbols_pkey PRIMARY KEY (organization_id, project_id, repo, id);


--
-- Name: entities entities_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.entities
    ADD CONSTRAINT entities_pkey PRIMARY KEY (organization_id, project_id, user_id, agent_id, kind, external_id);


--
-- Name: execution_traces execution_traces_agent_run_id_key; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.execution_traces
    ADD CONSTRAINT execution_traces_agent_run_id_key UNIQUE (agent_run_id);


--
-- Name: execution_traces execution_traces_organization_id_project_id_user_id_agent_i_key; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.execution_traces
    ADD CONSTRAINT execution_traces_organization_id_project_id_user_id_agent_i_key UNIQUE (organization_id, project_id, user_id, agent_id, id);


--
-- Name: execution_traces execution_traces_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.execution_traces
    ADD CONSTRAINT execution_traces_pkey PRIMARY KEY (id);


--
-- Name: memory_records memory_records_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.memory_records
    ADD CONSTRAINT memory_records_pkey PRIMARY KEY (collection, id);


--
-- Name: memory_storage_metadata memory_storage_metadata_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.memory_storage_metadata
    ADD CONSTRAINT memory_storage_metadata_pkey PRIMARY KEY (key);


--
-- Name: organization_memberships organization_memberships_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.organization_memberships
    ADD CONSTRAINT organization_memberships_pkey PRIMARY KEY (organization_id, principal_id);


--
-- Name: principal_permissions principal_permissions_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.principal_permissions
    ADD CONSTRAINT principal_permissions_pkey PRIMARY KEY (principal_id, organization_id, project_id, action);


--
-- Name: principals principals_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.principals
    ADD CONSTRAINT principals_pkey PRIMARY KEY (id);


--
-- Name: principals principals_token_sha256_key; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.principals
    ADD CONSTRAINT principals_token_sha256_key UNIQUE (token_sha256);


--
-- Name: project_memberships project_memberships_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.project_memberships
    ADD CONSTRAINT project_memberships_pkey PRIMARY KEY (organization_id, project_id, principal_id);


--
-- Name: relations relations_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.relations
    ADD CONSTRAINT relations_pkey PRIMARY KEY (organization_id, project_id, user_id, agent_id, source_kind, source_id, target_kind, target_id, relation_kind);


--
-- Name: source_events source_events_pkey; Type: CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.source_events
    ADD CONSTRAINT source_events_pkey PRIMARY KEY (id);


--
-- Name: code_edges_path; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX code_edges_path ON arkism.code_edges USING btree (organization_id, project_id, repo, path);


--
-- Name: code_edges_to; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX code_edges_to ON arkism.code_edges USING btree (organization_id, project_id, repo, to_id, kind);


--
-- Name: code_symbols_name; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX code_symbols_name ON arkism.code_symbols USING btree (organization_id, project_id, lower(name));


--
-- Name: code_symbols_path; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX code_symbols_path ON arkism.code_symbols USING btree (organization_id, project_id, repo, path);


--
-- Name: code_symbols_qname; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX code_symbols_qname ON arkism.code_symbols USING btree (organization_id, project_id, qname);


--
-- Name: code_symbols_search; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX code_symbols_search ON arkism.code_symbols USING gin (search);


--
-- Name: execution_traces_langsmith; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX execution_traces_langsmith ON arkism.execution_traces USING btree (organization_id, project_id, langsmith_run_id) WHERE (langsmith_run_id IS NOT NULL);


--
-- Name: execution_traces_privacy; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX execution_traces_privacy ON arkism.execution_traces USING btree (organization_id, project_id, user_id, agent_id, id) WHERE privacy_quarantined;


--
-- Name: execution_traces_thread; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX execution_traces_thread ON arkism.execution_traces USING btree (organization_id, project_id, user_id, agent_id, thread_id, finished_at DESC);


--
-- Name: memory_records_document; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_document ON arkism.memory_records USING gin (doc jsonb_path_ops);


--
-- Name: memory_records_fts; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_fts ON arkism.memory_records USING gin (to_tsvector('simple'::regconfig, ((((COALESCE((doc ->> 'text'::text), ''::text) || ' '::text) || COALESCE((doc ->> 'summary'::text), ''::text)) || ' '::text) || COALESCE(((doc -> 'keywords'::text))::text, ''::text))));


--
-- Name: memory_records_jobs; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_jobs ON arkism.memory_records USING btree (collection, status, run_at, lease_until);


--
-- Name: memory_records_keywords; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_keywords ON arkism.memory_records USING gin (((doc -> 'keywords_lower'::text)));


--
-- Name: memory_records_scope; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_scope ON arkism.memory_records USING btree (collection, workspace_id, project_id, user_id, agent_id);


--
-- Name: memory_records_scope_id; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_scope_id ON arkism.memory_records USING btree (collection, workspace_id, project_id, user_id, agent_id, id);


--
-- Name: memory_records_scope_state; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_scope_state ON arkism.memory_records USING btree (collection, workspace_id, project_id, user_id, agent_id, status, updated_at DESC);


--
-- Name: memory_records_search_tokens; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_search_tokens ON arkism.memory_records USING gin (search_tokens);


--
-- Name: memory_records_search_tokens_pending; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX memory_records_search_tokens_pending ON arkism.memory_records USING btree (collection, id) WHERE ((search_tokens IS NULL) AND (collection = ANY (ARRAY['memories'::text, 'skill_versions'::text, 'reflections'::text])));


--
-- Name: relations_target; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX relations_target ON arkism.relations USING btree (organization_id, project_id, user_id, agent_id, target_kind, target_id, relation_kind);


--
-- Name: source_events_privacy; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX source_events_privacy ON arkism.source_events USING btree (organization_id, project_id, user_id, agent_id, id) WHERE privacy_quarantined;


--
-- Name: source_events_trace; Type: INDEX; Schema: arkism; Owner: -
--

CREATE INDEX source_events_trace ON arkism.source_events USING btree (organization_id, project_id, user_id, agent_id, trace_id, occurred_at, id);


--
-- Name: memory_records memory_records_classifier_guard; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER memory_records_classifier_guard BEFORE INSERT OR DELETE OR UPDATE ON arkism.memory_records FOR EACH ROW EXECUTE FUNCTION arkism.classifier_write_guard();


--
-- Name: memory_records memory_records_graph_sync; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER memory_records_graph_sync AFTER INSERT OR DELETE OR UPDATE ON arkism.memory_records FOR EACH ROW EXECUTE FUNCTION arkism.sync_memory_record_graph();


--
-- Name: candidate_daily_usage recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.candidate_daily_usage FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: entities recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.entities FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: execution_traces recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.execution_traces FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: memory_records recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.memory_records FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: organization_memberships recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.organization_memberships FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: principal_permissions recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.principal_permissions FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: principals recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.principals FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: project_memberships recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.project_memberships FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: relations recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.relations FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: source_events recovery_write_gate; Type: TRIGGER; Schema: arkism; Owner: -
--

CREATE TRIGGER recovery_write_gate BEFORE INSERT OR DELETE OR UPDATE OR TRUNCATE ON arkism.source_events FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();


--
-- Name: code_edges code_edges_organization_id_project_id_repo_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.code_edges
    ADD CONSTRAINT code_edges_organization_id_project_id_repo_fkey FOREIGN KEY (organization_id, project_id, repo) REFERENCES arkism.code_repos(organization_id, project_id, repo) ON DELETE CASCADE;


--
-- Name: code_symbols code_symbols_organization_id_project_id_repo_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.code_symbols
    ADD CONSTRAINT code_symbols_organization_id_project_id_repo_fkey FOREIGN KEY (organization_id, project_id, repo) REFERENCES arkism.code_repos(organization_id, project_id, repo) ON DELETE CASCADE;


--
-- Name: organization_memberships organization_memberships_principal_id_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.organization_memberships
    ADD CONSTRAINT organization_memberships_principal_id_fkey FOREIGN KEY (principal_id) REFERENCES arkism.principals(id) ON DELETE CASCADE;


--
-- Name: principal_permissions principal_permissions_organization_id_project_id_principal_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.principal_permissions
    ADD CONSTRAINT principal_permissions_organization_id_project_id_principal_fkey FOREIGN KEY (organization_id, project_id, principal_id) REFERENCES arkism.project_memberships(organization_id, project_id, principal_id) ON DELETE CASCADE;


--
-- Name: principal_permissions principal_permissions_principal_id_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.principal_permissions
    ADD CONSTRAINT principal_permissions_principal_id_fkey FOREIGN KEY (principal_id) REFERENCES arkism.principals(id) ON DELETE CASCADE;


--
-- Name: project_memberships project_memberships_organization_id_principal_id_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.project_memberships
    ADD CONSTRAINT project_memberships_organization_id_principal_id_fkey FOREIGN KEY (organization_id, principal_id) REFERENCES arkism.organization_memberships(organization_id, principal_id) ON DELETE CASCADE;


--
-- Name: project_memberships project_memberships_principal_id_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.project_memberships
    ADD CONSTRAINT project_memberships_principal_id_fkey FOREIGN KEY (principal_id) REFERENCES arkism.principals(id) ON DELETE CASCADE;


--
-- Name: relations relations_organization_id_project_id_user_id_agent_id_sour_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.relations
    ADD CONSTRAINT relations_organization_id_project_id_user_id_agent_id_sour_fkey FOREIGN KEY (organization_id, project_id, user_id, agent_id, source_kind, source_id) REFERENCES arkism.entities(organization_id, project_id, user_id, agent_id, kind, external_id) ON DELETE CASCADE;


--
-- Name: relations relations_organization_id_project_id_user_id_agent_id_targ_fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.relations
    ADD CONSTRAINT relations_organization_id_project_id_user_id_agent_id_targ_fkey FOREIGN KEY (organization_id, project_id, user_id, agent_id, target_kind, target_id) REFERENCES arkism.entities(organization_id, project_id, user_id, agent_id, kind, external_id) ON DELETE CASCADE;


--
-- Name: source_events source_events_organization_id_project_id_user_id_agent_id__fkey; Type: FK CONSTRAINT; Schema: arkism; Owner: -
--

ALTER TABLE ONLY arkism.source_events
    ADD CONSTRAINT source_events_organization_id_project_id_user_id_agent_id__fkey FOREIGN KEY (organization_id, project_id, user_id, agent_id, trace_id) REFERENCES arkism.execution_traces(organization_id, project_id, user_id, agent_id, id) ON DELETE CASCADE;


--
-- Name: code_edges arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.code_edges FOR DELETE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_repos arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.code_repos FOR DELETE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_symbols arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.code_symbols FOR DELETE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: entities arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.entities FOR DELETE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: execution_traces arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.execution_traces FOR DELETE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: memory_records arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.memory_records FOR DELETE USING ((((workspace_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((workspace_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: relations arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.relations FOR DELETE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: source_events arkism_delete; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_delete ON arkism.source_events FOR DELETE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_edges arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.code_edges USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: code_repos arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.code_repos USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: code_symbols arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.code_symbols USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: entities arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.entities USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: execution_traces arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.execution_traces USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: memory_records arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.memory_records USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: relations arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.relations USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: source_events arkism_init_owner; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_init_owner ON arkism.source_events USING ((CURRENT_USER = 'arkism_init'::name)) WITH CHECK ((CURRENT_USER = 'arkism_init'::name));


--
-- Name: code_edges arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.code_edges FOR INSERT WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_repos arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.code_repos FOR INSERT WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_symbols arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.code_symbols FOR INSERT WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: entities arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.entities FOR INSERT WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: execution_traces arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.execution_traces FOR INSERT WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: memory_records arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.memory_records FOR INSERT WITH CHECK ((((workspace_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((workspace_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: relations arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.relations FOR INSERT WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: source_events arkism_insert; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_insert ON arkism.source_events FOR INSERT WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_edges arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.code_edges FOR SELECT USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_repos arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.code_repos FOR SELECT USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_symbols arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.code_symbols FOR SELECT USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: entities arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.entities FOR SELECT USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: execution_traces arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.execution_traces FOR SELECT USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: memory_records arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.memory_records FOR SELECT USING ((((workspace_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((workspace_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: relations arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.relations FOR SELECT USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: source_events arkism_read; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_read ON arkism.source_events FOR SELECT USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:read'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_edges arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.code_edges FOR UPDATE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_repos arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.code_repos FOR UPDATE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_symbols arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.code_symbols FOR UPDATE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: entities arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.entities FOR UPDATE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: execution_traces arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.execution_traces FOR UPDATE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: memory_records arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.memory_records FOR UPDATE USING ((((workspace_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((workspace_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((workspace_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((workspace_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: relations arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.relations FOR UPDATE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: source_events arkism_update; Type: POLICY; Schema: arkism; Owner: -
--

CREATE POLICY arkism_update ON arkism.source_events FOR UPDATE USING ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission)))) WITH CHECK ((((organization_id, project_id) IN ( SELECT permitted_scopes.org,
    permitted_scopes.project
   FROM arkism.permitted_scopes('db:write'::text) permitted_scopes(org, project))) OR ((organization_id = '__memory_blobs__'::text) AND (project_id = '__memory_blobs__'::text) AND ( SELECT arkism.has_any_permission('storage:internal'::text) AS has_any_permission))));


--
-- Name: code_edges; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.code_edges ENABLE ROW LEVEL SECURITY;

--
-- Name: code_repos; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.code_repos ENABLE ROW LEVEL SECURITY;

--
-- Name: code_symbols; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.code_symbols ENABLE ROW LEVEL SECURITY;

--
-- Name: entities; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.entities ENABLE ROW LEVEL SECURITY;

--
-- Name: execution_traces; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.execution_traces ENABLE ROW LEVEL SECURITY;

--
-- Name: memory_records; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.memory_records ENABLE ROW LEVEL SECURITY;

--
-- Name: relations; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.relations ENABLE ROW LEVEL SECURITY;

--
-- Name: source_events; Type: ROW SECURITY; Schema: arkism; Owner: -
--

ALTER TABLE arkism.source_events ENABLE ROW LEVEL SECURITY;

--
-- Name: FUNCTION artifact_cleanup_pending(org text, project text, subject_id text, agent text, job_id text, blob_digest text); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.artifact_cleanup_pending(org text, project text, subject_id text, agent text, job_id text, blob_digest text) FROM PUBLIC;


--
-- Name: FUNCTION artifact_gc_held(blob_digest text); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.artifact_gc_held(blob_digest text) FROM PUBLIC;


--
-- Name: FUNCTION authorize_principal(org text, project text, requested_action text); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.authorize_principal(org text, project text, requested_action text) FROM PUBLIC;


--
-- Name: FUNCTION check_recovery_write_gate(); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.check_recovery_write_gate() FROM PUBLIC;


--
-- Name: FUNCTION current_principal(); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.current_principal() FROM PUBLIC;


--
-- Name: FUNCTION has_any_permission(requested_action text); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.has_any_permission(requested_action text) FROM PUBLIC;


--
-- Name: FUNCTION has_permission(org text, project text, requested_action text); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.has_permission(org text, project text, requested_action text) FROM PUBLIC;


--
-- Name: FUNCTION permitted_scopes(requested_action text); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.permitted_scopes(requested_action text) FROM PUBLIC;


--
-- Name: FUNCTION reserve_candidate_quota(org text, project text, quota_day date, quota_limit integer); Type: ACL; Schema: arkism; Owner: -
--

REVOKE ALL ON FUNCTION arkism.reserve_candidate_quota(org text, project text, quota_day date, quota_limit integer) FROM PUBLIC;


--
-- PostgreSQL database dump complete
--

-- Restore the application connection defaults after the dump preamble.
SET search_path = arkism, extensions, pg_catalog;
SET row_security = on;
SET check_function_bodies = on;
