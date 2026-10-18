package crewrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnqueueReceiptConcurrentPublicationKeepsOneCompleteRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts with spaces")
	receipt := TraceReceipt{Scope: map[string]string{"workspace_id": "workspace"}, Trace: Ref{Collection: "execution_traces", ID: "synthetic-trace", Revision: 1}}
	var writers sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		writers.Add(1)
		go func() { defer writers.Done(); failures <- EnqueueReceipt(dir, receipt) }()
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("atomic receipt publication failed", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatal("duplicate or temporary receipt files remain", entries)
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Scope map[string]string `json:"scope"`
		Trace Ref               `json:"trace"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal("receipt was torn", err)
	}
	if stored.Trace != receipt.Trace || stored.Scope["workspace_id"] != "workspace" {
		t.Fatal("receipt contents changed", stored)
	}
	if err := EnqueueReceipt(dir, receipt); err != nil {
		t.Fatal("receipt replay failed", err)
	}
	if repeated, err := os.ReadFile(filepath.Join(dir, entries[0].Name())); err != nil || string(repeated) != string(raw) {
		t.Fatal("replay changed receipt contents", err)
	}
}

func TestEnqueueReceiptPublicationDoesNotReplaceOrHideErrors(t *testing.T) {
	dir := t.TempDir()
	first, second, target := filepath.Join(dir, "first.tmp"), filepath.Join(dir, "second.tmp"), filepath.Join(dir, "receipt.json")
	if err := os.WriteFile(first, []byte("first complete receipt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second complete receipt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishReceipt(first, target, dir); err != nil {
		t.Fatal(err)
	}
	if err := publishReceipt(second, target, dir); err != nil {
		t.Fatal("existing target should be idempotent", err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "first complete receipt" {
		t.Fatal("publication replaced an existing target", err)
	}
	missing := filepath.Join(dir, "missing", "receipt.json")
	if err := publishReceipt(second, missing, filepath.Dir(missing)); err == nil {
		t.Fatal("publication failure was ignored")
	}
	if raw, err := os.ReadFile(second); err != nil || string(raw) != "second complete receipt" {
		t.Fatal("failed publication consumed its source", err)
	}
}
