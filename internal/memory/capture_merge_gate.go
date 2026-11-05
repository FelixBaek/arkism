package memory

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// defaultCaptureMergeSimilarity is the cosine similarity at or above which a
// captured claim counts as a rewording of an approved memory. Paraphrases of one
// rule sit near the top of the scale; rules that merely share a topic sit well
// below it, so the default errs toward a second candidate over a wrong pairing.
const defaultCaptureMergeSimilarity = 0.92

// lessonNearDuplicateSimilarity is the lower bar for lessons: reworded lessons about one
// mistake pile up, so every approved lesson this close joins one proposal a person decides once.
const lessonNearDuplicateSimilarity = 0.85

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
	if cand.Kind == "lesson" {
		threshold = min(threshold, lessonNearDuplicateSimilarity)
	}
	targets, bestSim := mergeGateTargets(similar, vec, space, threshold, cand.Kind == "lesson")
	if len(targets) == 0 {
		return false
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	gen := *in.Generator
	p := MergeProposal{Candidate: Ref{Collection: "memory_candidates", ID: cand.ID, Revision: cand.Revision}, Action: "merge",
		Targets: targets, ReasonCode: "same_meaning",
		Reason: fmt.Sprintf("캡처 관문: 승인된 기억 %d개와 코사인 유사도 최고 %.3f", len(targets), bestSim)}
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

// mergeGateTargets is the closest memory at or above threshold, or for a lesson every
// approved lesson at or above it (closest first, at most mergeTargetsMax): the group a person decides at once.
func mergeGateTargets(similar []Record, vec []float64, space string, threshold float64, group bool) ([]Ref, float64) {
	type hit struct {
		ref Ref
		sim float64
	}
	var hits []hit
	for _, m := range similar {
		if m.Embedding == nil || m.Embedding.Space != space || group && m.Kind != "lesson" {
			continue
		}
		if sim := cosine(vec, m.Embedding.Vector); sim >= threshold {
			hits = append(hits, hit{Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, sim})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(b.sim, a.sim) })
	keep := min(len(hits), mergeTargetsMax)
	if !group {
		keep = min(keep, 1)
	}
	out := make([]Ref, keep)
	for i := range out {
		out[i] = hits[i].ref
	}
	if keep == 0 {
		return nil, 0
	}
	return out, hits[0].sim
}
