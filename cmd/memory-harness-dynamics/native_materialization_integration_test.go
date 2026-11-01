//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativeSourceMaterializationActualPythonCLI(t *testing.T) {
	runNativeSourceFixture(t, "test_materialize_native.py")
}

func TestNativePlanProductionActualPython(t *testing.T) {
	runNativeSourceFixture(t, "test_produce_run_plan.py")
}

func TestNativeExecutionRecordingActualPython(t *testing.T) {
	runNativeSourceFixture(t, "test_record_native.py")
}

func TestNativeRetriesActualPython(t *testing.T) {
	runNativeSourceFixture(t, "test_native_retries.py")
}

func TestNativeLiteralTaskAndREFActualPython(t *testing.T) {
	runNativeExperimentFixture(t, "exp_03", "test_task_adapter.py")
}

func runNativeSourceFixture(t *testing.T, filename string) {
	t.Helper()
	runNativeExperimentFixture(t, "exp_02", filename)
}

func runNativeExperimentFixture(t *testing.T, experiment, filename string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("../../evaluation/memory-harness-v1/experiments", experiment, filename))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "-B", script, "--integration", t.TempDir(), executable)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1"}
	if home, exists := os.LookupEnv("HOME"); exists {
		command.Env = append(command.Env, "HOME="+home)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("source materialization/native interop: %v\n%s", err, output)
	}
	t.Log(string(output))
}
