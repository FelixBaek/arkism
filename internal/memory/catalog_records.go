package memory

import "context"

type catalogRecordLister interface {
	ListCatalogRecords(context.Context, Scope, string, string, int) ([]Record, error)
}

// All-scope means all records within this exact scope, never an unscoped scan.
func collectCatalogRecords(ctx context.Context, repo Repository, scope Scope, collection string) ([]Record, error) {
	if repo == nil {
		return nil, ErrUnavailable
	}
	lister, ok := repo.(catalogRecordLister)
	if !ok {
		rows, err := repo.ListRecords(ctx, scope, collection, 1000)
		if err == nil && len(rows) >= 1000 {
			return nil, ErrUnavailable
		}
		return rows, err
	}
	return collectCatalogPages(ctx, func(after string) ([]Record, string, error) {
		rows, err := lister.ListCatalogRecords(ctx, scope, collection, after, 500)
		if err != nil {
			return nil, "", err
		}
		if len(rows) > 501 {
			return nil, "", ErrUnavailable
		}
		previous := after
		for _, row := range rows {
			if row.Scope != scope || row.ID <= previous {
				return nil, "", ErrUnavailable
			}
			previous = row.ID
		}
		if len(rows) > 500 {
			return rows[:500], rows[499].ID, nil
		}
		return rows, "", nil
	})
}
func (p *Postgres) ListCatalogRecords(ctx context.Context, scope Scope, collection, after string, limit int) ([]Record, error) {
	if !validScope(scope) || !knownCollection(collection) || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	where := ""
	var args []any
	if after != "" {
		where = "id>$6"
		args = []any{after}
	}
	return p.selectRecords(ctx, collection, scope, where, args, "id ASC", limit+1)
}
