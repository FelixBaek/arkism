package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// With ARKISM_CMD_TEST_MAIN=1 the test binary runs main() as the arkism binary.
func TestMain(m *testing.M) {
	if os.Getenv("ARKISM_CMD_TEST_MAIN") == "1" {
		os.Args = []string{"arkism"}
		main()
	}
	os.Exit(m.Run())
}

// The binary links every subcommand package: the usage list names each group.
func TestBinaryListsRegisteredCommands(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "ARKISM_CMD_TEST_MAIN=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatal("usage exit", err)
	}
	usage := stderr.String()
	if !strings.HasPrefix(usage, "usage: arkism COMMAND [ARGS]\ncommands:\n") {
		t.Fatal(usage)
	}
	for _, name := range []string{"backup", "doctor", "gateway"} {
		if !strings.Contains(usage, "\n  "+name+"\n") {
			t.Errorf("%s missing from usage:\n%s", name, usage)
		}
	}
}
