package memory

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"
)

// The caller has already authorized the human's delete:memories action.
func (s *Service) deleteMemoryAndProposal(ctx context.Context, token, id string, in ChangeInput, actor Decision) error {
	if in.ExpectedRevision < 1 {
		return ErrInvalid
	}
	if s.Repo == nil {
		return ErrUnavailable
	}
	memory, e := s.Repo.Get(ctx, "memories", in.Scope, id)
	if e != nil {
		return e
	}
	if memory.Revision != in.ExpectedRevision {
		return ErrConflict
	}
	if p := memory.Proposal; p != nil {
		if p.Collection != "memory_candidates" || p.ID != id || p.Revision < 1 {
			return ErrUnavailable
		}
		who, e := s.authorize(ctx, token, "delete:memory_candidates", in.Scope, p.ID, "")
		if e != nil {
			return e
		}
		if who.Role != "human" || who.PrincipalID != actor.PrincipalID {
			return ErrDenied
		}
	}
	return s.Repo.Transaction(s.digestCtx(ctx), func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, "memories", in.Scope, id)
		if e != nil {
			return e
		}
		if current.PrivacyQuarantined || current.Revision != in.ExpectedRevision || !reflect.DeepEqual(current.Proposal, memory.Proposal) {
			return ErrConflict
		}
		if p := current.Proposal; p != nil {
			candidate, e := tx.Get(ctx, "memory_candidates", in.Scope, p.ID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if e == nil {
				if candidate.PrivacyQuarantined || candidate.Revision < p.Revision {
					return ErrConflict
				}
				if e = tx.Delete(ctx, "memory_candidates", in.Scope, p.ID, candidate.Revision); e != nil {
					return e
				}
				if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "delete:memory_candidates", p.ID); e != nil {
					return e
				}
			}
		}
		if e = invalidateNativeDynamicsDependencyTx(ctx, tx, in.Scope, Ref{Collection: "memories", ID: id, Revision: in.ExpectedRevision}, "target_deleted"); e != nil {
			return e
		}
		if e = tx.Delete(ctx, "memories", in.Scope, id, in.ExpectedRevision); e != nil {
			return e
		}
		if e = enqueueMemoryErasure(ctx, tx, "memories", in.Scope, id, in.ExpectedRevision, actor.PrincipalID, current.Proposal, ""); e != nil {
			return e
		}
		if e = eraseMemoryFromDigests(ctx, tx, in.Scope, id, actor.PrincipalID); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "delete:memories", id)
	})
}

func memoryRelationReferences(r Record, id string, proposal bool, kind string) bool {
	for _, ref := range r.Relations {
		if ref.ID == id && slices.Contains(memoryErasureTargets(kind, proposal), ref.Collection) {
			return true
		}
	}
	return false
}

func isMemoryErasureKind(kind string) bool {
	return kind == memoryRelationErasureKind || kind == memoryEvaluationErasureKind || kind == candidateEvaluationErasureKind
}
func memoryEvaluationReferences(r Record, id string, proposal bool, kind string) bool {
	return r.Target != nil && r.Target.ID == id && slices.Contains(memoryErasureTargets(kind, proposal), r.Target.Collection)
}

func enqueueMemoryErasure(ctx context.Context, tx Tx, col string, scope Scope, id string, revision int64, actor string, proposal *Ref, parentID string) error {
	return enqueueMemoryErasureAs(ctx, tx, col, scope, id, revision, actor, "human", proposal, parentID)
}

func enqueueMemoryErasureAs(ctx context.Context, tx Tx, col string, scope Scope, id string, revision int64, actor, actorType string, proposal *Ref, parentID string) error {
	kind := memoryEvaluationErasureKind
	if col == "memory_candidates" {
		kind = candidateEvaluationErasureKind
		if proposal != nil {
			return ErrInvalid
		}
	} else if col != "memories" {
		return ErrInvalid
	}
	if col == colMemories {
		if e := deleteMemorySignal(ctx, tx, scope, id); e != nil {
			return e
		}
	}
	now := time.Now().UTC()
	job := Record{ErasureParentID: parentID, ID: deletionJobID(col, scope, id), Scope: scope, Kind: kind, Status: JobStatusPending, Revision: 1, CreatedBy: actor, CreatedByType: actorType, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: col, ID: id, Revision: revision}, Proposal: proposal, SourceMemoryErasure: &SourceMemoryErasure{}}
	return tx.Insert(ctx, "jobs", job)
}

func memoryErasureTargets(kind string, proposal bool) []string {
	if kind == candidateEvaluationErasureKind {
		return []string{"memory_candidates"}
	}
	if proposal {
		return []string{"memories", "memory_candidates"}
	}
	return []string{"memories"}
}
