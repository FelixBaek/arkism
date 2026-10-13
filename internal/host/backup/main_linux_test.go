package backup

import (
	"os"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// With ARKISM_TEST_MAIN=1 the test binary acts as the arkism binary, so shell
// scripts and flock can run the real commands.
func TestMain(m *testing.M) {
	if os.Getenv("ARKISM_TEST_MAIN") == "1" {
		os.Exit(host.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// arkismEnv returns the test binary and an environment where it runs as arkism
// (also as ARKISM_BIN for tools/backup_postgres.sh).
func arkismEnv(t *testing.T, extra ...string) (string, []string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return bin, append(append(os.Environ(), "ARKISM_TEST_MAIN=1", "ARKISM_BIN="+bin), extra...)
}

func run(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := host.Main(args, nil, &out, &errOut)
	return code, out.String(), errOut.String()
}
