package memory

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

var privacyLimitSuffix = regexp.MustCompile(` LIMIT \$[0-9]+$`)

// Legacy callers retain their fail-closed limit. Complete discovery uses a
// transaction-local cursor so each wire/database fetch contains at most 1000 rows.
func (p *Postgres) privacyImpactQuery(ctx context.Context, limit int, query string, args ...any) (pgx.Rows, error) {
	if limit > 0 {
		return p.q().Query(ctx, query, args...)
	}
	if p.tx == nil || !privacyLimitSuffix.MatchString(query) || len(args) == 0 {
		return nil, ErrInvalid
	}
	query = privacyLimitSuffix.ReplaceAllString(query, "")
	name := pgx.Identifier{"privacy_" + ID()}.Sanitize()
	if _, err := p.tx.Exec(ctx, "DECLARE "+name+" NO SCROLL CURSOR FOR "+query, args[:len(args)-1]...); err != nil {
		return nil, err
	}
	out := &privacyCursor{ctx: ctx, tx: p.tx, name: name}
	if err := out.fetch(); err != nil {
		out.Close()
		return nil, err
	}
	return out, nil
}

type privacyCursor struct {
	pgx.Rows
	ctx    context.Context
	tx     pgx.Tx
	name   string
	count  int
	err    error
	closed bool
}

func (r *privacyCursor) fetch() error {
	r.count = 0
	r.Rows, r.err = r.tx.Query(r.ctx, "FETCH FORWARD 1000 FROM "+r.name)
	return r.err
}
func (r *privacyCursor) Next() bool {
	if r.closed || r.err != nil {
		return false
	}
	for {
		if r.Rows.Next() {
			r.count++
			return true
		}
		r.err = r.Rows.Err()
		r.Rows.Close()
		if r.err != nil || r.count < 1000 {
			return false
		}
		if r.fetch() != nil {
			return false
		}
	}
}
func (r *privacyCursor) Err() error {
	if r.err != nil {
		return r.err
	}
	if r.Rows != nil {
		return r.Rows.Err()
	}
	return nil
}
func (r *privacyCursor) Close() {
	if r.closed {
		return
	}
	r.closed = true
	if r.Rows != nil {
		r.Rows.Close()
	}
	// Transaction rollback closes it even if cancellation prevents CLOSE.
	_, _ = r.tx.Exec(r.ctx, "CLOSE "+r.name)
}

func (p *Postgres) selectPrivacyRecords(ctx context.Context, collection string, scope Scope, where string, args []any, order string, limit int) ([]Record, error) {
	if limit > 0 {
		return p.selectRecords(ctx, collection, scope, where, args, order, limit+1)
	}
	query := "SELECT payload FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND (" + where + ") ORDER BY " + order + " LIMIT $" + fmt.Sprint(len(args)+6)
	params := []any{collection, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID}
	params = append(params, args...)
	params = append(params, 0)
	rows, err := p.privacyImpactQuery(ctx, 0, query, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			return nil, ErrUnavailable
		}
		r, e := pgDecode(raw)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, pgErr(rows.Err())
}
