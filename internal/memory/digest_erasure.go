package memory

import (
	"context"
	"slices"
	"time"
)

// Erasure of digests. A digest copies text from its member memories, so:
//   - deleting a memory clears, in the same transaction, the title and body of
//     every digest (any status) that lists it, and marks it stale and erased;
//   - erasing a source deletes the digests that carry it (sourceErasureCollections),
//     since a digest keeps its members' sources.
var digestErasureStatuses = []string{digestActive, digestCandidate, digestSuperseded, digestRejected, digestStale}

func eraseMemoryFromDigests(ctx context.Context, tx Tx, scope Scope, memoryID, actor string) error {
	lister, ok := tx.(statusLister)
	if !ok {
		lister, ok = ctx.Value(digestRepoKey{}).(statusLister)
	}
	if !ok {
		return ErrUnavailable
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
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
