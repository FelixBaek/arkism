package memory

import "context"

// EnqueueReindex queues one embedding job per record in an embeddable
// collection whose stored vector is not in space. It never embeds itself: the
// worker does, and the worker already skips records that are no longer live.
// Used after a store migration or an embedding model change.
func (p *Postgres) EnqueueReindex(ctx context.Context, space string) (int, error) {
	if space == "" {
		return 0, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT collection, workspace_id, project_id, user_id, agent_id, id FROM memory_records
WHERE collection = ANY($1) AND embedding_space IS DISTINCT FROM $2 ORDER BY collection, id`, []string{colMemories, colSkillVersions, colReflections}, space)
	if err != nil {
		return 0, pgErr(err)
	}
	type target struct {
		col string
		s   Scope
		id  string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.col, &t.s.WorkspaceID, &t.s.ProjectID, &t.s.UserID, &t.s.AgentID, &t.id); err != nil {
			rows.Close()
			return 0, pgErr(err)
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, pgErr(err)
	}
	queued := 0
	for _, t := range targets {
		err = p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			r, e := tx.Get(ctx, t.col, t.s, t.id)
			if e != nil || !embeddingEligible(t.col, r) || t.col == colSkillVersions && checkActiveSkill(ctx, tx, r) != nil {
				return nil
			}
			queued++
			return enqueue(ctx, tx, t.col, r)
		})
		if err != nil {
			return queued, err
		}
	}
	return queued, nil
}
