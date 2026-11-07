-- Locate one captured conversation before ordering its adjacent batches.
-- Hash keys keep large imported source identifiers out of index tuples;
-- the query also compares the complete identifiers to reject hash collisions.
CREATE INDEX IF NOT EXISTS memory_records_conversation_neighbors
ON memory_records (workspace_id, project_id, user_id, agent_id,
 (md5(doc->>'source_ref')), (md5(doc->>'origin_product')))
WHERE collection='sources' AND kind='conversation';
