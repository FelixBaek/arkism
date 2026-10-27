package memory

import (
	"context"
	"slices"
	"time"
)

// Stale cascade: when a person supersedes memory A, what was written from A
// is marked in the same transaction, so it stops passing as current:
//   - topic and user digests (active or candidate) that list A become stale
//     with the body removed, as the sweep would do later; a user digest also
//     asks for a new version, and a topic's members are re-clustered, so the
//     worker proposes a fresh candidate for a person to decide;
//   - lesson→skill proposals that cite A keep their status and get the mark,
//     visible wherever the proposal is reviewed;
//   - supports relations naming A keep serving the graph with the mark.
//
// Nothing is deleted or approved here (ADR-0022).

// StaleMark says a record was written from a memory a person has since replaced.
type StaleMark struct {
	Memory     Ref       `json:"memory" bson:"memory"`
	ReplacedBy Ref       `json:"replaced_by" bson:"replaced_by"`
	At         time.Time `json:"at" bson:"at"`
}

// listerOf lists by status inside a transaction: the transaction itself, or
// the repository the service bound to the context (in-memory test store).
func listerOf(ctx context.Context, tx Tx) statusLister {
	if l, ok := tx.(statusLister); ok {
		return l
	}
	l, _ := ctx.Value(digestRepoKey{}).(statusLister)
	return l
}

func staleCascade(ctx context.Context, tx Tx, old, next Record, actor string, now time.Time) error {
	lister := listerOf(ctx, tx)
	if lister == nil {
		return ErrUnavailable
	}
	mark := &StaleMark{Memory: Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}, ReplacedBy: Ref{Collection: colMemories, ID: next.ID, Revision: next.Revision}, At: now}
	if err := staleDigests(ctx, tx, lister, old.Scope, mark, actor); err != nil {
		return err
	}
	if err := staleSkillProposals(ctx, tx, lister, old.Scope, mark, actor); err != nil {
		return err
	}
	return staleSupports(ctx, tx, old.Scope, mark, actor)
}

func namesMemory(refs []Ref, id string) bool {
	return slices.ContainsFunc(refs, func(r Ref) bool { return r.ID == id })
}

func staleDigests(ctx context.Context, tx Tx, lister statusLister, scope Scope, mark *StaleMark, actor string) error {
	userGone := false
	for _, col := range []string{colTopicDigests, colUserDigests} {
		for _, status := range []string{digestActive, digestCandidate} {
			rows, err := lister.ListRecordsByStatus(ctx, scope, col, status, digestListLimit)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Digest == nil || row.Kind == digestKindState || !namesMemory(row.Digest.Members, mark.Memory.ID) {
					continue
				}
				if err := markStale(ctx, tx, col, scope, row.ID, mark, actor, func(r *Record) { r.Status, r.Text = digestStale, "" }); err != nil {
					return err
				}
				userGone = userGone || col == colUserDigests
			}
		}
	}
	if !userGone {
		return nil
	}
	return requestUserDigest(ctx, tx, scope, mark.At)
}

func staleSkillProposals(ctx context.Context, tx Tx, lister statusLister, scope Scope, mark *StaleMark, actor string) error {
	for _, status := range []string{"candidate", "approved", statusActive} {
		rows, err := lister.ListRecordsByStatus(ctx, scope, colSkillVersions, status, digestListLimit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Kind != proposedSkillKind || !namesMemory(row.Relations, mark.Memory.ID) {
				continue
			}
			if err := markStale(ctx, tx, colSkillVersions, scope, row.ID, mark, actor, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func staleSupports(ctx context.Context, tx Tx, scope Scope, mark *StaleMark, actor string) error {
	for after := ""; ; {
		refs, err := tx.MemoryErasureBatch(ctx, scope, mark.Memory.ID, false, after, memoryRelationErasureKind)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			r, err := tx.Get(ctx, "memory_relations", scope, ref.ID)
			if err != nil {
				return err
			}
			if r.Kind == GraphSupports && r.Status == statusActive {
				if err := markStale(ctx, tx, "memory_relations", scope, r.ID, mark, actor, nil); err != nil {
					return err
				}
			}
			after = ref.ID
		}
		if len(refs) < sourceMemoryErasureBatch {
			return nil
		}
	}
}

// markStale re-reads the row in the transaction, applies the mark (and change), and audits it.
func markStale(ctx context.Context, tx Tx, col string, scope Scope, id string, mark *StaleMark, actor string, change func(*Record)) error {
	cur, err := tx.Get(ctx, col, scope, id)
	if err != nil {
		return err
	}
	if cur.StaleMark != nil && cur.StaleMark.Memory.ID == mark.Memory.ID {
		return nil
	}
	prev := cur.Revision
	cur.StaleMark, cur.Revision, cur.UpdatedAt = mark, cur.Revision+1, mark.At
	if change != nil {
		change(&cur)
	}
	if err := tx.Replace(ctx, col, cur, prev); err != nil {
		return err
	}
	return audit(ctx, tx, scope, actor, "stale:"+col, id)
}

// requestUserDigest asks the worker for a new user summary when none is pending (as RegenerateDigest does).
func requestUserDigest(ctx context.Context, tx Tx, scope Scope, now time.Time) error {
	state, err := digestStateRecord(ctx, tx, colUserDigests, scope)
	if err != nil {
		return err
	}
	st := *state.Digest
	if st.Pending != "" {
		return nil
	}
	st.Pending = "requested"
	return saveDigestState(ctx, tx, colUserDigests, state, st, now)
}
