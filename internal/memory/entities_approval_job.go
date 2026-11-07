package memory

import (
	"context"
	"errors"
	"time"
)

// Durable deterministic recovery for explicitly approved entities and relations.
// The shared job worker provides lease fencing, bounded attempts and restore gates.
const approvedEntitiesJobKind = "approved_entities"

func enqueueApprovedEntities(ctx context.Context, tx Tx, r Record) error {
	now := time.Now().UTC()
	assertedBy := r.ApprovedBy
	if actor, ok := ctx.Value(requestActorKey{}).(Decision); ok {
		assertedBy = actor.PrincipalID
	}
	job := Record{ID: "approved-entities-" + ContentHash(structuredText(Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision})), Scope: r.Scope, Kind: approvedEntitiesJobKind, Status: JobStatusPending, Revision: 1, CreatedBy: assertedBy, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}}
	return tx.Insert(ctx, colJobs, job)
}

func (s *Service) runApprovedEntities(ctx context.Context, token string, job Record) error {
	if job.Target == nil || job.Target.Collection != colMemories {
		return ErrInvalid
	}
	r, err := s.Repo.Get(ctx, colMemories, job.Scope, job.Target.ID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !validKnowledgeEvidence(r, *job.Target, job.Scope) {
		return nil
	}
	actor, err := s.entityEditor(ctx, token, job.Scope, "", roleService)
	if err != nil {
		return err
	}
	if _, err = s.authorize(ctx, token, "read:memories", job.Scope, r.ID, structuredText(r)); err != nil {
		return err
	}
	ctx = bindRequestActor(ctx, actor)
	cfg, err := s.spaceSettings(ctx, r.Scope)
	if err != nil {
		return err
	}
	if _, err = s.applyEntityExtraction(ctx, r.Scope, actor.PrincipalID, cfg, memoryEntityFinds(r, cfg), false); err != nil {
		return err
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		// Match extraction: acquire the scope entity lock before fencing memory
		// and source rows, so the two writers cannot form a lock-order cycle.
		store, e := entityTx(tx)
		if e != nil {
			return e
		}
		if e = store.LockNamedEntities(ctx, job.Scope); e != nil {
			return e
		}
		checked := searchReadTx{Tx: tx}
		current, e := checked.Get(ctx, colMemories, job.Scope, r.ID)
		if e != nil {
			return e
		}
		if !validKnowledgeEvidence(current, *job.Target, job.Scope) {
			return ErrConflict
		}
		if e = checkSources(ctx, checked, current); e != nil {
			return e
		}

		edges, e := approvedEntityRelations(ctx, store, current)
		if e != nil {
			return e
		}
		if len(edges) == 0 {
			return nil
		}
		graph, ok := tx.(knowledgeGraphStore)
		if !ok {
			return ErrUnavailable
		}
		for _, edge := range edges {
			edge.AssertedBy = job.CreatedBy
			if edge.AssertedBy == "" {
				return ErrInvalid
			}
			if e = graph.PutKnowledgeRelation(ctx, edge); e != nil {
				return e
			}
		}
		return nil
	})
}
