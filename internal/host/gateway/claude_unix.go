//go:build unix

package gateway

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// claudeDetach starts the CLI in its own session so close can kill the whole group.
func claudeDetach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func claudeKill(p *os.Process) error { return unix.Kill(-p.Pid, unix.SIGKILL) }
