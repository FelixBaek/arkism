package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// A sorted UNION branch is otherwise costed as a full retrieval, which can
// choose a whole-backlog bitmap scan and sort even though claim consumes one
// row. Prefer ordered index paths only while selecting that row. Preserve a
// caller-owned transaction's setting, including when it was already off.
func (p *Postgres) preferOrderedJobClaim(ctx context.Context) (func() error, error) {
	if p.tx == nil {
		return nil, ErrInvalid
	}
	var previous string
	if err := p.q().QueryRow(ctx, "SELECT current_setting('enable_sort')").Scan(&previous); err != nil {
		return nil, pgErr(err)
	}
	if previous == "off" {
		return func() error { return nil }, nil
	}
	if _, err := p.q().Exec(ctx, "SET LOCAL enable_sort=off"); err != nil {
		return nil, pgErr(err)
	}
	restored := false
	return func() error {
		if restored {
			return nil
		}
		restored = true
		// A cancelled request must not leave a still-usable caller transaction
		// with a different preference. An aborted transaction requires rollback,
		// which also restores SET LOCAL; the original claim error takes priority.
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, err := p.q().Exec(restoreCtx, "SELECT set_config('enable_sort',$1,true)", previous)
		return pgErr(err)
	}, nil
}

// A concurrent reschedule can remain eligible but change its priority. Retry
// with a fresh snapshot rather than lease it at the old stream position. A
// caller-owned transaction receives the same transient signal for its retry.
func checkJobClaimPriority(current, snapshot *time.Time) error {
	if current == nil && snapshot == nil {
		return nil
	}
	if current == nil || snapshot == nil || !current.Equal(*snapshot) {
		return transientTransactionError{}
	}
	return nil
}

// Each kind has three ordered index streams: due waiting jobs, unscheduled
// waiting jobs, and expired leases. Merge Append can stop at the first unlocked
// job without sorting the queue or visiting unrelated kinds. Branches must not
// LIMIT or lock their heads: a locked head must not hide the next job, and losing
// candidates must remain available to other workers.
func claimJobSelectSQL(kindCount int) string {
	branches := make([]string, 0, kindCount*3)
	for i := 1; i <= kindCount; i++ {
		for _, state := range []string{
			"status IN ('pending','retry') AND run_at<=$2",
			"status IN ('pending','retry') AND run_at IS NULL",
			"status='running' AND lease_until<=$2",
		} {
			branches = append(branches, fmt.Sprintf(`(SELECT id,run_at FROM memory_records
WHERE collection='jobs' AND kind=($1::text[])[%d] AND %s
ORDER BY run_at ASC NULLS LAST,id)`, i, state))
		}
	}
	// Lock only the selected candidate by primary key. LATERAL keeps this lookup
	// parameterized. With ordered index paths, the outer LIMIT stops Merge
	// Append after the first unlocked row. Recheck the current row after a
	// concurrent commit; a stale candidate must never steal an active lease.
	return `SELECT claimed.*,ready.run_at
FROM (` + strings.Join(branches, " UNION ALL ") + ` ORDER BY run_at ASC NULLS LAST,id) AS ready
JOIN LATERAL (
SELECT job.id,job.workspace_id,job.project_id,job.user_id,job.agent_id,
job.revision,job.kind,job.status,job.attempts,job.payload,job.run_at
FROM memory_records AS job
WHERE job.collection='jobs' AND job.id=ready.id AND job.kind=ANY($1::text[]) AND
((job.status IN ('pending','retry') AND (job.run_at IS NULL OR job.run_at<=$2))
 OR (job.status='running' AND job.lease_until<=$2))
FOR UPDATE OF job SKIP LOCKED
) AS claimed ON true
ORDER BY ready.run_at ASC NULLS LAST,ready.id LIMIT 1`
}
