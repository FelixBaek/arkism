package subscription

import (
	"errors"
	"os"
)

// Windows has no FIFOs; the tests that need one or a Unix process group run on Linux CI.
func testMkfifo(string) error { return errors.New("no fifo on windows") }
func testKill(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
