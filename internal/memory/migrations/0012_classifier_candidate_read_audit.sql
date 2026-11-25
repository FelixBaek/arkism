CREATE OR REPLACE FUNCTION arkism.classifier_write_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
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
 -- A direct read of a pending candidate is committed together with its allow audit.
 -- Keep the read grant, reviewer identity and target scope bound to that same row.
 IF TG_OP='INSERT' AND NEW.collection='audit_events' AND NEW.kind='read:memory_candidates'
    AND NEW.status='active' AND NEW.doc->>'status'=NEW.status AND NEW.doc->>'kind'=NEW.kind
    AND NEW.doc->>'_id'=NEW.id AND NEW.doc->>'text'='' AND NEW.doc->>'summary'=''
    AND NEW.doc->'scope'->>'workspace_id'=NEW.workspace_id AND NEW.doc->'scope'->>'project_id'=NEW.project_id
    AND COALESCE(NEW.doc->'scope'->>'user_id','')=NEW.user_id AND COALESCE(NEW.doc->'scope'->>'agent_id','')=NEW.agent_id
    AND NEW.doc->'revision'='1'::jsonb AND NEW.doc->'audit'->>'decision'='allow'
    AND convert_from(NEW.payload,'UTF8')::jsonb=NEW.doc
    AND NEW.doc->>'created_by'=(SELECT c.id FROM arkism.current_principal() c)
    AND arkism.has_permission(NEW.workspace_id, NEW.project_id, 'read:memory_candidates')
    AND EXISTS (SELECT 1 FROM arkism.memory_records m WHERE m.collection='memory_candidates'
      AND m.id=NEW.doc->'target'->>'id'
      AND (m.workspace_id,m.project_id,m.user_id,m.agent_id)=(NEW.workspace_id,NEW.project_id,NEW.user_id,NEW.agent_id)
      AND m.status IN ('candidate','conflicted')
      AND NOT (m.doc @> '{"content_erased":true}' OR m.doc @> '{"privacy_quarantined":true}')) THEN
  RETURN NEW;
 END IF;
 RAISE EXCEPTION 'classifier may only classify memory candidates' USING ERRCODE='AK403';
END $$;
