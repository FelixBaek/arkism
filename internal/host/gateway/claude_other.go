//go:build !unix

package gateway

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func claudeDetach(*exec.Cmd) {
	// Only Windows needs a detached console; elsewhere the default is right.
}

// claudeKill ends the CLI and its descendants with taskkill.
func claudeKill(p *os.Process) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(p.Pid), "/T", "/F").Run()
}
