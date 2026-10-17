package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TC-BASE-005: the evaluation file is strict JSONL; "self" needs no file.
func TestBaselineQueries(t *testing.T) {
	if q, self, err := baselineQueries("self"); err != nil || !self || q != nil {
		t.Fatalf("self: %v %v %v", q, self, err)
	}
	path := filepath.Join(t.TempDir(), "q.jsonl")
	if err := os.WriteFile(path, []byte("{\"query\":\"a\",\"relevant\":[\"m1\"]}\n\n{\"query\":\"b\",\"negative\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	q, self, err := baselineQueries(path)
	if err != nil || self || len(q) != 2 || q[0].Relevant[0] != "m1" || !q[1].Negative {
		t.Fatalf("file: %+v %v %v", q, self, err)
	}
	for _, bad := range []string{"{\"query\":\"\"}\n", "{\"query\":\"a\",\"text\":\"x\"}\n", "not json\n"} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := baselineQueries(path); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
