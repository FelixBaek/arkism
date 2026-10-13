package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// recordTacitSequence turns consecutive skill.usage events in one thread into
// skill -often_followed_by-> skill edges on the graph relations table. The
// later trace is the evidence; the edge key is the relations primary key and
// evidence is a set, so replays never double count. Counts, confidence and
// last_seen are computed at read time from evidence traces that are still
// live, so erasure and quarantine remove an observation from the map.
func (p *Postgres) recordTacitSequence(ctx context.Context, in TraceIngestInput, events []inspectedTraceEvent) error {
	usage := make([]inspectedTraceEvent, 0, len(events))
	for _, e := range events {
		if e.Kind == skillUsageEventKind {
			usage = append(usage, e)
		}
	}
	if len(usage) == 0 {
		return nil
	}
	sort.SliceStable(usage, func(i, j int) bool { return usage[i].OccurredAt.Before(usage[j].OccurredAt) })
	skills := []string{}
	var prev string
	err := p.q().QueryRow(ctx, `SELECT e.payload->>'skill_id' FROM source_events e JOIN execution_traces t
  ON t.id=e.trace_id AND t.organization_id=e.organization_id AND t.project_id=e.project_id AND t.user_id=e.user_id AND t.agent_id=e.agent_id
WHERE t.organization_id=$1 AND t.project_id=$2 AND t.user_id=$3 AND t.agent_id=$4 AND t.thread_id=$5 AND t.id<>$6 AND t.finished_at<=$7
  AND e.kind=$8 AND NOT t.content_erased AND NOT t.privacy_quarantined AND NOT e.content_erased AND NOT e.privacy_quarantined
ORDER BY t.finished_at DESC, e.occurred_at DESC, e.id DESC LIMIT 1`,
		in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, in.ThreadID, in.TraceID, in.AgentRun.StartedAt, skillUsageEventKind).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return pgErr(err)
	}
	if prev != "" {
		skills = append(skills, prev)
	}
	for _, e := range usage {
		var u SkillUsage
		if json.Unmarshal(e.Payload, &u) != nil || u.SkillID == "" {
			return ErrInvalid
		}
		skills = append(skills, u.SkillID)
	}
	for i := 1; i < len(skills); i++ {
		if skills[i-1] == skills[i] {
			continue
		}
		if err = p.insertGraphEntity(ctx, in.Scope, "skill", skills[i-1]); err != nil {
			return err
		}
		if err = p.insertGraphEntity(ctx, in.Scope, "skill", skills[i]); err != nil {
			return err
		}
		// shortcut: evidence grows with every observation; cap or roll up when an edge passes ~10k runs.
		if _, err = p.q().Exec(ctx, `INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind,metadata)
VALUES($1,$2,$3,$4,'skill',$5,'skill',$6,$7,jsonb_build_object('evidence',jsonb_build_array($8::text)))
ON CONFLICT (organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind) DO UPDATE
SET metadata=relations.metadata||jsonb_build_object('evidence',CASE WHEN COALESCE(relations.metadata->'evidence','[]'::jsonb) ? $8::text
  THEN relations.metadata->'evidence' ELSE COALESCE(relations.metadata->'evidence','[]'::jsonb)||jsonb_build_array($8::text) END)`,
			in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, skills[i-1], skills[i], tacitFollowedBy, in.TraceID); err != nil {
			return pgErr(err)
		}
	}
	return nil
}

func (p *Postgres) TacitEdges(ctx context.Context, scope Scope, sourceKind, sourceID, relation string) ([]TacitEdge, error) {
	if checkScope(scope) != nil {
		return nil, ErrInvalid
	}
	// Return every live edge in this scope; callers also use this list to
	// locate a specific edge for human confirmation.
	rows, err := p.q().Query(ctx, `SELECT * FROM (SELECT r.source_kind,r.source_id,r.target_kind,r.target_id,r.relation_kind,r.metadata->>'confirmed_at',
  ARRAY(SELECT t.id FROM execution_traces t WHERE t.id IN (SELECT jsonb_array_elements_text(COALESCE(r.metadata->'evidence','[]'::jsonb)))
    AND t.organization_id=r.organization_id AND t.project_id=r.project_id AND t.user_id=r.user_id AND t.agent_id=r.agent_id
    AND NOT t.content_erased AND NOT t.privacy_quarantined ORDER BY t.id),
  (SELECT max(t.finished_at) FROM execution_traces t WHERE t.id IN (SELECT jsonb_array_elements_text(COALESCE(r.metadata->'evidence','[]'::jsonb)))
    AND t.organization_id=r.organization_id AND t.project_id=r.project_id AND t.user_id=r.user_id AND t.agent_id=r.agent_id
    AND NOT t.content_erased AND NOT t.privacy_quarantined) AS last_seen
FROM relations r WHERE r.organization_id=$1 AND r.project_id=$2 AND r.user_id=$3 AND r.agent_id=$4 AND r.relation_kind=ANY($5)
  AND ($6='' OR r.relation_kind=$6) AND ($7='' OR (r.source_kind=$7 AND r.source_id=$8))) e
WHERE e.last_seen IS NOT NULL
ORDER BY 1,2,5,3,4`,
		scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, []string{tacitFollowedBy}, relation, sourceKind, sourceID)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []TacitEdge{}
	for rows.Next() {
		var sk, sid, tk, tid, rel string
		var confirmed *string
		var evidence []string
		var lastSeen *time.Time
		if err = rows.Scan(&sk, &sid, &tk, &tid, &rel, &confirmed, &evidence, &lastSeen); err != nil {
			return nil, pgErr(err)
		}
		if len(evidence) == 0 || lastSeen == nil {
			continue
		}
		e := TacitEdge{Source: sk + ":" + sid, Target: tk + ":" + tid, RelationType: rel, OccurrenceCount: len(evidence), Confidence: countConfidence(len(evidence)),
			LastSeen: lastSeen.UTC(), EvidenceRefs: make([]Ref, 0, len(evidence)), Status: "candidate", Scope: scope}
		for _, id := range evidence {
			e.EvidenceRefs = append(e.EvidenceRefs, Ref{Collection: "execution_traces", ID: id, Revision: 1})
		}
		if e.OccurrenceCount >= improvementMinRuns {
			e.Status = "inferred"
		}
		if confirmed != nil {
			if at, perr := time.Parse(time.RFC3339Nano, *confirmed); perr == nil {
				e.ConfirmedAt, e.Status = &at, "confirmed"
			}
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, pgErr(err)
	}
	return out, nil
}

// ConfirmTacitEdge records a human promotion on the edge itself; evidence is
// unchanged, so the promotion stays traceable to the observed runs.
func (p *Postgres) ConfirmTacitEdge(ctx context.Context, scope Scope, source, target, relation, principal string, at time.Time) error {
	sk, sid, ok1 := parseTacitNode(source)
	tk, tid, ok2 := parseTacitNode(target)
	if checkScope(scope) != nil || !ok1 || !ok2 || relation != tacitFollowedBy || !validRunLabel(principal, 256) {
		return ErrInvalid
	}
	tag, err := p.q().Exec(ctx, `UPDATE relations r SET metadata=metadata||jsonb_build_object('confirmed_at',$10::text,'confirmed_by',$11::text)
WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND source_kind=$5 AND source_id=$6 AND target_kind=$7 AND target_id=$8 AND relation_kind=$9 AND metadata->>'confirmed_at' IS NULL
AND (SELECT count(*) FROM execution_traces t
  WHERE t.id IN (SELECT jsonb_array_elements_text(COALESCE(r.metadata->'evidence','[]'::jsonb)))
    AND t.organization_id=r.organization_id AND t.project_id=r.project_id AND t.user_id=r.user_id AND t.agent_id=r.agent_id
    AND NOT t.content_erased AND NOT t.privacy_quarantined) >= $12`,
		scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, sk, sid, tk, tid, relation, at.Format(time.RFC3339Nano), principal, improvementMinRuns)
	if err != nil {
		return pgErr(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return audit(ctx, p, scope, principal, "write:tacit_relations", source+"|"+relation+"|"+target)
}
