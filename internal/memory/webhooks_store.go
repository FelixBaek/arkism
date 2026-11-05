package memory

import (
	"context"
	"fmt"
)

// scopedRowQuery lists one collection's rows of one kind in an exact scope,
// newest id first (webhook delivery and model trace ids sort by time), below
// Before when set, optionally one status and one doc field value.
type scopedRowQuery struct {
	Collection string
	Kind       string
	Status     string
	Scope      Scope
	Path       []string // doc path compared with Value when Value != ""
	Value      string
	Before     string
	Limit      int
}

// scopedRowStore is the optional repository part webhooks and model call
// traces need; a repository without it has neither feature.
type scopedRowStore interface {
	ListScopedRows(context.Context, scopedRowQuery) ([]Record, error)
}

func (p *Postgres) ListScopedRows(ctx context.Context, q scopedRowQuery) ([]Record, error) {
	if q.Limit < 1 || q.Limit > 1000 || q.Kind == "" || q.Value != "" && len(q.Path) == 0 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, q.Collection, q.Scope, `kind=$6 AND ($7='' OR status=$7) AND ($8='' OR id<$8) AND ($9='' OR doc #>> $10::text[] = $9)`,
		[]any{q.Kind, q.Status, q.Before, q.Value, q.Path}, "id DESC", q.Limit)
}

// timeOrderedID sorts by creation time (milliseconds) and stays unique.
func timeOrderedID(unixMilli int64) string { return fmt.Sprintf("%012x", unixMilli) + ID()[:20] }
