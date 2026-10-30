package crewrun

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/FelixBaek/arkism/internal/receiptchain"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// The child is this Go test binary. No product runner/model is built or invoked.
func TestContextChainProcessHelper(t *testing.T) {
	if os.Getenv("ARKISM_RECEIPT_TEST_HELPER") != "1" {
		return
	}
	raw, e := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
	if e != nil {
		os.Exit(2)
	}
	var s aiSpec
	if json.Unmarshal(raw, &s) != nil {
		os.Exit(2)
	}
	out := map[string]any{"ok": true, "output": "local helper answer"}
	if mode := os.Getenv("ARKISM_RECEIPT_TEST_MODE"); mode == "off_hash" {
		out["output"] = receiptchain.Hash(raw)
	} else if mode == "failed" {
		out["ok"] = false
		out["error"] = "fixture_failure"
	}
	if s.ReceiptChain != nil {
		a := receiptchain.InputAcknowledgement{Protocol: receiptchain.Protocol, CycleID: s.ReceiptChain.CycleID, ReaderSHA256: s.ReceiptChain.ReaderSHA256, InputSHA256: receiptchain.Hash(raw)}
		switch os.Getenv("ARKISM_RECEIPT_TEST_MODE") {
		case "stale_cycle":
			a.CycleID = "old-cycle"
		case "wrong_hash":
			a.InputSHA256 = receiptchain.Hash([]byte("wrong"))
		case "wrong_reader":
			a.ReaderSHA256 = receiptchain.Hash([]byte("wrong reader"))
		}
		out["receipt_chain_ack"] = a
	}
	b, _ := json.Marshal(out)
	_, _ = fmt.Fprint(os.Stdout, string(b))
	os.Exit(0)
}
func receiptChainEngine(t *testing.T) *Engine {
	t.Helper()
	t.Setenv("ARKISM_RECEIPT_TEST_HELPER", "1")
	return &Engine{Runner: []string{os.Args[0], "-test.run=^TestContextChainProcessHelper$", "--"}}
}
func receiptChainCrewContext(runtime *ReceiptChainRuntime) context.Context {
	ctx := WithAttemptObserver(observerTestContext(), NewAttemptObserver(nil))
	ctx = withContextReceiptActor(ctx, "actual-reader")
	return WithReceiptChainRuntime(ctx, runtime)
}
func TestReceiptChainActualAdapterInput(t *testing.T) {
	for _, mode := range []string{"good", "stale_cycle", "wrong_hash", "wrong_reader", "failed"} {
		t.Run(mode, func(t *testing.T) {
			e := receiptChainEngine(t)
			t.Setenv("ARKISM_RECEIPT_TEST_MODE", mode)
			runtime := &ReceiptChainRuntime{Enabled: true, MaxCycles: 8}
			ctx := receiptChainCrewContext(runtime)
			answer, callErr := e.callAIOnce(ctx, aiSpec{Model: "local-fixture", Instructions: "INSTRUCTION_CANARY", Task: "TASK_CANARY", Once: true}, time.Second*10)
			if mode == "failed" {
				if callErr == nil {
					t.Fatal("business failure lost")
				}
			} else if callErr != nil || !answer.OK {
				t.Fatal(callErr)
			}
			sessions := runtime.Sessions()
			if len(sessions) != 1 {
				t.Fatal(runtime.Issues())
			}
			snap := sessions[0].Snapshot()
			valid := mode == "good" || mode == "failed"
			if valid && (!snap.Complete || len(snap.Records) != 5) {
				t.Fatal(snap.Issues, len(snap.Records))
			}
			if !valid {
				if snap.Complete {
					t.Fatal("invalid reader acknowledgement accepted")
				}
				for _, r := range snap.Records {
					if r.Stage == "reader_bytes_acknowledged" || r.Stage == "answer_observed" {
						t.Fatal("stale acknowledgement became observation")
					}
				}
			}
			raw, _ := json.Marshal(snap)
			if strings.Contains(string(raw), "TASK_CANARY") || strings.Contains(string(raw), "INSTRUCTION_CANARY") || strings.Contains(string(raw), "local helper answer") {
				t.Fatal("raw telemetry")
			}
			for _, r := range snap.Records {
				if r.ActualUse != nil || r.TaskSuccess != nil || r.CausalContribution != nil {
					t.Fatal("answer became success")
				}
			}
		})
	}
}
func TestReceiptChainActualAdapterOffWire(t *testing.T) {
	e := receiptChainEngine(t)
	t.Setenv("ARKISM_RECEIPT_TEST_MODE", "off_hash")
	s := aiSpec{Model: "local-fixture", Task: "TASK_CANARY", Once: true}
	raw, _ := json.Marshal(s)
	for _, ctx := range []context.Context{context.Background(), WithReceiptChainRuntime(context.Background(), &ReceiptChainRuntime{})} {
		answer, err := e.callAIOnce(ctx, s, time.Second*10)
		if err != nil || answer.Output != receiptchain.Hash(raw) {
			t.Fatal("off changed serialized input", err)
		}
	}
}
func TestReceiptChainAdapterFreshCycle(t *testing.T) {
	e := receiptChainEngine(t)
	runtime := &ReceiptChainRuntime{Enabled: true, MaxCycles: 8}
	ctx := receiptChainCrewContext(runtime)
	s := aiSpec{Model: "local-fixture", Task: "same body", Once: true}
	for range 2 {
		if _, err := e.callAIOnce(ctx, s, time.Second*10); err != nil {
			t.Fatal(err)
		}
	}
	sessions := runtime.Sessions()
	if len(sessions) != 2 || sessions[0].CycleID() == sessions[1].CycleID() {
		t.Fatal("cycle reused")
	}
	for _, s := range sessions {
		if !s.Snapshot().Complete {
			t.Fatal(s.Snapshot().Issues)
		}
	}
}
