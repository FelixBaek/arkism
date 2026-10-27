package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// defaultCaptureMergeSimilarity is the cosine similarity at or above which a
// captured claim counts as a rewording of an approved memory. Paraphrases of one
// rule sit near the top of the scale; rules that merely share a topic sit well
// below it, so the default errs toward a second candidate over a wrong pairing.
const defaultCaptureMergeSimilarity = 0.92

// captureMergeGate opens a merge proposal when the new candidate's text sits
// within CaptureMergeSimilarity of an approved memory's stored vector, and
// reports whether it did. It never fails a capture: no embedder, a refused or
// failed embedding, or no embedded memory nearby leaves the candidate on the
// ordinary review path. A person decides the proposal; nothing is merged or
// deleted here.
func (s *Service) captureMergeGate(ctx context.Context, token string, in CaptureInput, scope Scope, cand Record) bool {
	actor, ok := ctx.Value(requestActorKey{}).(Decision)
	if s.Embedder == nil || !ok {
		return false
	}
	similar, vec, space, _, err := s.similarMemories(ctx, token, actor, scope, cand, mergePoolK)
	if err != nil || len(vec) == 0 {
		return false
	}
	threshold := s.CaptureMergeSimilarity
	if threshold <= 0 {
		threshold = defaultCaptureMergeSimilarity
	}
	best, bestSim := -1, 0.0
	for i, m := range similar {
		if m.Embedding == nil || m.Embedding.Space != space {
			continue
		}
		if sim := cosine(vec, m.Embedding.Vector); sim >= threshold && sim > bestSim {
			best, bestSim = i, sim
		}
	}
	if best < 0 {
		return false
	}
	target := similar[best]
	now := time.Now().UTC().Truncate(time.Millisecond)
	gen := *in.Generator
	p := MergeProposal{Candidate: Ref{Collection: "memory_candidates", ID: cand.ID, Revision: cand.Revision}, Action: "merge",
		Targets: []Ref{{Collection: colMemories, ID: target.ID, Revision: target.Revision}}, ReasonCode: "same_meaning",
		Reason: fmt.Sprintf("캡처 관문: 승인된 기억과 코사인 유사도 %.3f", bestSim)}
	rec := Record{ID: mergeProposalID(scope, cand.ID, cand.Revision), Scope: scope, Kind: "merge", Status: mergeOpen, Revision: 1, Text: p.ReasonCode,
		Merge: &MergeRecord{Proposal: &p}, Generator: &gen, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colMergeProposals, rec); err != nil {
			return err
		}
		return audit(ctx, tx, scope, actor.PrincipalID, "create:"+colMergeProposals, rec.ID)
	})
	if err != nil {
		// Identifiers only: the claim text never reaches the log.
		slog.Warn("capture merge proposal not stored", "candidate_id", cand.ID, "error", err)
		return false
	}
	return true
}
