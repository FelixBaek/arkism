package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"slices"
	"testing"
)

func fixtureCrewRunner(log string) []string {
	return []string{os.Args[0], "-test.run=^TestFixtureCrewRunnerProcess$", "--", log}
}

// The engine starts the test executable as a real run-node child on every OS.
func TestFixtureCrewRunnerProcess(t *testing.T) {
	pos := slices.Index(os.Args, "--")
	if pos < 0 || pos+2 >= len(os.Args) || os.Args[pos+2] != "run-node" {
		return
	}
	log := os.Args[pos+1]
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if log != "" {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(append(bytes.TrimSpace(input), '\n'))
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	answer := "Fixture completed answer"
	if bytes.Contains(input, []byte("JSON 하나로만")) {
		answer = `{"summary":"Fixture conclusion","decisions":["Fixture decision"],"todos":[],"open_questions":[]}`
	} else if bytes.Contains(input, []byte("STANCE")) {
		answer = "Fixture review\nSTANCE: approve"
	}
	usage := map[string]any{"input_tokens": 100, "output_tokens": 20, "cost_usd": 0.001, "estimated": log == ""}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "output": answer, "model": "opus 5.5", "effort": "medium", "cli": "fake", "duration_ms": 1, "usage": usage}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
