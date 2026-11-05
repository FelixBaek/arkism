package memory

import "context"

type catalogRecordLister interface {
	ListCatalogRecords(context.Context, Scope, string, string, int) ([]Record, error)
}

// All-scope means all records within this exact scope, never an unscoped scan.
func collectCatalogRecords(ctx context.Context, repo Repository, scope Scope, collection string) ([]Record, error) {
	rows := []Record{}
	if err := eachCatalogRecord(ctx, repo, scope, collection, func(r Record) error { rows = append(rows, r); return nil }); err != nil {
		return nil, err
	}
	return rows, nil
}

// eachCatalogRecord hands every record of one exact scope to fn in id order, one page in memory at a time.
func eachCatalogRecord(ctx context.Context, repo Repository, scope Scope, collection string, fn func(Record) error) error {
	if repo == nil {
		return ErrUnavailable
	}
	lister, ok := repo.(catalogRecordLister)
	if !ok {
		rows, err := repo.ListRecords(ctx, scope, collection, 1000)
		if err == nil && len(rows) >= 1000 {
			return ErrUnavailable
		}
		return eachRecord(rows, err, fn)
	}
	for after := ""; ; {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		rows, err := lister.ListCatalogRecords(ctx, scope, collection, after, 500)
		if err != nil {
			return err
		}
		// catalogRecordWindow refuses a page that does not move past after, so this ends.
		page, next, err := catalogRecordWindow(rows, scope, after)
		if err = eachRecord(page, err, fn); err != nil || next == "" {
			return err
		}
		after = next
	}
}

func eachRecord(rows []Record, err error, fn func(Record) error) error {
	for i := 0; err == nil && i < len(rows); i++ {
		err = fn(rows[i])
	}
	return err
}
func catalogRecordWindow(rows []Record, scope Scope, after string) ([]Record, string, error) {
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
