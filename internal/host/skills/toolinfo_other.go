//go:build !unix

package skills

import "os/exec"

func newSession(cmd *exec.Cmd) {
	// No process session to set up on this platform.
}

func killGroup(cmd *exec.Cmd) { cmd.Process.Kill() }
