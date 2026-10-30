package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"fmt"
	"testing"
	"time"
)

// dynamicsStarvationDue counts each kind's open work that is due now; privacy also
// reports its phase, since one deletion advances by phases.
func dynamicsStarvationDue(t *testing.T, p *nativePGFixture, privacyID string) (map[string]int64, string) {
	t.Helper()
	now := time.Now()
	out := map[string]int64{"privacy": 0, "embedding": 0, "dynamics": dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count}
	phase := ""
	for _, row := range pgCollection(t, p.admin, colJobs) {
		due := row.RunAt == nil || !row.RunAt.After(now)
		open := row.Status != JobStatusDone && row.Status != JobStatusFailed && row.Status != JobStatusWaiting
		switch {
		case row.ID == privacyID:
			phase = row.PrivacyDeletion.State + "/" + row.Status
		case row.Kind == "embedding" && open && due:
			out["embedding"]++
		}
	}
	// Privacy eligibility is the product's own selection, which also waits for backups.
	if next, err := p.admin.NextPrivacyAdvance(testCtx, now.UTC()); err == nil && next.ID == privacyID {
		out["privacy"] = 1
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	return out, phase
}

// dynamicsStarvationKind is one kind's coverage in the A-v2 background shape.
type dynamicsStarvationKind struct {
	Eligible           int64  `json:"eligible"`
	StalledRounds      int    `json:"stalled_rounds"`
	StarvationObserved bool   `json:"starvation_observed"`
	LastProgressNS     *int64 `json:"last_progress_ns"`
}

// dynamicsStarvationRound runs one real WorkOnce and charges a stalled round to
// every kind that had eligible work and made no progress.
func dynamicsStarvationRound(t *testing.T, p *nativePGFixture, svc *Service, token, privacyID string, coverage map[string]*dynamicsStarvationKind, start int64) map[string]any {
	t.Helper()
	before, phase := dynamicsStarvationDue(t, p, privacyID)
	err := svc.WorkOnce(context.Background(), token)
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal("worker round:", err)
	}
	after, nextPhase := dynamicsStarvationDue(t, p, privacyID)
	at := dynamicsNowNS() - start
	for kind, c := range coverage {
		progressed := after[kind] < before[kind] || kind == "privacy" && nextPhase != phase
		if before[kind] > 0 && !progressed {
			c.StalledRounds++
			c.StarvationObserved = true
		}
		if before[kind] > 0 && progressed {
			c.LastProgressNS = &at
		}
	}
	return map[string]any{"due_before": before, "due_after": after, "privacy_phase": nextPhase, "error": fmt.Sprint(err)}
}

// dynamicsStarvationRun queues extra native and embedding backlog (native only
// when withNative) and runs rounds until nothing is eligible.
func dynamicsStarvationRun(t *testing.T, withNative bool) map[string]any {
	t.Helper()
	p, svc, fixture, token := dynamicsFairnessSetup(t)
	if withNative {
		for i := 0; i < 3; i++ {
			p.in.EventID = fmt.Sprintf("starvation-%d", i)
			p.accept(t)
		}
	} else {
		// Control: the fixture's one queued update stays, but no native round runs.
		svc.DynamicsLoad = nil
	}
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		row := Record{ID: fmt.Sprintf("embedding-extra-%d", i), Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, Target: &Ref{Collection: "memories", ID: "already-deleted", Revision: 1}, CreatedAt: now, UpdatedAt: now}
		dynamicsRebindExpect(t, p.admin.Insert(testCtx, colJobs, row), nil, "embedding backlog")
	}
	start := dynamicsNowNS()
	initial, _ := dynamicsStarvationDue(t, p, fixture.nextID)
	coverage := map[string]*dynamicsStarvationKind{}
	for _, kind := range []string{"privacy", "embedding", "dynamics"} {
		if kind != "dynamics" || withNative {
			coverage[kind] = &dynamicsStarvationKind{Eligible: initial[kind]}
		}
	}
	var rounds []map[string]any
	for round := 1; round <= 10; round++ {
		due, _ := dynamicsStarvationDue(t, p, fixture.nextID)
		if due["privacy"]+due["embedding"]+map[bool]int64{true: due["dynamics"]}[withNative] == 0 {
			break
		}
		rounds = append(rounds, dynamicsStarvationRound(t, p, svc, token, fixture.nextID, coverage, start))
	}
	final, phase := dynamicsStarvationDue(t, p, fixture.nextID)
	privacy, err := p.admin.Get(testCtx, colJobs, scopeA, fixture.nextID)
	dynamicsRebindExpect(t, err, nil, "privacy row")
	q := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	return map[string]any{"initial_eligible": initial, "final_eligible": final, "privacy_final_phase": phase,
		"privacy_report": privacy.PrivacyDeletion.Report, "privacy_error_summary": privacy.PrivacyDeletion.ErrorSummary,
		"queue_count": q.Count, "queue_withheld": q.Withheld, "queue_head": q.Head, "background_coverage": coverage, "rounds": rounds}
}

// Over several real WorkOnce rounds with a backlog of every kind, no kind that has
// eligible work may go a round without progress. Privacy eligibility is the
// product's own selection; a control run without the native round must end in the
// same privacy phase and report, so the native work did not change privacy's path.
// This covers one small synthetic scenario only.
func TestDynamicsLoadFunctionalStarvationRoundsPG(t *testing.T) {
	report := map[string]any{"protocol": "sys05-b-starvation-rounds-v2"}
	for _, withNative := range []bool{true, false} {
		name := map[bool]string{true: "with_native", false: "control_without_native"}[withNative]
		t.Run(name, func(t *testing.T) { report[name] = dynamicsStarvationRun(t, withNative) })
	}
	out, err := json.Marshal(report)
	dynamicsRebindExpect(t, err, nil, "report")
	t.Logf("%s", out)
	run, control := report["with_native"].(map[string]any), report["control_without_native"].(map[string]any)
	for kind, c := range run["background_coverage"].(map[string]*dynamicsStarvationKind) {
		if c.Eligible == 0 || c.StarvationObserved || run["final_eligible"].(map[string]int64)[kind] != 0 {
			t.Fatalf("%s starved or still eligible: %s", kind, out)
		}
	}
	// All four native updates applied (head advanced by four), none failed or withheld.
	if run["queue_count"] != int64(0) || run["queue_withheld"] != false || run["queue_head"] != int64(4) {
		t.Fatalf("native backlog did not apply cleanly: %s", out)
	}
	if run["privacy_final_phase"] != control["privacy_final_phase"] || !reflect.DeepEqual(run["privacy_report"], control["privacy_report"]) {
		t.Fatalf("native work changed the privacy path: %s", out)
	}
}
