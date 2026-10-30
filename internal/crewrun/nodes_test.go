package crewrun

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// This helper reuses the test executable. It never builds or contacts a provider.
func TestObservationRunnerProcess(t *testing.T) {
	if !slices.Contains(os.Args, "run-node") {
		return
	}
	var spec aiSpec
	if json.NewDecoder(os.Stdin).Decode(&spec) != nil {
		os.Exit(91)
	}
	if spec.Observation != nil {
		req := *spec.Observation
		writer, err := OpenObservationJournalWriter(req)
		if err != nil {
			os.Exit(92)
		}
		leaf, proof := transportTestLeaf(t, req.Parent, 0, .75, "provider_reported")
		if err = writer.Begin(leaf.Identity.AttemptID); err != nil {
			os.Exit(93)
		}
		if err = writer.Record(leaf, proof); err != nil {
			os.Exit(94)
		}
		if err = writer.CloseBoundary(); err != nil {
			os.Exit(95)
		}
	}
	ok := spec.Model == "good"
	output := "local-fake"
	if strings.Contains(spec.Task, "JSON 배열로만 답해") {
		output = `[{"worker":"a","task":"task-a"},{"worker":"b","task":"task-b"}]`
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": ok, "output": output, "model": spec.Model, "error": "local fake failure"}); err != nil {
		os.Exit(96)
	}
	os.Exit(0)
}

func observationProcessEngine() *Engine {
	return &Engine{Runner: []string{os.Args[0], "-test.run=^TestObservationRunnerProcess$", "--"}}
}

func TestObservationActualRunnerRetryPreservesEachFailedPaidLeaf(t *testing.T) {
	ctx, o := runtimeTestContext()
	e := observationProcessEngine()
	answer, err := e.callAI(ctx, aiSpec{Model: "bad", Task: "synthetic"}, 5*time.Second, 1)
	if err == nil || answer.OK || answer.Output != "" {
		t.Fatal("retry failure behavior changed", err)
	}
	snapshot := o.Snapshot()
	if len(snapshot.Latest) != 4 || snapshot.ProviderLeafTotals.Cost.Value == nil || *snapshot.ProviderLeafTotals.Cost.Value != 1.5 {
		t.Fatalf("retry accounting: %d %+v", len(snapshot.Latest), snapshot)
	}
	parents := 0
	for _, record := range snapshot.Latest {
		if record.UsageRole == "transport_aggregate" {
			parents++
			if record.Status != "failed" || record.LeafCoverage != "complete" {
				t.Fatalf("failed settled parent missing: %+v issues=%v", record, snapshot.Issues)
			}
			if record.Identity.RetryIndex == 1 && record.Identity.ParentAttemptID == nil {
				t.Fatal("actual retry causality lost")
			}
		}
	}
	if parents != 2 {
		t.Fatal("retry merged with previous call")
	}
	if _, err := FinalizeAttemptObservationReport(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestObservationActualRunnerFallbackKeepsOriginalDispatchIdentity(t *testing.T) {
	ctx, o := runtimeTestContext()
	e := observationProcessEngine()
	answer, err := e.callWithFallback(withCallLabel(ctx, "plan"), aiSpec{Model: "bad"}, "good", 5*time.Second, 0)
	if err != nil || !answer.OK || answer.Output != "local-fake" || answer.Model != "good" {
		t.Fatal("fallback result changed", err)
	}
	snapshot := o.Snapshot()
	if len(snapshot.Latest) != 4 || snapshot.ProviderLeafTotals.Cost.Value == nil || *snapshot.ProviderLeafTotals.Cost.Value != 1.5 {
		t.Fatal("fallback replaced failed usage")
	}
	found := false
	for _, r := range snapshot.Latest {
		if r.UsageRole == "transport_aggregate" && r.Status == "succeeded" {
			found = true
			if r.Identity.FallbackParentCallKey == nil || *r.Identity.FallbackParentCallKey != "plan/retry0" || r.Identity.ParentAttemptID == nil || r.Identity.Phase != "plan" {
				t.Fatal("fallback provenance lost")
			}
		}
	}
	if !found {
		t.Fatal("fallback missing")
	}
	if _, err := FinalizeAttemptObservationReport(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestObservationActualRunnerOffHasNoEnvelopeAndSameBusinessOutput(t *testing.T) {
	e := observationProcessEngine()
	off, err := e.callAI(context.Background(), aiSpec{Model: "good"}, 5*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := runtimeTestContext()
	on, err := e.callAI(ctx, aiSpec{Model: "good"}, 5*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	offJSON, _ := json.Marshal(off)
	onJSON, _ := json.Marshal(on)
	if string(offJSON) != string(onJSON) {
		t.Fatalf("business output changed: %s / %s", offJSON, onJSON)
	}
}

func TestObservationActualCollaborationDeterministicSchedule(t *testing.T) {
	var previous []string
	for replay := 0; replay < 2; replay++ {
		ctx, o := runtimeTestContext()
		e := observationProcessEngine()
		config := map[string]string{"workers": `[{"name":"a"},{"name":"b"}]`, "concurrency": "2"}
		result, err := e.runCollab(ctx, config, aiSpec{Model: "good", Task: "synthetic cooperation"}, 5*time.Second, 0, nil, "")
		if err != nil || result.out.(map[string]any)["output"] != "local-fake" {
			t.Fatal("collaboration result changed", err)
		}
		snapshot := o.Snapshot()
		if len(snapshot.Latest) != 8 || snapshot.ProviderLeafTotals.Cost.Value == nil || *snapshot.ProviderLeafTotals.Cost.Value != 3 {
			t.Fatal("plan/workers/synthesis missing")
		}
		phases := map[string]int{}
		ids := []string{}
		for _, row := range snapshot.Latest {
			ids = append(ids, row.Identity.AttemptID)
			if row.UsageRole == "transport_aggregate" {
				phases[row.Identity.Phase]++
			}
		}
		if phases["plan"] != 1 || phases["worker"] != 2 || phases["synthesis"] != 1 {
			t.Fatal("actual phases missing", phases)
		}
		slices.Sort(ids)
		if replay > 0 && !slices.Equal(previous, ids) {
			t.Fatal("concurrent schedule changed stable attempt IDs")
		}
		previous = ids
	}
}
