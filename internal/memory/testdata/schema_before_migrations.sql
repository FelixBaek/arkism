-- Historical unversioned schema fixture, captured independently before migration changes.
-- Frozen baseline captured from public commit 0262eb19e77eee070b77f6bccada94a227375bfa.
-- Append a new numbered migration; never edit an applied baseline.
-- $1: fresh 32-byte identity; $2: fresh recovery gate JSON. Existing values survive.

CREATE SCHEMA IF NOT EXISTS arkism; REVOKE ALL ON SCHEMA arkism FROM PUBLIC; CREATE SCHEMA IF NOT EXISTS extensions; CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA extensions;

CREATE TABLE IF NOT EXISTS memory_records (
collection text NOT NULL, id text NOT NULL, workspace_id text NOT NULL, project_id text NOT NULL,
user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '', revision bigint NOT NULL,
status text NOT NULL DEFAULT '', kind text NOT NULL DEFAULT '', updated_at timestamptz,
run_at timestamptz, lease_until timestamptz, lease_token text NOT NULL DEFAULT '', attempts integer NOT NULL DEFAULT 0,
	payload bytea NOT NULL, doc jsonb NOT NULL, embedding vector(1024), embedding_space text, embedding_dimension integer,
PRIMARY KEY (collection,id));

CREATE INDEX IF NOT EXISTS memory_records_scope ON memory_records (collection,workspace_id,project_id,user_id,agent_id);

ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding vector(1024); ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_space text; ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_dimension integer;

CREATE INDEX IF NOT EXISTS memory_records_jobs ON memory_records (collection,status,run_at,lease_until);

CREATE INDEX IF NOT EXISTS memory_records_scope_id ON memory_records(collection,workspace_id,project_id,user_id,agent_id,id);

CREATE INDEX IF NOT EXISTS memory_records_scope_state ON memory_records(collection,workspace_id,project_id,user_id,agent_id,status,updated_at DESC);

CREATE INDEX IF NOT EXISTS memory_records_keywords ON memory_records USING gin ((doc->'keywords_lower'));

CREATE INDEX IF NOT EXISTS memory_records_document ON memory_records USING gin (doc jsonb_path_ops);

CREATE INDEX IF NOT EXISTS memory_records_fts ON memory_records USING gin (to_tsvector('simple', COALESCE(doc->>'text','') || ' ' || COALESCE(doc->>'summary','') || ' ' || COALESCE((doc->'keywords')::text,'')));

ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS search_tokens tsvector;

CREATE INDEX IF NOT EXISTS memory_records_search_tokens ON memory_records USING gin (search_tokens);

CREATE INDEX IF NOT EXISTS memory_records_search_tokens_pending ON memory_records (collection,id) WHERE search_tokens IS NULL AND collection IN ('memories','skill_versions','reflections');

CREATE TABLE IF NOT EXISTS memory_storage_metadata (key text PRIMARY KEY, value bytea NOT NULL);

REVOKE ALL ON ALL TABLES IN SCHEMA arkism FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
  FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      EXECUTE format('REVOKE ALL ON SCHEMA arkism FROM %I', role_name);
      EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA arkism FROM %I', role_name);
    END IF;
  END LOOP;
END $$;

INSERT INTO memory_storage_metadata(key,value) VALUES('storage_identity',$1) ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS execution_traces (
id text PRIMARY KEY, organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
thread_id text NOT NULL, agent_run_id text NOT NULL UNIQUE, langsmith_run_id text, status text NOT NULL CHECK (status='completed'),
started_at timestamptz NOT NULL, finished_at timestamptz NOT NULL, payload_raw bytea NOT NULL, sensitivity text NOT NULL,
inspection jsonb NOT NULL, request_hash text NOT NULL, created_by text NOT NULL, created_at timestamptz NOT NULL, content_erased boolean NOT NULL DEFAULT false,
UNIQUE (organization_id,project_id,user_id,agent_id,id), CHECK (finished_at>=started_at));

CREATE INDEX IF NOT EXISTS execution_traces_thread ON execution_traces(organization_id,project_id,user_id,agent_id,thread_id,finished_at DESC);

CREATE INDEX IF NOT EXISTS execution_traces_langsmith ON execution_traces(organization_id,project_id,langsmith_run_id) WHERE langsmith_run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS source_events (
id text PRIMARY KEY, trace_id text NOT NULL, organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
kind text NOT NULL, occurred_at timestamptz NOT NULL, payload jsonb NOT NULL, payload_raw bytea NOT NULL, sensitivity text NOT NULL,
inspection jsonb NOT NULL, request_hash text NOT NULL, created_at timestamptz NOT NULL,
content_erased boolean NOT NULL DEFAULT false,
FOREIGN KEY (organization_id,project_id,user_id,agent_id,trace_id) REFERENCES execution_traces(organization_id,project_id,user_id,agent_id,id) ON DELETE CASCADE);

ALTER TABLE execution_traces ADD COLUMN IF NOT EXISTS content_erased boolean NOT NULL DEFAULT false;

ALTER TABLE source_events ADD COLUMN IF NOT EXISTS content_erased boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS source_events_trace ON source_events(organization_id,project_id,user_id,agent_id,trace_id,occurred_at,id);

CREATE TABLE IF NOT EXISTS entities (
organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
kind text NOT NULL, external_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
PRIMARY KEY (organization_id,project_id,user_id,agent_id,kind,external_id));

CREATE TABLE IF NOT EXISTS relations (
organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
source_kind text NOT NULL, source_id text NOT NULL, target_kind text NOT NULL, target_id text NOT NULL, relation_kind text NOT NULL,
metadata jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(),
PRIMARY KEY (organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind),
FOREIGN KEY (organization_id,project_id,user_id,agent_id,source_kind,source_id) REFERENCES entities(organization_id,project_id,user_id,agent_id,kind,external_id) ON DELETE CASCADE,
FOREIGN KEY (organization_id,project_id,user_id,agent_id,target_kind,target_id) REFERENCES entities(organization_id,project_id,user_id,agent_id,kind,external_id) ON DELETE CASCADE);

CREATE INDEX IF NOT EXISTS relations_target ON relations(organization_id,project_id,user_id,agent_id,target_kind,target_id,relation_kind);

CREATE OR REPLACE FUNCTION sync_memory_record_graph() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=arkism,pg_catalog AS $$
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

DROP TRIGGER IF EXISTS memory_records_graph_sync ON memory_records;

CREATE TRIGGER memory_records_graph_sync AFTER INSERT OR UPDATE OR DELETE ON memory_records FOR EACH ROW EXECUTE FUNCTION sync_memory_record_graph();

ALTER TABLE source_events ADD COLUMN IF NOT EXISTS privacy_quarantined boolean NOT NULL DEFAULT false;
ALTER TABLE execution_traces ADD COLUMN IF NOT EXISTS privacy_quarantined boolean NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS source_events_privacy ON source_events(organization_id,project_id,user_id,agent_id,id) WHERE privacy_quarantined;
CREATE INDEX IF NOT EXISTS execution_traces_privacy ON execution_traces(organization_id,project_id,user_id,agent_id,id) WHERE privacy_quarantined;

CREATE TABLE IF NOT EXISTS code_repos (
organization_id text NOT NULL, project_id text NOT NULL, repo text NOT NULL,
commit_sha text NOT NULL DEFAULT '', indexed_at timestamptz, updated_at timestamptz NOT NULL, revision bigint NOT NULL,
stats jsonb NOT NULL DEFAULT '{}'::jsonb, files jsonb NOT NULL DEFAULT '{}'::jsonb,
PRIMARY KEY (organization_id,project_id,repo));

CREATE TABLE IF NOT EXISTS code_symbols (
organization_id text NOT NULL, project_id text NOT NULL, repo text NOT NULL, id text NOT NULL,
path text NOT NULL, lang text NOT NULL, kind text NOT NULL, name text NOT NULL, qname text NOT NULL,
signature text NOT NULL DEFAULT '', start_line integer NOT NULL, end_line integer NOT NULL, exported boolean NOT NULL DEFAULT false, doc text NOT NULL DEFAULT '',
search tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig,
  name || ' ' || regexp_replace(name, '([a-z0-9])([A-Z])', '\1 \2', 'g') || ' ' || regexp_replace(qname, '[^A-Za-z0-9_]+', ' ', 'g'))) STORED,
PRIMARY KEY (organization_id,project_id,repo,id),
FOREIGN KEY (organization_id,project_id,repo) REFERENCES code_repos(organization_id,project_id,repo) ON DELETE CASCADE);

CREATE INDEX IF NOT EXISTS code_symbols_path ON code_symbols(organization_id,project_id,repo,path);

CREATE INDEX IF NOT EXISTS code_symbols_name ON code_symbols(organization_id,project_id,lower(name));

CREATE INDEX IF NOT EXISTS code_symbols_qname ON code_symbols(organization_id,project_id,qname);

CREATE INDEX IF NOT EXISTS code_symbols_search ON code_symbols USING gin (search);

CREATE TABLE IF NOT EXISTS code_edges (
organization_id text NOT NULL, project_id text NOT NULL, repo text NOT NULL,
from_id text NOT NULL, to_id text NOT NULL, kind text NOT NULL, path text NOT NULL, line integer NOT NULL,
PRIMARY KEY (organization_id,project_id,repo,from_id,to_id,kind),
FOREIGN KEY (organization_id,project_id,repo) REFERENCES code_repos(organization_id,project_id,repo) ON DELETE CASCADE);

CREATE INDEX IF NOT EXISTS code_edges_to ON code_edges(organization_id,project_id,repo,to_id,kind);

CREATE INDEX IF NOT EXISTS code_edges_path ON code_edges(organization_id,project_id,repo,path);

CREATE TABLE IF NOT EXISTS principals (
id text PRIMARY KEY, role text NOT NULL CHECK (role IN ('human','agent','service','pipeline','classifier')),
token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256)=32), active boolean NOT NULL DEFAULT true);

ALTER TABLE principals DROP CONSTRAINT IF EXISTS principals_role_check;

ALTER TABLE principals ADD CONSTRAINT principals_role_check CHECK (role IN ('human','agent','service','pipeline','classifier'));

CREATE TABLE IF NOT EXISTS organization_memberships (
organization_id text NOT NULL, principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
active boolean NOT NULL DEFAULT true, PRIMARY KEY(organization_id,principal_id));

CREATE TABLE IF NOT EXISTS project_memberships (
organization_id text NOT NULL, project_id text NOT NULL, principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
active boolean NOT NULL DEFAULT true, PRIMARY KEY(organization_id,project_id,principal_id),
FOREIGN KEY(organization_id,principal_id) REFERENCES organization_memberships(organization_id,principal_id) ON DELETE CASCADE);

CREATE TABLE IF NOT EXISTS principal_permissions (
principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE, organization_id text NOT NULL,
project_id text NOT NULL, action text NOT NULL, allowed boolean NOT NULL DEFAULT true,
PRIMARY KEY(principal_id,organization_id,project_id,action),
FOREIGN KEY(organization_id,project_id,principal_id) REFERENCES project_memberships(organization_id,project_id,principal_id) ON DELETE CASCADE);

CREATE TABLE IF NOT EXISTS candidate_daily_usage (
day date PRIMARY KEY, used integer NOT NULL CHECK (used > 0));

CREATE OR REPLACE FUNCTION current_principal() RETURNS TABLE(id text, role text, active boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT p.id,p.role,p.active FROM arkism.principals p
 WHERE encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true) LIMIT 1
$$;

CREATE OR REPLACE FUNCTION has_permission(org text, project text, requested_action text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
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

CREATE OR REPLACE FUNCTION has_any_permission(requested_action text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT EXISTS (
  SELECT 1 FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
 )
$$;

CREATE OR REPLACE FUNCTION permitted_scopes(requested_action text) RETURNS TABLE(org text, project text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT DISTINCT m.organization_id, m.project_id FROM arkism.principals p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
  WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
    AND x.action IN (requested_action,'*')
    AND (m.organization_id NOT IN ('101-internal','__memory_blobs__','__unparsed__') OR p.role='service')
$$;

CREATE OR REPLACE FUNCTION authorize_principal(org text, project text, requested_action text)
RETURNS TABLE(principal_id text, principal_role text, allowed boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT p.id,p.role,arkism.has_permission(org,project,requested_action)
 FROM arkism.principals p
 WHERE encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true) LIMIT 1
$$;

CREATE OR REPLACE FUNCTION reserve_candidate_quota(org text, project text, quota_day date, quota_limit integer) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
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

REVOKE ALL ON principals,organization_memberships,project_memberships,principal_permissions,candidate_daily_usage FROM PUBLIC;

REVOKE ALL ON FUNCTION current_principal(),has_permission(text,text,text),has_any_permission(text),permitted_scopes(text),authorize_principal(text,text,text),reserve_candidate_quota(text,text,date,integer) FROM PUBLIC;

DO $$ DECLARE role_name text; BEGIN
 FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role','arkism_app'] LOOP
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
   EXECUTE format('REVOKE ALL ON principals,organization_memberships,project_memberships,principal_permissions,candidate_daily_usage FROM %I',role_name);
   EXECUTE format('GRANT EXECUTE ON FUNCTION current_principal(),has_permission(text,text,text),has_any_permission(text),permitted_scopes(text),authorize_principal(text,text,text),reserve_candidate_quota(text,text,date,integer) TO %I',role_name);
  END IF;
 END LOOP;
END $$;

ALTER TABLE "memory_records" ENABLE ROW LEVEL SECURITY; ALTER TABLE "memory_records" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "memory_records";
CREATE POLICY arkism_init_owner ON "memory_records" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "memory_records";
CREATE POLICY arkism_read ON "memory_records" FOR SELECT TO PUBLIC USING (
 (workspace_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (workspace_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "memory_records";
DROP POLICY IF EXISTS arkism_insert ON "memory_records";
CREATE POLICY arkism_insert ON "memory_records" FOR INSERT TO PUBLIC WITH CHECK (
 (workspace_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (workspace_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "memory_records";
CREATE POLICY arkism_update ON "memory_records" FOR UPDATE TO PUBLIC USING (
 (workspace_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (workspace_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (workspace_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (workspace_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "memory_records";
CREATE POLICY arkism_delete ON "memory_records" FOR DELETE TO PUBLIC USING (
 (workspace_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (workspace_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

ALTER TABLE "execution_traces" ENABLE ROW LEVEL SECURITY; ALTER TABLE "execution_traces" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "execution_traces";
CREATE POLICY arkism_init_owner ON "execution_traces" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "execution_traces";
CREATE POLICY arkism_read ON "execution_traces" FOR SELECT TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "execution_traces";
DROP POLICY IF EXISTS arkism_insert ON "execution_traces";
CREATE POLICY arkism_insert ON "execution_traces" FOR INSERT TO PUBLIC WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "execution_traces";
CREATE POLICY arkism_update ON "execution_traces" FOR UPDATE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "execution_traces";
CREATE POLICY arkism_delete ON "execution_traces" FOR DELETE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

ALTER TABLE "source_events" ENABLE ROW LEVEL SECURITY; ALTER TABLE "source_events" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "source_events";
CREATE POLICY arkism_init_owner ON "source_events" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "source_events";
CREATE POLICY arkism_read ON "source_events" FOR SELECT TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "source_events";
DROP POLICY IF EXISTS arkism_insert ON "source_events";
CREATE POLICY arkism_insert ON "source_events" FOR INSERT TO PUBLIC WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "source_events";
CREATE POLICY arkism_update ON "source_events" FOR UPDATE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "source_events";
CREATE POLICY arkism_delete ON "source_events" FOR DELETE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

ALTER TABLE "entities" ENABLE ROW LEVEL SECURITY; ALTER TABLE "entities" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "entities";
CREATE POLICY arkism_init_owner ON "entities" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "entities";
CREATE POLICY arkism_read ON "entities" FOR SELECT TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "entities";
DROP POLICY IF EXISTS arkism_insert ON "entities";
CREATE POLICY arkism_insert ON "entities" FOR INSERT TO PUBLIC WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "entities";
CREATE POLICY arkism_update ON "entities" FOR UPDATE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "entities";
CREATE POLICY arkism_delete ON "entities" FOR DELETE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

ALTER TABLE "relations" ENABLE ROW LEVEL SECURITY; ALTER TABLE "relations" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "relations";
CREATE POLICY arkism_init_owner ON "relations" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "relations";
CREATE POLICY arkism_read ON "relations" FOR SELECT TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "relations";
DROP POLICY IF EXISTS arkism_insert ON "relations";
CREATE POLICY arkism_insert ON "relations" FOR INSERT TO PUBLIC WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "relations";
CREATE POLICY arkism_update ON "relations" FOR UPDATE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "relations";
CREATE POLICY arkism_delete ON "relations" FOR DELETE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

ALTER TABLE "code_repos" ENABLE ROW LEVEL SECURITY; ALTER TABLE "code_repos" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "code_repos";
CREATE POLICY arkism_init_owner ON "code_repos" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "code_repos";
CREATE POLICY arkism_read ON "code_repos" FOR SELECT TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "code_repos";
DROP POLICY IF EXISTS arkism_insert ON "code_repos";
CREATE POLICY arkism_insert ON "code_repos" FOR INSERT TO PUBLIC WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "code_repos";
CREATE POLICY arkism_update ON "code_repos" FOR UPDATE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "code_repos";
CREATE POLICY arkism_delete ON "code_repos" FOR DELETE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

ALTER TABLE "code_symbols" ENABLE ROW LEVEL SECURITY; ALTER TABLE "code_symbols" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "code_symbols";
CREATE POLICY arkism_init_owner ON "code_symbols" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "code_symbols";
CREATE POLICY arkism_read ON "code_symbols" FOR SELECT TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "code_symbols";
DROP POLICY IF EXISTS arkism_insert ON "code_symbols";
CREATE POLICY arkism_insert ON "code_symbols" FOR INSERT TO PUBLIC WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "code_symbols";
CREATE POLICY arkism_update ON "code_symbols" FOR UPDATE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "code_symbols";
CREATE POLICY arkism_delete ON "code_symbols" FOR DELETE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

ALTER TABLE "code_edges" ENABLE ROW LEVEL SECURITY; ALTER TABLE "code_edges" FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS arkism_init_owner ON "code_edges";
CREATE POLICY arkism_init_owner ON "code_edges" FOR ALL TO PUBLIC USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
DROP POLICY IF EXISTS arkism_read ON "code_edges";
CREATE POLICY arkism_read ON "code_edges" FOR SELECT TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:read')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_write ON "code_edges";
DROP POLICY IF EXISTS arkism_insert ON "code_edges";
CREATE POLICY arkism_insert ON "code_edges" FOR INSERT TO PUBLIC WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_update ON "code_edges";
CREATE POLICY arkism_update ON "code_edges" FOR UPDATE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))))
WITH CHECK (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));
DROP POLICY IF EXISTS arkism_delete ON "code_edges";
CREATE POLICY arkism_delete ON "code_edges" FOR DELETE TO PUBLIC USING (
 (organization_id,project_id) IN (SELECT org,project FROM permitted_scopes('db:write')) OR
 (organization_id='__memory_blobs__' AND project_id='__memory_blobs__' AND (SELECT has_any_permission('storage:internal'))));

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
 RAISE EXCEPTION 'classifier may only classify memory candidates' USING ERRCODE='AK403';
END $$;
DROP TRIGGER IF EXISTS memory_records_classifier_guard ON memory_records;
CREATE TRIGGER memory_records_classifier_guard BEFORE INSERT OR UPDATE OR DELETE ON memory_records FOR EACH ROW EXECUTE FUNCTION classifier_write_guard();

CREATE OR REPLACE FUNCTION arkism.artifact_gc_held(blob_digest text) RETURNS boolean
 LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $body$
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
 END $body$;

CREATE OR REPLACE FUNCTION arkism.artifact_cleanup_pending(org text,project text,subject_id text,agent text,job_id text,blob_digest text) RETURNS boolean
 LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $body$
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
 END $body$;

REVOKE ALL ON FUNCTION arkism.artifact_gc_held(text),arkism.artifact_cleanup_pending(text,text,text,text,text,text) FROM PUBLIC;

DO $$ DECLARE role_name text; BEGIN
  FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role','arkism_app'] LOOP
   IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
    EXECUTE format('GRANT EXECUTE ON FUNCTION arkism.artifact_gc_held(text),arkism.artifact_cleanup_pending(text,text,text,text,text,text) TO %I',role_name);
   END IF;
  END LOOP;
 END $$;

INSERT INTO memory_storage_metadata(key,value) VALUES('recovery_write_gate',$2) ON CONFLICT(key) DO NOTHING;

CREATE OR REPLACE FUNCTION arkism.check_recovery_write_gate() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
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
END $$;

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."memory_records"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."memory_records" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."execution_traces"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."execution_traces" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."source_events"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."source_events" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."entities"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."entities" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."relations"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."relations" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."principals"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."principals" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."organization_memberships"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."organization_memberships" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."project_memberships"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."project_memberships" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."principal_permissions"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."principal_permissions" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

DROP TRIGGER IF EXISTS recovery_write_gate ON "arkism"."candidate_daily_usage"; CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON "arkism"."candidate_daily_usage" FOR EACH STATEMENT EXECUTE FUNCTION arkism.check_recovery_write_gate();

REVOKE ALL ON FUNCTION arkism.check_recovery_write_gate() FROM PUBLIC;
