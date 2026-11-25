-- Legacy active memories with expiry can be located by scope without scanning
-- permanent memories or sorting every record in a database.
CREATE INDEX memory_records_dream_expiry_memories
ON arkism.memory_records (workspace_id,project_id,user_id,agent_id)
WHERE collection='memories' AND status='active' AND doc->'expires_at' IS NOT NULL;

CREATE INDEX memory_records_dream_expiry_jobs
ON arkism.memory_records (workspace_id,project_id,user_id,agent_id)
WHERE collection='jobs' AND kind='dream_expiry';
