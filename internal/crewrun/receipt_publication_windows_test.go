package crewrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsReceiptPublicationSupportsLongSpoolPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("long-a", 15), strings.Repeat("long-b", 15), strings.Repeat("long-c", 15))
	if len(dir) <= 260 {
		t.Fatal("fixture does not exercise long Windows paths")
	}
	receipt := TraceReceipt{Trace: Ref{Collection: "execution_traces", ID: "synthetic-long-path", Revision: 1}}
	for range 2 {
		if err := EnqueueReceipt(dir, receipt); err != nil {
			t.Fatal("long-path receipt publication failed", err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("long-path receipt replay duplicated or lost the record", files, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil || !strings.Contains(string(raw), receipt.Trace.ID) {
		t.Fatal("long-path receipt contents lost", err)
	}
}
