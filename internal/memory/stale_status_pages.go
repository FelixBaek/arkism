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

// dependentStatusLister narrows the scan before paging. The callback still checks
// the decoded typed document; SQL selection is not permission or provenance proof.
type dependentStatusLister interface {
	StaleDependentsAfter(context.Context, Scope, string, string, string, string, int) ([]Record, error)
}

type staleDependentLister struct {
	statusLister
	pager    dependentStatusLister
	memoryID string
}

func (l staleDependentLister) StatusRecordsAfter(ctx context.Context, scope Scope, col, status, after string, limit int) ([]Record, error) {
	return l.pager.StaleDependentsAfter(ctx, scope, col, status, l.memoryID, after, limit)
}
func walkStaleDependents(ctx context.Context, lister statusLister, scope Scope, col, status, memoryID string, visit func(Record) error) error {
	if pager, ok := lister.(dependentStatusLister); ok {
		lister = staleDependentLister{statusLister: lister, pager: pager, memoryID: memoryID}
	}
	return walkStatusRows(ctx, lister, scope, col, status, visit)
}
func (p *Postgres) StaleDependentsAfter(ctx context.Context, scope Scope, col, status, memoryID, after string, limit int) ([]Record, error) {
	if memoryID == "" || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	var refs string
	switch col {
	case colTopicDigests, colUserDigests:
		refs = "doc->'digest'->'members'"
	case colSkillVersions:
		refs = "doc->'relations'"
	case colObservations:
		refs = "doc->'observation'->'source_memories'"
	default:
		return nil, ErrInvalid
	}
	// Preserve namesMemory's ID-only legacy relation semantics; the typed callback
	// supplies kind and data checks. No public filter is interpolated into SQL.
	where := "status=$6 AND id>$7 AND EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(" + refs + ")='array' THEN " + refs + " ELSE '[]'::jsonb END) AS ref WHERE ref->>'id'=$8)"
	return p.selectRecords(ctx, col, scope, where, []any{status, after, memoryID}, "id ASC", limit)
}
