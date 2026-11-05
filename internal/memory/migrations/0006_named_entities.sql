-- Named entities: rows of kind 'named_entity' keep name, type and aliases in metadata; other kinds
-- leave it empty. Mentions are relations memory|lesson -mentions-> named_entity.
ALTER TABLE arkism.entities ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

-- An erased, quarantined or deleted memory takes its mention edges along, and an entity no other
-- memory still names goes too, so its name does not outlive the text it came from. Tombstones of
-- entities a person deleted hold only hashed name keys and stay.
CREATE FUNCTION arkism.drop_withheld_mentions() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
BEGIN
 DELETE FROM arkism.relations r WHERE r.organization_id=OLD.workspace_id AND r.project_id=OLD.project_id
  AND r.user_id=OLD.user_id AND r.agent_id=OLD.agent_id AND r.source_kind IN ('memory','lesson') AND r.source_id=OLD.id
  AND r.target_kind='named_entity' AND r.relation_kind IN ('mentions','mentions_proposed');
 DELETE FROM arkism.entities e WHERE e.organization_id=OLD.workspace_id AND e.project_id=OLD.project_id
  AND e.user_id=OLD.user_id AND e.agent_id=OLD.agent_id AND e.kind='named_entity'
  AND NOT COALESCE((e.metadata->>'deleted')::boolean,false)
  AND NOT EXISTS (SELECT 1 FROM arkism.relations r WHERE r.organization_id=e.organization_id AND r.project_id=e.project_id
   AND r.user_id=e.user_id AND r.agent_id=e.agent_id AND r.target_kind='named_entity' AND r.target_id=e.external_id);
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION arkism.drop_withheld_mentions() FROM PUBLIC;
CREATE TRIGGER drop_withheld_mentions_on_delete AFTER DELETE ON arkism.memory_records FOR EACH ROW
 WHEN (OLD.collection='memories')
 EXECUTE FUNCTION arkism.drop_withheld_mentions();
CREATE TRIGGER drop_withheld_mentions_on_update AFTER UPDATE ON arkism.memory_records FOR EACH ROW
 WHEN (OLD.collection='memories' AND (NEW.doc->'content_erased' IS DISTINCT FROM OLD.doc->'content_erased'
  OR NEW.doc->'privacy_quarantined' IS DISTINCT FROM OLD.doc->'privacy_quarantined'))
 EXECUTE FUNCTION arkism.drop_withheld_mentions();
