package memory

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

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
		Run:     &RunDetails{AgentID: cmp.Or(in.Scope.AgentID, in.AgentRun.AgentID), OriginAgentID: in.AgentRun.OriginAgentID, TaskType: in.AgentRun.TaskType, Outcome: in.AgentRun.Outcome, StartedAt: in.AgentRun.StartedAt, FinishedAt: &runFinished},
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
