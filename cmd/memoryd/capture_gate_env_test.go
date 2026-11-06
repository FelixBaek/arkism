package main

import (
	"testing"
	"time"
)

func TestCaptureGateFromEnv(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 0, "5s": 5 * time.Second, "40s": 40 * time.Second} {
		t.Setenv("MEMORY_CAPTURE_GATE_BUDGET", raw)
		if _, got, err := captureGateFromEnv(); err != nil || got != want {
			t.Fatal(raw, got, err)
		}
	}
	for _, raw := range []string{"1m", "0s", "-1s", "soon"} {
		t.Setenv("MEMORY_CAPTURE_GATE_BUDGET", raw)
		if _, _, err := captureGateFromEnv(); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	t.Setenv("MEMORY_CAPTURE_GATE_BUDGET", "")
	t.Setenv("MEMORY_CAPTURE_MERGE_SIMILARITY", "1.5")
	if _, _, err := captureGateFromEnv(); err == nil {
		t.Fatal("accepted similarity 1.5")
	}
}
