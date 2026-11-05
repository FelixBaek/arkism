//go:build !unix

package procgroup

import "os/exec"

// Set does nothing here: process groups are a Unix feature, and
// CommandContext still kills the direct child on timeout.
func Set(*exec.Cmd) {
	// no process groups on this platform
}
