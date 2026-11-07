package memory

import "context"

// Keyset pages remain stable while a cascade changes status/updated_at.
type statusPageLister interface {
	StatusRecordsAfter(context.Context, Scope, string, string, string, int) ([]Record, error)
}

func walkStatusRows(ctx context.Context, lister statusLister, scope Scope, col, status string, visit func(Record) error) error {
	if pager, ok := lister.(statusPageLister); ok {
		for after := ""; ; {
			rows, e := pager.StatusRecordsAfter(ctx, scope, col, status, after, 100)
			if e != nil {
				return e
			}
			for _, r := range rows {
				if r.ID <= after {
					return ErrUnavailable
				}
				after = r.ID
				if e = visit(r); e != nil {
					return e
				}
			}
			if len(rows) < 100 {
				return nil
			}
		}
	}
	// Older repository implementations must never silently report a truncated
	// cascade as complete. The transaction rolls back when the cap is reached.
	rows, e := lister.ListRecordsByStatus(ctx, scope, col, status, 1000)
	if e != nil {
		return e
	}
	if len(rows) >= 1000 {
		return ErrUnavailable
	}
	for _, r := range rows {
		if e = visit(r); e != nil {
			return e
		}
	}
	return nil
}
func (p *Postgres) StatusRecordsAfter(ctx context.Context, scope Scope, col, status, after string, limit int) ([]Record, error) {
	if !knownCollection(col) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, col, scope, `status=$6 AND id>$7`, []any{status, after}, "id ASC", limit)
}
