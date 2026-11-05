package memory

import (
	"context"
	"encoding/json"
	"time"
)

// Named entities live in the entities table (kind named_entity, metadata from
// migration 0006); mentions are relations rows memory -mentions-> entity.

func (p *Postgres) LockNamedEntities(ctx context.Context, scope Scope) error {
	if checkScope(scope) != nil {
		return ErrInvalid
	}
	_, err := p.q().Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, namedEntityLockKey(scope))
	return pgErr(err)
}

// namedEntityLockKey is text Postgres accepts (no NUL byte); the scope JSON keeps scopes apart.
func namedEntityLockKey(scope Scope) string { return "arkism.named_entities:" + structuredText(scope) }

func (p *Postgres) NamedEntities(ctx context.Context, scope Scope, limit int) ([]NamedEntity, error) {
	if checkScope(scope) != nil || limit < 1 || limit > entityScanLimit+1 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT external_id,metadata FROM entities
 WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind=$5 ORDER BY external_id LIMIT $6`,
		scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, namedEntityKind, limit)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []NamedEntity{}
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return nil, pgErr(err)
		}
		var e NamedEntity
		if json.Unmarshal(data, &e) != nil || e.ID != id {
			return nil, ErrInvalid
		}
		out = append(out, e)
	}
	return out, pgErr(rows.Err())
}

func (p *Postgres) PutNamedEntity(ctx context.Context, scope Scope, e NamedEntity) error {
	if checkScope(scope) != nil || !graphID.MatchString(e.ID) {
		return ErrInvalid
	}
	data, err := json.Marshal(e)
	if err != nil {
		return ErrInvalid
	}
	_, err = p.q().Exec(ctx, `INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id,metadata)
 VALUES($1,$2,$3,$4,$5,$6,$7::jsonb)
 ON CONFLICT (organization_id,project_id,user_id,agent_id,kind,external_id) DO UPDATE SET metadata=EXCLUDED.metadata`,
		scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, namedEntityKind, e.ID, data)
	return pgErr(err)
}

func (p *Postgres) DeleteNamedEntity(ctx context.Context, scope Scope, id string) error {
	if checkScope(scope) != nil {
		return ErrInvalid
	}
	_, err := p.q().Exec(ctx, `DELETE FROM entities WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind=$5 AND external_id=$6`,
		scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, namedEntityKind, id)
	return pgErr(err)
}

type mentionMetadata struct {
	Origin  string    `json:"origin"`
	EventAt time.Time `json:"event_at"`
}

func (p *Postgres) EntityMentions(ctx context.Context, scope Scope, limit int) ([]EntityMention, error) {
	if checkScope(scope) != nil || limit < 1 || limit > mentionScanLimit+1 {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT source_kind,source_id,target_id,relation_kind,metadata FROM relations
 WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND target_kind=$5 AND relation_kind IN ($6,$7)
 ORDER BY target_id,source_id,relation_kind LIMIT $8`,
		scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, namedEntityKind, relMentions, relMentionsProposed, limit)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []EntityMention{}
	for rows.Next() {
		var m EntityMention
		var data []byte
		if err = rows.Scan(&m.MemoryKind, &m.MemoryID, &m.EntityID, &m.Relation, &data); err != nil {
			return nil, pgErr(err)
		}
		var meta mentionMetadata
		if json.Unmarshal(data, &meta) != nil {
			return nil, ErrInvalid
		}
		m.Origin, m.EventAt = meta.Origin, meta.EventAt
		out = append(out, m)
	}
	return out, pgErr(rows.Err())
}

func (p *Postgres) ReplaceEntityMentions(ctx context.Context, scope Scope, remove, add []EntityMention) error {
	if checkScope(scope) != nil {
		return ErrInvalid
	}
	for _, m := range remove {
		if _, err := p.q().Exec(ctx, `DELETE FROM relations WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4
 AND source_kind=$5 AND source_id=$6 AND target_kind=$7 AND target_id=$8 AND relation_kind=$9`,
			scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, m.MemoryKind, m.MemoryID, namedEntityKind, m.EntityID, m.Relation); err != nil {
			return pgErr(err)
		}
	}
	for _, m := range add {
		data, err := json.Marshal(mentionMetadata{Origin: m.Origin, EventAt: m.EventAt})
		if err != nil {
			return ErrInvalid
		}
		// The memory's own entity row comes from the record graph trigger; a
		// missing one (memory gone meanwhile) fails the foreign key, as it should.
		if _, err = p.q().Exec(ctx, `INSERT INTO relations(organization_id,project_id,user_id,agent_id,source_kind,source_id,target_kind,target_id,relation_kind,metadata)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb) ON CONFLICT DO NOTHING`,
			scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, m.MemoryKind, m.MemoryID, namedEntityKind, m.EntityID, m.Relation, data); err != nil {
			return pgErr(err)
		}
	}
	return nil
}
