package crewrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A local subprocess exercises the real runner JSON boundary without a model.
func TestToolBudgetRunnerProcess(t *testing.T) {
	if !slices.Contains(os.Args, "run-node") {
		return
	}
	var s aiSpec
	if json.NewDecoder(os.Stdin).Decode(&s) != nil {
		os.Exit(91)
	}
	f, err := os.OpenFile(os.Getenv("ARKISM_TEST_TOOL_CALL_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(92)
	}
	_, err = f.WriteString(s.Model + "\n")
	if f.Close() != nil || err != nil {
		os.Exit(93)
	}
	answer := map[string]any{"ok": false, "model": s.Model, "error": "failed"}
	if strings.HasPrefix(s.Task, "count:") {
		n, err := strconv.Atoi(strings.TrimPrefix(s.Task, "count:"))
		if err != nil {
			os.Exit(95)
		}
		answer["tool_usage"] = aiToolUsage{Protocol: "crew-tool-calls-v1", Limit: ToolCallLimit, ObservedCalls: n, Complete: true, Enforcement: "post_run"}
	} else if s.Task == "slow" {
		time.Sleep(5 * time.Second)
	} else if s.Access == "read" {
		answer["routed"] = map[string]any{"stopped": s.Task}
	} else {
		answer["stopped"] = s.Task
	}
	if json.NewEncoder(os.Stdout).Encode(answer) != nil {
		os.Exit(94)
	}
	os.Exit(0)
}

func TestToolBudgetWorkflowSharesWallTimeAcrossRetries(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("ARKISM_TEST_TOOL_CALL_LOG", log)
	e := &Engine{Runner: []string{os.Args[0], "-test.run=^TestToolBudgetRunnerProcess$", "--"}}
	_, err := e.callWithFallback(context.Background(), aiSpec{Model: "original", Task: "slow", Access: "read"}, "replacement", 2*time.Second, 3)
	calls, readErr := os.ReadFile(log)
	if err == nil || readErr != nil || strings.TrimSpace(string(calls)) != "original" {
		t.Fatalf("wall-time budget reset: calls=%q err=%v read=%v", calls, err, readErr)
	}
}

func TestToolBudgetWorkflowRetryAndFallbackShareCalls(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		calls, retries, dispatches int
		stop                       string
	}{
		{"retry total", 30, 3, 2, "tool_call_limit_exceeded"},
		{"fallback total", 30, 0, 2, "tool_call_limit_exceeded"},
		{"retry exhausted", 50, 3, 1, "tool_call_limit_reached"},
		{"fallback exhausted", 50, 0, 1, "tool_call_limit_reached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "calls")
			t.Setenv("ARKISM_TEST_TOOL_CALL_LOG", log)
			e := &Engine{Runner: []string{os.Args[0], "-test.run=^TestToolBudgetRunnerProcess$", "--"}}
			answer, err := e.callWithFallback(context.Background(), aiSpec{Model: "original", Task: "count:" + strconv.Itoa(tc.calls), Access: "read"}, "replacement", 5*time.Second, tc.retries)
			calls, readErr := os.ReadFile(log)
			if err == nil || readErr != nil || len(strings.Fields(string(calls))) != tc.dispatches || answer.Stopped != tc.stop || answer.ToolUsage == nil || answer.ToolUsage.ObservedCalls != tc.calls*tc.dispatches {
				t.Fatalf("shared budget bypassed: calls=%q answer=%+v err=%v read=%v", calls, answer, err, readErr)
			}
		})
	}
}

func TestToolBudgetRunnerTerminalIsNotRetriedOrReplaced(t *testing.T) {
	for _, stop := range []string{"tool_call_limit_exceeded", "tool_call_count_unknown", "effect_unknown", "response_started", "budget_spent"} {
		t.Run(stop, func(t *testing.T) {
			for _, access := range []string{"read", "write"} {
				log := filepath.Join(t.TempDir(), "calls")
				t.Setenv("ARKISM_TEST_TOOL_CALL_LOG", log)
				e := &Engine{Runner: []string{os.Args[0], "-test.run=^TestToolBudgetRunnerProcess$", "--"}}
				_, err := e.callWithFallback(context.Background(), aiSpec{Model: "original", Task: stop, Access: access}, "replacement", 5*time.Second, 3)
				calls, readErr := os.ReadFile(log)
				if err == nil || readErr != nil || strings.TrimSpace(string(calls)) != "original" {
					t.Fatalf("terminal runner replayed: calls=%q err=%v read=%v", calls, err, readErr)
				}
			}
		})
	}
}
