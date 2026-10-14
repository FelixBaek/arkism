package memory

import (
	"context"
	"errors"
	"time"
)

const pendingCaptureCorrectionTag = "review:pending_correction"

type CandidateConflictHoldInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Memory           Ref    `json:"memory"`
	OperationID      string `json:"operation_id"`
}
type CandidateConflictHoldOutput struct {
	Candidate Ref `json:"candidate"`
	Memory    Ref `json:"memory"`
	Conflict  Ref `json:"conflict"`
}

// HoldCandidateConflict grants no approval. Both exact references leave active
// retrieval atomically, under configured service policy and ordinary grants.
func (s *Service) HoldCandidateConflict(ctx context.Context, token, id string, in CandidateConflictHoldInput) (CandidateConflictHoldOutput, error) {
	out := CandidateConflictHoldOutput{}
	if !validScope(in.Scope) || !validRunLabel(id, 256) || !validRunLabel(in.OperationID, 256) || in.ExpectedRevision < 1 || in.Memory.Collection != colMemories || !validRunLabel(in.Memory.ID, 256) || in.Memory.ID == id || in.Memory.Revision < 1 {
		return out, ErrInvalid
	}
	if containsSecret(structuredText(in)) {
		return out, ErrSecretDetected
	}
	actor, e := s.authorize(ctx, token, "transition:memory_candidates", in.Scope, id, "")
	if e != nil {
		return out, e
	}
	if actor.Role != roleService || !actor.ContentSafe || !s.memoryReviewerAllowed(actor, in.Scope, "") {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	for _, request := range []struct{ action, id string }{{"transition:memories", in.Memory.ID}, {"auto_review:memory_candidates", id}} {
		d, err := s.authorize(ctx, token, request.action, in.Scope, request.id, "")
		if err != nil {
			return out, err
		}
		if !d.ContentSafe {
			return out, ErrDenied
		}
	}
	receiptID := "mch_" + ContentHash(structuredText(in.Scope)+"/"+actor.PrincipalID+"/"+in.OperationID)
	hash := ContentHash(id + "/" + structuredText(in))
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		receipt, err := tx.Get(ctx, colJobs, in.Scope, receiptID)
		replay := err == nil
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		candidate, err := tx.Get(ctx, "memory_candidates", in.Scope, id)
		if err != nil {
			return err
		}
		memory, err := tx.Get(ctx, colMemories, in.Scope, in.Memory.ID)
		if err != nil {
			return err
		}
		for _, r := range []Record{candidate, memory} {
			if r.ContentErased || r.PrivacyQuarantined || !s.memoryReviewerAllowed(actor, in.Scope, r.Kind) {
				return ErrDenied
			}
			if err = checkSources(ctx, tx, r); err != nil {
				return err
			}
		}
		if replay {
			if receipt.Kind != "candidate_conflict_hold_receipt" || receipt.Status != JobStatusDone || receipt.RequestHash != hash || receipt.Target == nil || len(receipt.Relations) != 2 {
				return ErrConflict
			}
			rel, err := tx.Get(ctx, "memory_relations", in.Scope, receipt.Target.ID)
			if err != nil {
				return err
			}
			if rel.Revision != receipt.Target.Revision || !live(rel) || rel.ContentErased || candidate.Status != "conflicted" || memory.Status != "conflicted" || candidate.ConflictID != rel.ID || memory.ConflictID != rel.ID || candidate.Revision != receipt.Relations[0].Revision || memory.Revision != receipt.Relations[1].Revision {
				return ErrConflict
			}
			if err = checkSources(ctx, tx, rel); err != nil {
				return err
			}
			out = CandidateConflictHoldOutput{Candidate: receipt.Relations[0], Memory: receipt.Relations[1], Conflict: *receipt.Target}
			return nil
		}
		if candidate.Revision != in.ExpectedRevision || memory.Revision != in.Memory.Revision {
			return ErrConflict
		}
		held, err := applyConflictAs(ctx, tx, ConflictInput{Scope: in.Scope, Memories: []Ref{in.Memory, {Collection: "memory_candidates", ID: id, Revision: in.ExpectedRevision}}, Action: "mark", Reason: "Scoped correction awaiting independent consolidation", Hold: "both"}, actor.PrincipalID, actor.Role, -1)
		if err != nil {
			return err
		}
		out = CandidateConflictHoldOutput{Conflict: Ref{Collection: "memory_relations", ID: held.Relation.ID, Revision: held.Relation.Revision}}
		for _, r := range held.Memories {
			if r.ID == id {
				out.Candidate = Ref{Collection: "memory_candidates", ID: id, Revision: r.Revision}
			} else {
				out.Memory = Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}
			}
		}
		now := time.Now().UTC()
		return tx.Insert(ctx, colJobs, Record{ID: receiptID, Scope: in.Scope, Kind: "candidate_conflict_hold_receipt", Status: JobStatusDone, Revision: 1, RequestHash: hash, Target: &out.Conflict, Relations: []Ref{out.Candidate, out.Memory}, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now})
	})
	if e != nil {
		return CandidateConflictHoldOutput{}, e
	}
	return out, nil
}

func (s *Service) finishCaptureCorrection(ctx context.Context, token string, in CaptureInput, claim CaptureClaim, scope Scope, pointer Record, item CaptureItem) (CaptureItem, error) {
	if pointer.Target == nil || claim.Supersedes == nil {
		return item, ErrConflict
	}
	if in.Automated {
		_, e := s.HoldCandidateConflict(ctx, token, pointer.Target.ID, CandidateConflictHoldInput{Scope: scope, ExpectedRevision: pointer.Target.Revision, Memory: *claim.Supersedes, OperationID: pointer.ID})
		if e != nil {
			return item, e
		}
	} else {
		candidate, e := s.Repo.Get(ctx, "memory_candidates", scope, pointer.Target.ID)
		if e != nil {
			return item, e
		}
		if candidate.ConflictID == "" {
			_, e = s.Conflict(ctx, token, ConflictInput{Scope: scope, Action: "mark", Reason: "Captured user correction", Memories: []Ref{*claim.Supersedes, *pointer.Target}})
			if e != nil {
				return item, e
			}
		} else if candidate.ContentErased || candidate.PrivacyQuarantined || candidate.Status != "conflicted" {
			return item, ErrConflict
		}
	}
	e := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, "jobs", scope, pointer.ID)
		if e != nil {
			return e
		}
		if current.Revision != pointer.Revision || current.Target == nil || *current.Target != *pointer.Target {
			return ErrConflict
		}
		previous := current.Revision
		current.Revision++
		current.Status = JobStatusDone
		current.UpdatedAt = time.Now().UTC()
		return tx.Replace(ctx, "jobs", current, previous)
	})
	if e != nil {
		return item, e
	}
	item.Status = "candidate"
	item.Reason = "conflict_review"
	item.CandidateID = pointer.Target.ID
	return item, nil
}
