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
	if err = eraseDreamDerived(ctx, tx, lister, scope, memoryID, actor, now); err != nil {
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
				if err := eraseMemoryFromDigest(ctx, tx, col, row, scope, memoryID, actor, now); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// eraseMemoryFromDigest clears one listed digest row when it names memoryID.
func eraseMemoryFromDigest(ctx context.Context, tx Tx, col string, row Record, scope Scope, memoryID, actor string, now time.Time) error {
	if row.Digest == nil || !slices.ContainsFunc(row.Digest.Members, func(r Ref) bool { return r.ID == memoryID }) {
		return nil
	}
	cur, err := tx.Get(ctx, col, scope, row.ID)
	if err != nil {
		return err
	}
	if cur.Digest == nil || cur.ContentErased && cur.Text == "" && cur.Digest.Title == "" {
		return nil
	}
	d := *cur.Digest
	d.Title = ""
	prev := cur.Revision
	cur.Digest, cur.Text, cur.Summary, cur.Keywords, cur.KeywordsLower = &d, "", "", nil, nil
	cur.Status, cur.ContentErased, cur.Revision, cur.UpdatedAt = digestStale, true, cur.Revision+1, now
	if err := tx.Replace(ctx, col, cur, prev); err != nil {
		return err
	}
	return audit(ctx, tx, scope, actor, "erase:"+col, cur.ID)
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
			if err := eraseObservationText(ctx, tx, scope, cur, actor, now); err != nil {
				return nil, err
			}
		}
	}
	return erased, nil
}

// eraseObservationText blanks one observation's text; one already erased is left alone.
func eraseObservationText(ctx context.Context, tx Tx, scope Scope, cur Record, actor string, now time.Time) error {
	if cur.Observation == nil || cur.ContentErased && cur.Text == "" {
		return nil
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
		return err
	}
	return audit(ctx, tx, scope, actor, "erase:"+colObservations, cur.ID)
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
		if dropCitedRevisions(&m, ids) {
			changed = true
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

// dropCitedRevisions clears the active and draft revisions citing any of ids and reports whether one was cleared.
func dropCitedRevisions(m *MentalModelData, ids []string) bool {
	changed := false
	for _, rev := range []**DerivedRevision{&m.Active, &m.Draft} {
		if *rev != nil {
			if _, cited := scrubRevisions([]DerivedRevision{**rev}, ids); cited {
				*rev, changed = nil, true
			}
		}
	}
	return changed
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

// dreamDerivedStatuses: every status a memory a dream merged or derived, or its review entry, can hold.
var dreamDerivedStatuses = []struct{ col, status string }{
	{colMemories, statusActive}, {colMemories, "superseded"}, {colMemories, "expired"}, {colMemories, "rejected"},
	{"memory_candidates", "candidate"}, {"memory_candidates", "approved"}, {"memory_candidates", "rejected"},
}

// eraseDreamDerived blanks every memory and review entry a dream merged or derived from the erased
// memory, and in turn what it derived from those, so the erased text survives in none of them.
func eraseDreamDerived(ctx context.Context, tx Tx, lister statusLister, scope Scope, memoryID, actor string, now time.Time) error {
	queue, done := []string{memoryID}, map[string]bool{memoryID: true}
	for len(queue) > 0 {
		derived, err := dreamDerivedOf(ctx, lister, scope, queue[0])
		if err != nil {
			return err
		}
		queue = queue[1:]
		for _, ref := range derived {
			if err = eraseDerivedText(ctx, tx, scope, ref, actor, now); err != nil {
				return err
			}
			if !done[ref.ID] {
				done[ref.ID], queue = true, append(queue, ref.ID)
			}
		}
	}
	return nil
}

// dreamDerivedOf lists the rows that name id as one they were derived from.
// shortcut: reads the newest 1000 rows per status, as digest erasure does; query by the field if a scope outgrows it.
func dreamDerivedOf(ctx context.Context, lister statusLister, scope Scope, id string) ([]Ref, error) {
	out := []Ref{}
	for _, cs := range dreamDerivedStatuses {
		rows, err := lister.ListRecordsByStatus(ctx, scope, cs.col, cs.status, 1000)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if slices.Contains(row.DreamDerivedFrom, id) {
				out = append(out, Ref{Collection: cs.col, ID: row.ID})
			}
		}
	}
	return out, nil
}

// eraseDerivedText blanks one derived row and takes it out of use; one already erased is left alone.
func eraseDerivedText(ctx context.Context, tx Tx, scope Scope, ref Ref, actor string, now time.Time) error {
	cur, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
	if err != nil || cur.ContentErased && cur.Text == "" {
		return err
	}
	prev := cur.Revision
	cur.Text, cur.Summary, cur.Keywords, cur.KeywordsLower, cur.Embedding, cur.Entities, cur.EntityRelations = "", "", nil, nil, nil, nil, nil
	cur.Status, cur.ContentErased, cur.Revision, cur.UpdatedAt = "rejected", true, prev+1, now
	if err = tx.Replace(ctx, ref.Collection, cur, prev); err != nil {
		return err
	}
	return audit(ctx, tx, scope, actor, "erase:dream_derived:"+ref.Collection, cur.ID)
}
