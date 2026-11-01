//go:build unix

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Reuse the already compiled test executable for actual CLI interop. This is a
// test-only entry, not a standalone helper build or a production command hook.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "verify" || os.Args[1] == "run" || os.Args[1] == "hash-stream") {
		// A covered test image used as a CLI emits exit-hook diagnostics unless
		// it has a coverage directory. Its executable already lives in the
		// caller-owned temporary tree, removed by the Go/Python test driver.
		executable, err := os.Executable()
		if err != nil {
			os.Exit(1)
		}
		if err := os.Setenv("GOCOVERDIR", filepath.Dir(executable)); err != nil {
			os.Exit(1)
		}
		// Keep the actual cancellation fixtures (100ms) independent of how
		// fast a race/coverage-instrumented image starts on a particular host.
		if os.Args[1] == "run" {
			time.Sleep(250 * time.Millisecond)
		}
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestNativeFormalProjectionActualPythonCLI(t *testing.T) {
	o, args := cliFixture(t, false)
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("native fixture run exit%d: %s", code, stderr.String())
	}
	root := filepath.Dir(o.manifest)
	if err := os.WriteFile(filepath.Join(root, "stdout.json"), stdout.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../evaluation/memory-harness-v1/experiments/exp_03/test_native_projection.py")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "-B", script, "--integration", root, executable)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1"}
	if home, exists := os.LookupEnv("HOME"); exists {
		command.Env = append(command.Env, "HOME="+home)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("formal Python/native interop: %v\n%s", err, output)
	}
	t.Log(string(output))
}
