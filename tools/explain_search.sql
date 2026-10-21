-- Run with the dedicated application role in a PRIVATE operator session.
-- Pass token_hash (SHA-256 of its bearer), workspace/project/user/agent with
-- psql variables. No query text or live result contents should be published.
-- A synthetic lexical search avoids disclosing real search terms in the plan.
BEGIN READ ONLY;
SET LOCAL statement_timeout='10s';
SELECT set_config('arkism.token_hash', :'token_hash', true) IS NOT NULL AS token_bound;
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
SELECT id FROM arkism.memory_records
 WHERE collection='memories' AND workspace_id=:'workspace' AND project_id=:'project'
 AND user_id=:'user' AND agent_id=:'agent' AND status='active'
 AND doc->'keywords_lower' ?| ARRAY['synthetic-plan-probe']::text[]
 ORDER BY updated_at DESC,id LIMIT 20;
ROLLBACK;
