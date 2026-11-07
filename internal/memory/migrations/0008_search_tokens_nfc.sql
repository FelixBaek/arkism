-- Existing tokens require a resumable NFC backfill; fresh writes use version two.
ALTER TABLE memory_records ADD COLUMN search_tokens_version smallint NOT NULL DEFAULT 1;
ALTER TABLE memory_records ALTER COLUMN search_tokens_version SET DEFAULT 2;
CREATE INDEX memory_records_token_version_pending
ON memory_records(collection,id,workspace_id,project_id,user_id,agent_id)
WHERE collection IN ('memories','skill_versions','reflections','observations','memory_candidates','sources','knowledge_chunks')
AND search_tokens_version < 2;
