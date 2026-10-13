package memory

import (
	"context"
)

func searchHitHash(h Hit) string {
	h.Reasons = nil // lane/rank annotations are not stored record content.
	return ContentHash(structuredText(h))
}

// Reuse the existing eligibility validators while fencing every record they read.
// This wrapper is used only inside the transaction that records returned context.
// shortcut: shared dependencies may be fenced repeatedly; deduplicate per transaction if measured search latency requires it.
type searchReadTx struct{ Tx }

func (t searchReadTx) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	r, e := t.Tx.Get(ctx, col, scope, id)
	if e != nil {
		return Record{}, e
	}
	if e = t.Tx.FenceSearchRead(ctx, scope, col, id, r.Revision); e != nil {
		return Record{}, e
	}
	return r, nil
}
func (t searchReadTx) SourceMetadata(ctx context.Context, scope Scope, ids []string, text bool) ([]Record, error) {
	rows, e := t.Tx.SourceMetadata(ctx, scope, ids, text)
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		if e = t.Tx.FenceSource(ctx, scope, r.ID); e != nil {
			return nil, e
		}
	}
	return rows, nil
}
