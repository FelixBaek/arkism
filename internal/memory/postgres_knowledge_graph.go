package memory

import (
	"context"
	"encoding/json"
	"strings"
)

const knowledgeGraphPrefix = "asserted_"

func (p *Postgres) PutKnowledgeRelation(ctx context.Context, in KnowledgeRelation) error {
	if checkScope(in.Scope) != nil {
		return ErrInvalid
	}
	for _, node := range []GraphEntity{in.Source, in.Target} {
		if err := p.insertGraphEntity(ctx, in.Scope, knowledgeGraphPrefix+node.Kind, node.ID); err != nil {
			return err
		}
	}
	data, err := json.Marshal(in)
	if err != nil {
		return ErrInvalid
	}
	// A different assertion must not silently replace existing human evidence.
	tag, err := p.q().Exec(ctx, `INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind,metadata)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)
 ON CONFLICT (organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind)
 DO UPDATE SET metadata=relations.metadata WHERE relations.metadata=EXCLUDED.metadata`, in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID, knowledgeGraphPrefix+in.Source.Kind, in.Source.ID, knowledgeGraphPrefix+in.Target.Kind, in.Target.ID, knowledgeGraphPrefix+in.Relation, data)
	if err != nil {
		return pgErr(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (p *Postgres) KnowledgeRelations(ctx context.Context, scope Scope, node GraphEntity, limit int) ([]KnowledgeRelation, error) {
	if checkScope(scope) != nil || limit < 1 || limit > 201 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT relation_kind,metadata FROM relations
 WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND source_kind=$5 AND source_id=$6
 AND starts_with(relation_kind,$7) ORDER BY relation_kind,target_kind,target_id LIMIT $8`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, knowledgeGraphPrefix+node.Kind, node.ID, knowledgeGraphPrefix, limit)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []KnowledgeRelation{}
	for rows.Next() {
		var kind string
		var data []byte
		if err = rows.Scan(&kind, &data); err != nil {
			return nil, pgErr(err)
		}
		var edge KnowledgeRelation
		if json.Unmarshal(data, &edge) != nil || edge.Scope != scope || edge.Source != node || edge.Relation != strings.TrimPrefix(kind, knowledgeGraphPrefix) {
			return nil, ErrInvalid
		}
		out = append(out, edge)
	}
	return out, pgErr(rows.Err())
}
