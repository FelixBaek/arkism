package crew

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

func assistObservationFixture() (map[string]any, map[string]any) {
	scope := map[string]any{"workspace_id": "ws", "project_id": "proj"}
	return map[string]any{"id": "assist-1", "revision": json.Number("7"), "scope": scope}, map[string]any{"id": "agent-1", "revision": json.Number("3"), "scope": scope, "crew": map[string]any{"type": "agent", "model": "first", "fallback_model": "second"}}
}
func TestAssistObservationExactRefsAndFailedFallbackCosts(t *testing.T) {
	rec, item := assistObservationFixture()
	transport, err := crewrun.NewObservationTransport(hostObservationParent(t, "crew_assists"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transport.Close() }()
	patch(t, &runnerCall, func(string, any, time.Duration) (map[string]any, error) { return map[string]any{"item": item}, nil })
	calls := 0
	request := transport.Request()
	out, err := tryAgentWithObservation(rec, map[string]any{"expert": "fixture", "prompt": "synthetic task"}, func(spec map[string]any) (map[string]any, error) {
		calls++
		return map[string]any{"ok": calls == 2, "model": spec["model"], "output": "fixture", "error": "exit_1", "usage": map[string]any{"cost_usd": .25, "input_tokens": json.Number("1"), "output_tokens": json.Number("0")}}, nil
	}, &request)
	if err != nil || calls != 2 || out == nil {
		t.Fatal("business fallback changed", calls, err)
	}
	read, err := transport.Read()
	if err != nil || read.Coverage != "complete" || len(read.Records) != 2 || read.Aggregate.CostUSD == nil || *read.Aggregate.CostUSD != .5 {
		t.Fatalf("failed assist cost lost %+v %v", read, err)
	}
	var failedKey string
	for _, row := range read.Records {
		if row.Status == "failed" {
			failedKey = row.Identity.CallKey
		}
	}
	for _, row := range read.Records {
		if row.Status == "succeeded" && (row.Identity.FallbackParentCallKey == nil || *row.Identity.FallbackParentCallKey != failedKey) {
			t.Fatal("assist fallback lost actual failed call key")
		}
	}
	for _, row := range read.Records {
		if row.Identity.Run.RunRef.Collection != "crew_assists" || row.Identity.CanonicalAttemptID != nil {
			t.Fatal("actual assist identity not preserved")
		}
	}
}
func TestAssistObservationRejectsMissingMismatchedForeignRefs(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any, map[string]any)
	}{
		{"missing run revision", func(rec, item map[string]any) { delete(rec, "revision") }},
		{"float revision", func(rec, item map[string]any) { rec["revision"] = 7.0 }},
		{"wrong definition", func(rec, item map[string]any) { item["id"] = "other" }},
		{"foreign run", func(rec, item map[string]any) {
			rec["scope"] = map[string]any{"workspace_id": "foreign", "project_id": "proj"}
		}},
		{"foreign definition", func(rec, item map[string]any) {
			item["scope"] = map[string]any{"workspace_id": "foreign", "project_id": "proj"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, item := assistObservationFixture()
			c.change(rec, item)
			transport, err := crewrun.NewObservationTransport(hostObservationParent(t, "crew_assists"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = transport.Close() }()
			req := transport.Request()
			session := assistObservationSession(rec, item, &req)
			if session != nil {
				closeHostObservation(session)
				t.Fatal("foreign or missing source accepted")
			}
		})
	}
}
func TestAssistObservationNilOptionPreservesLegacyBusiness(t *testing.T) {
	rec, item := assistObservationFixture()
	patch(t, &runnerCall, func(string, any, time.Duration) (map[string]any, error) { return map[string]any{"item": item}, nil })
	var specs []map[string]any
	runner := func(spec map[string]any) (map[string]any, error) {
		specs = append(specs, spec)
		return map[string]any{"ok": true, "model": "first", "output": "fixture", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}, nil
	}
	a := map[string]any{"expert": "fixture", "prompt": "synthetic"}
	left, err := tryAgent(rec, a, runner)
	if err != nil {
		t.Fatal(err)
	}
	right, err := tryAgentWithObservation(rec, a, runner, nil)
	if err != nil || !reflect.DeepEqual(left, right) || len(specs) != 2 || !reflect.DeepEqual(specs[0], specs[1]) {
		t.Fatal("nil option changed behavior")
	}
	for key := range specs[0] {
		if key == hostObservationKey || key == "observation" {
			t.Fatal("nil option enabled observation")
		}
	}
}
