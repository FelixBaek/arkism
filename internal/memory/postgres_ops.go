package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// pgOpsWhere collects numbered predicates after the five scope parameters
// ($1 collection, $2..$5 scope) that selectRecords and OpsCounts bind.
type pgOpsWhere struct {
	clauses []string
	args    []any
}

func (w *pgOpsWhere) add(clause string, values ...any) {
	for _, v := range values {
		w.args = append(w.args, v)
		clause = strings.Replace(clause, "$?", fmt.Sprintf("$%d", len(w.args)+5), 1)
	}
	w.clauses = append(w.clauses, clause)
}

func (w *pgOpsWhere) sql() string { return strings.Join(w.clauses, " AND ") }

func (w *pgOpsWhere) common(statuses []string, from, to time.Time) {
	if len(statuses) > 0 {
		w.add(`status=ANY($?)`, statuses)
	}
	if !from.IsZero() {
		w.add(pgDate+` >= $?`, from)
	}
	if !to.IsZero() {
		w.add(pgDate+` < $?`, to)
	}
}

func opsRecordWhere(q opsRecordQuery) *pgOpsWhere {
	w := &pgOpsWhere{clauses: []string{"true"}}
	w.common(q.Statuses, q.From, q.To)
	if q.Kind != "" {
		w.add(`kind=$?`, q.Kind)
	}
	if q.KindPrefix != "" {
		w.add(`left(kind, length($?))=$?`, q.KindPrefix, q.KindPrefix)
	}
	if q.Actor != "" {
		w.add(`doc->>'created_by'=$?`, q.Actor)
	}
	if q.TargetID != "" {
		w.add(`doc->'target'->>'id'=$?`, q.TargetID)
	}
	if q.Tag != "" {
		w.add(`doc->'tags' ? $?`, q.Tag)
	}
	if q.After != nil {
		w.add(`(`+pgDate+` < $? OR (`+pgDate+` = $? AND id < $?))`, q.After.At, q.After.At, q.After.ID)
	}
	return w
}

func (p *Postgres) OpsRecords(ctx context.Context, q opsRecordQuery) ([]Record, error) {
	if !knownCollection(q.Collection) || q.Limit < 1 || q.Limit > tagRenameBatch+1 {
		return nil, ErrInvalid
	}
	w := opsRecordWhere(q)
	return p.selectRecords(ctx, q.Collection, q.Scope, w.sql(), w.args, pgDate+` DESC, id DESC`, q.Limit)
}

var opsKeyExpr = map[string]string{
	"":       `''`,
	"kind":   `kind`,
	"status": `status`,
	"source": `COALESCE(NULLIF(doc->>'origin_product',''),'unknown')`,
	"tag":    `t.tag`,
}

var opsBucketExpr = map[string]string{
	"":     `''`,
	"day":  `to_char(date_trunc('day', ` + pgDate + ` AT TIME ZONE 'UTC'),'YYYY-MM-DD')`,
	"week": `to_char(date_trunc('week', ` + pgDate + ` AT TIME ZONE 'UTC'),'YYYY-MM-DD')`,
}

func (p *Postgres) OpsCounts(ctx context.Context, q opsCountQuery) ([]OpsCount, error) {
	key, okKey := opsKeyExpr[q.By]
	bucket, okBucket := opsBucketExpr[q.Bucket]
	if checkScope(q.Scope) != nil || !knownCollection(q.Collection) || !okKey || !okBucket {
		return nil, ErrInvalid
	}
	from := `memory_records`
	if q.By == "tag" {
		from += ` CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(doc->'tags')='array' THEN doc->'tags' ELSE '[]'::jsonb END) AS t(tag)`
	}
	w := &pgOpsWhere{clauses: []string{"true"}}
	w.common(q.Statuses, q.From, q.To)
	query := `SELECT ` + bucket + `, ` + key + `, count(*) FROM ` + from + ` WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND (` + w.sql() + `) GROUP BY 1,2 ORDER BY 1,2 LIMIT ` + fmt.Sprint(opsCountRows)
	params := append([]any{q.Collection, q.Scope.WorkspaceID, q.Scope.ProjectID, q.Scope.UserID, q.Scope.AgentID}, w.args...)
	rows, err := p.q().Query(ctx, query, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []OpsCount{}
	for rows.Next() {
		var c OpsCount
		if err = rows.Scan(&c.Bucket, &c.Key, &c.Count); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, c)
	}
	return out, pgErr(rows.Err())
}
