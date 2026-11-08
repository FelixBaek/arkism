package memory

import (
	"context"
	"time"
)

// EnqueueReindex queues one embedding job per record in an embeddable
// collection whose stored vector is not in space. A target that already has a
// waiting or running job for its current revision is left alone, and a failed
// job for it is requeued instead of adding a second one, so running this again
// queues nothing new. It never embeds itself: the worker does, and the worker
// already skips records that are no longer live. Used after a store migration,
// an embedding model change, or an embedder outage.
func (p *Postgres) EnqueueReindex(ctx context.Context, space string) (int, error) {
	if space == "" {
		return 0, ErrInvalid
	}
	type target struct {
		col string
		s   Scope
		id  string
	}
	type jobKey struct {
		s        Scope
		col, id  string
		revision int64
	}
	rows, err := p.q().Query(ctx, `SELECT collection, workspace_id, project_id, user_id, agent_id, id FROM memory_records
WHERE collection = ANY($1) AND embedding_space IS DISTINCT FROM $2 ORDER BY collection, id`, embeddableCollections, space)
	if err != nil {
		return 0, pgErr(err)
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
	// Existing embedding jobs per target revision: "" = has an active job,
	// otherwise the id of a failed job that can be requeued.
	jobs := map[jobKey]string{}
	rows, err = p.q().Query(ctx, `SELECT workspace_id, project_id, user_id, agent_id, id, status,
coalesce(doc->'target'->>'collection',''), coalesce(doc->'target'->>'id',''), coalesce((doc->'target'->>'revision')::bigint,0)
FROM memory_records WHERE collection='jobs' AND kind='embedding' AND status IN ('pending','retry','running','failed')`)
	if err != nil {
		return 0, pgErr(err)
	}
	for rows.Next() {
		var k jobKey
		var id, status string
		if err = rows.Scan(&k.s.WorkspaceID, &k.s.ProjectID, &k.s.UserID, &k.s.AgentID, &id, &status, &k.col, &k.id, &k.revision); err != nil {
			rows.Close()
			return 0, pgErr(err)
		}
		if status != JobStatusFailed {
			jobs[k] = ""
		} else if prev, seen := jobs[k]; !seen || prev != "" {
			jobs[k] = id
		}
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
			failed, seen := jobs[jobKey{t.s, t.col, t.id, r.Revision}]
			if seen && failed == "" {
				return nil
			}
			if seen {
				job, e := tx.Get(ctx, colJobs, t.s, failed)
				if e != nil {
					return e
				}
				if job.Status != JobStatusFailed {
					return nil // picked up meanwhile
				}
				requeueJob(&job, time.Now().UTC())
				prev := job.Revision
				job.Revision++
				if e = tx.Replace(ctx, colJobs, job, prev); e != nil {
					return e
				}
			} else if e = enqueueEmbedding(ctx, tx, t.col, r); e != nil {
				return e
			}
			queued++
			return nil
		})
		if err != nil {
			return queued, err
		}
	}
	return queued, nil
}
