package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
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

// defaultCaptureGateBudget is how long one capture request may wait on the embedder for
// the merge gate, over all its claims. A busy embedder answers slowly; without a bound a
// 32-claim request outlives the server's request timeout and the whole capture fails.
const defaultCaptureGateBudget = 15 * time.Second

func (s *Service) captureGateBudget() time.Duration {
	return cmp.Or(s.CaptureGateBudget, defaultCaptureGateBudget)
}

// captureMergeGateKind is the job that runs the merge check of a candidate whose capture
// request spent its gate budget: the worker checks it later instead of skipping it.
const captureMergeGateKind = "capture_merge_gate"

// captureMergeGate opens a merge proposal when the new candidate's text sits
// within CaptureMergeSimilarity of an approved memory's stored vector, and
// reports whether it did. It never fails a capture: no embedder, a refused or
// failed embedding, or no embedded memory nearby leaves the candidate on the
// ordinary review path. A claim reached after the request's gate budget
// (in.gateUntil), or whose check the budget cut short, queues the same check for
// the worker (deferMergeGate). A person decides the proposal; nothing is merged or
// deleted here.
func (s *Service) captureMergeGate(ctx context.Context, token string, in CaptureInput, scope Scope, cand Record) bool {
	actor, ok := ctx.Value(requestActorKey{}).(Decision)
	if s.Embedder == nil || !ok {
		return false
	}
	if !time.Now().Before(in.gateUntil) {
		s.deferMergeGate(ctx, token, scope, cand, *in.Generator)
		return false
	}
	gateCtx, cancel := context.WithDeadline(ctx, in.gateUntil)
	defer cancel()
	proposed, err := s.mergeGateCheck(gateCtx, token, actor, scope, cand, *in.Generator)
	if err != nil && gateCtx.Err() != nil {
		s.deferMergeGate(ctx, token, scope, cand, *in.Generator)
	}
	return proposed
}

// mergeGateCheck stores a merge proposal for cand when an approved memory is close enough.
// errMergeGateUnembedded: the candidate's text could not be embedded this time.
func (s *Service) mergeGateCheck(ctx context.Context, token string, actor Decision, scope Scope, cand Record, gen Generator) (bool, error) {
	similar, vec, space, degraded, err := s.similarMemories(ctx, token, actor, scope, cand, mergePoolK)
	if err != nil {
		return false, err
	}
	if len(vec) == 0 {
		if slices.Contains(degraded, "memory_vector_unavailable") {
			return false, errMergeGateUnembedded
		}
		return false, nil
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
		return false, nil
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
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
		return false, err
	}
	return true, nil
}

var errMergeGateUnembedded = fmt.Errorf("%w: candidate not embedded", ErrUnavailable)

// deferMergeGate queues one captureMergeGateKind job per candidate revision; a second
// queueing of the same revision (a retried request) finds the job already there. Only a
// check the caller may run itself is queued: the worker sends the candidate's text to the
// embedder under its own permission, so the caller must be allowed to send it too.
func (s *Service) deferMergeGate(ctx context.Context, token string, scope Scope, cand Record, gen Generator) {
	query := mergeQueryText(cand)
	if sd, err := s.authorize(ctx, token, "search:"+colMemories, scope, "", query); err != nil || !sd.ExternalTransfer || !sd.ContentSafe || !safeTransfer(query) {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	job := Record{ID: "mgq_" + strings.TrimPrefix(mergeProposalID(scope, cand.ID, cand.Revision), "mg_"), Scope: scope, Kind: captureMergeGateKind,
		Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, Generator: &gen,
		Target: &Ref{Collection: "memory_candidates", ID: cand.ID, Revision: cand.Revision}}
	err := s.Repo.Insert(context.WithoutCancel(ctx), colJobs, job)
	switch {
	case err == nil:
		slog.Info("capture merge gate deferred: budget spent", "candidate_id", cand.ID)
	case !errors.Is(err, ErrConflict):
		slog.Warn("capture merge gate not deferred", "candidate_id", cand.ID, "error", err)
	}
}

// runDeferredMergeGate is the worker's merge check for a deferred candidate, with the
// worker's credentials. A candidate decided, changed, unsafe to send or gone, a refused worker,
// or a proposal already stored ends the job; an embedder that is still unavailable retries it.
func (s *Service) runDeferredMergeGate(ctx context.Context, workerToken string, job Record) error {
	if job.Target == nil || job.Generator == nil || s.Embedder == nil {
		return nil
	}
	cand, err := s.Repo.Get(ctx, job.Target.Collection, job.Scope, job.Target.ID)
	// Decided, changed, quarantined, restricted or erased since: nothing to check.
	if errors.Is(err, ErrNotFound) || err == nil && (cand.Revision != job.Target.Revision || !poolCandidate(ctx, s.Repo, cand, time.Now())) {
		slog.Info("capture merge gate deferred check", "candidate_id", job.Target.ID, "outcome", "stale")
		return nil
	}
	if err != nil {
		return err
	}
	actor, err := s.authorize(ctx, workerToken, "search:"+colMemories, job.Scope, "", "")
	if err != nil {
		slog.Warn("capture merge gate deferred check refused", "candidate_id", cand.ID, "error", err)
		return nil
	}
	proposed, err := s.mergeGateCheck(ctx, workerToken, actor, job.Scope, cand, *job.Generator)
	switch {
	case errors.Is(err, ErrConflict):
		proposed, err = true, nil // stored by an earlier attempt
	case errors.Is(err, ErrDenied):
		err = nil
	}
	if err == nil {
		slog.Info("capture merge gate deferred check", "candidate_id", cand.ID, "proposed", proposed)
	}
	return err
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
