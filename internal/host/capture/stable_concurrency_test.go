package capture

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitStableFixture(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

type blockedStableWriter struct{ root string }

func (w blockedStableWriter) Write(data []byte) (int, error) {
	if err := os.WriteFile(filepath.Join(w.root, "first-output"), nil, 0600); err != nil {
		return 0, err
	}
	if !waitStableFixture(filepath.Join(w.root, "release"), 10*time.Second) {
		return 0, fmt.Errorf("release timeout")
	}
	return os.Stdout.Write(data)
}

func TestConcurrentStableHookHelper(t *testing.T) {
	root := os.Getenv("ARKISM_STABLE_TEST_ROOT")
	if root == "" {
		return
	}
	stableFake(t)
	capState = root
	label := os.Getenv("ARKISM_STABLE_TEST_LABEL")
	// Both processes observe the same current backend memory; label is coordination only.
	if err := os.WriteFile(filepath.Join(root, label+"-ready"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"concurrent-session"}`)
	if label == "first" {
		hookMain(input, blockedStableWriter{root})
	} else {
		hookMain(input, os.Stdout)
	}
}

func TestConcurrentFirstSessionStartsKeepIdenticalStableOutput(t *testing.T) {
	root := t.TempDir()
	run := func(label string) (*exec.Cmd, *bytes.Buffer) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentStableHookHelper$")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "ARKISM_STABLE_TEST_ROOT="+root, "ARKISM_STABLE_TEST_LABEL="+label, "ARKISM_WORK_ID=", "ARKISM_WORK_AGENT=", "ARKISM_AGENT=")
		output := new(bytes.Buffer)
		cmd.Stdout = output
		cmd.Stderr = output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		return cmd, output
	}
	first, firstOut := run("first")
	if !waitStableFixture(filepath.Join(root, "first-output"), 10*time.Second) {
		t.Fatal("first hook never reached output")
	}
	second, secondOut := run("second")
	if !waitStableFixture(filepath.Join(root, "second-ready"), 10*time.Second) {
		t.Fatal("second hook never started")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- second.Wait() }()
	finished := false
	select {
	case err := <-secondDone:
		finished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatal(err, firstOut.String())
	}
	if !finished {
		if err := <-secondDone; err != nil {
			t.Fatal(err, secondOut.String())
		}
	}
	// Each helper also prints go test's own PASS/coverage lines; compare the hook JSON line only.
	hookLine := func(b *bytes.Buffer) string {
		for line := range strings.Lines(b.String()) {
			if strings.HasPrefix(line, `{"hookSpecificOutput"`) {
				return line
			}
		}
		return ""
	}
	if a := hookLine(firstOut); a == "" || a != hookLine(secondOut) {
		t.Fatalf("same session emitted different stable blocks:\nfirst %s\nsecond %s", firstOut.String(), secondOut.String())
	}
}
