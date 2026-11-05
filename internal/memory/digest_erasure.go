package memory

import (
	"context"
	"slices"
	"time"
)

// Erasure of derived text. Digests, observations and mental-model summaries copy
// text from the memories they were written from, so:
//   - deleting a memory clears, in the same transaction, the text of every digest
//     and observation (any status) that lists it, marking it stale and erased, and
//     the mental-model revisions that cite it or such an observation;
//   - erasing a source deletes the digests and observations that carry it
//     (sourceErasureCollections), since they keep their members' sources, and
//     clears the mental-model revisions citing each memory or observation it deletes.
var digestErasureStatuses = []string{digestActive, digestCandidate, digestSuperseded, digestRejected, digestStale}

func eraseMemoryFromDerived(ctx context.Context, tx Tx, scope Scope, memoryID, actor string) error {
	lister := listerOf(ctx, tx)
	if lister == nil {
		return ErrUnavailable
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := eraseMemoryFromDigests(ctx, tx, lister, scope, memoryID, actor, now); err != nil {
		return err
	}
	erased, err := eraseMemoryFromObservations(ctx, tx, lister, scope, memoryID, actor, now)
	if err != nil {
		return err
	}
	return eraseFromMentalModels(ctx, tx, lister, scope, append(erased, memoryID), actor, now)
}

func eraseMemoryFromDigests(ctx context.Context, tx Tx, lister statusLister, scope Scope, memoryID, actor string, now time.Time) error {
	for _, col := range []string{colTopicDigests, colUserDigests} {
		for _, status := range digestErasureStatuses {
			rows, err := lister.ListRecordsByStatus(ctx, scope, col, status, 1000)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Digest == nil || !slices.ContainsFunc(row.Digest.Members, func(r Ref) bool { return r.ID == memoryID }) {
					continue
				}
				cur, err := tx.Get(ctx, col, scope, row.ID)
				if err != nil {
					return err
				}
				if cur.Digest == nil || cur.ContentErased && cur.Text == "" && cur.Digest.Title == "" {
					continue
				}
				d := *cur.Digest
				d.Title = ""
				prev := cur.Revision
				cur.Digest, cur.Text, cur.Summary, cur.Keywords, cur.KeywordsLower = &d, "", "", nil, nil
				cur.Status, cur.ContentErased, cur.Revision, cur.UpdatedAt = digestStale, true, cur.Revision+1, now
				if err := tx.Replace(ctx, col, cur, prev); err != nil {
					return err
				}
				if err := audit(ctx, tx, scope, actor, "erase:"+col, cur.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// eraseMemoryFromObservations returns the ids of the observations it erased.
func eraseMemoryFromObservations(ctx context.Context, tx Tx, lister statusLister, scope Scope, memoryID, actor string, now time.Time) ([]string, error) {
	erased := []string{}
	for _, status := range obsStatuses {
		rows, err := lister.ListRecordsByStatus(ctx, scope, colObservations, status, obsListLimit)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Observation == nil || !namesMemory(row.Observation.SourceMemories, memoryID) {
				continue
			}
			cur, err := tx.Get(ctx, colObservations, scope, row.ID)
			if err != nil {
				return nil, err
			}
			erased = append(erased, cur.ID)
			if cur.Observation == nil || cur.ContentErased && cur.Text == "" {
				continue
			}
			o := *cur.Observation
			o.History, _ = scrubRevisions(o.History, nil)
			o.DurabilityReason = ""
			prev := cur.Revision
			cur.Observation, cur.Text, cur.Summary, cur.Keywords, cur.KeywordsLower, cur.Embedding = &o, "", "", nil, nil, nil
			if cur.Status != obsDeleted { // a deleted one stays deleted, so no refresh revives it
				cur.Status = obsStale
			}
			cur.ContentErased, cur.Revision, cur.UpdatedAt = true, cur.Revision+1, now
			if err := tx.Replace(ctx, colObservations, cur, prev); err != nil {
				return nil, err
			}
			if err := audit(ctx, tx, scope, actor, "erase:"+colObservations, cur.ID); err != nil {
				return nil, err
			}
		}
	}
	return erased, nil
}

// scrubRevisions blanks the text of revisions citing any of ids (every revision when ids is nil).
func scrubRevisions(history []DerivedRevision, ids []string) ([]DerivedRevision, bool) {
	out, changed := slices.Clone(history), false
	for i, rev := range out {
		if rev.Text != "" && (ids == nil || slices.ContainsFunc(rev.Citations, func(r Ref) bool { return slices.Contains(ids, r.ID) })) {
			out[i].Text, out[i].DurabilityReason, changed = "", "", true
		}
	}
	return out, changed
}

func eraseFromMentalModels(ctx context.Context, tx Tx, lister statusLister, scope Scope, ids []string, actor string, now time.Time) error {
	rows, err := lister.ListRecordsByStatus(ctx, scope, colMentalModels, statusActive, mmListLimit)
	if err != nil {
		return err
	}
	for _, row := range rows {
		cur, err := tx.Get(ctx, colMentalModels, scope, row.ID)
		if err != nil {
			return err
		}
		if cur.MentalModel == nil {
			continue
		}
		m := *cur.MentalModel
		changed := false
		m.History, changed = scrubRevisions(m.History, ids)
		for _, rev := range []**DerivedRevision{&m.Active, &m.Draft} {
			if *rev != nil {
				if _, cited := scrubRevisions([]DerivedRevision{**rev}, ids); cited {
					*rev, changed = nil, true
				}
			}
		}
		if !changed {
			continue
		}
		prev := cur.Revision
		cur.MentalModel, cur.Revision, cur.UpdatedAt = &m, cur.Revision+1, now
		if err := tx.Replace(ctx, colMentalModels, cur, prev); err != nil {
			return err
		}
		if err := audit(ctx, tx, scope, actor, "erase:"+colMentalModels, cur.ID); err != nil {
			return err
		}
	}
	return nil
}

// eraseCitedFromMentalModels scrubs mental-model text citing a memory or an
// observation an erasure job deleted: mental models keep no sources of their
// own, so the job's source match never reaches them.
func eraseCitedFromMentalModels(ctx context.Context, tx Tx, scope Scope, ref Ref, actor string) error {
	if ref.Collection != colMemories && ref.Collection != colObservations {
		return nil
	}
	lister := listerOf(ctx, tx)
	if lister == nil {
		return ErrUnavailable
	}
	return eraseFromMentalModels(ctx, tx, lister, scope, []string{ref.ID}, actor, time.Now().UTC().Truncate(time.Millisecond))
}
