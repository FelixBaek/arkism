package crew

import (
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The step's timeout is one budget for the whole fallback chain, not a fresh timeout per attempt.
func TestFallbackChainSharesOneTimeBudget(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	patch(t, &routeNow, func() time.Time { return clock })
	decision := map[string]any{"id": "xai/grok-5", "decision": "clef", "fallbacks": []any{"openai/gpt-6.1", "anthropic/opus"}}
	decide := func(string, string, host.Obj) (map[string]any, error) { return decision, nil }
	// Every attempt is refused before running (a crew error), so fallback is allowed, and spends spend on the clock.
	drive := func(spec map[string]any, spend time.Duration) (map[string]any, []any) {
		clock = time.Unix(1_000_000, 0)
		var given []any
		run := func(s map[string]any) (map[string]any, error) {
			given = append(given, s["timeout_s"])
			clock = clock.Add(spend)
			return nil, crewErr("refused")
		}
		out, err := runRouted(withKey(spec, "task", "x"), decide, run, func(map[string]any) {})
		if err != nil {
			t.Fatal(err)
		}
		return out, given
	}
	t.Setenv("ARKISM_CREW_TIMEOUT", "100")
	sec := time.Second
	for _, c := range []struct {
		name    string
		spec    map[string]any
		spend   time.Duration
		timeout []any // timeout_s each attempt was given
		stopped any
		id      string
	}{
		{"60 s: 60, 35, 10", map[string]any{"timeout_s": 60}, 25 * sec, []any{60, int64(35), int64(10)}, nil, "anthropic/opus"},
		{"5 s left, then none", map[string]any{"timeout_s": 50}, 45 * sec, []any{50, int64(5)}, "budget_spent", "openai/gpt-6.1"},
		{"0.5 s left stops", map[string]any{"timeout_s": 10}, 9500 * time.Millisecond, []any{10}, "budget_spent", "xai/grok-5"},
		{"just under 1 s left stops", map[string]any{"timeout_s": 10}, 9001 * time.Millisecond, []any{10}, "budget_spent", "xai/grok-5"},
		{"exactly 1 s left runs with 1", map[string]any{"timeout_s": 10}, 9 * sec, []any{10, int64(1)}, "budget_spent", "openai/gpt-6.1"},
		{"1.2 s left rounds up to 2", map[string]any{"timeout_s": 10}, 8800 * time.Millisecond, []any{10, int64(2)}, "budget_spent", "openai/gpt-6.1"},
		{"spent before the second", map[string]any{"timeout_s": 60}, 60 * sec, []any{60}, "budget_spent", "xai/grok-5"},
		{"no timeout_s uses the step default (100 s)", map[string]any{}, 40 * sec, []any{nil, int64(60), int64(20)}, nil, "anthropic/opus"},
		{"agent-runner step without timeout_s uses its own default", map[string]any{"backend": "box"}, time.Hour, []any{nil, int64(10800), int64(7200)}, nil, "anthropic/opus"},
		// invalid, zero-or-negative and overflowing limits get no budget: nothing is overridden and every model is tried
		{"invalid timeout_s", map[string]any{"timeout_s": "soon"}, time.Hour, []any{"soon", "soon", "soon"}, nil, "anthropic/opus"},
		{"negative timeout_s", map[string]any{"timeout_s": -5}, time.Hour, []any{-5, -5, -5}, nil, "anthropic/opus"},
		{"overflowing timeout_s", map[string]any{"timeout_s": int64(20_000_000_000)}, time.Hour, []any{int64(20_000_000_000), int64(20_000_000_000), int64(20_000_000_000)}, nil, "anthropic/opus"},
		{"timeout_s at the round-up edge", map[string]any{"timeout_s": int64(9_223_372_036)}, time.Hour, []any{int64(9_223_372_036), int64(9_223_372_036), int64(9_223_372_036)}, nil, "anthropic/opus"},
	} {
		out, given := drive(c.spec, c.spend)
		routed := out["routed"]
		eq(t, given, c.timeout)
		if field(routed, "stopped") != c.stopped || field(routed, "id") != c.id {
			t.Fatal(c.name, out)
		}
	}
}

// A started write step that fails is effect_unknown, not budget_spent, even when the failure also spent the budget;
// a read step that times out and spends the budget stops with the previous attempt's result.
func TestBudgetStopNeverHidesAnUnknownEffect(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	patch(t, &routeNow, func() time.Time { return clock })
	decision := map[string]any{"id": "xai/grok-5", "decision": "clef", "fallbacks": []any{"openai/gpt-6.1"}}
	decide := func(string, string, host.Obj) (map[string]any, error) { return decision, nil }
	for _, c := range []struct{ access, stopped string }{{"write", "effect_unknown"}, {"read", "budget_spent"}} {
		clock = time.Unix(1_000_000, 0)
		var ran []any
		run := func(s map[string]any) (map[string]any, error) {
			ran = append(ran, s["model"])
			clock = clock.Add(30 * time.Second)
			return result(false, "", pyStr(s["model"]), "", "grok", time.Now().Add(-time.Second), "timeout"), nil
		}
		out, err := runRouted(map[string]any{"task": "x", "access": c.access, "timeout_s": 30}, decide, run, func(map[string]any) {})
		if err != nil || len(ran) != 1 || out["error"] != "timeout" || out["model"] != "grok-5" ||
			field(out["routed"], "stopped") != c.stopped || field(out["routed"], "id") != "xai/grok-5" ||
			dumpLine(field(out["routed"], "tried")) != `["xai/grok-5"]` {
			t.Fatal(c, ran, err, out)
		}
	}
}

// However many fallbacks a decision lists, a routed step makes at most 4 attempts.
func TestFallbackChainCapsTheCallCount(t *testing.T) {
	all := []string{"a/m1", "a/m2", "a/m3", "a/m4", "a/m5", "a/m6"}
	for n, want := range map[int]int{0: 1, 1: 2, 3: 4, 4: 4, 6: 4} {
		fallbacks := []any{}
		for _, id := range all[:n] {
			fallbacks = append(fallbacks, id)
		}
		decision := map[string]any{"id": "a/m0", "decision": "clef", "fallbacks": fallbacks}
		decide := func(string, string, host.Obj) (map[string]any, error) { return decision, nil }
		var ran, reported []string
		run := func(s map[string]any) (map[string]any, error) {
			ran = append(ran, pyStr(s["model"]))
			return nil, crewErr("refused")
		}
		out, err := runRouted(map[string]any{"task": "x"}, decide, run, func(c map[string]any) { reported = append(reported, pyStr(c["model"])) })
		if err != nil {
			t.Fatal(err)
		}
		tried := append([]string{"a/m0"}, all[:want-1]...)
		eq(t, len(ran), want)
		eq(t, reported, tried)
		eq(t, field(out["routed"], "tried"), toAny(tried))
		if field(out["routed"], "id") != tried[want-1] || field(out["routed"], "stopped") != nil {
			t.Fatal(n, out)
		}
	}
}
