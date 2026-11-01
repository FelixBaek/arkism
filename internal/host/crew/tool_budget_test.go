package crew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func budgetEvent(v any) string {
	b, _ := json.Marshal(v)
	return string(b) + "\n"
}

// Legacy runner tests supply the answer text. Model the structured wire format
// only when production asks for it; malformed/event tests use fakeEventSpawn.
func fakeCLIOutput(argv []string, answer string, code int) string {
	if index(argv, "stream-json") >= 0 {
		subtype := "success"
		if code != 0 {
			subtype = "error_during_execution"
		}
		return budgetEvent(map[string]any{"type": "result", "subtype": subtype, "is_error": code != 0, "result": answer})
	}
	if index(argv, "--json") >= 0 && index(argv, "exec") >= 0 {
		terminal := "turn.completed"
		if code != 0 {
			terminal = "turn.failed"
		}
		return budgetEvent(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "id": "answer", "text": answer}}) + budgetEvent(map[string]any{"type": terminal})
	}
	return answer
}

func fakeEventSpawn(t *testing.T, stream string) *[]spawnCall {
	calls := &[]spawnCall{}
	patch(t, &spawnCLI, func(argv []string, stdin, cwd string, env map[string]string, _ time.Duration, _ string) (string, string, int, error) {
		*calls = append(*calls, spawnCall{argv, stdin, cwd, env})
		return stream, "", 0, nil
	})
	return calls
}

func claudeBudgetFixture(n int) string {
	var stream strings.Builder
	for i := 0; i < n; i++ {
		stream.WriteString(budgetEvent(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": fmt.Sprintf("call-%d", i), "name": "Read", "input": map[string]any{"file_path": "private-canary"}},
		}}}))
	}
	stream.WriteString(budgetEvent(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "final answer"}))
	return stream.String()
}

func TestToolBudgetCountsCallsRatherThanTurns(t *testing.T) {
	for _, n := range []int{0, 50, 51} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			calls := fakeEventSpawn(t, claudeBudgetFixture(n))
			res, err := spawnNode(nil, row(t, "opus"), "opus", "low", "synthetic", "", "", "read", t.TempDir(), nil, nil, time.Second, time.Now())
			if err != nil || res["ok"] != (n <= 50) || res["output"] != "final answer" {
				t.Fatalf("tool call bound missing: %+v, %v", res, err)
			}
			if n > 50 && res["error"] != "tool_call_limit_exceeded" {
				t.Fatalf("expected terminal budget error: %+v", res)
			}
			if len(*calls) != 1 || index((*calls)[0].argv, "stream-json") < 0 {
				t.Fatal("runner did not request observable tool events")
			}
			usage, ok := res["tool_usage"].(toolUsage)
			if !ok || !usage.Complete || usage.ObservedCalls != n || strings.Contains(dumpLine(ordered(res, resultOrder...)), "private-canary") {
				t.Fatal("missing usage or leaked tool arguments")
			}
		})
	}
}

func TestToolBudgetStopsReadOnlyRoutingFallback(t *testing.T) {
	fakeEventSpawn(t, claudeBudgetFixture(51))
	attempts := 0
	run := func(spec map[string]any) (map[string]any, error) {
		attempts++
		return spawnNode(spec, row(t, "opus"), "opus", "low", "synthetic", "", "", "read", t.TempDir(), nil, nil, time.Second, time.Now())
	}
	res, i, stopped, err := tryRoutes(map[string]any{"task": "synthetic", "access": "read"}, nil, []string{"a/opus", "b/opus"}, true, run, func(map[string]any) {})
	if err != nil || res["ok"] != false || i != 0 || attempts != 1 || stopped != "tool_call_limit_exceeded" {
		t.Fatalf("exhausted budget replayed: attempts=%d i=%d stop=%s err=%v", attempts, i, stopped, err)
	}
}

func TestToolBudgetKnownNoDispatchDoesNotPoisonFallback(t *testing.T) {
	attempts := 0
	patch(t, &spawnCLI, func([]string, string, string, map[string]string, time.Duration, string) (string, string, int, error) {
		attempts++
		if attempts == 1 {
			return "", "", 0, errSpawn
		}
		return claudeBudgetFixture(1), "", 0, nil
	})
	run := func(spec map[string]any) (map[string]any, error) {
		return spawnNode(spec, row(t, "opus"), "opus", "low", "synthetic", "", "", "read", t.TempDir(), nil, nil, time.Second, time.Now())
	}
	res, i, stopped, err := tryRoutes(map[string]any{"access": "read"}, nil, []string{"a/opus", "b/opus"}, true, run, func(map[string]any) {})
	usage := res["tool_usage"].(toolUsage)
	if err != nil || res["ok"] != true || !usage.Complete || usage.ObservedCalls != 1 || stopped != "" || i != 1 || attempts != 2 {
		t.Fatalf("known zero poisoned fallback: complete=%v calls=%d stop=%s attempts=%d", usage.Complete, usage.ObservedCalls, stopped, attempts)
	}
}

func TestToolBudgetStreamCompletenessAndDeduplication(t *testing.T) {
	call := budgetEvent(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "a"}, map[string]any{"type": "tool_use", "id": "b"},
	}}})
	terminal := claudeBudgetFixture(0)
	for _, tc := range []struct {
		name, raw string
		calls     int
		complete  bool
	}{
		{"duplicate messages", call + call + terminal, 2, true},
		{"truncated", call, 2, false},
		{"malformed", call + "not-json\n" + terminal, 2, false},
		{"new format", call + "{\"type\":\"new-tool-format\"}\n" + terminal, 2, false},
		{"plain result", "final answer", 0, false},
		{"empty", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, u, _ := readToolEvents(tc.raw, "claude")
			if u.ObservedCalls != tc.calls || u.Complete != tc.complete {
				t.Fatalf("wrong accounting: %+v", u)
			}
		})
	}
}

func TestToolBudgetCodexCountsInvocationsOnce(t *testing.T) {
	var raw strings.Builder
	for i, kind := range []string{"command_execution", "file_change", "mcp_tool_call", "web_search"} {
		for _, stage := range []string{"item.started", "item.updated", "item.completed"} {
			raw.WriteString(budgetEvent(map[string]any{"type": stage, "item": map[string]any{"id": fmt.Sprint(i), "type": kind}}))
		}
	}
	raw.WriteString(fakeCLIOutput([]string{"exec", "--json"}, "final", 0))
	answer, u, failed := readToolEvents(raw.String(), "codex")
	if answer != "final" || !u.Complete || u.ObservedCalls != 4 || failed {
		t.Fatalf("wrong Codex accounting: %+v failed=%v", u, failed)
	}
}

func TestToolBudgetAccumulatesAcrossRoutingAttempts(t *testing.T) {
	attempts := 0
	run := func(map[string]any) (map[string]any, error) {
		attempts++
		return map[string]any{"ok": attempts == 2, "duration_ms": 1, "error": "cli_run_failed",
			"tool_usage": toolUsage{Protocol: "crew-tool-calls-v1", Limit: 50, ObservedCalls: 30, Complete: true, Enforcement: "post_run"}}, nil
	}
	res, i, stopped, err := tryRoutes(map[string]any{"task": "synthetic", "access": "read"}, nil, []string{"a/opus", "b/opus", "c/opus"}, true, run, func(map[string]any) {})
	usage := res["tool_usage"].(toolUsage)
	if err != nil || res["ok"] != false || attempts != 2 || i != 1 || stopped != "tool_call_limit_exceeded" || usage.ObservedCalls != 60 {
		t.Fatal("fallback reset tool budget", attempts, i, stopped, usage, err)
	}
}

func TestToolBudgetAtLimitRefusesAnotherRoute(t *testing.T) {
	attempts := 0
	run := func(map[string]any) (map[string]any, error) {
		attempts++
		return map[string]any{"ok": false, "duration_ms": 1, "error": "cli_run_failed",
			"tool_usage": toolUsage{Protocol: "crew-tool-calls-v1", Limit: 50, ObservedCalls: 50, Complete: true, Enforcement: "post_run"}}, nil
	}
	res, i, stopped, err := tryRoutes(map[string]any{"task": "synthetic", "access": "read"}, nil, []string{"a/opus", "b/opus"}, true, run, func(map[string]any) {})
	if err != nil || attempts != 1 || i != 0 || stopped != "tool_call_limit_reached" || res["ok"] != false {
		t.Fatal("started after exhaustion", attempts, i, stopped, err)
	}
}

func TestToolBudgetPartialFailureNeverFallsOver(t *testing.T) {
	for _, failure := range []error{nil, errTimeout} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			stream := strings.TrimSuffix(claudeBudgetFixture(10), claudeBudgetFixture(0))
			calls := 0
			patch(t, &spawnCLI, func([]string, string, string, map[string]string, time.Duration, string) (string, string, int, error) {
				calls++
				return stream, "", 1, failure
			})
			run := func(spec map[string]any) (map[string]any, error) {
				return spawnNode(spec, row(t, "opus"), "opus", "low", "synthetic", "", "", "read", t.TempDir(), nil, nil, time.Second, time.Now())
			}
			res, i, stopped, err := tryRoutes(map[string]any{"task": "synthetic", "access": "read"}, nil, []string{"a/opus", "b/opus"}, true, run, func(map[string]any) {})
			if err != nil || calls != 1 || i != 0 || stopped != "tool_call_count_unknown" || res["ok"] != false {
				t.Fatal("partial stream replayed", calls, i, stopped, err)
			}
		})
	}
}

func TestToolBudgetMalformedSuccessIsTerminal(t *testing.T) {
	fakeEventSpawn(t, "a successful-looking plain answer")
	res, err := spawnNode(nil, row(t, "opus"), "opus", "low", "synthetic", "", "", "read", t.TempDir(), nil, nil, time.Second, time.Now())
	if err != nil || res["ok"] != false || res["error"] != "tool_call_count_unknown" || res["output"] != "" {
		t.Fatal("unknown count passed as zero", res, err)
	}
}

func TestToolBudgetPartialAnswerCannotSwitchModel(t *testing.T) {
	stream := budgetEvent(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "partial answer"}}}}) + budgetEvent(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": ""})
	calls := fakeEventSpawn(t, stream)
	run := func(spec map[string]any) (map[string]any, error) {
		return spawnNode(spec, row(t, "opus"), "opus", "low", "synthetic", "", "", "read", t.TempDir(), nil, nil, time.Second, time.Now())
	}
	_, i, stopped, err := tryRoutes(map[string]any{"task": "synthetic", "access": "read"}, nil, []string{"a/opus", "b/opus"}, true, run, func(map[string]any) {})
	if err != nil || len(*calls) != 1 || i != 0 || stopped != "response_started" {
		t.Fatal("partial response replayed", len(*calls), i, stopped, err)
	}
}

func TestToolBudgetUnsupportedCLINeverDispatches(t *testing.T) {
	for _, model := range []string{"grok-5", "gemini-3.8-flash"} {
		t.Run(model, func(t *testing.T) {
			calls := fakeEventSpawn(t, "raw private output must never be read")
			res, err := spawnNode(nil, row(t, model), model, "low", "synthetic", "", "", "write", t.TempDir(), nil, nil, time.Second, time.Now())
			usage, ok := res["tool_usage"].(toolUsage)
			if err != nil || len(*calls) != 0 || res["ok"] != false || res["error"] != "tool_call_count_unsupported" || res["output"] != "" || !ok || !usage.Complete || usage.ObservedCalls != 0 || usage.Enforcement != "not_dispatched" {
				t.Fatalf("unsupported CLI was not refused before execution: calls=%d result=%+v err=%v", len(*calls), res, err)
			}
			if mayHaveChanged(map[string]any{"access": "write"}, res) {
				t.Fatal("known predispatch refusal claimed possible effects")
			}
		})
	}
}

func TestToolBudgetUnsupportedLocalCanaryNeverStarts(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	script := filepath.Join(dir, "gemini-canary")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_GEMINI_BIN", script)
	res, err := spawnNode(nil, row(t, "gemini-3"), "gemini-3", "low", "synthetic", "", "", "read", dir, nil, nil, time.Second, time.Now())
	if err != nil || res["error"] != "tool_call_count_unsupported" {
		t.Fatal(res, err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("actual unsupported local executable started", err)
	}
}

func TestToolBudgetUnsupportedCLIAllowsKnownZeroFallback(t *testing.T) {
	calls := fakeEventSpawn(t, claudeBudgetFixture(1))
	run := func(spec map[string]any) (map[string]any, error) {
		model := sOr(spec["model"])
		return spawnNode(spec, row(t, model), model, "low", "synthetic", "", "", "write", t.TempDir(), nil, nil, time.Second, time.Now())
	}
	res, i, stop, err := tryRoutes(map[string]any{"access": "write"}, nil, []string{"xai/grok-5", "anthropic/opus"}, true, run, func(map[string]any) {})
	usage, ok := res["tool_usage"].(toolUsage)
	if err != nil || res["ok"] != true || i != 1 || stop != "" || len(*calls) != 1 || !ok || !usage.Complete || usage.ObservedCalls != 1 {
		t.Fatal("known-zero refusal poisoned fallback", res, i, stop, err, len(*calls))
	}
}

// Wire diagnostics and repeated tool events are not answer output tokens.
func TestToolBudgetObservationEstimatesExtractedAnswer(t *testing.T) {
	for _, model := range []string{"opus", "gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			cli := row(t, model)
			answer := "final answer"
			stream := claudeBudgetFixture(20)
			if cli.Name == "codex" {
				stream = budgetEvent(map[string]any{"type": "item.completed", "item": map[string]any{"type": "command_execution", "id": "tool-1", "aggregated_output": strings.Repeat("diagnostic", 1000)}}) + fakeCLIOutput([]string{"exec", "--json"}, answer, 0)
			}
			patch(t, &spawnCLI, func(_ []string, _, _ string, _ map[string]string, _ time.Duration, outFile string) (string, string, int, error) {
				if cli.Name == "codex" {
					if err := os.WriteFile(outFile, []byte(answer), 0600); err != nil {
						t.Fatal(err)
					}
					return strings.ReplaceAll(stream, answer, strings.Repeat("wire draft ", 100)), "", 0, nil
				}
				return stream, "", 0, nil
			})
			spec, _, _ := hostObservationSpec(t)
			call, owned := startHostObservation(spec)
			active := withKey(spec, hostObservationActiveKey, call)
			res, err := spawnNode(active, cli, model, "low", "synthetic", "", "", "read", t.TempDir(), nil, nil, time.Second, time.Now())
			finishHostObservation(call, owned, spec, res, err)
			expected := usageOf(map[string]any{"task": "synthetic"}, map[string]any{"output": answer, "model": model})
			if err != nil || res["ok"] != true || res["output"] != answer || !reflect.DeepEqual(call.estimated, expected) {
				t.Fatalf("wire events inflated answer estimate: got=%v want=%v err=%v", call.estimated, expected, err)
			}
		})
	}
}
