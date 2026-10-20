-- The memory reviewer (classifier role) reads the waiting list it classifies (ADR-0022); the service audits
-- that read, so the guard also lets a classifier record its own review:list event. Its other writes stay refused.
CREATE OR REPLACE FUNCTION classifier_write_guard() RETURNS trigger
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
 -- The audit row of its own waiting-list read, naming no record.
 IF TG_OP='INSERT' AND NEW.collection='audit_events' AND NEW.kind='review:list'
    AND convert_from(NEW.payload,'UTF8')::jsonb = NEW.doc
    AND NEW.doc->>'created_by' = (SELECT c.id FROM arkism.current_principal() c)
    AND COALESCE(NEW.doc->'target'->>'id','') = '' THEN
  RETURN NEW;
 END IF;
 RAISE EXCEPTION 'classifier may only classify memory candidates' USING ERRCODE='AK403';
END $$;
