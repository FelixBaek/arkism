//go:build unix

// Package procgroup runs a child process in its own process group so a
// timeout stops the whole group, not only the direct child.

package procgroup

import (
	"os/exec"
	"syscall"
	"time"
)

// Set puts the child in its own group and kills the whole group
// when the context ends, so a timed-out child leaves no children behind.
func Set(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// SIGTERM first so a child can stop what it started in its
		// own session; anything still in the group is killed shortly after.
		pid := cmd.Process.Pid
		time.AfterFunc(time.Second, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
}
