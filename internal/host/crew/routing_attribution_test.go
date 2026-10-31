package crew

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// routeOnce runs one routed step (the pick fails before it starts, the fallback succeeds) and returns the result,
// the call reports and every attribution body the runner sent.
func routeOnce(t *testing.T, spec map[string]any, sendErr error) (map[string]any, []map[string]any, []string) {
	t.Helper()
	patch(t, &routeNow, func() time.Time { return time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC) })
	var bodies []string
	patch(t, &runnerCall, func(path string, payload any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/routing/attributions" {
			t.Errorf("unexpected call %s", path)
		}
		raw, _ := host.Dumps(payload, host.DumpOpts{})
		bodies = append(bodies, raw)
		return map[string]any{}, sendErr
	})
	decide := func(_, group string, _ host.Obj) (map[string]any, error) {
		d := map[string]any{"id": "xai/grok-5", "decision": "clef", "fallbacks": []any{"openai/gpt-6.1"}}
		if group != "" {
			d["group"] = group
		}
		return d, nil
	}
	run := func(s map[string]any) (map[string]any, error) {
		if s["model"] == "grok-5" {
			return result(false, "", "grok-5", "", "", time.Time{}, "cli_not_found"), nil
		}
		return map[string]any{"ok": true, "output": "secret output", "model": s["model"], "duration_ms": 900}, nil
	}
	var calls []map[string]any
	out, err := runRouted(withKey(spec, "task", "fix the login bug"), decide, run, func(c map[string]any) { calls = append(calls, c) })
	if err != nil {
		t.Fatal(err)
	}
	return out, calls, bodies
}

// Off (the default) writes no record and routes exactly as on: same result, same call reports.
func TestAttributionOffWritesNothingAndRoutesTheSame(t *testing.T) {
	patch(t, &attributionOn, func() bool { return false })
	offOut, offCalls, offBodies := routeOnce(t, map[string]any{}, nil)
	if len(offBodies) != 0 {
		t.Fatal("record written while off", offBodies)
	}
	patch(t, &attributionOn, func() bool { return true })
	onOut, onCalls, onBodies := routeOnce(t, map[string]any{}, nil)
	if len(onBodies) != 1 || !reflect.DeepEqual(offOut, onOut) || !reflect.DeepEqual(offCalls, onCalls) {
		t.Fatal(onBodies, offOut, onOut)
	}
	t.Setenv("ARKISM_ROUTING_ATTRIBUTION", "")
	if attributionOnDefault() {
		t.Fatal("on without the variable")
	}
	t.Setenv("ARKISM_ROUTING_ATTRIBUTION", "1")
	if !attributionOnDefault() {
		t.Fatal("off with the variable")
	}
}

func TestAttributionOnWritesOneRecordPerStep(t *testing.T) {
	patch(t, &attributionOn, func() bool { return true })
	origin := host.Obj{{Key: "kind", Value: "workflow"}, {Key: "run_id", Value: "run-1"}, {Key: "node_id", Value: "n1"}}
	_, _, bodies := routeOnce(t, map[string]any{"origin": origin, "group": "group/fast"}, nil)
	if len(bodies) != 1 {
		t.Fatal(bodies)
	}
	b := bodies[0]
	for _, want := range []string{`"decided_at": "2026-10-31T01:02:03Z"`, `"decision": "clef"`, `"chosen": "xai/grok-5"`, `"fallbacks": ["openai/gpt-6.1"]`,
		`{"index": 0, "model": "xai/grok-5", "ok": false, "latency_ms": 0, "error": "cli_not_found"}`,
		`{"index": 1, "model": "openai/gpt-6.1", "ok": true, "latency_ms": 900}`, `"group": "group/fast"`, `"run_id": "run-1"`} {
		if !strings.Contains(b, want) {
			t.Error(want, b)
		}
	}
	for _, leak := range []string{"fix the login bug", "secret output", "stopped", "arm"} {
		if strings.Contains(b, leak) {
			t.Error("record carries", leak, b)
		}
	}
	// A second step is another record with its own decision id.
	_, _, again := routeOnce(t, map[string]any{}, nil)
	if decisionID(t, again[0]) == decisionID(t, b) {
		t.Fatal("two steps share a decision id")
	}
}

// A failed send is retried once with the same record (same decision id); the server keeps one.
func TestAttributionResendKeepsTheDecisionID(t *testing.T) {
	patch(t, &attributionOn, func() bool { return true })
	_, _, bodies := routeOnce(t, map[string]any{}, errors.New("down"))
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatal(bodies)
	}
}

func TestPilotArmNeedsAttribution(t *testing.T) {
	decide := func(string, string, host.Obj) (map[string]any, error) {
		t.Fatal("decided without the switch")
		return nil, nil
	}
	patch(t, &attributionOn, func() bool { return false })
	if _, err := runRouted(map[string]any{"task": "x", "pilot_arm": "F-ADAPTIVE"}, decide, nil, nil); !isCrewErr(err) || !strings.Contains(err.Error(), "ARKISM_ROUTING_ATTRIBUTION") {
		t.Fatal(err)
	}
	if rc, _, errOut := crewCLI([]string{"route", "--task", "x", "--pilot-arm", "F-ADAPTIVE", "--group", "group/fast"}, ""); rc != 2 || !strings.Contains(errOut, "ARKISM_ROUTING_ATTRIBUTION") {
		t.Fatal(rc, errOut)
	}
	patch(t, &attributionOn, func() bool { return true })
	if _, err := runRouted(map[string]any{"task": "x", "pilot_arm": "bad arm"}, decide, nil, nil); !isCrewErr(err) {
		t.Fatal(err)
	}
	_, _, bodies := routeOnce(t, map[string]any{"pilot_arm": "F-ADAPTIVE"}, nil)
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"arm": "F-ADAPTIVE"`) {
		t.Fatal(bodies)
	}
}

func decisionID(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `"decision_id": "`)
	id, _, _ := strings.Cut(rest, `"`)
	if !ok || !strings.HasPrefix(id, "rte-") || len(id) != 36 {
		t.Fatal(body)
	}
	return id
}
