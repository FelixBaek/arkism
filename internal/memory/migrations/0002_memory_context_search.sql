-- Mark existing indexes for bounded refresh without updating records while the
-- recovery write gate is paused. New writes already include context qualifiers.
ALTER TABLE arkism.memory_records ADD COLUMN search_context_indexed boolean NOT NULL DEFAULT false;
ALTER TABLE arkism.memory_records ALTER COLUMN search_context_indexed SET DEFAULT true;
CREATE INDEX memory_records_context_tokens_pending ON arkism.memory_records(collection,id)
WHERE collection IN ('memories','skill_versions','reflections') AND NOT search_context_indexed
AND (COALESCE(doc->'memory_context'->>'applicability','') <> ''
  OR COALESCE(doc->'memory_context'->>'uncertainty','') <> ''
  OR COALESCE(doc->'memory_context'->>'valid_from','') <> '');
CREATE INDEX memory_records_context_fts ON arkism.memory_records USING gin
(to_tsvector('simple', COALESCE(doc->'memory_context'->>'applicability','') || ' ' || COALESCE(doc->'memory_context'->>'uncertainty','') || ' ' || COALESCE(doc->'memory_context'->>'valid_from','')));
