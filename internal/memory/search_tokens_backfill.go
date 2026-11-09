package memory

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// BackfillSearchTokens rebuilds derived tokens using the current normalization.
// It preserves canonical payload/revision, uses the complete tenant key, and
// reports pending rows skipped by revision races or inserted behind its cursor.
func (p *Postgres) BackfillSearchTokens(ctx context.Context, budget time.Duration) (int, bool, error) {
	deadline := time.Now().Add(budget)
	filled := 0
	last := [6]string{}
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return filled, true, nil
		}
		rows, err := p.q().Query(ctx, `SELECT collection,payload FROM memory_records WHERE `+searchTokensPending+` AND (collection,id,workspace_id,project_id,user_id,agent_id)>($1,$2,$3,$4,$5,$6) ORDER BY collection,id,workspace_id,project_id,user_id,agent_id LIMIT $7`, last[0], last[1], last[2], last[3], last[4], last[5], searchTokensBackfillBatch)
		if err != nil {
			return filled, true, pgErr(err)
		}
		var b searchTokenBatch
		if last, err = b.read(rows, last); err != nil {
			return filled, true, err
		}
		if len(b.ids) == 0 {
			var pending bool
			err = p.q().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_records WHERE `+searchTokensPending+`)`).Scan(&pending)
			return filled, pending, pgErr(err)
		}
		tag, err := p.q().Exec(ctx, `UPDATE memory_records m SET search_tokens=v.t::tsvector,search_context_indexed=true,search_tokens_version=2 FROM unnest($1::text[],$2::text[],$3::text[],$4::text[],$5::text[],$6::text[],$7::bigint[],$8::text[]) AS v(c,i,w,p,u,a,r,t) WHERE m.collection=v.c AND m.id=v.i AND m.workspace_id=v.w AND m.project_id=v.p AND m.user_id=v.u AND m.agent_id=v.a AND m.revision=v.r AND (`+searchTokensPending+`)`, b.cols, b.ids, b.workspaces, b.projects, b.users, b.agents, b.revs, b.vecs)
		if err != nil {
			return filled, true, pgErr(err)
		}
		filled += int(tag.RowsAffected())
	}
}

// searchTokenBatch is one page of rows to refill, as the parallel arrays the UPDATE unnests.
type searchTokenBatch struct {
	cols, ids, workspaces, projects, users, agents, vecs []string
	revs                                                 []int64
}

// read drains and closes rows and returns the keyset position of the last row read.
func (b *searchTokenBatch) read(rows pgx.Rows, last [6]string) ([6]string, error) {
	for rows.Next() {
		var col string
		var payload []byte
		if err := rows.Scan(&col, &payload); err != nil {
			rows.Close()
			return last, pgErr(err)
		}
		r, e := pgDecode(payload)
		if e != nil {
			rows.Close()
			return last, ErrUnavailable
		}
		vector, ok := pgSearchTokens(col, r).(string)
		if !ok {
			rows.Close()
			return last, ErrUnavailable
		}
		b.cols = append(b.cols, col)
		b.ids = append(b.ids, r.ID)
		b.workspaces = append(b.workspaces, r.Scope.WorkspaceID)
		b.projects = append(b.projects, r.Scope.ProjectID)
		b.users = append(b.users, r.Scope.UserID)
		b.agents = append(b.agents, r.Scope.AgentID)
		b.revs = append(b.revs, r.Revision)
		b.vecs = append(b.vecs, vector)
		last = [6]string{col, r.ID, r.Scope.WorkspaceID, r.Scope.ProjectID, r.Scope.UserID, r.Scope.AgentID}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return last, pgErr(err)
	}
	return last, nil
}
