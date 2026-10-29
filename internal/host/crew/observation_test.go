package crew

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
)

func hostObservationParent(t *testing.T, collection string) crewrun.AttemptObservation {
	t.Helper()
	scope := crewrun.ObservationScope{WorkspaceID: "ws", ProjectID: "proj"}
	source, path, mode, id, definition := "workflow", "workflow", "flow", "run-1", "definition"
	var canonical *string
	basis := "observed_legacy_call"
	if collection == "crew_assists" {
		source, path, mode, id, definition = "agent", "single", "legacy", "assist-1", "agent-1"
	} else {
		value := "reservation"
		canonical = &value
		basis = "canonical_flow_receipt"
	}
	run := crewrun.ObservationRun{Scope: scope, RunRef: crewrun.ObservationRef{Collection: collection, ID: id, Revision: 7}, RootRunID: &id, RootRunCollection: &collection, Definition: crewrun.ObservationDefinition{Ref: crewrun.ObservationRef{Collection: "crew", ID: definition, Revision: 3}, SourceType: source, HashState: "not_recorded"}, ExecutionPath: path, InvocationMode: mode}
	result, err := crewrun.SealAttemptObservation(crewrun.AttemptObservation{ContractID: crewrun.CrewObservationContractID, Identity: crewrun.AttemptIdentity{Run: run, NodeID: "node", NodeAttempt: 1, CallKey: "call", Phase: "main", IdentityBasis: basis, CanonicalAttemptID: canonical}, Task: crewrun.ObservationTask{Namespace: "crew_experiment", TaskID: "synthetic-task", TaskVersion: "v1", InputSHA256: strings.Repeat("a", 64), SpecSHA256: strings.Repeat("b", 64)}, Status: "reserved", DispatchState: "not_dispatched", Usage: crewrun.UnknownUsageObservation(), ObservationRevision: 1, UsageRole: "transport_aggregate", LeafCoverage: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func hostObservationSpec(t *testing.T) (map[string]any, *crewrun.ObservationTransport, *hostObservationSession) {
	t.Helper()
	transport, err := crewrun.NewObservationTransport(hostObservationParent(t, "crew_runs"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(transport.Request())
	spec, session := openHostObservation(map[string]any{"model": "first", "task": "synthetic-task", "instructions": "synthetic instruction"}, raw)
	if session == nil {
		t.Fatal("no session")
	}
	t.Cleanup(func() { closeHostObservation(session); _ = transport.Close() })
	return spec, transport, session
}
func TestHostObservationRoutingCapturesFailureBeforeFallbackOverwrite(t *testing.T) {
	spec, transport, session := hostObservationSpec(t)
	spec["model"] = "auto"
	calls, reports := 0, 0
	decide := func(task, group string) (map[string]any, error) {
		return map[string]any{"id": "fixture/first", "decision": "fixed", "fallbacks": []any{"fixture/second"}}, nil
	}
	run := func(input map[string]any) (map[string]any, error) {
		calls++
		if calls == 1 {
			return map[string]any{"ok": false, "model": "first", "error": "exit_1", "usage": map[string]any{"input_tokens": json.Number("3"), "cost_usd": .6}}, nil
		}
		return map[string]any{"ok": true, "model": "second", "output": "done", "usage": map[string]any{"input_tokens": json.Number("4"), "output_tokens": json.Number("2"), "cost_usd": .9}}, nil
	}
	res, err := runRouted(spec, decide, run, func(map[string]any) { reports++ })
	closeHostObservation(session)
	if err != nil || res["ok"] != true || calls != 2 || reports != 2 {
		t.Fatal("business behavior changed", res, err, calls, reports)
	}
	read, err := transport.Read()
	if err != nil || len(read.Records) != 2 || read.Aggregate.CostUSD == nil || *read.Aggregate.CostUSD != 1.5 {
		t.Fatalf("failed cost lost records=%d coverage=%s err=%v", len(read.Records), read.Coverage, err)
	}
	for _, record := range read.Records {
		if record.Identity.CanonicalAttemptID != nil || record.Identity.AttemptRef != nil {
			t.Fatal("outer reservation cloned")
		}
	}
}
func TestHostObservationEarlierRowsSurviveLaterRunnerError(t *testing.T) {
	spec, transport, session := hostObservationSpec(t)
	spec["model"] = "auto"
	calls := 0
	decide := func(task, group string) (map[string]any, error) {
		return map[string]any{"id": "fixture/first", "fallbacks": []any{"fixture/second"}}, nil
	}
	_, err := runRouted(spec, decide, func(map[string]any) (map[string]any, error) {
		calls++
		if calls == 1 {
			return map[string]any{"ok": false, "error": "exit_1", "usage": map[string]any{"cost_usd": .3}}, nil
		}
		return nil, errors.New("cancelled")
	}, func(map[string]any) {})
	closeHostObservation(session)
	if err == nil || calls != 2 {
		t.Fatal("original runner error lost")
	}
	read, readErr := transport.Read()
	if readErr != nil || len(read.Records) != 2 {
		t.Fatal("earlier record lost", readErr)
	}
	found := false
	for _, record := range read.Records {
		if record.Usage.CostUSD != nil && *record.Usage.CostUSD == .3 {
			found = true
		}
	}
	if !found {
		t.Fatal("earlier failed charge missing")
	}
}
func TestHostObservationDefaultOffNoJSONOrJournalSideEffects(t *testing.T) {
	spec := map[string]any{"model": "first", "task": "t"}
	same, session := openHostObservation(spec, nil)
	if session != nil || !reflect.DeepEqual(same, spec) {
		t.Fatal("off changed spec")
	}
	patch(t, &runNodeFn, func(input map[string]any) (map[string]any, error) {
		if _, exists := input[hostObservationKey]; exists {
			t.Fatal("off added session")
		}
		return map[string]any{"ok": true, "model": "first", "output": "done"}, nil
	})
	result := runNodeCLI(`{"model":"first","task":"t"}`)
	if _, exists := result["usage_observation"]; exists {
		t.Fatal("off added JSON")
	}
}
func TestHostObservationInvalidRequestPreservesWork(t *testing.T) {
	patch(t, &runNodeFn, func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": false, "model": "first", "error": "unchanged"}, nil
	})
	result := runNodeCLI(`{"model":"first","task":"t","observation":{"protocol":"wrong","parent":{},"journal_dir":"/foreign"}}`)
	if result["error"] != "unchanged" {
		t.Fatal("instrumentation replaced outcome", result)
	}
}
func TestHostObservationInjectedAndLowlevelCaptureOneLeaf(t *testing.T) {
	spec, transport, session := hostObservationSpec(t)
	calls := 0
	_, err := observedInjectedRun(spec, func(active map[string]any) (map[string]any, error) {
		call, owned := startHostObservation(active)
		if owned {
			t.Fatal("nested call owned a duplicate leaf")
		}
		call.dispatched()
		call.offerReported(map[string]any{"input_tokens": json.Number("0"), "output_tokens": json.Number("1"), "cost_usd": 0.0}, "synthetic-provider-v1")
		calls++
		return map[string]any{"ok": true, "model": "first", "output": "done"}, nil
	})
	closeHostObservation(session)
	read, readErr := transport.Read()
	if err != nil || readErr != nil || calls != 1 || len(read.Records) != 1 || read.Records[0].Usage.CostBasis != "provider_reported" {
		t.Fatal("nested dispatch doubled or changed provenance", err, readErr)
	}
}
func TestHostObservationChatUsageCapturedBeforeEmptyError(t *testing.T) {
	spec, transport, session := hostObservationSpec(t)
	call, owned := startHostObservation(spec)
	fakeProvider(t, `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":0,"cost_usd":0.4}}`, func(*http.Request, []byte) {})
	res, err := runChatObserved(call, owned, spec, map[string]any{"base_url": "https://fixture.invalid/v1", "key": "synthetic-key"}, "first", "medium", "instructions", "task", time.Second, time.Now())
	closeHostObservation(session)
	read, readErr := transport.Read()
	if err != nil || res["ok"] != false || readErr != nil || len(read.Records) != 1 || read.Records[0].Usage.CostUSD == nil || *read.Records[0].Usage.CostUSD != .4 {
		t.Fatal("empty error discarded usage", err, readErr)
	}
}

func TestHostObservationInvalidUsageComponentKeepsKnownCost(t *testing.T) {
	spec, transport, session := hostObservationSpec(t)
	res, err := observedInjectedRun(spec, func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": false, "usage": map[string]any{"input_tokens": true, "cost_usd": .6}}, nil
	})
	closeHostObservation(session)
	read, readErr := transport.Read()
	if err != nil || readErr != nil || len(read.Records) != 1 || read.Coverage != "partial" || read.Records[0].Usage.CostUSD == nil || *read.Records[0].Usage.CostUSD != .6 {
		t.Fatal("invalid component erased cost", err, readErr)
	}
	raw := dumpLine(res)
	var wire map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &wire) != nil {
		t.Fatal("opt-in stdout no longer JSON")
	}
	if _, err = crewrun.DecodeUsageObservation(wire["usage_observation"]); err != nil {
		t.Fatal("rich stdout sidecar cannot decode", err)
	}
}
func TestHostObservationCLIHiddenCallsAndTimeoutRemainPartial(t *testing.T) {
	spec, transport, session := hostObservationSpec(t)
	patch(t, &spawnCLI, func([]string, string, string, map[string]string, time.Duration, string) (string, string, int, error) {
		return "paid partial output", "", 1, errTimeout
	})
	res, err := spawnNode(spec, host.CLI{Name: "claude"}, "first", "medium", "synthetic task", "instructions", "", "read", "", nil, nil, time.Second, time.Now())
	closeHostObservation(session)
	read, readErr := transport.Read()
	if err != nil || res["error"] != "timeout" || readErr != nil || len(read.Records) != 1 || read.Coverage != "partial" {
		t.Fatal("timeout business/coverage changed", err, readErr)
	}
	row := read.Records[0]
	if row.Status != "timeout" || row.Usage.OutputTokens == nil || *row.Usage.OutputTokens <= 0 || row.Usage.OutputBasis != "estimated" {
		t.Fatal("partial output estimate discarded or promoted")
	}
}

func TestHostObservationFallbackNeverInventsAllocatedPredecessor(t *testing.T) {
	spec, transport, session := hostObservationSpec(t)
	unused, owned := startHostObservation(spec)
	if unused == nil || !owned {
		t.Fatal("allocation failed")
	}
	fallbackSpec := withKey(spec, hostObservationRouteKey, 1)
	next, owned := startHostObservation(fallbackSpec)
	if next == nil || !owned {
		t.Fatal("allocation failed")
	}
	if next.identity.FallbackParentCallKey != nil {
		t.Fatal("allocated but unrecorded call invented predecessor")
	}
	next.begin()
	finishHostObservation(next, true, fallbackSpec, map[string]any{"ok": false, "usage": map[string]any{"cost_usd": .3}}, nil)
	_, _ = startHostObservation(spec)
	last, _ := startHostObservation(withKey(spec, hostObservationRouteKey, 2))
	if last.identity.FallbackParentCallKey == nil || *last.identity.FallbackParentCallKey != next.identity.CallKey {
		t.Fatal("fallback did not retain actual recorded failed predecessor")
	}
	closeHostObservation(session)
	read, err := transport.Read()
	if err != nil || len(read.Records) != 1 {
		t.Fatal("allocation without begin became observation", err)
	}
}
