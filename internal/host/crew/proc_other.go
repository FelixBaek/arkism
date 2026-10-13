//go:build !linux

package crew

import (
	"os"
	"os/exec"
	"syscall"
)

func newSession(cmd *exec.Cmd) {
	// No process session to set up on this platform.
}

// killGroup can only stop the process itself where there are no process groups.
func killGroup(pid int, sig syscall.Signal) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

const sigKill, sigTerm = syscall.Signal(9), syscall.Signal(15)

// flock is unavailable here; callers run unlocked.
func flock(f *os.File, blocking bool) (bool, error) { return true, nil }

func ownedByMe(info os.FileInfo) bool { return true }

func linkCount(info os.FileInfo) uint64 { return 1 }

func inode(info os.FileInfo) uint64 { return 0 }

func statIdentity(info os.FileInfo) string { return "" }

func canExecute(path string) bool { return true }

func uid() int { return 0 }

const oNoFollow = 0

// exitCode is the process exit status.
func exitCode(ps *os.ProcessState) int { return ps.ExitCode() }
