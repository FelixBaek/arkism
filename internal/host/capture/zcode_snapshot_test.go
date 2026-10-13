package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The fixture and expected export come from the Python zcode_snapshot.
func TestZcodeSnapshotMatchesPythonGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/zcode_snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		JSONL    string      `json:"jsonl"`
		Observed string      `json:"observed"`
		Revision json.Number `json:"revision"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	called := false
	err = zcodeSnapshot(filepath.Join("testdata", "zcode_fixture.sqlite"), "s1", func(snapshot string, observed float64, revision any) error {
		called = true
		got, err := os.ReadFile(snapshot)
		if err != nil {
			return err
		}
		if string(got) != want.JSONL {
			t.Errorf("jsonl differs:\n got %s\nwant %s", got, want.JSONL)
		}
		if pyRepr(observed) != want.Observed || str(revision) != string(want.Revision) {
			t.Errorf("observed %s revision %v", pyRepr(observed), revision)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatal(err)
	}
	if err := zcodeSnapshot(filepath.Join("testdata", "zcode_fixture.sqlite"), "missing", nil); err == nil || err.Error() != "zcode_session_missing" {
		t.Fatal(err)
	}
	if err := zcodeSnapshot(filepath.Join(t.TempDir(), "none.sqlite"), "s1", nil); err == nil || err.Error() != "zcode_session_missing" {
		t.Fatal(err)
	}
}
