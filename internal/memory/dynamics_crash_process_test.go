package memory

import (
	"bufio"
	"bytes"
	"encoding/json"
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

// dynamicsCrashFixture is dynamicsRefreshFixture with an explicit lease length, so
// both sides of the queue wait limit can be run.
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
func dynamicsCrashKill(t *testing.T, p *nativePGFixture, cfg DynamicsLoadConfig, events int) (*DynamicsLoadPayload, int64, string) {
	t.Helper()
	claim := dynamicsCrashCommand(t, p, cfg, "claim")
	var stderr bytes.Buffer
	claim.Stderr = &stderr
	stdout, err := claim.StdoutPipe()
	dynamicsRebindExpect(t, err, nil, "claim pipe")
	dynamicsRebindExpect(t, claim.Start(), nil, "claim process")
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "claimed ") {
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
	if held.LeaseToken == "" || held.LeaseUntilNS <= killedAt || held.Count != 1 || len(p.rows(t, "event")) != events {
		t.Fatalf("dead worker's lease not held or state changed: %+v", held)
	}
	early := dynamicsCrashDrain(t, p, cfg)
	if after := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; after.Count != 1 || after.LeaseToken != held.LeaseToken || len(p.rows(t, "event")) != events {
		t.Fatalf("a new process took a live lease (%s): %+v", early, after)
	}
	time.Sleep(time.Duration(held.LeaseUntilNS-dynamicsNowNS()) + 50*time.Millisecond)
	return held, killedAt, early
}

// A worker process killed with SIGKILL while it holds a lease must neither lose nor
// double-apply the accepted update. A new process cannot claim before the lease
// expires. If the lease ends inside the queue wait limit, a new process takes a new
// lease after the dead one and applies once; if it ends after, the entry fails closed as
// queue_wait_exhausted and publication stays withheld until an explicit repair.
func TestDynamicsWorkerProcessCrashPG(t *testing.T) {
	report := map[string]any{"protocol": "sys05-b-worker-crash-v1", "killed_signal": "SIGKILL"}
	for name, leaseNS := range map[string]int64{"lease_within_wait": int64(2 * time.Second), "lease_beyond_wait": int64(10 * time.Second)} {
		t.Run(name, func(t *testing.T) { report[name] = dynamicsCrashCase(t, leaseNS) })
	}
	out, err := json.Marshal(report)
	dynamicsRebindExpect(t, err, nil, "report")
	t.Logf("%s", out)
}

func dynamicsCrashCase(t *testing.T, leaseNS int64) map[string]any {
	t.Helper()
	p, cfg := dynamicsCrashFixture(t, leaseNS)
	events := len(p.rows(t, "event"))
	if q := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad; q.Count != 1 {
		t.Fatalf("fixture did not queue one update: %d", q.Count)
	}
	held, killedAt, early := dynamicsCrashKill(t, p, cfg, events)
	late := dynamicsCrashDrain(t, p, cfg)
	after := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	entry, err := p.admin.Get(testCtx, colJobs, scopeA, dynamicsEntryID(scopeA, held.Head+1))
	dynamicsRebindExpect(t, err, nil, "entry row")
	applied := len(p.rows(t, "event")) - events
	row := map[string]any{"lease_ns": leaseNS, "max_wait_ns": cfg.Limits.MaxWaitNS, "lease_left_at_kill_ns": held.LeaseUntilNS - killedAt,
		"early_drain": early, "late_drain": late, "applied_events": applied, "withheld_after": after.Withheld,
		"lease_generation_before": held.LeaseGeneration, "lease_generation_after": after.LeaseGeneration,
		"entry_status": entry.Status, "entry_reason": entry.DynamicsLoad.Reason, "entry_attempts": entry.DynamicsLoad.Attempts}
	if leaseNS < cfg.Limits.MaxWaitNS {
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
		t.Fatalf("lease beyond the wait limit did not fail closed: %v %+v", row, after)
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
