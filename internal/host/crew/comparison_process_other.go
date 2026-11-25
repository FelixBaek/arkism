//go:build !windows

package crew

import "os/exec"

func startComparisonAdapter(cmd *exec.Cmd) (func(), error) {
	newSession(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() { killGroup(cmd.Process.Pid, sigKill) }, nil
}
