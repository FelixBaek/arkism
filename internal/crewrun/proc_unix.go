//go:build unix

package crewrun

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup puts the child in its own group and kills the whole group
// when the context ends, so a timed-out script leaves no children behind.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// SIGTERM first so the expert runner can kill the CLI it started in its
		// own session; anything still in the group is killed shortly after.
		pid := cmd.Process.Pid
		time.AfterFunc(time.Second, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
}
