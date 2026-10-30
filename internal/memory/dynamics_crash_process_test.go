package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// dynamicsCrashChild runs inside a separate worker test process. "claim" takes the
// lease and reports it, then waits to be killed; "drain" runs one ordinary worker
// round and reports its result instead of failing, so the parent judges it.
func dynamicsCrashChild(t *testing.T, svc *Service, token string, scope Scope) {
	t.Helper()
	switch os.Getenv("SYS05_CRASH_CHILD_ROLE") {
	case "claim":
		ctx, err := svc.nativeActor(testCtx, token, scope, "write:jobs")
		if err != nil {
			t.Fatal(err)
		}
		claim, err := svc.claimDynamics(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("claimed %s pid=%d\n", claim.ID, os.Getpid())
		select {}
	case "apply":
		// The apply transaction does all its writes and then waits, uncommitted, to be killed.
		pg := svc.Repo.(*Postgres)
		hold := &dynamicsCrashInApply{Postgres: pg}
		svc.Repo = hold
		ctx, err := svc.nativeActor(testCtx, token, scope, "write:jobs")
		if err != nil {
			t.Fatal(err)
		}
		claim, err := svc.claimDynamics(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		hold.armed = true
		t.Fatal("apply returned instead of waiting:", svc.applyDynamicsClaim(ctx, token, scope, claim))
	case "drain":
		result := "ok"
		if err := svc.WorkDynamicsOnce(testCtx, token); err != nil {
			result = err.Error()
		}
		fmt.Printf("drain %s\n", result)
	default:
		t.Fatal("unknown crash child role")
	}
}

// dynamicsCrashInApply passes transactions through until armed; the armed one runs
// its body and then reports and blocks before commit.
type dynamicsCrashInApply struct {
	*Postgres
	armed bool
}

func (r *dynamicsCrashInApply) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Postgres.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		err := fn(ctx, tx)
		if err == nil && r.armed {
			fmt.Printf("in-apply pid=%d\n", os.Getpid())
			select {}
		}
		return err
	})
}

func dynamicsCrashCommand(t *testing.T, p *nativePGFixture, cfg DynamicsLoadConfig, role string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	dynamicsRebindExpect(t, err, nil, "test binary")
	raw, err := json.Marshal(cfg)
	dynamicsRebindExpect(t, err, nil, "config")
	command := exec.Command(binary, "-test.run=^TestDynamicsSnapshotRefreshWorkerProcess$", "-test.count=1", "-test.timeout=30s")
	command.Env = append(dynamicsRefreshChildEnv(p, raw), "SYS05_CRASH_CHILD_ROLE="+role)
	return command
}

// dynamicsCrashDrain runs one worker round in a fresh process and returns its report line.
func dynamicsCrashDrain(t *testing.T, p *nativePGFixture, cfg DynamicsLoadConfig) string {
	t.Helper()
	out, err := dynamicsCrashCommand(t, p, cfg, "drain").CombinedOutput()
	dynamicsRebindExpect(t, err, nil, "drain process "+string(out))
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "drain ") {
			return strings.TrimPrefix(line, "drain ")
		}
	}
	t.Fatalf("drain process gave no report: %s", out)
	return ""
}

// dynamicsCrashFixture is dynamicsRefreshFixture with an explicit lease length.
func dynamicsCrashFixture(t *testing.T, leaseNS int64) (*nativePGFixture, DynamicsLoadConfig) {
	t.Helper()
	p, shadow, _ := nativeSearchPGFixture(t)
	dynamicsUseLogin(t, p)
	cfg := dynamicsFixtureConfig()
	cfg.Profile = "readonly-process-fixture-v1"
	cfg.Limits.AllowedFreshnessNS = int64(30 * time.Second)
	cfg.Limits.LeaseNS = leaseNS
	runtime := dynamicsFixtureRuntime(t, cfg)
	p.checker.DynamicsLoad, p.worker.DynamicsLoad, p.worker.NativeSearchShadow = runtime, runtime, shadow
	p.accept(t)
	return p, cfg
}

// dynamicsCrashKill starts a claiming worker process, kills it with SIGKILL once it
// holds the lease and checks that the dead lease still blocks a new process.
func dynamicsCrashKill(t *testing.T, p *nativePGFixture, cfg DynamicsLoadConfig, events int, role string) (*DynamicsLoadPayload, int64, string) {
	t.Helper()
	queued := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count
	claim := dynamicsCrashCommand(t, p, cfg, role)
	var stderr bytes.Buffer
	claim.Stderr = &stderr
	stdout, err := claim.StdoutPipe()
	dynamicsRebindExpect(t, err, nil, "claim pipe")
	dynamicsRebindExpect(t, claim.Start(), nil, "claim process")
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, map[string]string{"claim": "claimed ", "apply": "in-apply "}[role]) {
		_ = claim.Process.Kill()
		_ = claim.Wait()
		t.Fatalf("claim process did not report a claim: %q %v %s", line, err, stderr.String())
	}
	dynamicsRebindExpect(t, claim.Process.Kill(), nil, "SIGKILL")
	if err = claim.Wait(); err == nil {
		t.Fatal("killed worker exited cleanly")
	}
	killedAt := dynamicsNowNS()
	held := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	if held.LeaseToken == "" || held.LeaseUntilNS <= killedAt || held.Count != queued || len(p.rows(t, "event")) != events {
		t.Fatalf("dead worker's lease not held or state changed: %+v", held)
	}
	early := dynamicsCrashDrain(t, p, cfg)
	if after := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; after.Count != queued || after.LeaseToken != held.LeaseToken || len(p.rows(t, "event")) != events {
		t.Fatalf("a new process took a live lease (%s): %+v", early, after)
	}
	time.Sleep(time.Duration(held.LeaseUntilNS-dynamicsNowNS()) + 50*time.Millisecond)
	return held, killedAt, early
}

// A worker process killed with SIGKILL while it holds a lease must neither lose nor
// double-apply the accepted update. A new process cannot claim before the lease
// expires. The config keeps the lease shorter than the queue wait limit, so a new process
// draining right after the dead lease takes a new lease and applies once; a drain that
// only comes after the wait limit fails the entry closed as queue_wait_exhausted and
// publication stays withheld until an explicit repair. A
// process killed inside its apply transaction, after all writes and before commit,
// must leave nothing applied, and the next process applies once.
func TestDynamicsWorkerProcessCrashPG(t *testing.T) {
	report := map[string]any{"protocol": "sys05-b-worker-crash-v1", "killed_signal": "SIGKILL"}
	for _, c := range []struct {
		name, role      string
		leaseNS, lateNS int64
	}{{"lease_within_wait", "claim", int64(2 * time.Second), 0}, {"drained_after_wait", "claim", int64(4 * time.Second), int64(1500 * time.Millisecond)}, {"killed_inside_apply_transaction", "apply", int64(2 * time.Second), 0}} {
		t.Run(c.name, func(t *testing.T) { report[c.name] = dynamicsCrashCase(t, c.leaseNS, c.lateNS, c.role) })
	}
	out, err := json.Marshal(report)
	dynamicsRebindExpect(t, err, nil, "report")
	t.Logf("%s", out)
}

func dynamicsCrashCase(t *testing.T, leaseNS, lateNS int64, role string) map[string]any {
	t.Helper()
	p, cfg := dynamicsCrashFixture(t, leaseNS)
	events := len(p.rows(t, "event"))
	if q := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; q.Count != 1 {
		t.Fatalf("fixture did not queue one update: %d", q.Count)
	}
	held, killedAt, early := dynamicsCrashKill(t, p, cfg, events, role)
	time.Sleep(time.Duration(lateNS))
	late := dynamicsCrashDrain(t, p, cfg)
	after := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	entry, err := p.admin.Get(testCtx, colJobs, scopeA, dynamicsEntryID(scopeA, held.Head+1))
	dynamicsRebindExpect(t, err, nil, "entry row")
	applied := len(p.rows(t, "event")) - events
	row := map[string]any{"killed_in": role, "lease_ns": leaseNS, "max_wait_ns": cfg.Limits.MaxWaitNS, "drain_delay_after_lease_ns": lateNS, "lease_left_at_kill_ns": held.LeaseUntilNS - killedAt,
		"early_drain": early, "late_drain": late, "applied_events": applied, "withheld_after": after.Withheld,
		"lease_generation_before": held.LeaseGeneration, "lease_generation_after": after.LeaseGeneration,
		"entry_status": entry.Status, "entry_reason": entry.DynamicsLoad.Reason, "entry_attempts": entry.DynamicsLoad.Attempts}
	if lateNS == 0 {
		if late != "ok" || after.Count != 0 || after.Withheld || after.LeaseGeneration <= held.LeaseGeneration || applied != 1 || entry.Status != JobStatusDone || entry.DynamicsLoad.Attempts != 2 {
			t.Fatalf("recovery inside the wait limit: %v %+v", row, after)
		}
		readonly, token := dynamicsRefreshReader(t, p, cfg, false)
		dynamicsAssertRefreshUnchanged(t, p, readonly, token, true)
		published := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.PublicationGeneration
		if published < 1 {
			t.Fatal("recovered update was not published")
		}
		row["published_generation"] = published
		return row
	}
	if after.Count != 0 || !after.Withheld || applied != 0 || late == "ok" || entry.Status != JobStatusFailed || entry.DynamicsLoad.Reason != "queue_wait_exhausted" || entry.DynamicsLoad.Attempts != 1 {
		t.Fatalf("drain after the wait limit did not fail closed: %v %+v", row, after)
	}
	// The failed entry is terminal: repair releases the hold and nothing is applied later.
	dynamicsRebindExpect(t, p.worker.RepairDynamicsPublication(testCtx, p.workerToken, scopeA), nil, "repair")
	row["drain_after_repair"] = dynamicsCrashDrain(t, p, cfg)
	repaired := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	if repaired.Withheld || repaired.Count != 0 || len(p.rows(t, "event")) != events {
		t.Fatalf("repair after a failed crash entry: %+v", repaired)
	}
	row["withheld_after_repair"] = repaired.Withheld
	return row
}

// dynamicsCrashFullQueue fills the queue with four updates on one trace and drains
// it with new worker processes; with crash, the first worker dies inside its apply
// transaction and an admission is tried while its lease is held.
func dynamicsCrashFullQueue(t *testing.T, crash bool) map[string]any {
	t.Helper()
	p, cfg := dynamicsCrashFixture(t, int64(2*time.Second))
	for i := 1; i < int(cfg.Limits.QueueCountLimit); i++ {
		p.in.EventID = fmt.Sprintf("crash-load-%d", i)
		p.accept(t)
	}
	events := len(p.rows(t, "event"))
	if q := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; q.Count != cfg.Limits.QueueCountLimit {
		t.Fatalf("queue not full: %d", q.Count)
	}
	out := map[string]any{"queue_limit": cfg.Limits.QueueCountLimit, "batch_limit": cfg.Limits.BatchLimit}
	if crash {
		dynamicsCrashKill(t, p, cfg, events, "apply")
		p.in.EventID = "crash-load-overflow"
		_, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in)
		if !errors.Is(err, ErrDynamicsQueueFull) {
			t.Fatal("admission while full and the dead lease held:", err)
		}
		out["admission_while_held"] = err.Error()
	}
	var drains []string
	for round := 0; round < 4 && dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count > 0; round++ {
		drains = append(drains, dynamicsCrashDrain(t, p, cfg))
	}
	q := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	done := 0
	for i := int64(1); i <= q.Head; i++ {
		entry, err := p.admin.Get(testCtx, colJobs, scopeA, dynamicsEntryID(scopeA, i))
		dynamicsRebindExpect(t, err, nil, "entry row")
		if entry.Status == JobStatusDone {
			done++
		}
	}
	out["drains"], out["events_added"], out["entries_done"] = drains, len(p.rows(t, "event"))-events, done
	out["count_after"], out["head_after"], out["withheld_after"], out["published_generation"] = q.Count, q.Head, q.Withheld, q.PublicationGeneration
	return out
}

// A full queue when the worker dies inside its first apply: a new admission is
// refused as queue full while the dead lease is held, and after the lease new
// processes finish all four entries in order with no hold. The four updates share
// one trace, so native duplicate suppression applies; the crash run must add the
// same native events as an uncrashed run of the same plan.
func TestDynamicsWorkerProcessCrashUnderFullQueuePG(t *testing.T) {
	report := map[string]any{"protocol": "sys05-b-worker-crash-full-queue-v1"}
	for _, crash := range []bool{false, true} {
		name := map[bool]string{false: "no_crash", true: "crash_in_first_apply"}[crash]
		t.Run(name, func(t *testing.T) { report[name] = dynamicsCrashFullQueue(t, crash) })
	}
	out, err := json.Marshal(report)
	dynamicsRebindExpect(t, err, nil, "report")
	t.Logf("%s", out)
	base, ok := report["no_crash"].(map[string]any)
	run, ran := report["crash_in_first_apply"].(map[string]any)
	if !ok || !ran {
		// Subtests without PostgreSQL skip; a failed subtest has already failed this test.
		t.Skip("a subtest produced no report")
	}
	for _, r := range []map[string]any{base, run} {
		if r["count_after"] != int64(0) || r["head_after"] != int64(4) || r["entries_done"] != 4 || r["withheld_after"] != false || r["published_generation"].(int64) < 1 {
			t.Fatalf("full queue did not finish cleanly: %s", out)
		}
	}
	if run["events_added"] != base["events_added"] {
		t.Fatalf("crash changed the native events: %s", out)
	}
}
