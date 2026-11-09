package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const legacyClaimPlanSQL = `SELECT id,workspace_id,project_id,user_id,agent_id,revision,kind,status,attempts,payload
FROM memory_records WHERE collection='jobs' AND kind=ANY($1)
AND ((status=ANY($2) AND (run_at IS NULL OR run_at<=$3)) OR (status='running' AND lease_until<=$3))
ORDER BY run_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`

// Use the real store's encoding and all normal table triggers/indexes. Only IDs
// and kind vary; no malformed payload shortcuts in the throughput fixture.
func seedQueuePlanJobs(t *testing.T, p *Postgres, prefix, kind string, count int, now time.Time) {
	t.Helper()
	seedQueuePlanState(t, p, prefix, kind, count, JobStatusPending, now.Add(-time.Hour), time.Time{})
}

func seedQueuePlanState(t *testing.T, p *Postgres, prefix, kind string, count int, status string, runAt, leaseUntil time.Time) {
	t.Helper()
	template := prefix + "template"
	queueTestJob(t, p, template, kind, status, &runAt, leaseUntil)
	schemaExec(t, p, `INSERT INTO memory_records(collection,id,workspace_id,project_id,user_id,agent_id,revision,status,kind,updated_at,run_at,lease_until,payload,doc)
SELECT 'jobs',new_id,workspace_id,project_id,user_id,agent_id,revision,status,kind,updated_at,run_at,lease_until,
convert_to(new_doc::text,'UTF8'),new_doc FROM memory_records AS original
CROSS JOIN generate_series(1,$2::integer) AS n
CROSS JOIN LATERAL (SELECT $3::text||lpad(n::text,8,'0') AS new_id) AS identity
CROSS JOIN LATERAL (SELECT jsonb_set(original.doc,'{_id}',to_jsonb(new_id)) AS new_doc) AS encoded
WHERE original.collection='jobs' AND original.id=$1`, template, count-1, prefix)
}

type queuePlan struct {
	Plan          map[string]any `json:"Plan"`
	ExecutionTime float64        `json:"Execution Time"`
	PlanningTime  float64        `json:"Planning Time"`
}

func readQueuePlan(t *testing.T, p *Postgres, label, sql string, args ...any) queuePlan {
	t.Helper()
	var raw []byte
	ctx := context.Background()
	var err error
	if strings.HasPrefix(sql, "SELECT claimed.*") {
		err = p.transactionWithIsolation(ctx, pgx.ReadCommitted, func(ctx context.Context, tx Tx) (queryErr error) {
			child := tx.(*Postgres)
			restore, e := child.preferOrderedJobClaim(ctx)
			if e != nil {
				return e
			}
			defer func() {
				if e := restore(); queryErr == nil {
					queryErr = e
				}
			}()
			return child.q().QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+sql, append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw)
		})
	} else {
		err = p.q().QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+sql, args...).Scan(&raw)
	}
	if err != nil {
		t.Fatal(err)
	}
	return decodeQueuePlan(t, label, raw)
}

func decodeQueuePlan(t *testing.T, label string, raw []byte) queuePlan {
	t.Helper()
	var plans []queuePlan
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode plan: %v", err)
	}
	if dir := os.Getenv("ARKISM_GOAL5_ARTIFACT_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, label+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan := plans[0]
	t.Logf("plan=%s execution_ms=%.3f planning_ms=%.3f shared_hit=%v shared_read=%v", label, plan.ExecutionTime, plan.PlanningTime, plan.Plan["Shared Hit Blocks"], plan.Plan["Shared Read Blocks"])
	return plan
}

func assertQueueOrderedPlan(t *testing.T, plan queuePlan, wantIndex string) {
	t.Helper()
	found := false
	var visit func(map[string]any)
	visit = func(node map[string]any) {
		kind, _ := node["Node Type"].(string)
		if kind == "Sort" || kind == "Incremental Sort" || strings.HasPrefix(kind, "Bitmap") || kind == "Seq Scan" {
			t.Errorf("queue plan has unbounded %s", kind)
		}
		if node["Index Name"] == wantIndex {
			found = true
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				visit(child.(map[string]any))
			}
		}
	}
	visit(plan.Plan)
	if !found {
		t.Errorf("missing ordered index %s", wantIndex)
	}
	if plan.ExecutionTime > 2000 {
		t.Errorf("queue select exceeded 2-second regression ceiling: %.3fms", plan.ExecutionTime)
	}
}

func genericQueuePlan(t *testing.T, p *Postgres, label, sql, types, values string) queuePlan {
	t.Helper()
	ctx := context.Background()
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SET plan_cache_mode=force_generic_plan"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "RESET plan_cache_mode")
	if strings.HasPrefix(sql, "SELECT claimed.*") {
		if _, err = conn.Exec(ctx, "SET enable_sort=off"); err != nil {
			t.Fatal(err)
		}
		defer conn.Exec(ctx, "RESET enable_sort")
	}
	if _, err = conn.Exec(ctx, "PREPARE queue_plan ("+types+") AS "+sql); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DEALLOCATE queue_plan")
	var raw []byte
	if err = conn.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) EXECUTE queue_plan("+values+")").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return decodeQueuePlan(t, label, raw)
}

func TestPostgresJobQueueLargeMixedKindPlansAndConcurrency(t *testing.T) {
	p := isolatedPostgres(t)
	cfg := p.pool.Config().Copy()
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.pool.Close()
	p.pool = pool
	now := time.Now().UTC().Truncate(time.Millisecond)
	kinds := append([]string{"embedding", webhookDeliveryKind, autoDeriveJobKind, deriveScheduleKind, captureMergeGateKind, approvedEntitiesJobKind}, erasureJobKinds...)
	seedQueuePlanJobs(t, p, "other-small-", "memory_evaluation", 1000, now)
	seedQueuePlanJobs(t, p, "target-", "embedding", 2000, now)
	schemaExec(t, p, "ANALYZE memory_records")
	small := readQueuePlan(t, p, "claim-small-after", claimJobSelectSQL(len(kinds)), kinds, now)
	assertQueueOrderedPlan(t, small, "memory_records_jobs_waiting_order")
	seedQueuePlanJobs(t, p, "other-large-", "memory_evaluation", 49000, now)
	schemaExec(t, p, "DROP INDEX memory_records_jobs_waiting_order; DROP INDEX memory_records_jobs_running_order; DROP INDEX memory_records_jobs_kind_updated; ANALYZE memory_records")
	readQueuePlan(t, p, "claim-52000-before", legacyClaimPlanSQL, kinds, []string{JobStatusPending, JobStatusRetry}, now)
	const privacySQL = `SELECT payload FROM memory_records WHERE collection='jobs' AND kind=$1 AND status=ANY($2)
AND doc->'privacy_deletion'->>'state' IN ('purging_derived','verifying')
AND doc->'privacy_deletion'->>'legal_hold_status'<>'held' AND (run_at IS NULL OR run_at<=$3) ORDER BY updated_at,id LIMIT 1`
	const backupSQL = `SELECT payload FROM memory_records WHERE collection='jobs' AND kind=$1 AND status=ANY($2)
AND doc->'privacy_deletion'->>'state'=$3 AND doc->'privacy_deletion'->>'legal_hold_status'<>'held' AND updated_at<$4 ORDER BY updated_at,id LIMIT 100`
	const extractSQL = `SELECT payload FROM memory_records WHERE collection='jobs' AND kind=$1 AND status=$2
AND doc->'file_extract'->'batch' IS NOT NULL AND jsonb_typeof(doc->'file_extract'->'batch')='object' ORDER BY updated_at,id LIMIT $3`
	readQueuePlan(t, p, "privacy-52000-before", privacySQL, privacyDeletionKind, []string{JobStatusPending, JobStatusRetry}, now)
	readQueuePlan(t, p, "backup-52000-before", backupSQL, privacyDeletionKind, []string{JobStatusWaiting, JobStatusPending}, privacyWaiting, now)
	readQueuePlan(t, p, "extract-52000-before", extractSQL, fileExtractJobKind, JobStatusRetry, 100)
	schemaExec(t, p, schemaJobQueueIndexesSQL)
	schemaExec(t, p, "ANALYZE memory_records")
	large := readQueuePlan(t, p, "claim-52000-after", claimJobSelectSQL(len(kinds)), kinds, now)
	assertQueueOrderedPlan(t, large, "memory_records_jobs_waiting_order")
	// Unrelated kinds may enlarge the index tree, but must not add a backlog scan.
	if large.Plan["Shared Hit Blocks"].(float64) > small.Plan["Shared Hit Blocks"].(float64)+100 {
		t.Error("unrelated kind backlog increased claim buffers")
	}
	array := "ARRAY['" + strings.Join(kinds, "','") + "']"
	stamp := "'" + now.Format(time.RFC3339Nano) + "'::timestamptz"
	generic := genericQueuePlan(t, p, "claim-52000-generic", claimJobSelectSQL(len(kinds)), "text[],timestamptz", array+","+stamp)
	assertQueueOrderedPlan(t, generic, "memory_records_jobs_waiting_order")
	for _, test := range []struct{ name, sql, types, values string }{
		{"privacy", privacySQL, "text,text[],timestamptz", "'" + privacyDeletionKind + "',ARRAY['pending','retry']," + stamp},
		{"backup", backupSQL, "text,text[],text,timestamptz", "'" + privacyDeletionKind + "',ARRAY['waiting','pending'],'" + privacyWaiting + "'," + stamp},
		{"extract", extractSQL, "text,text,integer", "'" + fileExtractJobKind + "','retry',100"},
	} {
		plan := genericQueuePlan(t, p, test.name+"-52000-generic", test.sql, test.types, test.values)
		assertQueuePollingPlan(t, plan)
	}
	measure := func(workers int) time.Duration {
		schemaExec(t, p, "DELETE FROM memory_records WHERE collection='jobs' AND kind='embedding'")
		seedQueuePlanJobs(t, p, "target-", "embedding", 2000, now)
		schemaExec(t, p, "VACUUM (ANALYZE) memory_records")
		warmQueueClaimConnections(t, p, kinds, 12, now)
		return claimQueueConcurrently(t, p, workers, 240, now)
	}
	four := measure(4)
	twelve := measure(12)
	t.Logf("throughput_12_to_4_ratio=%.3f (shared-host observation, not a linear-scaling guarantee)", float64(four)/float64(twelve))
	if err := p.Init(context.Background()); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
}

func TestPostgresJobQueueFutureBacklogAndRunningLimit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Run("future", func(t *testing.T) {
		p := isolatedPostgres(t)
		seedQueuePlanState(t, p, "future-", "embedding", 50000, JobStatusPending, now.Add(time.Hour), time.Time{})
		queueTestJob(t, p, "ready-null", "embedding", JobStatusPending, nil, time.Time{})
		schemaExec(t, p, "ANALYZE memory_records")
		plan := readQueuePlan(t, p, "future-50000-null-ready", claimJobSelectSQL(1), []string{"embedding"}, now)
		assertQueueOrderedPlan(t, plan, "memory_records_jobs_waiting_order")
		if plan.Plan["Shared Hit Blocks"].(float64) > 100 {
			t.Error("future backlog scanned before NULL ready job")
		}
		got, err := p.ClaimJob(context.Background(), now, time.Minute)
		if err != nil || got.ID != "ready-null" {
			t.Fatalf("NULL ready claim %s: %v", got.ID, err)
		}
	})
	// Record the opposing-order limit separately: lease filtering cannot lead
	// the index while preserving arbitrary run_at priority.
	t.Run("unexpired_running", func(t *testing.T) {
		p := isolatedPostgres(t)
		seedQueuePlanState(t, p, "busy-", "embedding", 50000, JobStatusRunning, now.Add(-time.Hour), now.Add(time.Hour))
		schemaExec(t, p, "ANALYZE memory_records")
		plan := readQueuePlan(t, p, "running-50000-unexpired-limit", claimJobSelectSQL(1), []string{"embedding"}, now)
		// Empty expired-leases results may use a different index and sort zero
		// rows. Bound actual I/O rather than requiring a particular empty plan.
		if plan.Plan["Shared Hit Blocks"].(float64)+plan.Plan["Shared Read Blocks"].(float64) > 1000 {
			t.Error("unexpired-running poll scanned excessive pages")
		}
		if plan.Plan["Actual Rows"].(float64) != 0 {
			t.Error("unexpired running job was eligible")
		}
	})
}

// Polling an empty kind can sort its zero matching rows. The regression is
// scanning unrelated jobs, so require the kind index and bounded page reads.
func assertQueuePollingPlan(t *testing.T, plan queuePlan) {
	t.Helper()
	found := false
	var walk func(map[string]any)
	walk = func(node map[string]any) {
		if node["Index Name"] == "memory_records_jobs_kind_updated" && strings.Contains(fmt.Sprint(node["Index Cond"]), "kind =") {
			found = true
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
	}
	walk(plan.Plan)
	if !found {
		t.Error("poll did not constrain its ordered index by kind")
	}
	if plan.Plan["Shared Hit Blocks"].(float64)+plan.Plan["Shared Read Blocks"].(float64) > 100 {
		t.Error("empty kind poll scanned unrelated queue pages")
	}
}

func warmQueueClaimConnections(t *testing.T, p *Postgres, kinds []string, count int, now time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var release []func()
	defer func() {
		for _, fn := range release {
			fn()
		}
	}()
	for n := 0; n < count; n++ {
		conn, err := p.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release = append(release, conn.Release)
		for repeat := 0; repeat < 6; repeat++ {
			rows, err := conn.Query(ctx, claimJobSelectSQL(len(kinds)), kinds, now)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPostgresJobQueueLargeTargetKindPlan(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedQueuePlanJobs(t, p, "target-only-", "embedding", 50000, now)
	schemaExec(t, p, "ANALYZE memory_records")
	kinds := append([]string{"embedding", webhookDeliveryKind, autoDeriveJobKind, deriveScheduleKind, captureMergeGateKind, approvedEntitiesJobKind}, erasureJobKinds...)
	plan := readQueuePlan(t, p, "claim-50000-target", claimJobSelectSQL(len(kinds)), kinds, now)
	assertQueueOrderedPlan(t, plan, "memory_records_jobs_waiting_order")
	if plan.Plan["Shared Hit Blocks"].(float64)+plan.Plan["Shared Read Blocks"].(float64) > 200 {
		t.Error("large target kind was scanned")
	}
	array := "ARRAY['" + strings.Join(kinds, "','") + "']"
	stamp := "'" + now.Format(time.RFC3339Nano) + "'::timestamptz"
	plan = genericQueuePlan(t, p, "claim-50000-target-generic", claimJobSelectSQL(len(kinds)), "text[],timestamptz", array+","+stamp)
	assertQueueOrderedPlan(t, plan, "memory_records_jobs_waiting_order")
}

func TestPostgresJobQueueMigrationUpgrade(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	historicalSchema(t, p, 9)
	queueTestJob(t, p, "upgrade-existing", "embedding", JobStatusPending, nil, time.Time{})
	if err := p.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := p.ClaimJob(context.Background(), time.Now().UTC(), time.Minute)
	if err != nil || got.ID != "upgrade-existing" {
		t.Fatalf("upgrade lost existing queue: %s %v", got.ID, err)
	}
	for _, name := range []string{"memory_records_jobs_waiting_order", "memory_records_jobs_running_order", "memory_records_jobs_kind_updated"} {
		var exists bool
		if err := p.pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", "arkism."+name).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s exists=%v: %v", name, exists, err)
		}
	}
	if got := schemaCount(t, p); got != len(schemaMigrations()) {
		t.Fatalf("migration count %d", got)
	}
}
