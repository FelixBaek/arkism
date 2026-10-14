package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (p *Postgres) initTraceSchema(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS execution_traces (
id text PRIMARY KEY, organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
thread_id text NOT NULL, agent_run_id text NOT NULL UNIQUE, langsmith_run_id text, status text NOT NULL CHECK (status='completed'),
started_at timestamptz NOT NULL, finished_at timestamptz NOT NULL, payload_raw bytea NOT NULL, sensitivity text NOT NULL,
inspection jsonb NOT NULL, request_hash text NOT NULL, created_by text NOT NULL, created_at timestamptz NOT NULL, content_erased boolean NOT NULL DEFAULT false,
UNIQUE (organization_id,project_id,user_id,agent_id,id), CHECK (finished_at>=started_at))`,
		`CREATE INDEX IF NOT EXISTS execution_traces_thread ON execution_traces(organization_id,project_id,user_id,agent_id,thread_id,finished_at DESC)`,
		`CREATE INDEX IF NOT EXISTS execution_traces_langsmith ON execution_traces(organization_id,project_id,langsmith_run_id) WHERE langsmith_run_id IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS source_events (
id text PRIMARY KEY, trace_id text NOT NULL, organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
kind text NOT NULL, occurred_at timestamptz NOT NULL, payload jsonb NOT NULL, payload_raw bytea NOT NULL, sensitivity text NOT NULL,
inspection jsonb NOT NULL, request_hash text NOT NULL, created_at timestamptz NOT NULL,
content_erased boolean NOT NULL DEFAULT false,
FOREIGN KEY (organization_id,project_id,user_id,agent_id,trace_id) REFERENCES execution_traces(organization_id,project_id,user_id,agent_id,id) ON DELETE CASCADE)`,
		`ALTER TABLE execution_traces ADD COLUMN IF NOT EXISTS content_erased boolean NOT NULL DEFAULT false`,
		`ALTER TABLE source_events ADD COLUMN IF NOT EXISTS content_erased boolean NOT NULL DEFAULT false`,
		`CREATE INDEX IF NOT EXISTS source_events_trace ON source_events(organization_id,project_id,user_id,agent_id,trace_id,occurred_at,id)`,
		`CREATE TABLE IF NOT EXISTS entities (
organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
kind text NOT NULL, external_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
PRIMARY KEY (organization_id,project_id,user_id,agent_id,kind,external_id))`,
		`CREATE TABLE IF NOT EXISTS relations (
organization_id text NOT NULL, project_id text NOT NULL, user_id text NOT NULL DEFAULT '', agent_id text NOT NULL DEFAULT '',
source_kind text NOT NULL, source_id text NOT NULL, target_kind text NOT NULL, target_id text NOT NULL, relation_kind text NOT NULL,
metadata jsonb NOT NULL DEFAULT '{}'::jsonb, created_at timestamptz NOT NULL DEFAULT now(),
PRIMARY KEY (organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind),
FOREIGN KEY (organization_id,project_id,user_id,agent_id,source_kind,source_id) REFERENCES entities(organization_id,project_id,user_id,agent_id,kind,external_id) ON DELETE CASCADE,
FOREIGN KEY (organization_id,project_id,user_id,agent_id,target_kind,target_id) REFERENCES entities(organization_id,project_id,user_id,agent_id,kind,external_id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS relations_target ON relations(organization_id,project_id,user_id,agent_id,target_kind,target_id,relation_kind)`,
		traceGraphTriggerSQL,
		`DROP TRIGGER IF EXISTS memory_records_graph_sync ON memory_records`,
		`CREATE TRIGGER memory_records_graph_sync AFTER INSERT OR UPDATE OR DELETE ON memory_records FOR EACH ROW EXECUTE FUNCTION sync_memory_record_graph()`,
	} {
		if _, err := p.q().Exec(ctx, statement); err != nil {
			return pgErr(err)
		}
	}
	return nil
}

const traceGraphTriggerSQL = `CREATE OR REPLACE FUNCTION sync_memory_record_graph() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=arkism,pg_catalog AS $$
DECLARE entity_kind text; target_kind text; evidence_id text; link jsonb;
BEGIN
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
END $$`

func (p *Postgres) storeCompletedTrace(ctx context.Context, prepared preparedTrace) (TraceIngestResult, error) {
	if p.tx == nil {
		return TraceIngestResult{}, ErrInvalid
	}
	in := prepared.input
	out := traceRefs(in)
	var existing string
	err := p.q().QueryRow(ctx, `SELECT request_hash FROM execution_traces WHERE id=$1 AND organization_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 FOR UPDATE`, in.TraceID, in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID).Scan(&existing)
	if err == nil {
		if existing != prepared.requestHash || p.completeTracePresent(ctx, out) != nil {
			return TraceIngestResult{}, ErrConflict
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TraceIngestResult{}, pgErr(err)
	}
	lineageRefs := make(map[string]SourceRef, len(prepared.lineageSourceIDs))
	if len(prepared.lineageSourceIDs) > 0 {
		rows, metadataErr := p.SourceMetadata(ctx, in.Scope, prepared.lineageSourceIDs, true)
		if metadataErr != nil {
			return TraceIngestResult{}, metadataErr
		}
		if len(rows) != len(prepared.lineageSourceIDs) {
			return TraceIngestResult{}, ErrConflict
		}
		for _, row := range rows {
			if row.Scope != in.Scope || !live(row) {
				return TraceIngestResult{}, ErrConflict
			}
			lineageRefs[row.ID] = sourceReference(row, "memory_context")
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	inspection, _ := json.Marshal(traceInspections(prepared.events))
	raw, _ := json.Marshal(in)
	_, err = p.q().Exec(ctx, `INSERT INTO execution_traces(id,organization_id,project_id,user_id,agent_id,thread_id,agent_run_id,langsmith_run_id,status,started_at,finished_at,payload_raw,sensitivity,inspection,request_hash,created_by,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),'completed',$9,$10,$11,$12,$13,$14,$15,$16)`, in.TraceID, in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, in.ThreadID, in.AgentRun.ID, in.LangSmithRunID, in.AgentRun.StartedAt, in.AgentRun.FinishedAt, raw, prepared.sensitivity, inspection, prepared.requestHash, prepared.actor.PrincipalID, now)
	if err != nil {
		return TraceIngestResult{}, pgErr(err)
	}
	sources := make([]SourceRef, 0, len(prepared.events))
	for _, event := range prepared.events {
		payloadHash := ContentHash(string(event.Payload))
		inspectionJSON, _ := json.Marshal(event.Inspection)
		if _, err = p.q().Exec(ctx, `INSERT INTO source_events(id,trace_id,organization_id,project_id,user_id,agent_id,kind,occurred_at,payload,payload_raw,sensitivity,inspection,request_hash,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, event.ID, in.TraceID, in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, event.Kind, event.OccurredAt, string(event.Payload), []byte(event.Payload), event.Sensitivity, string(inspectionJSON), payloadHash, now); err != nil {
			return TraceIngestResult{}, pgErr(err)
		}
		collected := event.OccurredAt
		source := Record{ID: event.ID, Scope: in.Scope, Kind: traceSourceKind(event.Kind), Status: "active", Revision: 1, Text: string(event.Payload),
			OriginProduct: "langgraph", SourceRef: fmt.Sprintf("execution-trace/%s/event/%s", in.TraceID, event.ID), CollectedAt: &collected,
			CreatedAt: now, UpdatedAt: now, CreatedBy: prepared.actor.PrincipalID, CreatedByType: prepared.actor.Role, TrustLevel: "external_untrusted", Trust: .5,
			SourceEventID: event.ID, TraceID: in.TraceID, Sensitivity: event.Sensitivity, Inspection: &event.Inspection, RequestHash: payloadHash}
		source.Sources = []SourceRef{sourceReference(source, "original")}
		for _, id := range prepared.lineageByEvent[event.ID] {
			source.Sources = append(source.Sources, lineageRefs[id])
		}
		if err = p.Insert(ctx, "sources", source); err != nil {
			return TraceIngestResult{}, err
		}
		sources = append(sources, source.Sources[0])
		if err = p.insertGraphEntity(ctx, in.Scope, "source_event", event.ID); err != nil {
			return TraceIngestResult{}, err
		}
	}
	for _, id := range prepared.lineageSourceIDs {
		sources = append(sources, lineageRefs[id])
	}
	runFinished := in.AgentRun.FinishedAt
	run := Record{ID: in.AgentRun.ID, Scope: in.Scope, Kind: "agent", Status: "active", Revision: 1, Text: in.AgentRun.Text,
		Run:     &RunDetails{AgentID: in.Scope.AgentID, OriginAgentID: in.AgentRun.OriginAgentID, TaskType: in.AgentRun.TaskType, Outcome: in.AgentRun.Outcome, StartedAt: in.AgentRun.StartedAt, FinishedAt: &runFinished},
		Sources: sources, Relations: []Ref{{Collection: "execution_traces", ID: in.TraceID, Revision: 1}}, CreatedAt: now, UpdatedAt: now,
		CreatedBy: prepared.actor.PrincipalID, CreatedByType: prepared.actor.Role, Trust: .5, SourceEventID: prepared.events[0].ID, TraceID: in.TraceID, Sensitivity: prepared.sensitivity, RequestHash: prepared.requestHash}
	trace := Record{ID: in.TraceID, Scope: in.Scope, Kind: "execution_trace", Status: "active", Revision: 1,
		Text: "LangGraph execution trace " + in.ThreadID, Sources: sources, Relations: []Ref{{Collection: "agent_runs", ID: run.ID, Revision: 1}},
		CreatedAt: now, UpdatedAt: now, CreatedBy: prepared.actor.PrincipalID, CreatedByType: prepared.actor.Role, Trust: .5, SourceEventID: prepared.events[0].ID, TraceID: in.TraceID, VerificationStatus: VerificationVerified, Sensitivity: prepared.sensitivity, RequestHash: prepared.requestHash}
	if err = p.Insert(ctx, "execution_traces", trace); err != nil {
		return TraceIngestResult{}, err
	}
	if err = p.Insert(ctx, "agent_runs", run); err != nil {
		return TraceIngestResult{}, err
	}
	for _, relation := range [][5]string{{"langgraph_thread", in.ThreadID, "agent_run", run.ID, "started"}, {"agent_run", run.ID, "trace", in.TraceID, "recorded_by"}} {
		if err = p.insertGraphRelation(ctx, in.Scope, relation[0], relation[1], relation[2], relation[3], relation[4]); err != nil {
			return TraceIngestResult{}, err
		}
	}
	if in.LangSmithRunID != "" {
		if err = p.insertGraphRelation(ctx, in.Scope, "trace", in.TraceID, "langsmith_run", in.LangSmithRunID, "observed_as"); err != nil {
			return TraceIngestResult{}, err
		}
	}
	for _, event := range prepared.events {
		if err = p.insertGraphRelation(ctx, in.Scope, "trace", in.TraceID, "source_event", event.ID, "contains"); err != nil {
			return TraceIngestResult{}, err
		}
		for _, sourceID := range prepared.lineageByEvent[event.ID] {
			if err = p.insertGraphRelation(ctx, in.Scope, "source_event", event.ID, "source_event", sourceID, "context_uses"); err != nil {
				return TraceIngestResult{}, err
			}
		}
	}
	for _, sourceID := range prepared.lineageSourceIDs {
		if err = p.insertGraphRelation(ctx, in.Scope, "trace", in.TraceID, "source_event", sourceID, "context_uses"); err != nil {
			return TraceIngestResult{}, err
		}
	}
	if err = p.recordTacitSequence(ctx, in, prepared.events); err != nil {
		return TraceIngestResult{}, err
	}
	if err = audit(ctx, p, in.Scope, prepared.actor.PrincipalID, "write:execution_traces", in.TraceID); err != nil {
		return TraceIngestResult{}, err
	}
	return out, nil
}

func (p *Postgres) completeTracePresent(ctx context.Context, out TraceIngestResult) error {
	var count int
	ids := make([]string, 0, len(out.SourceEvents))
	for _, ref := range out.SourceEvents {
		ids = append(ids, ref.ID)
	}
	err := p.q().QueryRow(ctx, `SELECT count(*) FROM memory_records WHERE (collection='execution_traces' AND id=$1) OR (collection='agent_runs' AND id=$2) OR (collection='sources' AND id=ANY($3))`, out.Trace.ID, out.Run.ID, ids).Scan(&count)
	if err != nil {
		return pgErr(err)
	}
	if count != 2+len(ids) {
		return ErrConflict
	}
	if err = p.q().QueryRow(ctx, `SELECT count(*) FROM source_events WHERE trace_id=$1 AND id=ANY($2) AND content_erased=false`, out.Trace.ID, ids).Scan(&count); err != nil {
		return pgErr(err)
	}
	if count != len(ids) {
		return ErrConflict
	}
	return nil
}

func traceRefs(in TraceIngestInput) TraceIngestResult {
	out := TraceIngestResult{Trace: Ref{Collection: "execution_traces", ID: in.TraceID, Revision: 1}, Run: Ref{Collection: "agent_runs", ID: in.AgentRun.ID, Revision: 1}}
	for _, event := range in.Events {
		out.SourceEvents = append(out.SourceEvents, Ref{Collection: "sources", ID: event.ID, Revision: 1})
	}
	return out
}

func traceInspections(events []inspectedTraceEvent) []Inspection {
	out := make([]Inspection, len(events))
	for i := range events {
		out[i] = events[i].Inspection
	}
	return out
}

func traceSourceKind(kind string) string {
	if kind == "message" || kind == "tool_result" {
		return kind
	}
	return "note"
}

func (p *Postgres) insertGraphEntity(ctx context.Context, scope Scope, kind, id string) error {
	_, err := p.q().Exec(ctx, `INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, kind, id)
	return pgErr(err)
}

func (p *Postgres) insertGraphRelation(ctx context.Context, scope Scope, sourceKind, sourceID, targetKind, targetID, relationKind string) error {
	if err := p.insertGraphEntity(ctx, scope, sourceKind, sourceID); err != nil {
		return err
	}
	if err := p.insertGraphEntity(ctx, scope, targetKind, targetID); err != nil {
		return err
	}
	_, err := p.q().Exec(ctx, `INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, sourceKind, sourceID, targetKind, targetID, relationKind)
	return pgErr(err)
}

func (p *Postgres) CanonicalEvidence(ctx context.Context, scope Scope, sourceEventID, traceID string) error {
	if checkScope(scope) != nil || sourceEventID == "" && traceID == "" || sourceEventID != "" && !traceIdentifier(sourceEventID) || traceID != "" && !traceIdentifier(traceID) {
		return ErrInvalid
	}
	if sourceEventID != "" && traceID == "" {
		source, err := p.Get(ctx, "sources", scope, sourceEventID)
		if errors.Is(err, ErrNotFound) || err == nil && (source.Scope != scope || !live(source) || source.TraceID != "") {
			return ErrConflict
		}
		return err
	}
	if traceID != "" {
		var status string
		var erased, quarantined bool
		err := p.q().QueryRow(ctx, `SELECT status,content_erased,privacy_quarantined FROM execution_traces WHERE id=$1 AND organization_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5`, traceID, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID).Scan(&status, &erased, &quarantined)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (status != "completed" || erased || quarantined) {
			return ErrConflict
		}
		if err != nil {
			return pgErr(err)
		}
	}
	if sourceEventID != "" {
		var eventTrace string
		var erased, quarantined bool
		err := p.q().QueryRow(ctx, `SELECT trace_id,content_erased,privacy_quarantined FROM source_events WHERE id=$1 AND organization_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5`, sourceEventID, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID).Scan(&eventTrace, &erased, &quarantined)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (erased || quarantined || traceID != "" && eventTrace != traceID) {
			return ErrConflict
		}
		if err != nil {
			return pgErr(err)
		}
	}
	return nil
}

func (p *Postgres) TraceForAgentRun(ctx context.Context, scope Scope, agentRunID string) (string, error) {
	if checkScope(scope) != nil || !traceIdentifier(agentRunID) {
		return "", ErrInvalid
	}
	var id string
	err := p.q().QueryRow(ctx, `SELECT id FROM execution_traces WHERE agent_run_id=$1 AND organization_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND status='completed' AND content_erased=false AND privacy_quarantined=false`, agentRunID, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", pgErr(err)
	}
	return id, nil
}
