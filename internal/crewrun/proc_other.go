//go:build !unix

package crewrun

import "os/exec"

// setProcessGroup does nothing here: process groups are a Unix feature, and
// CommandContext still kills the direct child on timeout.
func setProcessGroup(*exec.Cmd) {
	// no process groups on this platform
}
