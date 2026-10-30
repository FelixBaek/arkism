package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// Sustained overload: every round offers more distinct updates than one worker
// round drains, so the queue stays at its limit. Each offer must be accepted
// durably or refused as queue full; a refusal leaves no durable trace, so a later
// retry of the same update is a new admission. Once offers stop, everything
// accepted is applied once, in admission order, with no hold.
func TestDynamicsLoadSustainedOverloadPG(t *testing.T) {
	p := dynamicsPG(t)
	const rounds, offered = 8, 3
	var accepted []string
	var captures []Ref
	before, beforeCaptures := len(pgCollection(t, p.admin, "evaluations")), len(p.rows(t, "capture"))
	admit := func(event string) error {
		p.in.EventID = event
		ref, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in)
		if err == nil {
			accepted = append(accepted, event)
			captures = append(captures, Ref{Collection: colJobs, ID: nativeCaptureID(p.in.Scope, ref), Revision: 1})
		}
		return err
	}
	refused, full, report := dynamicsOverloadRounds(t, p, admit, rounds, offered)
	if len(refused) == 0 || full < rounds-1 {
		t.Fatalf("overload not sustained: refused %d, full in %d of %d rounds", len(refused), full, rounds)
	}
	offeredAccepted := len(accepted)
	if e, c := len(pgCollection(t, p.admin, "evaluations"))-before, len(p.rows(t, "capture"))-beforeCaptures; e != offeredAccepted || c != offeredAccepted {
		t.Fatalf("refused offers left durable rows: %d evaluations and %d captures for %d accepted", e, c, offeredAccepted)
	}
	// A durable outcome is answered idempotently before admission, so a refused offer
	// that had left one behind would retry without a new queue entry.
	for _, event := range refused {
		dynamicsOverloadDrain(t, p)
		tail := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Tail
		dynamicsRebindExpect(t, admit(event), nil, "retry of a refused update")
		if after := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Tail; after != tail+1 {
			t.Fatalf("refused %s was not a new admission: tail %d -> %d", event, tail, after)
		}
	}
	dynamicsOverloadDrain(t, p)
	final := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	if final.Count != 0 || final.Withheld || final.Head != int64(len(accepted)) || final.Head != final.Tail {
		t.Fatalf("accepted work not reconciled: %+v for %d accepted", final, len(accepted))
	}
	dynamicsOverloadOrder(t, p, captures, final.Head)
	out, err := json.Marshal(map[string]any{"protocol": "sys05-b-sustained-overload-v1", "rounds": report, "offered": rounds * offered, "accepted_while_offered": offeredAccepted, "refused": len(refused), "rounds_at_limit": full, "refused_retried_and_admitted": len(refused), "final_head": final.Head})
	dynamicsRebindExpect(t, err, nil, "report")
	t.Logf("%s", out)
}

// dynamicsOverloadDrain runs worker rounds until the queue is empty; a bounded
// count turns a stalled worker into a failure instead of a hang.
func dynamicsOverloadDrain(t *testing.T, p *nativePGFixture) {
	t.Helper()
	for i := 0; dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count > 0; i++ {
		if i == 20 {
			t.Fatal("queue did not drain")
		}
		dynamicsRebindExpect(t, p.worker.WorkDynamicsOnce(testCtx, p.workerToken), nil, "drain")
	}
}

// dynamicsOverloadRounds offers distinct updates faster than one worker round drains.
func dynamicsOverloadRounds(t *testing.T, p *nativePGFixture, admit func(string) error, rounds, offered int) ([]string, int, []map[string]any) {
	t.Helper()
	cfg := dynamicsFixtureConfig()
	var refused []string
	var report []map[string]any
	full := 0
	for round := range rounds {
		for i := range offered {
			event := fmt.Sprintf("overload-%d-%d", round, i)
			if err := admit(event); errors.Is(err, ErrDynamicsQueueFull) {
				refused = append(refused, event)
			} else if err != nil {
				t.Fatalf("offer %s: %v", event, err)
			}
		}
		o, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
		dynamicsRebindExpect(t, err, nil, "observe")
		if o.QueueCount > cfg.Limits.QueueCountLimit || o.Withheld || o.OldestAgeNS == nil || *o.OldestAgeNS >= cfg.Limits.MaxWaitNS {
			t.Fatalf("round %d: %+v", round, o)
		}
		if o.QueueCount == cfg.Limits.QueueCountLimit {
			full++
		}
		report = append(report, map[string]any{"round": round, "depth_before_drain": o.QueueCount, "oldest_age_ns": *o.OldestAgeNS, "accepted_total": (round+1)*offered - len(refused), "refused_total": len(refused)})
		dynamicsRebindExpect(t, p.worker.WorkDynamicsOnce(testCtx, p.workerToken), nil, "drain")
	}
	return refused, full, report
}

// dynamicsOverloadOrder checks each entry is done and maps to the update admitted at its ordinal.
func dynamicsOverloadOrder(t *testing.T, p *nativePGFixture, captures []Ref, head int64) {
	t.Helper()
	var applied int64
	for ordinal := int64(1); ordinal <= head; ordinal++ {
		entry, e := p.admin.Get(testCtx, colJobs, scopeA, dynamicsEntryID(scopeA, ordinal))
		dynamicsRebindExpect(t, e, nil, "entry")
		if entry.Status != JobStatusDone || entry.DynamicsLoad.Capture != captures[ordinal-1] || entry.DynamicsLoad.AppliedNS < applied {
			t.Fatalf("entry %d out of admission order or not done: %s %s %+v", ordinal, entry.Status, entry.DynamicsLoad.Reason, entry.DynamicsLoad.Capture)
		}
		applied = entry.DynamicsLoad.AppliedNS
	}
}
