package memory

import (
	"context"
	"fmt"
)

func (p *Postgres) independentOutcomeCounts(ctx context.Context, in MemoryOutcomeQuery) ([]MemoryOutcomeCount, error) {
	in, _, err := normalizeMemoryOutcomeQuery(in, false)
	if err != nil {
		return nil, err
	}
	base := in
	base.From, base.To = nil, nil
	w, args := memoryOutcomeWhere(base)
	params := append([]any{"evaluations", in.Scope.WorkspaceID, in.Scope.ProjectID, in.Scope.UserID, in.Scope.AgentID}, args...)
	where := "position=1"
	if in.From != nil {
		params = append(params, *in.From)
		where += fmt.Sprintf(" AND judged >= $%d", len(params))
	}
	if in.To != nil {
		params = append(params, *in.To)
		where += fmt.Sprintf(" AND judged < $%d", len(params))
	}
	query := `WITH eligible AS (
 SELECT id,doc,` + pgDate + ` AS judged,
 (SELECT ref->>'id' FROM jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'relations')='array' THEN doc->'relations' ELSE '[]'::jsonb END) ref WHERE ref->>'collection'='execution_traces' LIMIT 1) AS trace
 FROM memory_records WHERE collection=$1 AND workspace_id=$2 AND project_id=$3 AND user_id=$4 AND agent_id=$5 AND (` + w + `) AND COALESCE(doc->>'memory_outcome_content_hash','')<>''
 ), ranked AS (
 SELECT doc,judged,row_number() OVER (PARTITION BY doc->'target'->>'id',doc->>'memory_outcome_content_hash',CASE WHEN doc->'tags' ? 'dimension:personal_compliance' THEN 'personal_compliance' ELSE 'work_task' END,CASE WHEN COALESCE(doc->>'memory_outcome_task_hash','')<>'' AND doc->'memory_outcome_completed_at'->>'$date' IS NOT NULL THEN 'task:' || (doc->>'memory_outcome_task_hash') ELSE 'trace:' || trace END ORDER BY COALESCE((doc->'memory_outcome_completed_at'->>'$date')::timestamptz,judged),judged,id) AS position
 FROM eligible WHERE COALESCE(trace,'')<>''
 ) SELECT doc->'target'->>'id',count(*),count(*) FILTER (WHERE (doc->>'passed')::boolean),max(judged) FROM ranked WHERE ` + where + ` GROUP BY 1 ORDER BY 1`
	rows, err := p.q().Query(ctx, query, params...)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	out := []MemoryOutcomeCount{}
	for rows.Next() {
		var c MemoryOutcomeCount
		if err = rows.Scan(&c.MemoryID, &c.Total, &c.Passed, &c.Last); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, c)
	}
	return out, pgErr(rows.Err())
}
